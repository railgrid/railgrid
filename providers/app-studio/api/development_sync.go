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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	"github.com/faroshq/provider-app-studio/hubmcp"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectDevelopmentEnvironmentName   = "development"
	projectDevelopmentBindingName       = "dev"
	projectDevelopmentProviderAppStudio = "app-studio"
	projectSandboxComponentSyncTimeout  = 5 * time.Minute
	projectSandboxSyncTimeout           = 10 * time.Minute
	projectDevelopmentSyncErrorMaxBytes = 4096
)

type projectDevelopmentSyncTargetInfo struct {
	EnvironmentName string
	BindingName     string
	Provider        string
	ResourceName    string

	// Resource / Kind / APIVersion are the instance coordinates the data
	// plane and tenant client address (the Project template's instanceCRD).
	Resource   string `json:"Resource,omitempty"`
	Kind       string `json:"Kind,omitempty"`
	APIVersion string `json:"APIVersion,omitempty"`

	// Components maps a development component name to its workspacePath, for
	// template-backed projects (docs/app-studio-template-sandboxes.md §4.2).
	// Empty means the legacy single-runner target: whole-workspace sync to
	// the instance-level verbs.
	Components map[string]projectTemplateComponent `json:"Components,omitempty"`

	// PreviewAccessModes is non-empty when this Template uses the standard
	// access-proxy input and App Studio may offer a visibility control.
	PreviewAccessModes []string `json:"previewAccessModes,omitempty"`
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

// componentWorkspacePathSummary renders the component → workspacePath map for
// error messages, sorted by component name (e.g. "backend → api/, frontend → web/").
func (t projectDevelopmentSyncTargetInfo) componentWorkspacePathSummary() string {
	parts := make([]string, 0, len(t.Components))
	for _, name := range t.sortedComponents() {
		wp := path.Clean(strings.TrimSpace(t.Components[name].WorkspacePath))
		if wp == "." {
			parts = append(parts, name+" → the workspace root")
			continue
		}
		parts = append(parts, name+" → "+wp+"/")
	}
	return strings.Join(parts, ", ")
}

// projectSandboxSyncFile is one /sync file entry. Encoding is omitted for
// UTF-8 text and "base64" for a binary, sent only to an agent whose /status
// advertises base64 in syncEncodings.
type projectSandboxSyncFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
}

type projectSandboxSyncRequest struct {
	Files          []projectSandboxSyncFile `json:"files"`
	DeletedPaths   []string                 `json:"deletePaths,omitempty"`
	SourceRevision uint64                   `json:"sourceRevision"`
	SourceDigest   string                   `json:"sourceDigest"`
	Restart        string                   `json:"restart,omitempty"`
}

type projectWorkspaceSyncSnapshot struct {
	// Files are the UTF-8 text files every agent accepts.
	Files []projectSandboxSyncFile
	// BinaryFiles are base64 entries, read only when the caller asked for
	// them; BinaryPaths always lists every binary in the workspace.
	BinaryFiles    []projectSandboxSyncFile
	BinaryPaths    []string
	DeletedPaths   []string
	SourceRevision uint64
}

type projectDevelopmentSyncResponse struct {
	Target projectDevelopmentSyncTargetInfo `json:"target"`
	Result json.RawMessage                  `json:"result,omitempty"`
}

type projectSandboxSyncResult struct {
	ReloadError string `json:"reloadError,omitempty"`
}

type projectDevelopmentPreviewAuthorizeResponse struct {
	Target          projectDevelopmentSyncTargetInfo `json:"target"`
	Ready           bool                             `json:"ready"`
	PreviewURL      string                           `json:"previewURL,omitempty"`
	Message         string                           `json:"message,omitempty"`
	Reason          string                           `json:"reason,omitempty"`
	DesiredAccess   string                           `json:"desiredAccess,omitempty"`
	ObservedAccess  string                           `json:"observedAccess,omitempty"`
	AccessConverged bool                             `json:"accessConverged"`
}

type projectSandboxPreviewURLResponse struct {
	Ready          bool   `json:"ready"`
	PreviewURL     string `json:"previewURL,omitempty"`
	Message        string `json:"message,omitempty"`
	Reason         string `json:"reason,omitempty"`
	ObservedAccess string `json:"observedAccess,omitempty"`
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
		EnvironmentName:    projectDevelopmentEnvironmentName,
		BindingName:        projectDevelopmentBindingName,
		Provider:           projectDevelopmentProviderAppStudio,
		Resource:           info.Resource,
		Kind:               info.Kind,
		APIVersion:         info.APIVersion,
		ResourceName:       name,
		Components:         info.Components,
		PreviewAccessModes: append([]string(nil), info.PreviewAccessModes...),
	}, nil
}

func (s *Server) syncProjectDevelopment(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	release, ok := s.reserveProjectExternalOperation(w, r.Context(), id, p, "manually synchronizing the development workspace")
	if !ok {
		return
	}
	defer release()
	lock := s.developmentSyncLock(id, p)
	lock.Lock()
	defer lock.Unlock()
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
	writeJSON(w, http.StatusOK, projectDevelopmentPreviewAuthorizationState(p, target, preview))
}

func projectDevelopmentPreviewAuthorizationState(p *aiv1alpha1.Project, target projectDevelopmentSyncTargetInfo, preview projectSandboxPreviewURLResponse) projectDevelopmentPreviewAuthorizeResponse {
	response := projectDevelopmentPreviewAuthorizeResponse{
		Target:          target,
		Ready:           preview.Ready,
		PreviewURL:      preview.PreviewURL,
		Message:         preview.Message,
		Reason:          preview.Reason,
		ObservedAccess:  preview.ObservedAccess,
		AccessConverged: true,
	}
	if len(target.PreviewAccessModes) == 0 {
		return response
	}
	response.DesiredAccess = effectiveProjectPreviewAccess(p.Spec.Sharing.Preview.Mode)
	response.AccessConverged = preview.ObservedAccess == response.DesiredAccess
	if preview.ObservedAccess != "" && !response.AccessConverged {
		response.Ready = false
		response.Reason = "preview_access_updating"
		response.Message = "Updating preview access…"
	}
	return response
}

func (s *Server) syncProjectDevelopmentTarget(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project, target projectDevelopmentSyncTargetInfo) (json.RawMessage, error) {
	if s.workspaces == nil {
		return nil, fmt.Errorf("project workspace store is not configured")
	}
	scope := projectWorkspaceScope(id, p)
	snapshot, err := s.projectWorkspaceSyncFiles(ctx, scope)
	if err != nil {
		return nil, err
	}
	// Binaries go only to components whose agent accepts base64; read their
	// bytes (inside the same revision fence) only when one does.
	binaryComponents := s.projectSyncBinaryComponents(ctx, id, target, snapshot.BinaryPaths)
	if len(binaryComponents) > 0 {
		if snapshot, err = s.projectWorkspaceSyncFilesWithBinaries(ctx, scope, true); err != nil {
			return nil, err
		}
	}
	// Mirror the monotonic source-revision fence onto the durable project
	// claim so it survives the project moving between replicas — the sandbox
	// rejects stale revisions, and a fence restarting at 1 on a new owner
	// would break that check (Phase C seeds it back on hydration).
	s.recordProjectClaimRevision(id, p.Name, snapshot.SourceRevision)
	files := snapshot.Files
	// Validate the instance exists in the workspace first (clear 404 vs proxy err).
	if err := s.validateDevelopmentInstance(ctx, c, target); err != nil {
		return nil, err
	}

	// Route files to each component's own sync verb
	// by workspacePath prefix (docs/app-studio-template-sandboxes.md §4.2).
	// Files outside every component (README, docs) sync nowhere.
	routed := routeProjectSyncFiles(files, target.Components)
	routedBinaries := routeProjectSyncFiles(snapshot.BinaryFiles, target.Components)
	routedDeleted := routeProjectSyncDeletedPaths(snapshot.DeletedPaths, target.Components)
	// A populated workspace whose files all fall outside every component
	// directory would "succeed" while shipping nothing to the sandbox — the
	// app never starts and nothing explains why. Fail with the expected
	// layout instead.
	if len(files) > 0 && countRoutedProjectSyncFiles(routed) == 0 {
		return nil, fmt.Errorf(
			"none of the %d workspace files are under a development component directory (%s); application source must live under those directories to reach the development sandbox",
			len(files), target.componentWorkspacePathSummary())
	}
	// Files landing in the right directory but written for the wrong runtime
	// fail silently otherwise: the sandbox image has no toolchain for them, the
	// start command finds nothing to run, and the pod simply never listens.
	if err := validateProjectSyncToolchains(routed, target.Components); err != nil {
		return nil, err
	}
	results := map[string]json.RawMessage{}
	for _, component := range target.sortedComponents() {
		componentFiles := routed[component]
		if binaryComponents[component] {
			componentFiles = appendProjectSyncBinaries(p.Name, component, componentFiles, routedBinaries[component])
		}
		request := projectSandboxSyncRequest{
			Files:        componentFiles,
			DeletedPaths: routedDeleted[component],
			SourceDigest: projectSandboxSyncDigest(componentFiles),
			Restart:      "auto",
		}
		body, err := s.postProjectComponentSync(ctx, id, target.dataPlaneRefFor(component), component, snapshot.SourceRevision, request)
		if err != nil {
			return nil, err
		}
		results[component] = json.RawMessage(body)
	}
	aggregated, err := json.Marshal(results)
	if err != nil {
		return nil, err
	}
	return aggregated, nil
}

// postProjectComponentSync sends one component's authoritative sync. The
// development agent also stamps revisions on plain syncs (the faros CLI, MCP
// dev_sync), which can move its applied revision past App Studio's FileStore
// revision; the agent then rejects the next authoritative sync with a 409.
// On such a conflict App Studio reads the applied revision from the
// component's status, continues numbering from applied+1, and retries once.
func (s *Server) postProjectComponentSync(ctx context.Context, id identity, ref dataPlaneRef, component string, workspaceRevision uint64, request projectSandboxSyncRequest) ([]byte, error) {
	request.SourceRevision = s.developmentSyncRevision(id, ref, workspaceRevision)
	post := func() ([]byte, int, error) {
		payload, err := json.Marshal(request)
		if err != nil {
			return nil, 0, fmt.Errorf("encode %s sync payload: %w", component, err)
		}
		body, status, err := s.dataPlanePostWithTimeout(ctx, id, ref, dataPlaneVerbSync, payload, projectSandboxComponentSyncTimeout)
		if err != nil {
			return nil, 0, fmt.Errorf("component %s: %w", component, err)
		}
		return body, status, nil
	}
	body, status, err := post()
	if err != nil {
		return nil, err
	}
	if status == http.StatusConflict && projectDevelopmentSyncRevisionConflict(body) {
		if applied, ok := s.projectComponentAppliedRevision(ctx, id, ref); ok && applied >= request.SourceRevision {
			request.SourceRevision = applied + 1
			s.adoptDevelopmentSyncRevision(id, ref, workspaceRevision, request.SourceRevision)
			if body, status, err = post(); err != nil {
				return nil, err
			}
		}
	}
	if err := validateProjectComponentSyncResponse(component, status, body); err != nil {
		return nil, err
	}
	var synced struct {
		SourceRevision uint64 `json:"sourceRevision"`
	}
	if json.Unmarshal(body, &synced) == nil && synced.SourceRevision > request.SourceRevision {
		s.adoptDevelopmentSyncRevision(id, ref, workspaceRevision, synced.SourceRevision)
	}
	return body, nil
}

// projectDevelopmentSyncRevisionConflict recognizes the development agent's
// two revision-fence rejections, the only 409s a renumbered retry can fix.
func projectDevelopmentSyncRevisionConflict(body []byte) bool {
	detail := strings.ToLower(string(body))
	return strings.Contains(detail, "older than the applied revision") ||
		strings.Contains(detail, "already applied with a different digest")
}

// projectComponentAppliedRevision reads the component's applied source
// revision from its status endpoint (the "process" data-plane verb). ok is
// false when the status is unreadable or reports no verified revision.
func (s *Server) projectComponentAppliedRevision(ctx context.Context, id identity, ref dataPlaneRef) (uint64, bool) {
	body, status, err := s.dataPlaneGet(ctx, id, ref, dataPlaneVerbProcess, 16<<10)
	if err != nil || status < 200 || status >= 300 {
		return 0, false
	}
	var process struct {
		SourceRevision uint64 `json:"sourceRevision"`
	}
	if json.Unmarshal(body, &process) != nil || process.SourceRevision == 0 {
		return 0, false
	}
	return process.SourceRevision, true
}

func developmentSyncRevisionKey(id identity, ref dataPlaneRef) string {
	return strings.Join([]string{id.clusterID, ref.Resource, ref.Name, ref.Component}, "/")
}

// developmentSyncRevision maps a FileStore revision onto the revision the
// component's development agent expects. The offset only grows, so the
// mapped revision stays monotonic with the FileStore's. Every caller that
// sends or compares a development agent revision must go through it.
func (s *Server) developmentSyncRevision(id identity, ref dataPlaneRef, workspaceRevision uint64) uint64 {
	if s == nil || workspaceRevision == 0 {
		return workspaceRevision
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return workspaceRevision + s.developmentSyncRevisionOffsets[developmentSyncRevisionKey(id, ref)]
}

// adoptDevelopmentSyncRevision records that workspaceRevision now maps to
// agentRevision, raising (never lowering) the component's offset.
func (s *Server) adoptDevelopmentSyncRevision(id identity, ref dataPlaneRef, workspaceRevision, agentRevision uint64) {
	if s == nil || agentRevision <= workspaceRevision {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.developmentSyncRevisionOffsets == nil {
		s.developmentSyncRevisionOffsets = map[string]uint64{}
	}
	key := developmentSyncRevisionKey(id, ref)
	if offset := agentRevision - workspaceRevision; offset > s.developmentSyncRevisionOffsets[key] {
		s.developmentSyncRevisionOffsets[key] = offset
	}
}

// projectDevelopmentSyncHTTPError preserves the upstream status so the
// run-sandbox seed path can retry only readiness races. Other callers retain
// the same human-readable error string through Error.
type projectDevelopmentSyncHTTPError struct {
	component string
	status    int
	detail    string
}

func (e *projectDevelopmentSyncHTTPError) Error() string {
	if e == nil {
		return "development sync failed"
	}
	return fmt.Sprintf("component %s sync returned %d: %s", e.component, e.status, e.detail)
}

func validateProjectComponentSyncResponse(component string, status int, body []byte) error {
	boundedBody := strings.TrimSpace(string(body))
	if len(boundedBody) > projectDevelopmentSyncErrorMaxBytes {
		boundedBody = boundedBody[:projectDevelopmentSyncErrorMaxBytes] + "..."
	}
	if status < 200 || status >= 300 {
		return &projectDevelopmentSyncHTTPError{component: component, status: status, detail: boundedBody}
	}
	var result projectSandboxSyncResult
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("component %s sync returned an invalid response: %w", component, err)
	}
	if failure := strings.TrimSpace(result.ReloadError); failure != "" {
		if len(failure) > projectDevelopmentSyncErrorMaxBytes {
			failure = failure[:projectDevelopmentSyncErrorMaxBytes] + "..."
		}
		return fmt.Errorf("component %s dependency reload failed: %s", component, failure)
	}
	return nil
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
func routeProjectSyncFiles(files []projectSandboxSyncFile, components map[string]projectTemplateComponent) map[string][]projectSandboxSyncFile {
	out := make(map[string][]projectSandboxSyncFile, len(components))
	for component, comp := range components {
		wp := path.Clean(strings.TrimSpace(comp.WorkspacePath))
		if wp == "." {
			out[component] = files
			continue
		}
		prefix := wp + "/"
		for _, f := range files {
			if strings.HasPrefix(f.Path, prefix) {
				out[component] = append(out[component], projectSandboxSyncFile{
					Path:     strings.TrimPrefix(f.Path, prefix),
					Content:  f.Content,
					Encoding: f.Encoding,
				})
			}
		}
	}
	return out
}

// routeProjectSyncDeletedPaths mirrors routeProjectSyncFiles for paths that
// disappeared from the provider-owned workspace. The infrastructure agent
// uses these only as an explicit deletion hint; its managed manifest remains
// authoritative and never removes runtime-generated directories.
func routeProjectSyncDeletedPaths(paths []string, components map[string]projectTemplateComponent) map[string][]string {
	out := make(map[string][]string, len(components))
	for component, comp := range components {
		wp := path.Clean(strings.TrimSpace(comp.WorkspacePath))
		if wp == "." {
			out[component] = append([]string(nil), paths...)
			sort.Strings(out[component])
			continue
		}
		prefix := wp + "/"
		for _, raw := range paths {
			clean := path.Clean(strings.TrimSpace(raw))
			if strings.HasPrefix(clean, prefix) {
				out[component] = append(out[component], strings.TrimPrefix(clean, prefix))
			}
		}
		sort.Strings(out[component])
	}
	return out
}

// projectToolchainManifests names the file every known toolchain needs before
// its component's start command can run: without it the dev process exits
// immediately (or never starts), the port stays closed, and the only symptom is
// an app that "looks up" while every request to it fails.
//
// Keyed by the toolchain half of the template's ${faros.devImage.<toolchain>}
// token. A toolchain absent from this map is not validated — an unknown
// toolchain must never block a sync, since the template, not App Studio, is the
// authority on what its sandbox can run.
var projectToolchainManifests = map[string]struct {
	// Files are the accepted manifest names; any one present satisfies the check.
	Files []string
	// Hint tells the caller what to write instead, in the terms the agent
	// needs to act on.
	Hint string
}{
	"node": {
		Files: []string{"package.json"},
		Hint:  "write a package.json whose \"dev\" or \"start\" script launches the server on $PORT",
	},
	"python": {
		Files: []string{"requirements.txt", "pyproject.toml", "Pipfile", "setup.py"},
		Hint:  "write a requirements.txt or pyproject.toml declaring the app's dependencies",
	},
	"go": {
		Files: []string{"go.mod"},
		Hint:  "write a go.mod at the component root",
	},
	"ruby": {
		Files: []string{"Gemfile"},
		Hint:  "write a Gemfile at the component root",
	},
}

// validateProjectSyncToolchains rejects a sync whose routed files cannot
// possibly run in the component's sandbox. It fires only when a component
// received files AND its toolchain is one we know AND that toolchain's manifest
// is absent — so an empty component (nothing written yet) and an unrecognized
// toolchain both pass untouched.
//
// This is the backstop for the contract being ignored rather than unavailable:
// the template declares the toolchain and start command, inspect_development_
// templates surfaces them, and the prompt states them — but none of that
// guarantees the generated code matches, and a mismatch is otherwise invisible
// until someone opens the app and finds nothing listening.
func validateProjectSyncToolchains(routed map[string][]projectSandboxSyncFile, components map[string]projectTemplateComponent) error {
	names := make([]string, 0, len(routed))
	for name := range routed {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		files := routed[name]
		if len(files) == 0 {
			continue
		}
		comp, ok := components[name]
		if !ok {
			continue
		}
		manifest, known := projectToolchainManifests[comp.Toolchain]
		if !known {
			continue
		}
		if projectSyncFilesContainManifest(files, manifest.Files) {
			continue
		}
		where := path.Clean(strings.TrimSpace(comp.WorkspacePath))
		if where == "." {
			where = "the workspace root"
		} else {
			where += "/"
		}
		return fmt.Errorf(
			"component %q runs a %s development sandbox but %s contains no %s — %s. The sandbox has no other toolchain installed and starts this component with: %s",
			name, comp.Toolchain, where,
			humanizeProjectManifestList(manifest.Files), manifest.Hint,
			summarizeProjectStartCommand(comp.StartCommand))
	}
	return nil
}

// projectSyncFilesContainManifest reports whether any accepted manifest sits at
// the component root. Paths are already component-relative here (the router
// strips the workspacePath prefix), so a root manifest has no separator — a
// nested one (e.g. "vendor/package.json") must not satisfy the check.
func projectSyncFilesContainManifest(files []projectSandboxSyncFile, accepted []string) bool {
	for _, f := range files {
		p := path.Clean(strings.TrimSpace(f.Path))
		if strings.Contains(p, "/") {
			continue
		}
		for _, name := range accepted {
			if p == name {
				return true
			}
		}
	}
	return false
}

// humanizeProjectManifestList renders accepted manifests as "a package.json" or
// "a requirements.txt, pyproject.toml, Pipfile, or setup.py".
func humanizeProjectManifestList(files []string) string {
	switch len(files) {
	case 0:
		return "manifest"
	case 1:
		return files[0]
	case 2:
		return files[0] + " or " + files[1]
	default:
		return strings.Join(files[:len(files)-1], ", ") + ", or " + files[len(files)-1]
	}
}

// projectStartCommandSummaryMaxChars bounds a start command in an error
// message. Templates may inline a long config shim (the application template's
// frontend embeds a base64 vite config), and the useful signal is the leading
// command, not the payload.
const projectStartCommandSummaryMaxChars = 160

func summarizeProjectStartCommand(cmd string) string {
	cmd = strings.Join(strings.Fields(cmd), " ")
	if cmd == "" {
		return "(the template declares no start command)"
	}
	if len(cmd) > projectStartCommandSummaryMaxChars {
		return cmd[:projectStartCommandSummaryMaxChars] + "..."
	}
	return cmd
}

// countRoutedProjectSyncFiles totals the files routed across all components.
func countRoutedProjectSyncFiles(routed map[string][]projectSandboxSyncFile) int {
	total := 0
	for _, files := range routed {
		total += len(files)
	}
	return total
}

// authorizeProjectDevelopmentPreviewTarget resolves the preview for a
// development environment: the template instance's own public URL — the dev
// overlay keeps the production route wiring, so the dev instance is served
// where a production one would be. See docs/app-studio-template-sandboxes.md §1.
func (s *Server) authorizeProjectDevelopmentPreviewTarget(ctx context.Context, c *asclient.Client, _ identity, _ *aiv1alpha1.Project, target projectDevelopmentSyncTargetInfo) (projectSandboxPreviewURLResponse, error) {
	return s.templateDevelopmentPreview(ctx, c, target)
}

// projectWorkspaceSyncFiles snapshots the workspace's text files (and the
// paths of its binaries) under the source-revision fence.
func (s *Server) projectWorkspaceSyncFiles(ctx context.Context, scope workspace.Scope) (projectWorkspaceSyncSnapshot, error) {
	return s.projectWorkspaceSyncFilesWithBinaries(ctx, scope, false)
}

// projectWorkspaceSyncFilesWithBinaries also reads binaries as base64
// entries when includeBinary is set.
func (s *Server) projectWorkspaceSyncFilesWithBinaries(ctx context.Context, scope workspace.Scope, includeBinary bool) (projectWorkspaceSyncSnapshot, error) {
	for attempt := 0; attempt < 3; attempt++ {
		revisionBefore, err := s.workspaces.SourceRevision(ctx, scope)
		if err != nil {
			return projectWorkspaceSyncSnapshot{}, err
		}
		snapshot, err := s.projectWorkspaceSyncFilesOnce(ctx, scope, revisionBefore, includeBinary)
		if err != nil {
			return projectWorkspaceSyncSnapshot{}, err
		}
		revisionAfter, err := s.workspaces.SourceRevision(ctx, scope)
		if err != nil {
			return projectWorkspaceSyncSnapshot{}, err
		}
		if revisionBefore == revisionAfter {
			return snapshot, nil
		}
	}
	return projectWorkspaceSyncSnapshot{}, errors.New("workspace changed while preparing development synchronization")
}

func (s *Server) projectWorkspaceSyncFilesOnce(ctx context.Context, scope workspace.Scope, revision uint64, includeBinary bool) (projectWorkspaceSyncSnapshot, error) {
	list, err := s.workspaces.ListFiles(ctx, scope, workspace.ListOptions{Limit: workspace.MaxListLimit})
	if err != nil {
		return projectWorkspaceSyncSnapshot{}, err
	}
	files := make([]projectSandboxSyncFile, 0, len(list.Files))
	var binaryFiles []projectSandboxSyncFile
	var binaryPaths []string
	for _, f := range list.Files {
		read, err := s.workspaces.ReadFile(ctx, scope, workspace.ReadOptions{Path: f.Path, MaxBytes: workspace.MaxWriteBytes})
		if err != nil {
			return projectWorkspaceSyncSnapshot{}, err
		}
		if read.Binary {
			binaryPaths = append(binaryPaths, read.Path)
			if !includeBinary || read.Size > hubmcp.BinaryFileMaxBytes {
				continue
			}
			data, err := s.workspaces.ReadFileBytes(ctx, scope, read.Path, hubmcp.BinaryFileMaxBytes)
			if err != nil {
				return projectWorkspaceSyncSnapshot{}, err
			}
			binaryFiles = append(binaryFiles, projectSandboxSyncFile{Path: read.Path, Content: base64.StdEncoding.EncodeToString(data), Encoding: hubmcp.EncodingBase64})
			continue
		}
		if read.Truncated {
			continue
		}
		files = append(files, projectSandboxSyncFile{Path: read.Path, Content: read.Content})
	}
	changed, err := s.workspaces.UncommittedPaths(ctx, scope)
	if err != nil {
		return projectWorkspaceSyncSnapshot{}, err
	}
	deleted := make([]string, 0)
	for _, changedPath := range changed {
		if _, err := s.workspaces.ReadFile(ctx, scope, workspace.ReadOptions{Path: changedPath, MaxBytes: workspace.MaxWriteBytes}); errors.Is(err, fs.ErrNotExist) {
			deleted = append(deleted, changedPath)
		} else if err != nil {
			return projectWorkspaceSyncSnapshot{}, err
		}
	}
	sort.Strings(deleted)
	return projectWorkspaceSyncSnapshot{Files: files, BinaryFiles: binaryFiles, BinaryPaths: binaryPaths, DeletedPaths: deleted, SourceRevision: revision}, nil
}

// projectSandboxSyncDigest is the component-local source identity shared with
// the infrastructure development agent: sorted path\0bytes\0 entries over the
// decoded file bytes (so a base64 entry hashes its binary content), without
// the App Studio workspacePath prefix.
func projectSandboxSyncDigest(files []projectSandboxSyncFile) string {
	entries := append([]projectSandboxSyncFile(nil), files...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	hash := sha256.New()
	for _, file := range entries {
		content := []byte(file.Content)
		if decoded, err := hubmcp.DecodeWireContent(file.Content, file.Encoding); err == nil {
			content = decoded
		}
		_, _ = hash.Write([]byte(file.Path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// projectSyncBinaryComponents returns the components that receive at least
// one binary and whose development agent accepts base64 sync. Components
// that cannot are logged once and keep receiving text only.
func (s *Server) projectSyncBinaryComponents(ctx context.Context, id identity, target projectDevelopmentSyncTargetInfo, binaryPaths []string) map[string]bool {
	if len(binaryPaths) == 0 {
		return nil
	}
	routed := routeProjectSyncFiles(projectSyncPathEntries(binaryPaths), target.Components)
	out := map[string]bool{}
	for _, component := range target.sortedComponents() {
		if len(routed[component]) == 0 {
			continue
		}
		ref := target.dataPlaneRefFor(component)
		if s.developmentAgentSupportsBase64(ctx, id, ref) {
			out[component] = true
			continue
		}
		s.noteSyncBinariesSkipped(id, ref, component, len(routed[component]))
	}
	return out
}

func projectSyncPathEntries(paths []string) []projectSandboxSyncFile {
	entries := make([]projectSandboxSyncFile, 0, len(paths))
	for _, p := range paths {
		entries = append(entries, projectSandboxSyncFile{Path: p})
	}
	return entries
}

// developmentAgentSupportsBase64 reads the component agent's /status (the
// "process" verb) for syncEncodings and caches the answer per component. An
// unreadable status answers false without caching, so a starting agent is
// asked again on the next sync.
func (s *Server) developmentAgentSupportsBase64(ctx context.Context, id identity, ref dataPlaneRef) bool {
	key := developmentSyncRevisionKey(id, ref)
	if supported, ok := s.syncBinary.Get(key); ok {
		return supported
	}
	body, status, err := s.dataPlaneGet(ctx, id, ref, dataPlaneVerbProcess, 64<<10)
	if err != nil || status < 200 || status >= 300 {
		return false
	}
	var agent struct {
		SyncEncodings []string `json:"syncEncodings"`
	}
	if json.Unmarshal(body, &agent) != nil {
		return false
	}
	supported := false
	for _, encoding := range agent.SyncEncodings {
		if strings.EqualFold(strings.TrimSpace(encoding), hubmcp.EncodingBase64) {
			supported = true
		}
	}
	s.syncBinary.Set(key, supported)
	return supported
}

func (s *Server) noteSyncBinariesSkipped(id identity, ref dataPlaneRef, component string, count int) {
	key := developmentSyncRevisionKey(id, ref)
	s.mu.Lock()
	if s.syncBinaryNotices == nil {
		s.syncBinaryNotices = map[string]bool{}
	}
	seen := s.syncBinaryNotices[key]
	s.syncBinaryNotices[key] = true
	s.mu.Unlock()
	if !seen {
		klog.Infof("development sync: component %s of %s/%s does not accept binary files (no base64 in its syncEncodings); %d binary file(s) are not synced to it", component, ref.Resource, ref.Name, count)
	}
}

// appendProjectSyncBinaries adds base64 entries to one component's text
// files within the agent's bounds (48 MiB decoded, 500 files per request).
// A binary past the bounds is left out (and logged) rather than failing the
// whole sync, so text edits always reach the sandbox.
func appendProjectSyncBinaries(project, component string, files, binaries []projectSandboxSyncFile) []projectSandboxSyncFile {
	var total int64
	for _, file := range files {
		total += int64(len(file.Content))
	}
	out := append([]projectSandboxSyncFile(nil), files...)
	var dropped []string
	for _, binary := range binaries {
		size := int64(base64.StdEncoding.DecodedLen(len(binary.Content)))
		if len(out) >= hubmcp.BundleMaxFiles || total+size > hubmcp.BundleMaxBytes {
			dropped = append(dropped, binary.Path)
			continue
		}
		total += size
		out = append(out, binary)
	}
	if len(dropped) > 0 {
		klog.Warningf("development sync for project %s component %s: %d binary file(s) exceed one sync's bounds and were not sent: %s", project, component, len(dropped), strings.Join(dropped, ", "))
	}
	return out
}

func (s *Server) projectAssistantPreviewRefreshNeeded(_ context.Context, _ workspace.Scope, _ string, _ bool, toolCalls []projectToolCallStreamEvent) bool {
	return projectAssistantToolCallsRequireDevelopmentSync(toolCalls)
}

func shouldSyncDevelopmentAfterTool(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile, projectToolMoveFile,
		projectToolImportAttachment, projectToolDownloadFile,
		projectToolSelectTemplate, projectActionWorkspaceSync, projectActionRestoreWorkspace, projectActionWorkspaceFileWrite:
		return true
	default:
		return false
	}
}

func (s *Server) scheduleDevelopmentSyncAfterMutation(id identity, p *aiv1alpha1.Project, name string) {
	s.scheduleDevelopmentSyncAfterMutationWithCompletion(id, p, name, nil)
}

func (s *Server) scheduleDevelopmentSyncAfterMutationWithCompletion(
	id identity,
	p *aiv1alpha1.Project,
	name string,
	complete func(error),
) bool {
	if s == nil || p == nil || !shouldSyncDevelopmentAfterTool(name) {
		return false
	}
	project := p.DeepCopy()
	key := developmentSyncFailureKey(id, project)
	// A mutation landing on the tree means the assistant is now editing what
	// is actually on disk; a rebuild notice from the takeover has done its job.
	s.clearWorkspaceRebuild(id, project)
	s.mu.Lock()
	hook := s.developmentSyncAfterMutation
	if s.developmentSyncTails == nil {
		s.developmentSyncTails = map[string]chan struct{}{}
	}
	previous := s.developmentSyncTails[key]
	done := make(chan struct{})
	s.developmentSyncTails[key] = done
	s.mu.Unlock()
	go func() {
		if previous != nil {
			<-previous
		}
		defer func() {
			close(done)
			s.mu.Lock()
			if s.developmentSyncTails[key] == done {
				delete(s.developmentSyncTails, key)
			}
			s.mu.Unlock()
		}()
		var err error
		if hook != nil {
			err = hook(id, project, name)
		} else {
			err = s.syncDevelopmentAfterMutation(id, project, name)
		}
		if complete != nil {
			complete(err)
		}
	}()
	return true
}

func (s *Server) syncDevelopmentAfterMutation(id identity, p *aiv1alpha1.Project, name string) error {
	if s.gql == nil {
		err := errors.New("tenant GraphQL client is not configured")
		s.recordDevelopmentSyncFailure(id, p, fmt.Sprintf("the workspace sync after %s could not start: %v", projectToolBaseName(name), err))
		klog.V(2).Infof("development sandbox sync after %s skipped for project %s: %v", projectToolBaseName(name), p.Name, err)
		return err
	}
	c, err := s.clientFor(id)
	if err != nil {
		s.recordDevelopmentSyncFailure(id, p, fmt.Sprintf("the workspace sync after %s could not start: %v", projectToolBaseName(name), err))
		klog.V(2).Infof("development sandbox sync after %s failed for project %s: %v", projectToolBaseName(name), p.Name, err)
		return err
	}
	return s.syncDevelopmentAfterMutationWithClient(c, id, p, name)
}

func (s *Server) syncDevelopmentAfterMutationWithClient(c *asclient.Client, id identity, p *aiv1alpha1.Project, name string) error {
	lock := s.developmentSyncLock(id, p)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), projectSandboxSyncTimeout)
	defer cancel()
	target, err := s.projectDevelopmentTarget(ctx, c, p, id)
	if err != nil {
		s.recordDevelopmentSyncFailure(id, p, fmt.Sprintf("the workspace sync after %s could not resolve its development target: %v", projectToolBaseName(name), err))
		klog.V(2).Infof("development sync after %s skipped for project %s: %v", projectToolBaseName(name), p.Name, err)
		return err
	}
	if _, err := s.syncProjectDevelopmentTarget(ctx, c, id, p, target); err != nil {
		// A failed post-mutation sync means the user's edit never reached the
		// development sandbox — warn, don't bury it at debug verbosity, and
		// record it so the assistant's own verification reports it instead of
		// silently diagnosing a stale sandbox.
		klog.Warningf("development sync after %s failed for project %s: %v", projectToolBaseName(name), p.Name, err)
		s.recordDevelopmentSyncFailure(id, p, fmt.Sprintf("the last workspace sync after %s failed, so the development sandbox is still running the previous code: %v", projectToolBaseName(name), err))
		return err
	}
	s.clearDevelopmentSyncFailure(id, p)
	return nil
}

// developmentSyncFailureKey scopes a recorded failure to one tenant's project,
// matching developmentSyncLock's key.
func developmentSyncFailureKey(id identity, project *aiv1alpha1.Project) string {
	if project == nil {
		return id.orgUUID + "/" + id.workspaceUUID + "//"
	}
	return id.orgUUID + "/" + id.workspaceUUID + "/" + project.Name + "/" + string(project.UID)
}

// recordDevelopmentSyncFailure stores the reason the last background sync
// failed. Overwrites any previous reason: only the latest matters.
func (s *Server) recordDevelopmentSyncFailure(id identity, project *aiv1alpha1.Project, reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.developmentSyncFailures == nil {
		s.developmentSyncFailures = map[string]string{}
	}
	s.developmentSyncFailures[developmentSyncFailureKey(id, project)] = reason
}

// clearDevelopmentSyncFailure drops a recorded failure once a sync succeeds,
// so a stale blocker never outlives the problem it described.
func (s *Server) clearDevelopmentSyncFailure(id identity, project *aiv1alpha1.Project) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.developmentSyncFailures, developmentSyncFailureKey(id, project))
}

// lastDevelopmentSyncFailure returns the recorded failure for a project, if any.
func (s *Server) lastDevelopmentSyncFailure(id identity, project *aiv1alpha1.Project) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.developmentSyncFailures[developmentSyncFailureKey(id, project)]
}
