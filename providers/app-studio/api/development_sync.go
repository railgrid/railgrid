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
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/previewtoken"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectDevelopmentEnvironmentName   = "development"
	projectDevelopmentBindingName       = "dev"
	projectDevelopmentProviderAppStudio = "app-studio"
	previewChannelDevelopment           = "development-preview"
	sandboxPreviewHTTPRouteNamespace    = "default"
	projectSandboxSyncTimeout           = 20 * time.Second
)

var gatewayReferenceGrantGVR = schema.GroupVersionResource{
	Group:    "gateway.networking.k8s.io",
	Version:  "v1beta1",
	Resource: "referencegrants",
}

type projectDevelopmentSyncTargetInfo struct {
	EnvironmentName string
	BindingName     string
	Provider        string
	ResourceName    string
}

type sandboxPreviewHTTPRouteInfo struct {
	URL                string
	HTTPRouteNamespace string
	BackendNamespace   string
	BackendServiceName string
	ReferenceGrantName string
}

type projectSandboxSyncFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type projectSandboxSyncRequest struct {
	Files   []projectSandboxSyncFile `json:"files"`
	Restart string                   `json:"restart,omitempty"`
}

type projectDevelopmentSyncResponse struct {
	Target projectDevelopmentSyncTargetInfo `json:"target"`
	Result json.RawMessage                  `json:"result,omitempty"`
}

type projectDevelopmentPreviewAuthorizeResponse struct {
	Target                projectDevelopmentSyncTargetInfo `json:"target"`
	Ready                 bool                             `json:"ready"`
	PreviewURL            string                           `json:"previewURL,omitempty"`
	PreviewTokenExpiresAt string                           `json:"previewTokenExpiresAt,omitempty"`
	Message               string                           `json:"message,omitempty"`
	Reason                string                           `json:"reason,omitempty"`
}

type projectSandboxPreviewURLResponse struct {
	Ready                 bool   `json:"ready"`
	PreviewURL            string `json:"previewURL,omitempty"`
	PreviewTokenExpiresAt string `json:"previewTokenExpiresAt,omitempty"`
	Message               string `json:"message,omitempty"`
	Reason                string `json:"reason,omitempty"`
}

func projectDevelopmentSyncTarget(p *aiv1alpha1.Project, id identity) (projectDevelopmentSyncTargetInfo, bool) {
	if p == nil {
		return projectDevelopmentSyncTargetInfo{}, false
	}
	for _, env := range p.Spec.Environments {
		if strings.TrimSpace(env.Name) != projectDevelopmentEnvironmentName {
			continue
		}
		if env.Mode != "" && env.Mode != aiv1alpha1.ProjectEnvironmentModeLive {
			continue
		}
		for _, binding := range env.Bindings {
			if strings.TrimSpace(binding.Provider) != projectDevelopmentProviderAppStudio {
				continue
			}
			if !isSandboxRunnerBinding(binding) {
				continue
			}
			target := projectDevelopmentSyncTargetInfo{
				EnvironmentName: env.Name,
				BindingName:     binding.Name,
				Provider:        binding.Provider,
			}
			if target.BindingName == "" {
				target.BindingName = projectDevelopmentBindingName
			}
			values, _ := projectProviderBindingValues(binding)
			target.ResourceName = projectProviderBindingResourceName(p, binding, values, id)
			if target.ResourceName == "" {
				return projectDevelopmentSyncTargetInfo{}, false
			}
			return target, true
		}
	}
	return projectDevelopmentSyncTargetInfo{}, false
}

func (s *Server) syncProjectDevelopment(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	target, ok := projectDevelopmentSyncTarget(p, id)
	if !ok {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "project has no sandbox runner binding")
		return
	}
	result, err := s.syncProjectDevelopmentTarget(r.Context(), c, id, p, target)
	if err != nil {
		writeStatus(w, http.StatusBadGateway, "BadGateway", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projectDevelopmentSyncResponse{Target: target, Result: result})
}

func (s *Server) authorizeProjectDevelopmentPreview(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	target, ok := projectDevelopmentSyncTarget(p, id)
	if !ok {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "project has no sandbox runner binding")
		return
	}
	preview, err := s.authorizeProjectDevelopmentPreviewTarget(r.Context(), c, id, p, target)
	if err != nil {
		writeStatus(w, http.StatusBadGateway, "BadGateway", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projectDevelopmentPreviewAuthorizeResponse{
		Target:                target,
		Ready:                 preview.Ready,
		PreviewURL:            preview.PreviewURL,
		PreviewTokenExpiresAt: preview.PreviewTokenExpiresAt,
		Message:               preview.Message,
		Reason:                preview.Reason,
	})
}

func (s *Server) syncProjectDevelopmentTarget(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project, target projectDevelopmentSyncTargetInfo) (json.RawMessage, error) {
	if s.workspaces == nil {
		return nil, fmt.Errorf("project workspace store is not configured")
	}
	files, err := s.projectWorkspaceSyncFiles(ctx, projectWorkspaceScope(id, p.Name))
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(projectSandboxSyncRequest{Files: files, Restart: "auto"})
	if err != nil {
		return nil, fmt.Errorf("encode sandbox sync payload: %w", err)
	}
	runtimeTarget, _, err := s.runtimeTargetForProject(ctx, c, target.ResourceName)
	if err != nil {
		return nil, err
	}
	body, status, err := s.postRuntimeService(ctx, runtimeTarget, "sync", payload)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("sandbox runtime sync returned %d: %s", status, strings.TrimSpace(string(body)))
	}
	_ = patchLastSync(ctx, c, target.ResourceName, metav1.Now())
	return json.RawMessage(body), nil
}

func (s *Server) authorizeProjectDevelopmentPreviewTarget(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project, target projectDevelopmentSyncTargetInfo) (projectSandboxPreviewURLResponse, error) {
	runtimeTarget, runner, err := s.runtimeTargetForProject(ctx, c, target.ResourceName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return projectSandboxPreviewURLResponse{
				Ready:   false,
				Reason:  "sandbox_runner_not_found",
				Message: "Preview is getting ready. The sandbox runner has not been created yet.",
			}, nil
		}
		return projectSandboxPreviewURLResponse{}, err
	}
	preview := s.previewReadiness(ctx, runtimeTarget)
	if !preview.Ready {
		return preview, nil
	}
	route, err := sandboxRunnerPreviewRoute(runner)
	if err != nil {
		return projectSandboxPreviewURLResponse{}, err
	}
	if strings.TrimSpace(route.URL) == "" {
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  "sandbox_preview_route_not_ready",
			Message: "Preview is getting ready. The sandbox preview route does not have a URL yet.",
		}, nil
	}
	if err := s.ensureSandboxPreviewReferenceGrant(ctx, p.Name, route); err != nil {
		return projectSandboxPreviewURLResponse{}, err
	}
	preview.PreviewURL, preview.PreviewTokenExpiresAt = s.signedProjectPreviewURLAndExpiry(p.Name, id, target, runtimeTarget, route.URL, aiv1alpha1.ProjectSharingModePrivate)
	return preview, nil
}

func sandboxRunnerPreviewRoute(obj *unstructured.Unstructured) (sandboxPreviewHTTPRouteInfo, error) {
	if obj == nil {
		return sandboxPreviewHTTPRouteInfo{}, fmt.Errorf("sandbox runner is nil")
	}
	name, err := sandboxRunnerInstanceName(obj)
	if err != nil {
		return sandboxPreviewHTTPRouteInfo{}, err
	}
	rawURL, _, _ := unstructured.NestedString(obj.Object, "status", "previewRoute", "url")
	httpRouteNamespace, _, _ := unstructured.NestedString(obj.Object, "status", "previewRoute", "httpRouteRef", "namespace")
	expectedHost := sandboxRunnerPreviewRouteHost(name)
	if strings.TrimSpace(rawURL) == "" || expectedHost == "" {
		return sandboxPreviewHTTPRouteInfo{ReferenceGrantName: name}, nil
	}
	if host := previewtoken.NormalizeHost(rawURL); host != expectedHost {
		return sandboxPreviewHTTPRouteInfo{}, fmt.Errorf("sandbox preview route host %q does not match expected host %q", host, expectedHost)
	}
	httpRouteNamespace = strings.TrimSpace(httpRouteNamespace)
	if httpRouteNamespace == "" {
		httpRouteNamespace = sandboxPreviewHTTPRouteNamespace
	}
	if !isExpectedSandboxPreviewHTTPRouteNamespace(obj, httpRouteNamespace) {
		return sandboxPreviewHTTPRouteInfo{}, fmt.Errorf("sandbox preview HTTPRoute namespace %q does not match expected namespace %q", httpRouteNamespace, sandboxPreviewHTTPRouteNamespace)
	}
	return sandboxPreviewHTTPRouteInfo{
		URL:                previewPublicURL(strings.TrimSpace(rawURL)),
		HTTPRouteNamespace: httpRouteNamespace,
		BackendNamespace:   previewBackendNamespace(),
		BackendServiceName: previewBackendServiceName(),
		ReferenceGrantName: name,
	}, nil
}

func sandboxRunnerPreviewRouteHost(runnerName string) string {
	runnerName = strings.TrimSpace(runnerName)
	baseDomain := strings.Trim(previewtoken.NormalizeHost(previewHTTPRouteBaseDomain()), ".")
	if runnerName == "" || baseDomain == "" || !previewHTTPRouteEnabled() {
		return ""
	}
	return runnerName + "." + baseDomain
}

func isExpectedSandboxPreviewHTTPRouteNamespace(obj *unstructured.Unstructured, namespace string) bool {
	namespace = strings.TrimSpace(namespace)
	if namespace == sandboxPreviewHTTPRouteNamespace {
		return true
	}
	return namespace != "" && namespace == expectedKROPrefixedNamespace(obj, sandboxPreviewHTTPRouteNamespace)
}

func (s *Server) ensureSandboxPreviewReferenceGrant(ctx context.Context, projectName string, route sandboxPreviewHTTPRouteInfo) error {
	if s.runtimeDynamic == nil {
		return fmt.Errorf("sandbox preview reference grant requires runtime dynamic client")
	}
	if route.ReferenceGrantName == "" {
		return fmt.Errorf("sandbox preview reference grant name is empty")
	}
	if route.HTTPRouteNamespace == "" {
		return fmt.Errorf("sandbox preview HTTPRoute namespace is empty")
	}
	if route.BackendNamespace == "" || route.BackendServiceName == "" {
		return fmt.Errorf("sandbox preview backend service reference is incomplete")
	}
	res := s.runtimeDynamic.Resource(gatewayReferenceGrantGVR).Namespace(route.BackendNamespace)
	wantSpec := map[string]any{
		"from": []any{
			map[string]any{
				"group":     "gateway.networking.k8s.io",
				"kind":      "HTTPRoute",
				"namespace": route.HTTPRouteNamespace,
			},
		},
		"to": []any{
			map[string]any{
				"group": "",
				"kind":  "Service",
				"name":  route.BackendServiceName,
			},
		},
	}
	labels := map[string]string{
		"app-studio.kedge.faros.sh/project":    projectName,
		"app-studio.kedge.faros.sh/managed-by": "app-studio",
	}
	existing, err := res.Get(ctx, route.ReferenceGrantName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		grant := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1beta1",
			"kind":       "ReferenceGrant",
			"metadata": map[string]any{
				"name":      route.ReferenceGrantName,
				"namespace": route.BackendNamespace,
				"labels": map[string]any{
					"app-studio.kedge.faros.sh/project":    projectName,
					"app-studio.kedge.faros.sh/managed-by": "app-studio",
				},
			},
			"spec": wantSpec,
		}}
		_, err = res.Create(ctx, grant, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.SetAPIVersion("gateway.networking.k8s.io/v1beta1")
	existing.SetKind("ReferenceGrant")
	existing.Object["spec"] = wantSpec
	existingLabels := existing.GetLabels()
	if existingLabels == nil {
		existingLabels = map[string]string{}
	}
	for key, value := range labels {
		existingLabels[key] = value
	}
	existing.SetLabels(existingLabels)
	_, err = res.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func previewPublicURL(raw string) string {
	port := previewPublicPort()
	if raw == "" || port == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Port() != "" {
		return raw
	}
	u.Host = net.JoinHostPort(u.Hostname(), port)
	return u.String()
}

func previewPublicPort() string {
	value := envValue("APP_STUDIO_PREVIEW_PUBLIC_PORT")
	if value == "" {
		return ""
	}
	port, err := strconv.ParseInt(value, 10, 32)
	if err != nil || port < 1 || port > 65535 {
		return ""
	}
	return strconv.FormatInt(port, 10)
}

func (s *Server) signedProjectPreviewURLAndExpiry(projectName string, id identity, target projectDevelopmentSyncTargetInfo, runtimeTarget runtimeTarget, previewBaseURL string, accessMode aiv1alpha1.ProjectSharingMode) (string, string) {
	u, err := url.Parse(strings.TrimSpace(previewBaseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", ""
	}
	u.Path = "/"
	u.RawQuery = ""
	host := previewtoken.NormalizeHost(u.Host)
	token, expiresAt, err := s.previewSigner.Sign(previewtoken.Payload{
		ProjectName:        projectName,
		TenantPath:         id.tenantPath,
		ResourceName:       target.ResourceName,
		Subject:            id.user,
		Host:               host,
		RuntimeNamespace:   runtimeTarget.Preview.Namespace,
		PreviewServiceName: runtimeTarget.Preview.Name,
		PreviewPortName:    runtimeTarget.Preview.PortName,
		AccessMode:         string(accessMode),
	})
	if err != nil {
		return "", ""
	}
	q := u.Query()
	q.Set(previewtoken.QueryParam, token)
	u.RawQuery = q.Encode()
	return u.String(), expiresAt.Format(time.RFC3339)
}

func (s *Server) projectWorkspaceSyncFiles(ctx context.Context, scope workspace.Scope) ([]projectSandboxSyncFile, error) {
	list, err := s.workspaces.ListFiles(ctx, scope, workspace.ListOptions{Limit: workspace.MaxListLimit})
	if err != nil {
		return nil, err
	}
	files := make([]projectSandboxSyncFile, 0, len(list.Files))
	for _, f := range list.Files {
		read, err := s.workspaces.ReadFile(ctx, scope, workspace.ReadOptions{Path: f.Path, MaxBytes: workspace.MaxWriteBytes})
		if err != nil {
			return nil, err
		}
		if read.Binary || read.Truncated {
			continue
		}
		files = append(files, projectSandboxSyncFile{Path: read.Path, Content: read.Content})
	}
	return files, nil
}

func (s *Server) projectAssistantPreviewRefreshNeeded(_ context.Context, _ workspace.Scope, _ string, _ bool, toolCalls []projectToolCallStreamEvent) bool {
	return projectAssistantToolCallsRequireDevelopmentSync(toolCalls)
}

func shouldSyncDevelopmentAfterTool(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
		return true
	default:
		return false
	}
}

func (s *Server) scheduleDevelopmentSyncAfterMutation(id identity, p *aiv1alpha1.Project, name string) {
	if s == nil || p == nil || !shouldSyncDevelopmentAfterTool(name) {
		return
	}
	project := p.DeepCopy()
	s.mu.Lock()
	hook := s.developmentSyncAfterMutation
	s.mu.Unlock()
	if hook != nil {
		hook(id, project, name)
		return
	}
	go s.syncDevelopmentAfterMutation(id, project, name)
}

func (s *Server) syncDevelopmentAfterMutation(id identity, p *aiv1alpha1.Project, name string) {
	if s.clients == nil {
		klog.V(2).Infof("development sandbox sync after %s skipped for project %s: tenant client factory is not configured", projectToolBaseName(name), p.Name)
		return
	}
	c, err := s.clientFor(id)
	if err != nil {
		klog.V(2).Infof("development sandbox sync after %s failed for project %s: %v", projectToolBaseName(name), p.Name, err)
		return
	}
	s.syncDevelopmentAfterMutationWithClient(c, id, p, name)
}

func (s *Server) syncDevelopmentAfterMutationWithClient(c *asclient.Client, id identity, p *aiv1alpha1.Project, name string) {
	target, ok := projectDevelopmentSyncTarget(p, id)
	if !ok {
		return
	}
	lock := s.developmentSyncLock(id, p.Name)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), projectSandboxSyncTimeout)
	defer cancel()
	if _, err := s.syncProjectDevelopmentTarget(ctx, c, id, p, target); err != nil {
		klog.V(2).Infof("development sandbox sync after %s failed for project %s: %v", projectToolBaseName(name), p.Name, err)
	}
}
