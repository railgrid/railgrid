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
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
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
	gv, err := schema.ParseGroupVersion(t.APIVersion)
	if err != nil {
		return tenant.Resource{}, fmt.Errorf("target apiVersion %q: %w", t.APIVersion, err)
	}
	return providerBindingResource(gv.WithResource(t.Resource), t.Kind), nil
}

// dataPlaneRefFor addresses the target's instance, optionally scoped to a
// component.
func (t projectDevelopmentSyncTargetInfo) dataPlaneRefFor(component string) dataPlaneRef {
	return dataPlaneRef{Resource: t.Resource, Name: t.ResourceName, Component: component}
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

// projectDevelopmentTarget resolves the Project's development data-plane
// target: the template instance, with the Template's component map read live
// from the tenant catalog. A project without a bound template has no
// development environment.
func (s *Server) projectDevelopmentTarget(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, _ identity) (projectDevelopmentSyncTargetInfo, error) {
	if p == nil {
		return projectDevelopmentSyncTargetInfo{}, fmt.Errorf("project is nil")
	}
	if p.Spec.Template == nil || strings.TrimSpace(p.Spec.Template.Name) == "" {
		return projectDevelopmentSyncTargetInfo{}, newValidationError("project has no development template yet — select one first")
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

	// Route files to each component's own sync verb
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
// than a data-plane proxy error.
func (s *Server) validateDevelopmentInstance(ctx context.Context, c *asclient.Client, target projectDevelopmentSyncTargetInfo) error {
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

// authorizeProjectDevelopmentPreviewTarget resolves the preview for a
// development environment: the template instance's own public URL — the dev
// overlay keeps the production route wiring, so the dev instance is served
// where a production one would be. See docs/app-studio-template-sandboxes.md §1.
func (s *Server) authorizeProjectDevelopmentPreviewTarget(ctx context.Context, c *asclient.Client, _ identity, _ *aiv1alpha1.Project, target projectDevelopmentSyncTargetInfo) (projectSandboxPreviewURLResponse, error) {
	return s.templateDevelopmentPreview(ctx, c, target)
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
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir, projectToolSelectTemplate, projectToolHydrateWorkspace:
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
