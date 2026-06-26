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
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/faroshq/provider-app-studio/previewtoken"
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
			if isSandboxPreviewHTTPRouteBinding(binding) {
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
	if isSandboxPreviewHTTPRouteBinding(binding) {
		return nil, fmt.Errorf("sandbox preview HTTPRoute bindings are no longer reconciled")
	}
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
	normalizeProjectProviderBindingValues(p, binding, values, name)
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
			runtimeNamespace := ""
			if isSandboxRunnerBinding(binding) && s != nil && s.runtimeClient != nil {
				obj, err := c.Resource(providerBindingResource(gvr, binding.ResourceRef.Kind), "").Get(ctx, name, metav1.GetOptions{})
				if err != nil && !apierrors.IsNotFound(err) {
					return err
				}
				if err == nil {
					runtimeNamespace, err = sandboxRunnerRuntimeNamespaceForCleanup(obj)
					if err != nil {
						return err
					}
				}
			}
			err = c.Resource(providerBindingResource(gvr, binding.ResourceRef.Kind), "").Delete(ctx, name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if runtimeNamespace != "" {
				if err := s.deleteSandboxRuntimeNamespace(ctx, runtimeNamespace); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func sandboxRunnerRuntimeNamespaceForCleanup(obj *unstructured.Unstructured) (string, error) {
	name, err := sandboxRunnerInstanceName(obj)
	if err != nil {
		return "", err
	}
	if statusNamespace, ok, err := sandboxRunnerStatusRuntimeNamespace(obj, name); err != nil || ok {
		return statusNamespace, err
	}
	if prefixed := expectedKROPrefixedRuntimeNamespace(obj, name); prefixed != "" {
		return prefixed, nil
	}
	return name, nil
}

func (s *Server) deleteSandboxRuntimeNamespace(ctx context.Context, namespace string) error {
	if s == nil || s.runtimeClient == nil {
		return nil
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil
	}
	err := s.runtimeClient.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
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
			if isSandboxPreviewHTTPRouteBinding(binding) {
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
	if isSandboxRunnerBinding(binding) {
		if route, err := sandboxRunnerPreviewRoute(obj); err == nil && route.URL != "" {
			status.PreviewURL = route.URL
		}
	} else if !isSandboxRunnerBinding(binding) {
		if previewURL, _, _ := unstructured.NestedString(obj.Object, "status", "previewURL"); previewURL != "" {
			status.PreviewURL = previewURL
		}
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

func projectProviderBindingResourceName(p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec, values map[string]any, id identity) string {
	if isSandboxRunnerBinding(binding) && p != nil && strings.TrimSpace(id.tenantPath) != "" && strings.TrimSpace(p.Name) != "" {
		return sandboxRunnerResourceName(id.tenantPath, p.Name)
	}
	if isSandboxPreviewHTTPRouteBinding(binding) && p != nil && strings.TrimSpace(id.tenantPath) != "" && strings.TrimSpace(p.Name) != "" {
		return sandboxPreviewHTTPRouteResourceName(id.tenantPath, p.Name)
	}
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

func normalizeProjectProviderBindingValues(p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec, values map[string]any, resourceName string) {
	switch {
	case isSandboxRunnerBinding(binding):
		values["name"] = resourceName
		if p != nil {
			values["projectRef"] = p.Name
		}
		normalizeSandboxRunnerPreviewRouteValues(values, resourceName)
	}
}

func normalizeSandboxRunnerPreviewRouteValues(values map[string]any, runnerName string) {
	if !previewHTTPRouteEnabled() {
		values["previewRouteEnabled"] = false
		delete(values, "previewRoute")
		return
	}
	baseDomain := strings.TrimSuffix(previewtoken.NormalizeHost(previewHTTPRouteBaseDomain()), "/")
	values["previewRouteEnabled"] = true
	route := map[string]any{
		"channel":    previewChannelDevelopment,
		"accessMode": string(aiv1alpha1.ProjectSharingModePrivate),
		"parentGateway": map[string]any{
			"name":        previewHTTPRouteParentGatewayName(),
			"namespace":   previewHTTPRouteParentGatewayNamespace(),
			"sectionName": previewHTTPRouteParentGatewaySectionName(),
		},
		"backend": map[string]any{
			"namespace":   previewBackendNamespace(),
			"serviceName": previewBackendServiceName(),
			"servicePort": previewBackendServicePort(),
		},
	}
	if runnerName != "" && baseDomain != "" {
		route["host"] = runnerName + "." + strings.Trim(strings.TrimSpace(baseDomain), ".")
	}
	values["previewRoute"] = route
}

func isSandboxRunnerBinding(binding aiv1alpha1.ProjectProviderBindingSpec) bool {
	if strings.TrimSpace(binding.Provider) != projectDevelopmentProviderAppStudio || binding.ResourceRef == nil {
		return false
	}
	gv, err := schema.ParseGroupVersion(strings.TrimSpace(binding.ResourceRef.APIVersion))
	if err != nil {
		return false
	}
	return gv.Group == "infrastructure.kedge.faros.sh" &&
		gv.Version == "v1alpha1" &&
		strings.TrimSpace(binding.ResourceRef.Kind) == "SandboxRunner" &&
		strings.TrimSpace(binding.ResourceRef.Resource) == "sandboxrunners"
}

func isSandboxPreviewHTTPRouteBinding(binding aiv1alpha1.ProjectProviderBindingSpec) bool {
	if strings.TrimSpace(binding.Provider) != projectDevelopmentProviderAppStudio || binding.ResourceRef == nil {
		return false
	}
	gv, err := schema.ParseGroupVersion(strings.TrimSpace(binding.ResourceRef.APIVersion))
	if err != nil {
		return false
	}
	return gv.Group == "infrastructure.kedge.faros.sh" &&
		gv.Version == "v1alpha1" &&
		strings.TrimSpace(binding.ResourceRef.Kind) == "SandboxPreviewHTTPRoute" &&
		strings.TrimSpace(binding.ResourceRef.Resource) == "sandboxpreviewhttproutes"
}

func sandboxRunnerResourceName(tenantPath, projectName string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tenantPath) + "\x00" + strings.TrimSpace(projectName)))
	return "kedge-sandbox-" + hex.EncodeToString(sum[:8])
}

func sandboxPreviewHTTPRouteResourceName(tenantPath, projectName string) string {
	return sandboxRunnerResourceName(tenantPath, projectName) + "-preview"
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
