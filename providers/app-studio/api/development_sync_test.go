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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

// devComponentPaths builds a component map from name → workspacePath for tests
// that only exercise path routing. Tests covering the toolchain contract build
// projectTemplateComponent values directly.
func devComponentPaths(paths map[string]string) map[string]projectTemplateComponent {
	out := make(map[string]projectTemplateComponent, len(paths))
	for name, wp := range paths {
		out[name] = projectTemplateComponent{WorkspacePath: wp}
	}
	return out
}

func TestRouteProjectSyncFilesDropsFilesOutsideEveryComponent(t *testing.T) {
	components := devComponentPaths(map[string]string{"backend": "api", "frontend": "web"})
	files := []projectSandboxSyncFile{
		{Path: "backend/index.js", Content: "x"},
		{Path: "frontend/src/App.jsx", Content: "y"},
		{Path: "README.md", Content: "z"},
	}

	routed := routeProjectSyncFiles(files, components)

	if got := countRoutedProjectSyncFiles(routed); got != 0 {
		t.Fatalf("countRoutedProjectSyncFiles = %d, want 0 — files under directories matching no component workspacePath must route nowhere", got)
	}
}

func TestRouteProjectSyncFilesCountsRoutedFiles(t *testing.T) {
	components := devComponentPaths(map[string]string{"backend": "api", "frontend": "web"})
	files := []projectSandboxSyncFile{
		{Path: "api/index.js", Content: "x"},
		{Path: "web/src/App.jsx", Content: "y"},
		{Path: "README.md", Content: "z"},
	}

	routed := routeProjectSyncFiles(files, components)

	if got := countRoutedProjectSyncFiles(routed); got != 2 {
		t.Fatalf("countRoutedProjectSyncFiles = %d, want 2", got)
	}
	if len(routed["backend"]) != 1 || routed["backend"][0].Path != "index.js" {
		t.Errorf("backend routed = %+v, want [index.js]", routed["backend"])
	}
	if len(routed["frontend"]) != 1 || routed["frontend"][0].Path != "src/App.jsx" {
		t.Errorf("frontend routed = %+v, want [src/App.jsx]", routed["frontend"])
	}
}

func TestRouteProjectSyncDeletedPathsAndComponentDigest(t *testing.T) {
	components := devComponentPaths(map[string]string{"backend": "api", "frontend": "web", "root": "."})
	deleted := routeProjectSyncDeletedPaths([]string{"api/old.go", "web/old.tsx", "README.md"}, components)
	if got := strings.Join(deleted["backend"], ","); got != "old.go" {
		t.Fatalf("backend deleted paths = %q, want old.go", got)
	}
	if got := strings.Join(deleted["frontend"], ","); got != "old.tsx" {
		t.Fatalf("frontend deleted paths = %q, want old.tsx", got)
	}
	if got := strings.Join(deleted["root"], ","); got != "README.md,api/old.go,web/old.tsx" {
		t.Fatalf("root deleted paths = %q, want sorted workspace paths", got)
	}
	first := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "b.txt", Content: "b"}, {Path: "a.txt", Content: "a"}})
	second := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "a.txt", Content: "a"}, {Path: "b.txt", Content: "b"}})
	if first == "" || first != second {
		t.Fatalf("component digest = %q/%q, want stable sorted digest", first, second)
	}
}

func TestComponentWorkspacePathSummary(t *testing.T) {
	target := projectDevelopmentSyncTargetInfo{
		Components: devComponentPaths(map[string]string{"frontend": "web", "backend": "api"}),
	}
	if got, want := target.componentWorkspacePathSummary(), "backend → api/, frontend → web/"; got != want {
		t.Errorf("componentWorkspacePathSummary = %q, want %q", got, want)
	}

	root := projectDevelopmentSyncTargetInfo{Components: devComponentPaths(map[string]string{"app": "."})}
	if got, want := root.componentWorkspacePathSummary(), "app → the workspace root"; got != want {
		t.Errorf("componentWorkspacePathSummary = %q, want %q", got, want)
	}
}

func TestProjectAssistantTemplateComponentsIsNilSafe(t *testing.T) {
	ctx := context.Background()

	if got := projectAssistantTemplateComponents(ctx, projectAssistantRunRequest{}); got != nil {
		t.Errorf("nil project/client: got %v, want nil", got)
	}
	if got := projectAssistantTemplateComponents(ctx, projectAssistantRunRequest{
		Project: &aiv1alpha1.Project{},
	}); got != nil {
		t.Errorf("project without template: got %v, want nil", got)
	}
	if got := projectAssistantTemplateComponents(ctx, projectAssistantRunRequest{
		Project: &aiv1alpha1.Project{
			Spec: aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "  "}},
		},
	}); got != nil {
		t.Errorf("blank template name: got %v, want nil", got)
	}
}

func TestSystemPromptCarriesComponentDirectoryContract(t *testing.T) {
	p := &aiv1alpha1.Project{}
	p.Name = "demo"
	p.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}

	prompt := projectSystemPromptForMode(p, nil, projectAssistantCollaborationModeDefault, false)

	for _, required := range []string{
		"developmentComponents",
		"files outside every component directory are not synchronized to that preview runtime",
		"An active codingEnvironment is separate",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("system prompt for a template-backed project missing %q", required)
		}
	}
}

// The Go-in-a-Node-sandbox failure: source lands in the right directory, so the
// existing "nothing routed anywhere" check passes, but the sandbox image has no
// toolchain for it. Without this guard the sync succeeds, the dev process never
// listens, and the only symptom is an app whose API silently returns nothing.
func TestValidateProjectSyncToolchainsRejectsWrongLanguageSource(t *testing.T) {
	components := map[string]projectTemplateComponent{
		"backend": {WorkspacePath: "api", Toolchain: "node", StartCommand: "npm run dev || npm start"},
	}
	routed := map[string][]projectSandboxSyncFile{
		"backend": {
			{Path: "main.go", Content: "package main"},
			{Path: "go.mod", Content: "module app"},
			{Path: "Dockerfile", Content: "FROM golang"},
		},
	}

	err := validateProjectSyncToolchains(routed, components)
	if err == nil {
		t.Fatal("validateProjectSyncToolchains = nil, want an error for Go source in a node component")
	}
	for _, want := range []string{"backend", "node", "api/", "package.json", "npm run dev || npm start"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q — the message must name the component, its toolchain, and what to write", err, want)
		}
	}
}

func TestValidateProjectSyncToolchainsAcceptsMatchingSource(t *testing.T) {
	components := map[string]projectTemplateComponent{
		"backend":  {WorkspacePath: "api", Toolchain: "node", StartCommand: "npm run dev"},
		"frontend": {WorkspacePath: "web", Toolchain: "node", StartCommand: "npm run dev"},
	}
	routed := map[string][]projectSandboxSyncFile{
		"backend":  {{Path: "package.json", Content: "{}"}, {Path: "server.js", Content: "x"}},
		"frontend": {{Path: "package.json", Content: "{}"}},
	}
	if err := validateProjectSyncToolchains(routed, components); err != nil {
		t.Fatalf("validateProjectSyncToolchains = %v, want nil", err)
	}
}

// A component nobody has written to yet must not block the sync — that is the
// normal state while an assistant builds one component at a time.
func TestValidateProjectSyncToolchainsIgnoresEmptyComponents(t *testing.T) {
	components := map[string]projectTemplateComponent{
		"backend":  {WorkspacePath: "api", Toolchain: "node"},
		"frontend": {WorkspacePath: "web", Toolchain: "node"},
	}
	routed := map[string][]projectSandboxSyncFile{
		"frontend": {{Path: "package.json", Content: "{}"}},
	}
	if err := validateProjectSyncToolchains(routed, components); err != nil {
		t.Fatalf("validateProjectSyncToolchains = %v, want nil for an untouched component", err)
	}
}

// The template, not App Studio, is the authority on what its sandbox can run:
// an unrecognized toolchain must never block a sync.
func TestValidateProjectSyncToolchainsSkipsUnknownToolchain(t *testing.T) {
	components := map[string]projectTemplateComponent{
		"backend": {WorkspacePath: "api", Toolchain: "elixir"},
		"other":   {WorkspacePath: "svc"}, // template declared no parseable devImage
	}
	routed := map[string][]projectSandboxSyncFile{
		"backend": {{Path: "main.ex", Content: "x"}},
		"other":   {{Path: "main.bin", Content: "x"}},
	}
	if err := validateProjectSyncToolchains(routed, components); err != nil {
		t.Fatalf("validateProjectSyncToolchains = %v, want nil for an unknown toolchain", err)
	}
}

// A manifest must sit at the component root: the dev process runs there, so a
// nested one (a vendored dependency, a subpackage) does not make it runnable.
func TestValidateProjectSyncToolchainsRequiresRootManifest(t *testing.T) {
	components := map[string]projectTemplateComponent{
		"backend": {WorkspacePath: "api", Toolchain: "node", StartCommand: "npm start"},
	}
	routed := map[string][]projectSandboxSyncFile{
		"backend": {{Path: "vendor/dep/package.json", Content: "{}"}},
	}
	if err := validateProjectSyncToolchains(routed, components); err == nil {
		t.Fatal("validateProjectSyncToolchains = nil, want an error when package.json is only nested")
	}
}

func TestProjectTemplateToolchain(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"${faros.devImage.node}", "node"},
		{"  ${faros.devImage.python}  ", "python"},
		{"${faros.devImage.dotnet-8}", "dotnet-8"},
		{"docker.io/library/node:22-bookworm", ""}, // a literal image is not a token
		{"${faros.devAgentImage}", ""},
		{"", ""},
	} {
		if got := projectTemplateToolchain(tc.in); got != tc.want {
			t.Errorf("projectTemplateToolchain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A background sync failure used to reach klog and nothing else, so the sandbox
// kept serving stale code while the assistant verified it as healthy and
// debugged source that was never deployed. Verification must lead with it.
func TestDevelopmentSyncFailureSurfacesAsVerificationBlocker(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "project-uid-demo"

	if got := server.lastDevelopmentSyncFailure(id, project); got != "" {
		t.Fatalf("lastDevelopmentSyncFailure = %q, want empty before any failure", got)
	}

	server.recordDevelopmentSyncFailure(id, project, "the last workspace sync after create_file failed: boom")
	runCtx := projectAssistantWorkflowRunContext{Server: server, Project: project, Identity: id}
	if got := projectAssistantLastSyncFailure(runCtx); !strings.Contains(got, "boom") {
		t.Fatalf("projectAssistantLastSyncFailure = %q, want the recorded reason", got)
	}

	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		RunContext: runCtx,
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "ready",
			Summary:    "runtime is ready",
			PreviewURL: "https://demo.example",
		},
	})
	if err != nil {
		t.Fatalf("formatProjectAssistantRuntimeVerification: %v", err)
	}
	if result.Status != "not_ready" {
		t.Errorf("status = %q, want not_ready — a ready sandbox running stale code is not verified", result.Status)
	}
	if len(result.Blockers) == 0 || !strings.Contains(result.Blockers[0], "boom") {
		t.Errorf("blockers = %v, want the sync failure reported first", result.Blockers)
	}

	// A later successful sync must clear it, so the blocker never outlives the
	// problem it described.
	server.clearDevelopmentSyncFailure(id, project)
	result, err = formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		RunContext: runCtx,
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "ready",
			Summary:    "runtime is ready",
			PreviewURL: "https://demo.example",
		},
	})
	if err != nil {
		t.Fatalf("formatProjectAssistantRuntimeVerification after clear: %v", err)
	}
	for _, b := range result.Blockers {
		if strings.Contains(b, "boom") {
			t.Errorf("blockers = %v, want the cleared sync failure gone", result.Blockers)
		}
	}
}

func TestDevelopmentSyncSchedulingPreservesMutationOrder(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "project-uid-demo"

	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	server.developmentSyncAfterMutation = func(_ identity, _ *aiv1alpha1.Project, name string) error {
		switch name {
		case projectToolEditFile:
			close(firstEntered)
			<-releaseFirst
		case projectToolSelectTemplate:
			close(secondEntered)
		}
		return nil
	}

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	if !server.scheduleDevelopmentSyncAfterMutationWithCompletion(id, project, projectToolEditFile, func(err error) { firstDone <- err }) {
		t.Fatal("first development sync was not scheduled")
	}
	if !server.scheduleDevelopmentSyncAfterMutationWithCompletion(id, project, projectToolSelectTemplate, func(err error) { secondDone <- err }) {
		t.Fatal("second development sync was not scheduled")
	}

	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first development sync did not start")
	}
	select {
	case <-secondEntered:
		t.Fatal("second development sync overtook the first mutation")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	for name, done := range map[string]<-chan error{"first": firstDone, "second": secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s development sync: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s development sync did not complete", name)
		}
	}
}

// A workspace the sandbox cannot run is the caller's problem, not a gateway
// failure: it must answer 4xx with the reason in the body. Reported as 502 the
// message never reached anyone — the edge in front of the hub swaps origin 502
// bodies for its own error page, which is how "no package.json" surfaced to
// REST callers as a bare "error code: 502".
func TestValidateProjectSyncToolchainsIsAPreconditionFailure(t *testing.T) {
	components := map[string]projectTemplateComponent{
		"app": {WorkspacePath: ".", Toolchain: "node", StartCommand: "npm run dev"},
	}
	routed := map[string][]projectSandboxSyncFile{
		"app": {{Path: "main.go", Content: "package main"}, {Path: "go.mod", Content: "module app"}},
	}
	err := validateProjectSyncToolchains(routed, components)
	var precondition *projectDevelopmentSyncPreconditionError
	if !errors.As(err, &precondition) {
		t.Fatalf("validateProjectSyncToolchains = %T %v, want a precondition error", err, err)
	}
	for _, want := range []string{`component "app" has no package.json`, "Node.js", "the workspace root", "skip the sandbox and promote"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	rec := httptest.NewRecorder()
	writeDevelopmentSyncError(rec, err)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("precondition status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	var status metav1.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || status.Reason != "UnprocessableEntity" || !strings.Contains(status.Message, "package.json") {
		t.Fatalf("precondition body = %s (decode err %v), want a Status naming package.json", rec.Body.String(), err)
	}
}

func TestWriteDevelopmentSyncErrorMapsSandboxRejectionsAndFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "layout precondition", err: &projectDevelopmentSyncPreconditionError{msg: "nothing under a component directory"}, want: http.StatusUnprocessableEntity},
		{name: "sandbox rejected payload", err: &projectDevelopmentSyncHTTPError{component: "app", status: http.StatusBadRequest, detail: "package.json missing"}, want: http.StatusUnprocessableEntity},
		{name: "sandbox unprocessable", err: &projectDevelopmentSyncHTTPError{component: "app", status: http.StatusUnprocessableEntity, detail: "bad manifest"}, want: http.StatusUnprocessableEntity},
		{name: "sandbox revision conflict", err: &projectDevelopmentSyncHTTPError{component: "app", status: http.StatusConflict, detail: "older than the applied revision"}, want: http.StatusConflict},
		{name: "sandbox failed", err: &projectDevelopmentSyncHTTPError{component: "app", status: http.StatusInternalServerError, detail: "boom"}, want: http.StatusBadGateway},
		{name: "sandbox unreachable", err: fmt.Errorf("component app: development data plane sync: dial tcp: connection refused"), want: http.StatusBadGateway},
		{name: "wrapped precondition", err: fmt.Errorf("sync: %w", &projectDevelopmentSyncPreconditionError{msg: "x"}), want: http.StatusUnprocessableEntity},
		{name: "instance missing", err: apierrors.NewNotFound(schema.GroupResource{Group: "infrastructure.faros.sh", Resource: "instances"}, "demo-dev"), want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeDevelopmentSyncError(rec, test.err)
			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, test.want, rec.Body.String())
			}
			var status metav1.Status
			if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || status.Message != test.err.Error() {
				t.Fatalf("body %s does not carry the error %q (decode err %v)", rec.Body.String(), test.err, err)
			}
		})
	}
}

// Files outside every component are a workspace-layout problem the caller
// fixes, so the check must report through the same precondition path.
func TestSyncProjectDevelopmentTargetRejectsUnroutedWorkspaceAsPrecondition(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", clusterID: "cluster-a"}
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")}}
	scope := projectWorkspaceScope(id, p)
	if err := workspaces.ApplyFiles(context.Background(), scope, []workspace.File{{Path: "docs/notes.md", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	dyn := publishingTestDynamic(publishingTestTarget("demo-dev", "uid", "private", ""))
	s := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, workspaces: workspaces}
	target := projectDevelopmentSyncTargetInfo{
		ResourceName: "demo-dev",
		Components:   map[string]projectTemplateComponent{"api": {WorkspacePath: "api", Toolchain: "node"}},
	}
	target.APIVersion = publishingTestTargetGVR.GroupVersion().String()
	target.Kind = "Instance"
	target.Resource = "instances"
	_, err := s.syncProjectDevelopmentTarget(context.Background(), asclient.NewFromDynamic(dyn), id, p, target)
	var precondition *projectDevelopmentSyncPreconditionError
	if !errors.As(err, &precondition) || !strings.Contains(err.Error(), "none of the 1 workspace files") {
		t.Fatalf("sync error = %T %v, want the unrouted-workspace precondition", err, err)
	}
}
