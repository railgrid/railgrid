// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcpserver

// CRD-backed catalog + instance operations. These mirror what the portal
// does in portal/src/api.ts: read Templates and create/list/delete the
// per-template instance CRs DIRECTLY against the tenant's kcp workspace —
// never the RGD-on-the-kro-cluster path. Templates are projected into each
// tenant workspace by the publish-templates CachedResource + the provider's
// APIExport, so a tenant-scoped dynamic client sees them.

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/faroshq/provider-infrastructure/kro"
)

// templateGroup is the fixed group every Template + per-template instance
// kind lives under (see apis/v1alpha1 TemplateInstanceCRD.Group).
const templateGroup = "infrastructure.kedge.faros.sh"

// templatesGVR is the cluster-scoped Template resource the portal and MCP
// both read from the tenant workspace.
var templatesGVR = schema.GroupVersionResource{Group: templateGroup, Version: "v1alpha1", Resource: "templates"}

// templateLabel tags an instance CR with its originating Template's name so
// listInstances can attribute a CR without a second lookup.
const templateLabel = "kedge.faros.sh/template"

// listTemplates reads every Template in the tenant workspace.
func listTemplates(ctx context.Context, dyn dynamic.Interface) ([]kro.Template, error) {
	list, err := dyn.Resource(templatesGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	out := make([]kro.Template, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, templateFromUnstructured(&list.Items[i]))
	}
	return out, nil
}

// getTemplate fetches one Template by name. Returns kro.ErrTemplateNotFound
// when absent so callers map it to a friendly message.
func getTemplate(ctx context.Context, dyn dynamic.Interface, name string) (*kro.Template, error) {
	u, err := dyn.Resource(templatesGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, kro.ErrTemplateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting template %q: %w", name, err)
	}
	t := templateFromUnstructured(u)
	return &t, nil
}

func templateFromUnstructured(u *unstructured.Unstructured) kro.Template {
	name := u.GetName()
	displayName, _, _ := unstructured.NestedString(u.Object, "spec", "displayName")
	description, _, _ := unstructured.NestedString(u.Object, "spec", "description")
	category, _, _ := unstructured.NestedString(u.Object, "spec", "category")
	cloud, _, _ := unstructured.NestedString(u.Object, "spec", "cloud")
	version, _, _ := unstructured.NestedString(u.Object, "spec", "version")
	iconURL, _, _ := unstructured.NestedString(u.Object, "spec", "iconURL")
	group, _, _ := unstructured.NestedString(u.Object, "spec", "instanceCRD", "group")
	crdVersion, _, _ := unstructured.NestedString(u.Object, "spec", "instanceCRD", "version")
	resource, _, _ := unstructured.NestedString(u.Object, "spec", "instanceCRD", "resource")
	kind, _, _ := unstructured.NestedString(u.Object, "spec", "instanceCRD", "kind")
	inputs, _, _ := unstructured.NestedMap(u.Object, "spec", "schema")
	sample, _, _ := unstructured.NestedMap(u.Object, "spec", "sampleValues")
	agent := templateAgentFromSpec(u)

	if displayName == "" {
		displayName = name
	}
	if group == "" {
		group = templateGroup
	}
	if crdVersion == "" {
		crdVersion = "v1alpha1"
	}
	return kro.Template{
		Name:         name,
		DisplayName:  displayName,
		Description:  description,
		Category:     category,
		Cloud:        cloud,
		Version:      version,
		IconURL:      iconURL,
		Backend:      "kro",
		InstanceKind: kind,
		InstanceGVR:  schema.GroupVersionResource{Group: group, Version: crdVersion, Resource: resource},
		InputsSchema: inputs,
		SampleValues: sample,
		Agent:        agent,
	}
}

// templateAgentFromSpec reads spec.agent (AI-agent guidance) into the MCP DTO.
// Returns nil when the Template defines no agent block.
func templateAgentFromSpec(u *unstructured.Unstructured) *kro.TemplateAgent {
	m, found, _ := unstructured.NestedMap(u.Object, "spec", "agent")
	if !found || len(m) == 0 {
		return nil
	}
	usage, _, _ := unstructured.NestedString(u.Object, "spec", "agent", "usage")
	prereqs, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "agent", "prerequisites")
	outputs, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "agent", "outputs")
	return &kro.TemplateAgent{
		Usage:         usage,
		Prerequisites: prereqs,
		Outputs:       outputs,
	}
}

// createInstance writes the per-template instance CR into the tenant
// workspace (apiVersion+kind from the Template; values verbatim under spec).
// The caller maps apierrors.IsAlreadyExists.
func createInstance(ctx context.Context, dyn dynamic.Interface, t *kro.Template, name string, values map[string]any) (*kro.Instance, error) {
	if t.InstanceGVR.Resource == "" || t.InstanceKind == "" {
		return nil, fmt.Errorf("template %q has no instanceCRD", t.Name)
	}
	cr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": t.InstanceGVR.GroupVersion().String(),
		"kind":       t.InstanceKind,
		"metadata": map[string]any{
			"name":   name,
			"labels": map[string]any{templateLabel: t.Name},
		},
		"spec": values,
	}}
	created, err := dyn.Resource(t.InstanceGVR).Create(ctx, cr, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return instanceFromUnstructured(created, t.Name), nil
}

// listInstances lists every Template's instance CRs in the tenant workspace.
// Per-kind NotFound (CRD not yet established) is tolerated as empty so one
// lagging projection doesn't fail the whole list.
func listInstances(ctx context.Context, dyn dynamic.Interface, templates []kro.Template) ([]kro.Instance, error) {
	var out []kro.Instance
	for i := range templates {
		t := &templates[i]
		if t.InstanceGVR.Resource == "" {
			continue
		}
		list, err := dyn.Resource(t.InstanceGVR).List(ctx, metav1.ListOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", t.InstanceGVR.Resource, err)
		}
		for j := range list.Items {
			out = append(out, *instanceFromUnstructured(&list.Items[j], t.Name))
		}
	}
	return out, nil
}

// getInstance probes each Template's plural for a CR with the given name —
// instance names are unique per workspace but the kind isn't carried on the
// MCP input, so we try each. Returns kro.ErrInstanceNotFound when none match.
func getInstance(ctx context.Context, dyn dynamic.Interface, templates []kro.Template, name string) (*kro.Instance, error) {
	for i := range templates {
		t := &templates[i]
		if t.InstanceGVR.Resource == "" {
			continue
		}
		u, err := dyn.Resource(t.InstanceGVR).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("getting %s/%s: %w", t.InstanceGVR.Resource, name, err)
		}
		return instanceFromUnstructured(u, t.Name), nil
	}
	return nil, kro.ErrInstanceNotFound
}

// deleteInstance probes each plural and deletes the first match. Returns
// kro.ErrInstanceNotFound when nothing matched so the tool stays idempotent.
func deleteInstance(ctx context.Context, dyn dynamic.Interface, templates []kro.Template, name string) error {
	for i := range templates {
		t := &templates[i]
		if t.InstanceGVR.Resource == "" {
			continue
		}
		err := dyn.Resource(t.InstanceGVR).Delete(ctx, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("deleting %s/%s: %w", t.InstanceGVR.Resource, name, err)
		}
		return nil
	}
	return kro.ErrInstanceNotFound
}

func instanceFromUnstructured(u *unstructured.Unstructured, templateName string) *kro.Instance {
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	message, _, _ := unstructured.NestedString(u.Object, "status", "message")
	values, _, _ := unstructured.NestedMap(u.Object, "spec")
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")

	out := &kro.Instance{
		Name:      u.GetName(),
		Namespace: u.GetNamespace(),
		Template:  templateName,
		Phase:     phase,
		Message:   message,
		Values:    values,
		CreatedAt: u.GetCreationTimestamp().Time,
	}
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		ctype, _ := cm["type"].(string)
		cstatus, _ := cm["status"].(string)
		creason, _ := cm["reason"].(string)
		cmsg, _ := cm["message"].(string)
		ic := kro.InstanceCondition{Type: ctype, Status: cstatus, Reason: creason, Message: cmsg}
		if ts, ok := cm["lastTransitionTime"].(string); ok {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				ic.Time = t
			}
		}
		out.Conditions = append(out.Conditions, ic)
	}
	// Default phase when the backend hasn't written status yet: Ready iff a
	// Ready=True condition exists, else Pending. Mirrors api.ts.
	if out.Phase == "" {
		out.Phase = "Pending"
		for _, c := range out.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				out.Phase = "Ready"
			}
		}
	}
	return out
}
