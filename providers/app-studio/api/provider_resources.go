/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

func (s *Server) reconcileProjectLiveBindings(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) (*aiv1alpha1.Project, error) {
	if c == nil || p == nil {
		return p, nil
	}
	for _, env := range p.Spec.Environments {
		if env.Mode != aiv1alpha1.ProjectEnvironmentModeLive {
			continue
		}
		for _, binding := range env.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderResource || binding.ResourceRef == nil {
				continue
			}
			if _, err := ensureProjectProviderResource(ctx, c, p, binding, id); err != nil {
				return nil, err
			}
		}
	}
	return syncProjectLiveBindingStatus(ctx, c, p, id)
}

func ensureProjectProviderResource(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec, id identity) (*unstructured.Unstructured, error) {
	gvr, err := projectProviderResourceGVR(binding.ResourceRef)
	if err != nil {
		return nil, err
	}
	values, err := projectProviderBindingValues(binding)
	if err != nil {
		return nil, err
	}
	name := projectProviderBindingResourceName(p, binding, values, id)
	if name == "" {
		return nil, fmt.Errorf("provider binding %q has no resource name", binding.Name)
	}
	spec := map[string]any{}
	for key, value := range values {
		spec[key] = value
	}
	want := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": binding.ResourceRef.APIVersion,
			"kind":       binding.ResourceRef.Kind,
			"metadata": map[string]any{
				"name": name,
				"labels": map[string]any{
					"app-studio.kedge.faros.sh/project": p.Name,
				},
			},
			"spec": spec,
		},
	}
	if owner := projectProviderResourceOwnerRef(p); owner != nil {
		want.SetOwnerReferences([]metav1.OwnerReference{*owner})
	}
	res := c.Resource(providerBindingResource(gvr, binding.ResourceRef.Kind), "")
	existing, err := res.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return res.Create(ctx, want, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	existing.SetAPIVersion(binding.ResourceRef.APIVersion)
	existing.SetKind(binding.ResourceRef.Kind)
	existing.Object["spec"] = spec
	labels := existing.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["app-studio.kedge.faros.sh/project"] = p.Name
	existing.SetLabels(labels)
	if owner := projectProviderResourceOwnerRef(p); owner != nil {
		existing.SetOwnerReferences([]metav1.OwnerReference{*owner})
	}
	return res.Update(ctx, existing, metav1.UpdateOptions{})
}

func (s *Server) deleteProjectProviderResources(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) error {
	if c == nil || p == nil {
		return nil
	}
	for _, env := range p.Spec.Environments {
		if env.Mode != aiv1alpha1.ProjectEnvironmentModeLive {
			continue
		}
		for _, binding := range env.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderResource || binding.ResourceRef == nil {
				continue
			}
			gvr, err := projectProviderResourceGVR(binding.ResourceRef)
			if err != nil {
				return err
			}
			values, err := projectProviderBindingValues(binding)
			if err != nil {
				return err
			}
			name := projectProviderBindingResourceName(p, binding, values, id)
			if name == "" {
				return fmt.Errorf("provider binding %q has no resource name", binding.Name)
			}
			// Deleting the SandboxRunner instance is enough: the
			// infrastructure provider's kro template owns the runtime
			// namespace and garbage-collects it (and every materialized
			// workload) when the instance goes away. App Studio no longer
			// holds a runtime-cluster client to delete it directly.
			err = c.Resource(providerBindingResource(gvr, binding.ResourceRef.Kind), "").Delete(ctx, name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func projectProviderResourceOwnerRef(p *aiv1alpha1.Project) *metav1.OwnerReference {
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

func syncProjectLiveBindingStatus(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) (*aiv1alpha1.Project, error) {
	statuses := projectLiveEnvironmentStatuses(ctx, c, p, id)
	if len(statuses) == 0 {
		return p, nil
	}
	patch := map[string]any{
		"status": map[string]any{
			"environments": statuses,
		},
	}
	raw, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	return c.Projects().Patch(ctx, p.Name, types.MergePatchType, raw, metav1.PatchOptions{}, "status")
}

func projectWithLiveBindingStatus(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) *aiv1alpha1.Project {
	if c == nil || p == nil {
		return p
	}
	statuses := projectLiveEnvironmentStatuses(ctx, c, p, id)
	if len(statuses) == 0 {
		return p
	}
	next := p.DeepCopy()
	next.Status.Environments = mergeProjectEnvironmentStatuses(next.Status.Environments, statuses)
	return next
}

func projectLiveEnvironmentStatuses(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) []aiv1alpha1.ProjectEnvironmentStatus {
	if c == nil || p == nil {
		return nil
	}
	statuses := []aiv1alpha1.ProjectEnvironmentStatus{}
	for _, env := range p.Spec.Environments {
		if env.Mode != aiv1alpha1.ProjectEnvironmentModeLive {
			continue
		}
		envStatus := aiv1alpha1.ProjectEnvironmentStatus{
			Name: env.Name,
			Mode: env.Mode,
		}
		for _, binding := range env.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderResource || binding.ResourceRef == nil {
				continue
			}
			envStatus.Bindings = append(envStatus.Bindings, projectProviderBindingStatus(ctx, c, p, binding, id))
		}
		if len(envStatus.Bindings) == 0 {
			continue
		}
		for _, binding := range envStatus.Bindings {
			if envStatus.Phase == "" && binding.Phase != "" {
				envStatus.Phase = binding.Phase
			}
		}
		statuses = append(statuses, envStatus)
	}
	return statuses
}

func projectProviderBindingStatus(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec, id identity) aiv1alpha1.ProjectProviderBindingStatus {
	status := aiv1alpha1.ProjectProviderBindingStatus{
		Name:     binding.Name,
		Provider: binding.Provider,
	}
	gvr, err := projectProviderResourceGVR(binding.ResourceRef)
	if err != nil {
		status.Phase = "Invalid"
		return status
	}
	values, err := projectProviderBindingValues(binding)
	if err != nil {
		status.Phase = "Invalid"
		return status
	}
	name := projectProviderBindingResourceName(p, binding, values, id)
	if name == "" {
		status.Phase = "Invalid"
		return status
	}
	obj, err := c.Resource(providerBindingResource(gvr, binding.ResourceRef.Kind), "").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		status.Phase = "Pending"
		return status
	}
	status.Phase = projectProviderResourcePhase(obj)
	if previewURL, _, _ := unstructured.NestedString(obj.Object, "status", "previewURL"); previewURL != "" {
		status.PreviewURL = previewURL
	}
	if url, _, _ := unstructured.NestedString(obj.Object, "status", "url"); url != "" {
		status.URL = url
	}
	if outputs, ok := nestedStringMap(obj.Object, "status", "outputs"); ok {
		status.Outputs = outputs
	}
	return status
}

func projectProviderResourcePhase(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	if phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase"); strings.TrimSpace(phase) != "" {
		return strings.TrimSpace(phase)
	}
	if conditionStatus, ok := projectProviderResourceConditionStatus(obj, "Ready"); ok {
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

func projectProviderResourceConditionStatus(obj *unstructured.Unstructured, conditionType string) (string, bool) {
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

func mergeProjectEnvironmentStatuses(existing, live []aiv1alpha1.ProjectEnvironmentStatus) []aiv1alpha1.ProjectEnvironmentStatus {
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

func projectProviderResourceGVR(ref *aiv1alpha1.ProjectProviderResourceReference) (schema.GroupVersionResource, error) {
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

func projectProviderBindingValues(binding aiv1alpha1.ProjectProviderBindingSpec) (map[string]any, error) {
	if len(binding.Values.Raw) == 0 {
		return map[string]any{}, nil
	}
	values := map[string]any{}
	if err := json.Unmarshal(binding.Values.Raw, &values); err != nil {
		return nil, fmt.Errorf("decode provider binding %q values: %w", binding.Name, err)
	}
	return values, nil
}

func projectProviderBindingResourceName(p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec, values map[string]any, _ identity) string {
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

func nestedStringMap(obj map[string]any, fields ...string) (map[string]string, bool) {
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
