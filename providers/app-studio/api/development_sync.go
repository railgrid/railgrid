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
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/previewtoken"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectDevelopmentEnvironmentName   = "development"
	projectDevelopmentBindingName       = "dev"
	projectDevelopmentProviderAppStudio = "app-studio"
	projectSandboxSyncTimeout           = 20 * time.Second
)

type projectDevelopmentSyncTargetInfo struct {
	EnvironmentName string
	BindingName     string
	Provider        string
	ResourceName    string

	// Resource / Kind / APIVersion are the instance coordinates the data
	// plane and tenant client address (sandboxrunners, or the Project
	// template's instanceCRD).
	Resource   string `json:"Resource,omitempty"`
	Kind       string `json:"Kind,omitempty"`
	APIVersion string `json:"APIVersion,omitempty"`

	// Components maps a development component name to its workspacePath, for
	// template-backed projects (docs/app-studio-template-sandboxes.md §4.2).
	// Empty means the legacy single-runner target: whole-workspace sync to
	// the instance-level verbs.
	Components map[string]string `json:"Components,omitempty"`
}

// instanceResource is the tenant.Resource descriptor for the target instance.
func (t projectDevelopmentSyncTargetInfo) instanceResource() (tenant.Resource, error) {
	if t.Resource == "" || t.Resource == sandboxRunnersResource {
		return sandboxRunnerResource, nil
	}
	gv, err := schema.ParseGroupVersion(t.APIVersion)
	if err != nil {
		return tenant.Resource{}, fmt.Errorf("target apiVersion %q: %w", t.APIVersion, err)
	}
	return providerBindingResource(gv.WithResource(t.Resource), t.Kind), nil
}

// dataPlaneRefFor addresses the target's instance, optionally scoped to a
// component.
func (t projectDevelopmentSyncTargetInfo) dataPlaneRefFor(component string) dataPlaneRef {
	resource := t.Resource
	if resource == "" {
		resource = sandboxRunnersResource
	}
	return dataPlaneRef{Resource: resource, Name: t.ResourceName, Component: component}
}

// sortedComponents returns the component names in deterministic order.
func (t projectDevelopmentSyncTargetInfo) sortedComponents() []string {
	names := make([]string, 0, len(t.Components))
	for name := range t.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type sandboxPreviewHTTPRouteInfo struct {
	URL string
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

// projectDevelopmentSyncTarget resolves the legacy (sandbox-runner) target
// from the Project spec alone. Template-backed projects resolve through
// projectDevelopmentTarget, which also reads the Template's component map.
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
				Resource:        sandboxRunnersResource,
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

// projectDevelopmentTarget resolves the Project's development data-plane
// target. A template-backed Project (spec.template set) targets its template
// instance with the Template's component map read live from the tenant
// catalog; otherwise the legacy sandbox-runner target applies.
func (s *Server) projectDevelopmentTarget(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) (projectDevelopmentSyncTargetInfo, error) {
	if p == nil {
		return projectDevelopmentSyncTargetInfo{}, fmt.Errorf("project is nil")
	}
	if p.Spec.Template == nil || strings.TrimSpace(p.Spec.Template.Name) == "" {
		target, ok := projectDevelopmentSyncTarget(p, id)
		if !ok {
			return projectDevelopmentSyncTargetInfo{}, fmt.Errorf("project has no development runtime binding")
		}
		return target, nil
	}
	info, err := fetchProjectTemplate(ctx, c, p.Spec.Template.Name)
	if err != nil {
		return projectDevelopmentSyncTargetInfo{}, fmt.Errorf("read project template %q: %w", p.Spec.Template.Name, err)
	}
	// A template-backed target without development components must never fall
	// through to the legacy sandbox-runner code paths — they would mis-handle
	// a non-sandbox instance. selectProjectTemplate refuses such templates;
	// this guards against the template losing its development block later.
	if len(info.Components) == 0 {
		return projectDevelopmentSyncTargetInfo{}, fmt.Errorf("project template %q no longer declares development components", info.Name)
	}
	name := projectTemplateInstanceName(p)
	if name == "" {
		return projectDevelopmentSyncTargetInfo{}, fmt.Errorf("project has no name")
	}
	return projectDevelopmentSyncTargetInfo{
		EnvironmentName: projectDevelopmentEnvironmentName,
		BindingName:     projectDevelopmentBindingName,
		Provider:        projectDevelopmentProviderAppStudio,
		Resource:        info.Resource,
		Kind:            info.Kind,
		APIVersion:      info.APIVersion,
		ResourceName:    name,
		Components:      info.Components,
	}, nil
}

func (s *Server) syncProjectDevelopment(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	target, err := s.projectDevelopmentTarget(r.Context(), c, p, id)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
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
	target, err := s.projectDevelopmentTarget(r.Context(), c, p, id)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
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
	// Validate the instance exists in the workspace first (clear 404 vs proxy err).
	if err := s.validateDevelopmentInstance(ctx, c, target); err != nil {
		return nil, err
	}

	// Legacy single-runner target: whole workspace to the instance-level verb.
	if len(target.Components) == 0 {
		payload, err := json.Marshal(projectSandboxSyncRequest{Files: files, Restart: "auto"})
		if err != nil {
			return nil, fmt.Errorf("encode sandbox sync payload: %w", err)
		}
		body, status, err := s.dataPlanePost(ctx, id, target.dataPlaneRefFor(""), dataPlaneVerbSync, payload)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("sandbox runtime sync returned %d: %s", status, strings.TrimSpace(string(body)))
		}
		_ = patchLastSync(ctx, c, target.ResourceName, metav1.Now())
		return json.RawMessage(body), nil
	}

	// Template-backed target: route files to each component's own sync verb
	// by workspacePath prefix (docs/app-studio-template-sandboxes.md §4.2).
	// Files outside every component (README, docs) sync nowhere.
	routed := routeProjectSyncFiles(files, target.Components)
	results := map[string]json.RawMessage{}
	for _, component := range target.sortedComponents() {
		payload, err := json.Marshal(projectSandboxSyncRequest{Files: routed[component], Restart: "auto"})
		if err != nil {
			return nil, fmt.Errorf("encode %s sync payload: %w", component, err)
		}
		body, status, err := s.dataPlanePost(ctx, id, target.dataPlaneRefFor(component), dataPlaneVerbSync, payload)
		if err != nil {
			return nil, fmt.Errorf("component %s: %w", component, err)
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("component %s sync returned %d: %s", component, status, strings.TrimSpace(string(body)))
		}
		results[component] = json.RawMessage(body)
	}
	aggregated, err := json.Marshal(results)
	if err != nil {
		return nil, err
	}
	return aggregated, nil
}

// validateDevelopmentInstance confirms the target instance exists in the
// tenant workspace so a missing instance surfaces as a Kubernetes 404 rather
// than a data-plane proxy error. Legacy sandbox targets additionally apply
// the runner's status-ref anti-spoof validation.
func (s *Server) validateDevelopmentInstance(ctx context.Context, c *asclient.Client, target projectDevelopmentSyncTargetInfo) error {
	if target.Resource == "" || target.Resource == sandboxRunnersResource {
		_, _, err := s.runtimeTargetForProject(ctx, c, target.ResourceName)
		return err
	}
	res, err := target.instanceResource()
	if err != nil {
		return err
	}
	_, err = c.Resource(res, "").Get(ctx, target.ResourceName, metav1.GetOptions{})
	return err
}

// routeProjectSyncFiles groups workspace files by development component: a
// file under a component's workspacePath syncs to that component with the
// prefix stripped (the component's PVC holds only its own subtree). "." claims
// the whole workspace (single-component templates); the Template validation
// guarantees paths never nest, so a file maps to at most one component.
func routeProjectSyncFiles(files []projectSandboxSyncFile, components map[string]string) map[string][]projectSandboxSyncFile {
	out := make(map[string][]projectSandboxSyncFile, len(components))
	for component, workspacePath := range components {
		wp := path.Clean(strings.TrimSpace(workspacePath))
		if wp == "." {
			out[component] = files
			continue
		}
		prefix := wp + "/"
		for _, f := range files {
			if strings.HasPrefix(f.Path, prefix) {
				out[component] = append(out[component], projectSandboxSyncFile{
					Path:    strings.TrimPrefix(f.Path, prefix),
					Content: f.Content,
				})
			}
		}
	}
	return out
}

func (s *Server) authorizeProjectDevelopmentPreviewTarget(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project, target projectDevelopmentSyncTargetInfo) (projectSandboxPreviewURLResponse, error) {
	// A template-backed development environment keeps its production route
	// wiring — the instance is exposed at its own public URL (the dev overlay
	// preserves the HTTPRoute split), so the preview is that URL, not a
	// signed sandbox-gateway URL. See docs/app-studio-template-sandboxes.md §1.
	if len(target.Components) > 0 {
		return s.templateDevelopmentPreview(ctx, c, target)
	}
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
	preview := s.previewReadiness(ctx, id, target.ResourceName)
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
	// The cross-namespace ReferenceGrant that lets the preview HTTPRoute target
	// the shared preview-gateway Service is now materialized by the
	// sandbox-runner kro template on the runtime cluster (App Studio no longer
	// writes to that cluster). See providers/infrastructure/install/templates/
	// sandbox-runner.yaml.
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
	runtimeNamespace, _, _ := unstructured.NestedString(obj.Object, "status", "runtimeNamespace")
	expectedHost := sandboxRunnerPreviewRouteHost(name)
	if strings.TrimSpace(rawURL) == "" || expectedHost == "" {
		return sandboxPreviewHTTPRouteInfo{}, nil
	}
	if host := previewtoken.NormalizeHost(rawURL); host != expectedHost {
		return sandboxPreviewHTTPRouteInfo{}, fmt.Errorf("sandbox preview route host %q does not match expected host %q", host, expectedHost)
	}
	// The HTTPRoute is created by the SandboxRunner RGD in the sandbox's own
	// runtime namespace (same namespace as the preview Service). Anti-spoof: the
	// route's namespace must match the runner's recorded runtime namespace.
	httpRouteNamespace = strings.TrimSpace(httpRouteNamespace)
	runtimeNamespace = strings.TrimSpace(runtimeNamespace)
	if runtimeNamespace == "" || httpRouteNamespace != runtimeNamespace {
		return sandboxPreviewHTTPRouteInfo{}, fmt.Errorf("sandbox preview HTTPRoute namespace %q does not match the runtime namespace %q", httpRouteNamespace, runtimeNamespace)
	}
	return sandboxPreviewHTTPRouteInfo{
		URL: previewPublicURL(strings.TrimSpace(rawURL)),
	}, nil
}

func sandboxRunnerPreviewRouteHost(runnerName string) string {
	runnerName = strings.TrimSpace(runnerName)
	baseDomain := strings.Trim(previewtoken.NormalizeHost(previewHTTPRouteBaseDomain()), ".")
	if runnerName == "" || baseDomain == "" {
		return ""
	}
	return runnerName + "." + baseDomain
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
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir, projectToolSelectTemplate:
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
	if s.gql == nil {
		klog.V(2).Infof("development sandbox sync after %s skipped for project %s: tenant GraphQL client is not configured", projectToolBaseName(name), p.Name)
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
	lock := s.developmentSyncLock(id, p.Name)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), projectSandboxSyncTimeout)
	defer cancel()
	target, err := s.projectDevelopmentTarget(ctx, c, p, id)
	if err != nil {
		klog.V(2).Infof("development sync after %s skipped for project %s: %v", projectToolBaseName(name), p.Name, err)
		return
	}
	if _, err := s.syncProjectDevelopmentTarget(ctx, c, id, p, target); err != nil {
		klog.V(2).Infof("development sync after %s failed for project %s: %v", projectToolBaseName(name), p.Name, err)
	}
}
