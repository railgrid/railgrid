// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package project

import (
	"encoding/json"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
)

// InstanceRef is one instance the Project's spec demands: the fully-resolved
// GVR/Kind (recorded by the approve path from Template.spec.instanceCRD), the
// instance name, and the spec values to create it with. Everything the
// reconciler needs, nothing it has to look up.
type InstanceRef struct {
	Environment string
	Binding     string
	// Template is the infrastructure Template this instance comes from.
	Template string
	GVR      schema.GroupVersionResource
	Kind     string
	Name     string
	Values   map[string]any
}

// Key identifies the ref within one Project (environment/binding).
func (ref InstanceRef) Key() string { return ref.Environment + "/" + ref.Binding }

// InstanceRefs derives the demanded instances from Project spec. Pure. Skips
// bindings that are not infrastructure provider-resources or that lack a
// resolvable resourceRef — those are not this controller's to converge.
func InstanceRefs(p *vibev1alpha1.Project) []InstanceRef {
	var out []InstanceRef
	for _, env := range p.Spec.Environments {
		for _, b := range env.Bindings {
			if b.Kind != vibev1alpha1.ProjectBindingKindProviderResource || b.Provider != "infrastructure" {
				continue
			}
			ref := b.ResourceRef
			if ref == nil || ref.Name == "" || ref.Resource == "" || ref.Kind == "" || ref.APIVersion == "" {
				continue
			}
			gv, err := schema.ParseGroupVersion(ref.APIVersion)
			if err != nil {
				continue
			}
			values := map[string]any{}
			if len(b.Values.Raw) > 0 {
				// Invalid JSON means an authoring bug upstream; skip rather
				// than create an instance with an empty spec.
				if err := json.Unmarshal(b.Values.Raw, &values); err != nil {
					continue
				}
			}
			out = append(out, InstanceRef{
				Environment: env.Name,
				Binding:     b.Name,
				Template:    b.Template,
				GVR:         gv.WithResource(ref.Resource),
				Kind:        ref.Kind,
				Name:        ref.Name,
				Values:      values,
			})
		}
	}
	return out
}

// DesiredInstance builds the instance CR for a ref. Pure. Mirrors the
// infrastructure provider's own provision shape: the template name under
// spec.template, values verbatim under spec.values, template attribution
// label, plus the Project back-reference.
func DesiredInstance(p *vibev1alpha1.Project, ref InstanceRef) *unstructured.Unstructured {
	labels := map[string]any{projectLabel: p.Name}
	// Attribution is per binding: a project's search backend is a searxng
	// instance, not an instance of the app's template.
	templateName := ref.Template
	if templateName == "" && ref.Binding == vibev1alpha1.BindingRuntime && p.Spec.Template != nil {
		// Bindings written before the field existed: only the runtime is
		// known to come from the Project's template. Guessing for the others
		// is what produced a searxng instance labelled "application".
		templateName = p.Spec.Template.Name
	}
	if templateName != "" {
		labels[templateLabel] = templateName
	}
	inst := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": ref.GVR.GroupVersion().String(),
		"kind":       ref.Kind,
		"metadata": map[string]any{
			"name":   ref.Name,
			"labels": labels,
		},
		"spec": map[string]any{
			"template": templateName,
			"values":   ref.Values,
		},
	}}
	return inst
}

// MirrorStatus computes the Project status from observed instances. Pure —
// now is injected. Phase: Ready when every demanded binding's instance reports
// ready; Provisioning otherwise.
func MirrorStatus(p *vibev1alpha1.Project, observed map[string]*unstructured.Unstructured, now metav1.Time) vibev1alpha1.ProjectStatus {
	refs := InstanceRefs(p)
	byEnv := map[string][]vibev1alpha1.ProjectProviderBindingStatus{}
	allReady := len(refs) > 0
	for _, ref := range refs {
		bs := vibev1alpha1.ProjectProviderBindingStatus{
			Name:     ref.Binding,
			Provider: "infrastructure",
			Phase:    "Pending",
		}
		if inst := observed[ref.Key()]; inst != nil {
			bs.Phase, bs.URL, bs.Outputs = observeInstance(inst)
		}
		if !isReadyPhase(bs.Phase) {
			allReady = false
		}
		byEnv[ref.Environment] = append(byEnv[ref.Environment], bs)
	}

	status := vibev1alpha1.ProjectStatus{
		Phase:     vibev1alpha1.ProjectPhaseProvisioning,
		UpdatedAt: &now,
	}
	if allReady {
		status.Phase = vibev1alpha1.ProjectPhaseReady
	}
	for _, env := range p.Spec.Environments {
		bindings, ok := byEnv[env.Name]
		if !ok {
			continue
		}
		envPhase := "Ready"
		for _, b := range bindings {
			if !isReadyPhase(b.Phase) {
				envPhase = "Provisioning"
				break
			}
		}
		status.Environments = append(status.Environments, vibev1alpha1.ProjectEnvironmentStatus{
			Name:     env.Name,
			Mode:     env.Mode,
			Revision: env.Revision,
			Phase:    envPhase,
			Bindings: bindings,
		})
	}
	return status
}

// observeInstance extracts phase, url, and string outputs from an instance's
// unstructured status. Mirrors the infrastructure catalog reader: empty phase
// defaults to Pending, or Ready when a Ready=True condition exists.
func observeInstance(inst *unstructured.Unstructured) (phase, url string, outputs map[string]string) {
	status, _, _ := unstructured.NestedMap(inst.Object, "status")
	if status == nil {
		return "Pending", "", nil
	}
	phase, _ = status["phase"].(string)
	url, _ = status["url"].(string)
	if phase == "" {
		phase = "Pending"
		if conds, ok := status["conditions"].([]any); ok {
			for _, c := range conds {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := cm["type"].(string); t == "Ready" {
					if s, _ := cm["status"].(string); strings.EqualFold(s, "True") {
						phase = "Ready"
					}
				}
			}
		}
	}
	// Every scalar status field becomes an output so the portal/tools can
	// render template-declared values (host, redirectURL, …) untyped.
	for k, v := range status {
		s, ok := v.(string)
		if !ok || k == "phase" || k == "url" {
			continue
		}
		if outputs == nil {
			outputs = map[string]string{}
		}
		outputs[k] = s
	}
	return phase, url, outputs
}

func isReadyPhase(phase string) bool {
	switch strings.ToLower(phase) {
	case "ready", "active", "succeeded":
		return true
	}
	return false
}
