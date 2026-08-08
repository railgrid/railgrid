/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package bindings holds the pure desired-state and status-fold logic for a
// Project's provider-resource bindings. Both consumers — the HTTP layer (as
// the calling user, read-through status) and the Project reconciler (as the
// provider identity, converging instances) — must agree on what an instance
// should look like and how its phase is read, so that logic lives here once.
package bindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

// ProjectLabel attributes an instance back to its Project.
const ProjectLabel = "app-studio.kedge.faros.sh/project"

// Identity annotations bridging the CR keyspace to the workspace/store
// keyspace (reconcilers only know the cluster; store scopes are keyed by the
// org/workspace UUIDs the hub derives from the tenant path). Stamped by the
// API layer at resource creation.
const (
	OrgUUIDAnnotation       = "ai.kedge.faros.sh/org-uuid"
	WorkspaceUUIDAnnotation = "ai.kedge.faros.sh/workspace-uuid"
)

// GVR derives the instance GroupVersionResource from a binding's resourceRef
// (recorded from Template.spec.instanceCRD at bind time — self-contained, no
// Template read needed).
func GVR(ref *aiv1alpha1.ProjectProviderResourceReference) (schema.GroupVersionResource, error) {
	if ref == nil {
		return schema.GroupVersionResource{}, fmt.Errorf("resourceRef is required")
	}
	gv, err := schema.ParseGroupVersion(strings.TrimSpace(ref.APIVersion))
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	resource := strings.TrimSpace(ref.Resource)
	if resource == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("resourceRef.resource is required")
	}
	return gv.WithResource(resource), nil
}

// Values decodes the binding's raw values into the instance spec fields.
func Values(binding aiv1alpha1.ProjectProviderBindingSpec) (map[string]any, error) {
	if len(binding.Values.Raw) == 0 {
		return map[string]any{}, nil
	}
	values := map[string]any{}
	if err := json.Unmarshal(binding.Values.Raw, &values); err != nil {
		return nil, fmt.Errorf("decode provider binding %q values: %w", binding.Name, err)
	}
	return values, nil
}

// ResourceName resolves the instance name: explicit resourceRef.name, then a
// "name" value, then "<project>-<binding>".
func ResourceName(p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec, values map[string]any) string {
	if binding.ResourceRef != nil && strings.TrimSpace(binding.ResourceRef.Name) != "" {
		return strings.TrimSpace(binding.ResourceRef.Name)
	}
	if name, ok := values["name"].(string); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	projectName := ""
	if p != nil {
		projectName = strings.TrimSpace(p.Name)
	}
	bindingName := strings.TrimSpace(binding.Name)
	if projectName == "" || bindingName == "" {
		return ""
	}
	return projectName + "-" + bindingName
}

// OwnerRef points an instance back at its Project so kcp garbage-collects it
// with the Project even if the finalizer never ran.
func OwnerRef(p *aiv1alpha1.Project) *metav1.OwnerReference {
	if p == nil || p.UID == "" || strings.TrimSpace(p.Name) == "" {
		return nil
	}
	controller := true
	return &metav1.OwnerReference{
		APIVersion: aiv1alpha1.SchemeGroupVersion.String(),
		Kind:       "Project",
		Name:       p.Name,
		UID:        p.UID,
		Controller: &controller,
	}
}

// InvalidBindingError marks a binding whose desired state cannot be computed
// at all (bad ref, undecodable values, no name) — retrying cannot help, only
// a spec change can.
type InvalidBindingError struct{ Err error }

func (e *InvalidBindingError) Error() string { return e.Err.Error() }
func (e *InvalidBindingError) Unwrap() error { return e.Err }

// IsInvalidBinding reports whether err (anywhere in its chain) is an
// InvalidBindingError.
func IsInvalidBinding(err error) bool {
	var invalid *InvalidBindingError
	return errors.As(err, &invalid)
}

// Desired builds the desired instance object for a binding. Returns the GVR
// alongside so callers can address the right resource. Errors are
// InvalidBindingError — the spec, not the world, is wrong.
func Desired(p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	gvr, err := GVR(binding.ResourceRef)
	if err != nil {
		return nil, schema.GroupVersionResource{}, &InvalidBindingError{Err: err}
	}
	values, err := Values(binding)
	if err != nil {
		return nil, schema.GroupVersionResource{}, &InvalidBindingError{Err: err}
	}
	name := ResourceName(p, binding, values)
	if name == "" {
		return nil, schema.GroupVersionResource{}, &InvalidBindingError{Err: fmt.Errorf("provider binding %q has no resource name", binding.Name)}
	}
	spec := map[string]any{}
	maps.Copy(spec, values)
	want := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": binding.ResourceRef.APIVersion,
			"kind":       binding.ResourceRef.Kind,
			"metadata": map[string]any{
				"name": name,
				"labels": map[string]any{
					ProjectLabel: p.Name,
				},
			},
			"spec": spec,
		},
	}
	if owner := OwnerRef(p); owner != nil {
		want.SetOwnerReferences([]metav1.OwnerReference{*owner})
	}
	return want, gvr, nil
}

// Phase reads an instance's phase: status.phase, then the Ready condition,
// then state=ACTIVE. Empty when nothing is published yet.
func Phase(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	if phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase"); strings.TrimSpace(phase) != "" {
		return strings.TrimSpace(phase)
	}
	if conditionStatus, ok := conditionStatus(obj, "Ready"); ok {
		if strings.EqualFold(conditionStatus, "True") {
			return "Ready"
		}
		return "Pending"
	}
	if state, _, _ := unstructured.NestedString(obj.Object, "status", "state"); strings.EqualFold(strings.TrimSpace(state), "ACTIVE") {
		return "Ready"
	}
	return ""
}

func conditionStatus(obj *unstructured.Unstructured, conditionType string) (string, bool) {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rawType, _ := condition["type"].(string)
		if rawType != conditionType {
			continue
		}
		status, _ := condition["status"].(string)
		return strings.TrimSpace(status), strings.TrimSpace(status) != ""
	}
	return "", false
}

// StatusFromObject folds one fetched instance (nil = not found → Pending)
// into the binding's status entry.
func StatusFromObject(binding aiv1alpha1.ProjectProviderBindingSpec, obj *unstructured.Unstructured) aiv1alpha1.ProjectProviderBindingStatus {
	status := aiv1alpha1.ProjectProviderBindingStatus{
		Name:     binding.Name,
		Provider: binding.Provider,
	}
	if obj == nil {
		status.Phase = "Pending"
		return status
	}
	status.Phase = Phase(obj)
	if previewURL, _, _ := unstructured.NestedString(obj.Object, "status", "previewURL"); previewURL != "" {
		status.PreviewURL = previewURL
	}
	if url, _, _ := unstructured.NestedString(obj.Object, "status", "url"); url != "" {
		status.URL = url
	}
	if outputs, ok := NestedStringMap(obj.Object, "status", "outputs"); ok {
		status.Outputs = outputs
	}
	return status
}

// InvalidStatus is the fold for a binding whose desired state cannot even be
// computed (bad ref, bad values, no name).
func InvalidStatus(binding aiv1alpha1.ProjectProviderBindingSpec) aiv1alpha1.ProjectProviderBindingStatus {
	return aiv1alpha1.ProjectProviderBindingStatus{
		Name:     binding.Name,
		Provider: binding.Provider,
		Phase:    "Invalid",
	}
}

// FoldEnvironment assembles one environment's status from its per-binding
// statuses (the environment phase is the first non-empty binding phase).
func FoldEnvironment(env aiv1alpha1.ProjectEnvironmentSpec, bindingStatuses []aiv1alpha1.ProjectProviderBindingStatus) aiv1alpha1.ProjectEnvironmentStatus {
	envStatus := aiv1alpha1.ProjectEnvironmentStatus{
		Name:     env.Name,
		Mode:     env.Mode,
		Bindings: bindingStatuses,
	}
	for _, binding := range bindingStatuses {
		if envStatus.Phase == "" && binding.Phase != "" {
			envStatus.Phase = binding.Phase
		}
	}
	return envStatus
}

// MergeEnvironmentStatuses overlays live environment statuses onto the
// existing list, preserving entries (and order) the live set doesn't cover.
func MergeEnvironmentStatuses(existing, live []aiv1alpha1.ProjectEnvironmentStatus) []aiv1alpha1.ProjectEnvironmentStatus {
	liveByName := map[string]aiv1alpha1.ProjectEnvironmentStatus{}
	for _, st := range live {
		liveByName[st.Name] = st
	}
	out := make([]aiv1alpha1.ProjectEnvironmentStatus, 0, len(existing)+len(liveByName))
	for _, st := range existing {
		if liveStatus, ok := liveByName[st.Name]; ok {
			out = append(out, liveStatus)
			delete(liveByName, st.Name)
			continue
		}
		out = append(out, st)
	}
	for _, st := range liveByName {
		out = append(out, st)
	}
	return out
}

// NestedStringMap reads a map[string]string from an unstructured object,
// tolerating map[string]any with string values.
func NestedStringMap(obj map[string]any, fields ...string) (map[string]string, bool) {
	raw, ok, _ := unstructured.NestedStringMap(obj, fields...)
	if ok {
		return raw, true
	}
	values, ok, _ := unstructured.NestedMap(obj, fields...)
	if !ok {
		return nil, false
	}
	out := map[string]string{}
	for key, value := range values {
		if s, ok := value.(string); ok {
			out[key] = s
		}
	}
	return out, len(out) > 0
}
