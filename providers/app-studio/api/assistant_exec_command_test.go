/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestNormalizeProjectAssistantExecCommandInput(t *testing.T) {
	tests := []struct {
		name     string
		input    *projectAssistantExecCommandInput
		wantErr  string
		wantWork string
		wantTime int
	}{
		{name: "defaults", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test", "./..."}}, wantWork: "", wantTime: projectAssistantExecDefaultTimeout},
		{name: "cleans component relative workdir", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}, Workdir: "./internal"}, wantWork: "internal", wantTime: projectAssistantExecDefaultTimeout},
		{name: "rejects parent", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}, Workdir: "../other"}, wantErr: "under the selected component"},
		{name: "rejects absolute", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}, Workdir: "/workspace"}, wantErr: "relative path"},
		{name: "rejects windows separator", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}, Workdir: `internal\\test`}, wantErr: "relative path"},
		{name: "allows explicit interpreter argv", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"sh", "-c", "go test"}}, wantWork: "", wantTime: projectAssistantExecDefaultTimeout},
		{name: "rejects timeout", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}, TimeoutSeconds: projectAssistantExecMaxTimeout + 1}, wantErr: "timeoutSeconds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, blockers := normalizeProjectAssistantExecCommandInput(tt.input)
			if tt.wantErr == "" {
				if len(blockers) != 0 {
					t.Fatalf("blockers = %v", blockers)
				}
				if got.Workdir != tt.wantWork || got.TimeoutSeconds != tt.wantTime {
					t.Fatalf("normalized input = %#v", got)
				}
				return
			}
			if !strings.Contains(strings.Join(blockers, "; "), tt.wantErr) {
				t.Fatalf("blockers = %v, want %q", blockers, tt.wantErr)
			}
		})
	}
}

func TestProjectAssistantExecCommandContractAndPolicy(t *testing.T) {
	spec, ok := projectAssistantWorkflowToolSpec(projectToolExecCommand)
	if !ok {
		t.Fatal("exec_command workflow spec is missing")
	}
	if spec.Risk != projectAssistantToolRiskRuntime || spec.ParallelSafe {
		t.Fatalf("exec spec = %#v, want effectful exclusive runtime tool", spec)
	}
	if !projectAssistantToolHasEffect(spec) {
		t.Fatal("exec_command must be effectful")
	}
	debugging := projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging)
	if debugging.AllowsTool(spec) {
		t.Fatal("debugging tool catalog must not expose exec_command")
	}
	implementation := projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	if !implementation.AllowsTool(spec) {
		t.Fatal("implementation tool catalog must expose exec_command")
	}
	for _, tt := range []struct {
		mode store.AssistantApprovalMode
		want projectAssistantPermissionDecision
	}{
		{mode: "", want: projectAssistantPermissionAllow},
		{mode: store.AssistantApprovalModeOnRequest, want: projectAssistantPermissionAllow},
		{mode: store.AssistantApprovalModeAlwaysAsk, want: projectAssistantPermissionAsk},
		{mode: store.AssistantApprovalModeNever, want: projectAssistantPermissionDeny},
		// Existing runs may still carry this value while the legacy run API is
		// being retired; it remains equivalent to Allow but is not a new
		// preference API option.
		{mode: store.AssistantApprovalModeAutoApprove, want: projectAssistantPermissionAllow},
	} {
		if got := projectAssistantPermissionForV2(spec, tt.mode, nil, nil, false); got != tt.want {
			t.Fatalf("exec_command permission for %q = %q, want %q", tt.mode, got, tt.want)
		}
	}
	if got := projectAssistantToolsForCollaborationMode([]projectAssistantTool{projectAssistantToolFunc{spec: spec}}, projectAssistantCollaborationModePlan); len(got) != 0 {
		t.Fatal("plan mode must hide exec_command")
	}
}

func TestProjectAssistantExecCommandSandboxPresentationPinsWorkspace(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetSandbox(&projectAssistantRunSandbox{
		target: projectDevelopmentSyncTargetInfo{
			Components: map[string]projectTemplateComponent{
				projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."},
			},
		},
	})
	tool, err := newProjectAssistantExecCommandGraphTool(projectAssistantWorkflowRunContext{RunState: state})
	if err != nil {
		t.Fatalf("create sandbox exec tool: %v", err)
	}
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("read sandbox exec tool info: %v", err)
	}
	if !strings.Contains(info.Desc, `ALWAYS pass component="workspace"`) || strings.Contains(info.Desc, "for example, backend or frontend") {
		t.Fatalf("sandbox exec description = %q, want an explicit workspace-only contract", info.Desc)
	}
	for _, want := range []string{"Go, Node.js, and Python", "has no public preview", "MUST NOT mutate source files", "gofmt -d", "never gofmt -w"} {
		if !strings.Contains(info.Desc, want) {
			t.Fatalf("sandbox exec description = %q, want %q", info.Desc, want)
		}
	}

	parametersJSON, ok := info.Extra[projectEinoToolParametersExtraKey].(string)
	if !ok || parametersJSON == "" {
		t.Fatalf("sandbox exec parameters metadata = %#v, want JSON schema", info.Extra)
	}
	var parameters map[string]any
	if err := json.Unmarshal([]byte(parametersJSON), &parameters); err != nil {
		t.Fatalf("decode sandbox exec parameters: %v", err)
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("sandbox exec parameters properties = %#v", parameters["properties"])
	}
	component, ok := properties["component"].(map[string]any)
	if !ok {
		t.Fatalf("sandbox exec component schema = %#v", properties["component"])
	}
	if got := component["description"]; got != "The active per-run universal sandbox has exactly one component: workspace. Always use workspace." {
		t.Fatalf("sandbox component description = %#v", got)
	}
	enum, ok := component["enum"].([]any)
	if !ok || len(enum) != 1 || enum[0] != projectAssistantRunSandboxWorkspaceVerb {
		t.Fatalf("sandbox component enum = %#v, want [workspace]", component["enum"])
	}

	generated, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("generate sandbox exec schema: %v", err)
	}
	generatedComponent, ok := generated.Properties.Get("component")
	if !ok || len(generatedComponent.Enum) != 1 || generatedComponent.Enum[0] != projectAssistantRunSandboxWorkspaceVerb {
		t.Fatalf("generated sandbox component schema = %#v, want workspace enum", generatedComponent)
	}
}

func TestProjectAssistantFirstExecLazilyInitializesSandboxExactlyOnce(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	client := &sandboxClientFake{execResponse: projectSandboxExecResponse{SessionID: "exec-session", State: "succeeded", Stdout: "EXEC_LAZY_OK"}}
	setupCalls := 0
	state.ConfigureSandboxCapability(CodingSandboxEligibility{
		Eligible: true, ProviderExportPath: projectAssistantPlatformInfrastructureExportPath, TransportGeneration: projectAssistantSandboxTransportGeneration,
	}, func(context.Context) (*projectAssistantRunSandbox, func(), error) {
		setupCalls++
		return &projectAssistantRunSandbox{
			client: client, runState: state,
			target:   projectDevelopmentSyncTargetInfo{Components: map[string]projectTemplateComponent{projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."}}},
			metadata: projectAssistantRunSandboxMetadata{Status: "active", RemoteRevision: 7, RemoteDigest: "sha256:remote"},
		}, func() {}, nil
	})
	run := execProjectAssistantCommand(projectAssistantWorkflowRunContext{AssistantRunID: "run-lazy-exec", RunState: state})
	input := &projectAssistantExecCommandInput{Component: projectAssistantRunSandboxWorkspaceVerb, Argv: []string{"go", "test", "./..."}}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := run(context.Background(), input)
		if err != nil || result.Status != "succeeded" || strings.Join(result.Stdout, "\n") != "EXEC_LAZY_OK" {
			t.Fatalf("exec attempt %d result = %#v, err=%v", attempt+1, result, err)
		}
	}
	client.mu.Lock()
	execCalls := client.execCalls
	client.mu.Unlock()
	if setupCalls != 1 || execCalls != 2 {
		t.Fatalf("lazy exec setup calls=%d exec calls=%d, want one setup and two commands", setupCalls, execCalls)
	}
}

func TestProjectAssistantExecCanceledDuringSandboxSetupIsNotReportedFailed(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	runCtx, cancelRun := context.WithCancel(context.Background())
	setupStarted := make(chan struct{})
	state.ConfigureSandboxCapabilityWithContext(runCtx, CodingSandboxEligibility{Eligible: true}, func(ctx context.Context) (*projectAssistantRunSandbox, func(), error) {
		close(setupStarted)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})
	run := execProjectAssistantCommand(projectAssistantWorkflowRunContext{AssistantRunID: "run-canceled-setup", RunState: state})
	resultCh := make(chan *projectAssistantExecCommandResult, 1)
	go func() {
		result, _ := run(runCtx, &projectAssistantExecCommandInput{Component: projectAssistantRunSandboxWorkspaceVerb, Argv: []string{"go", "test", "./..."}})
		resultCh <- result
	}()
	<-setupStarted
	cancelRun()
	result := <-resultCh
	if result == nil || result.Status != "canceled" || result.Summary != "Command canceled before the coding sandbox was ready." {
		t.Fatalf("canceled setup result = %#v, want canceled command", result)
	}
}

func TestProjectAssistantExecCommandMultiComponentPresentationRemainsGeneric(t *testing.T) {
	tool, err := newProjectAssistantExecCommandGraphTool(projectAssistantWorkflowRunContext{})
	if err != nil {
		t.Fatalf("create project exec tool: %v", err)
	}
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("read project exec tool info: %v", err)
	}
	if strings.Contains(info.Desc, "active per-run universal sandbox") || strings.Contains(info.Desc, `ALWAYS pass component="workspace"`) {
		t.Fatalf("ordinary exec description was narrowed to run sandbox: %q", info.Desc)
	}
	generated, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("generate project exec schema: %v", err)
	}
	component, ok := generated.Properties.Get("component")
	if !ok {
		t.Fatal("ordinary exec schema is missing component")
	}
	if len(component.Enum) != 0 {
		t.Fatalf("ordinary exec component enum = %#v, want no runtime-specific restriction", component.Enum)
	}
}

func TestProjectAssistantRunSandboxExecAcceptsOnlyWorkspaceComponent(t *testing.T) {
	fake := &assistantRunSandboxExecGateFake{}
	sandbox := &projectAssistantRunSandbox{
		client: fake,
		target: projectDevelopmentSyncTargetInfo{
			Resource:     "instances",
			ResourceName: "as-run-project-1234567890ab",
			Components: map[string]projectTemplateComponent{
				projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."},
				"backend":                               {WorkspacePath: "backend"},
			},
		},
		metadata: projectAssistantRunSandboxMetadata{
			Status:         "active",
			SourceRevision: 1,
			SourceDigest:   "sha256:source",
			RemoteRevision: 1,
			RemoteDigest:   "sha256:source",
		},
	}
	current := projectAssistantWorkflowRunContext{AssistantRunID: "run"}

	blocked, err := execProjectAssistantRunSandboxCommand(context.Background(), current, sandbox, &projectAssistantExecCommandInput{
		Component: "backend",
		Argv:      []string{"npm", "run", "build"},
	})
	if err != nil {
		t.Fatalf("unexpected non-workspace error: %v", err)
	}
	if blocked.Status != "blocked" || len(fake.execs) != 0 {
		t.Fatalf("non-workspace result = %#v, exec calls = %d; want blocked without remote execution", blocked, len(fake.execs))
	}
	if len(blocked.Blockers) != 1 || !strings.Contains(blocked.Blockers[0], `component "workspace"`) {
		t.Fatalf("non-workspace blockers = %#v, want workspace-only explanation", blocked.Blockers)
	}

	accepted, err := execProjectAssistantRunSandboxCommand(context.Background(), current, sandbox, &projectAssistantExecCommandInput{
		Component: projectAssistantRunSandboxWorkspaceVerb,
		Argv:      []string{"npm", "run", "build"},
	})
	if err != nil {
		t.Fatalf("workspace execution error: %v", err)
	}
	if accepted.Status != "succeeded" || accepted.Component != projectAssistantRunSandboxWorkspaceVerb || len(fake.execs) != 1 {
		t.Fatalf("workspace result = %#v, exec calls = %d; want one accepted workspace call", accepted, len(fake.execs))
	}
}

type assistantRunSandboxExecGateFake struct {
	execs []projectSandboxExecRequest
}

func (f *assistantRunSandboxExecGateFake) Workspace(context.Context, identity, dataPlaneRef, projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	return projectAssistantSandboxWorkspaceResponse{}, nil
}

func (f *assistantRunSandboxExecGateFake) Exec(_ context.Context, _ identity, _ dataPlaneRef, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	f.execs = append(f.execs, request)
	return projectSandboxExecResponse{SessionID: "session-1", State: "succeeded"}, nil
}

func TestProjectAssistantExecSnapshotRoutesSelectedComponent(t *testing.T) {
	root := t.TempDir()
	files := workspace.NewFileStore(root)
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	if err := files.ApplyFiles(context.Background(), scope, []workspace.File{
		{Path: "backend/main.go", Content: "package main\n"},
		{Path: "frontend/index.html", Content: "<!doctype html>\n"},
	}); err != nil {
		t.Fatal(err)
	}
	got, digest, revision, err := projectAssistantExecSnapshot(context.Background(), projectAssistantWorkflowRunContext{
		Workspace:      files,
		WorkspaceScope: scope,
	}, projectTemplateComponent{WorkspacePath: "backend"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" || revision == 0 || len(got) != 1 || got[0].Path != "main.go" || got[0].Content != "package main\n" {
		t.Fatalf("snapshot = %#v, digest = %q, revision=%d", got, digest, revision)
	}
	wantDigest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "package main\n"}})
	if digest != wantDigest {
		t.Fatalf("snapshot digest = %q, component source digest = %q", digest, wantDigest)
	}
}

func TestProjectAssistantExecSyncEvidenceBootstrapsFreshProject(t *testing.T) {
	// A fresh App Studio project has source in the FileStore before the
	// assistant run state has observed a mutation. There is no runtime manifest
	// in this store; the provider-owned sync hook is the authority that must be
	// awaited before exec can address the live component workspace.
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	if err := files.ApplyFiles(context.Background(), scope, []workspace.File{{Path: "frontend/index.html", Content: "<!doctype html>\n"}}); err != nil {
		t.Fatal(err)
	}
	calls := make(chan string, 1)
	server := &Server{
		workspaces: files,
		developmentSyncAfterMutation: func(_ identity, _ *aiv1alpha1.Project, name string) error {
			calls <- name
			return nil
		},
	}
	state := newProjectEinoAssistantRunState()
	project := &aiv1alpha1.Project{}
	runCtx := projectAssistantWorkflowRunContext{
		Server:         server,
		Project:        project,
		RunState:       state,
		Workspace:      files,
		WorkspaceScope: scope,
	}

	revision, status, failure := projectAssistantExecSyncEvidence(context.Background(), runCtx)
	if revision != 1 || status != "succeeded" || failure != "" {
		t.Fatalf("fresh-project sync evidence = (%d, %q, %q), want (1, succeeded, empty)", revision, status, failure)
	}
	select {
	case name := <-calls:
		if name != projectActionWorkspaceSync {
			t.Fatalf("bootstrap sync tool = %q, want %q", name, projectActionWorkspaceSync)
		}
	default:
		t.Fatal("fresh-project exec evidence did not schedule initial workspace sync")
	}
	if sourceRevision, _ := state.SourceMutationRevisions(); sourceRevision != 1 {
		t.Fatalf("synthetic bootstrap source revision = %d, want 1", sourceRevision)
	}

	// Once the synthetic bootstrap is settled, another exec preparation in the
	// same run must reuse its evidence rather than enqueueing another sync.
	if revision, status, failure := projectAssistantExecSyncEvidence(context.Background(), runCtx); revision != 1 || status != "succeeded" || failure != "" {
		t.Fatalf("reused fresh-project sync evidence = (%d, %q, %q), want (1, succeeded, empty)", revision, status, failure)
	}
	select {
	case name := <-calls:
		t.Fatalf("fresh-project exec evidence enqueued duplicate sync %q", name)
	default:
	}
}

func TestProjectAssistantExecSnapshotRejectsChangedMutationRevision(t *testing.T) {
	root := t.TempDir()
	files := workspace.NewFileStore(root)
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	if err := files.ApplyFiles(context.Background(), scope, []workspace.File{{Path: "backend/main.go", Content: "package main\n"}}); err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.RecordSourceMutation()
	_, _, _, err := projectAssistantExecSnapshot(context.Background(), projectAssistantWorkflowRunContext{
		Workspace:      files,
		WorkspaceScope: scope,
		RunState:       runState,
	}, projectTemplateComponent{WorkspacePath: "backend"}, 0)
	if !errors.Is(err, errProjectAssistantExecRevisionChanged) {
		t.Fatalf("snapshot error = %v, want mutation revision change", err)
	}
}

func TestProjectAssistantExecResultBoundsOutput(t *testing.T) {
	result := projectAssistantExecResult(projectSandboxExecResponse{SessionID: "session", State: "succeeded", Stdout: strings.Repeat("x", projectAssistantExecMaxOutput+1)}, "backend", 3, "sha256:digest", "succeeded", 2, "")
	if !result.OutputTruncated || len(result.Stdout) == 0 || result.Status != "succeeded" || result.SourceRevision != 3 {
		t.Fatalf("result = %#v", result)
	}
	empty := projectAssistantExecResult(projectSandboxExecResponse{State: "succeeded"}, "backend", 0, "sha256:empty", "succeeded", 0, "")
	if empty.OutputTruncated {
		t.Fatal("empty output must not be marked truncated")
	}
}

func TestProjectAssistantExecRequestIDStable(t *testing.T) {
	first := projectAssistantExecRequestID("run", "call")
	if first == "" || first != projectAssistantExecRequestID("run", "call") || first == projectAssistantExecRequestID("run", "other") {
		t.Fatalf("request IDs = %q", first)
	}
	if anonymous := projectAssistantExecRequestID("", ""); anonymous == "" || anonymous != projectAssistantExecRequestID("", "") {
		t.Fatalf("anonymous request ID = %q", anonymous)
	}
}

func TestProjectAssistantExecStartRetries503WithSameRequestID(t *testing.T) {
	request := projectSandboxExecRequest{Action: "start", RequestID: "run-call", Argv: []string{"go", "test"}}
	calls := 0
	errResponse := &projectAssistantExecHTTPError{status: http.StatusServiceUnavailable, detail: "connection refused"}
	response, err := retryProjectAssistantExecStart(context.Background(), request, func(_ context.Context, got projectSandboxExecRequest) (projectSandboxExecResponse, error) {
		calls++
		if got.RequestID != request.RequestID || got.Action != "start" {
			t.Fatalf("retry request = %#v, want same start request ID", got)
		}
		if calls == 1 {
			return projectSandboxExecResponse{}, errResponse
		}
		return projectSandboxExecResponse{SessionID: "session", State: "running"}, nil
	})
	if err != nil || response.SessionID != "session" {
		t.Fatalf("start retry response = %#v, err=%v", response, err)
	}
	if calls != 2 {
		t.Fatalf("start attempts = %d, want 2", calls)
	}
}

func TestProjectAssistantExecStartPermanentErrorFailsFast(t *testing.T) {
	calls := 0
	_, err := retryProjectAssistantExecStart(context.Background(), projectSandboxExecRequest{Action: "start", RequestID: "run-call"}, func(context.Context, projectSandboxExecRequest) (projectSandboxExecResponse, error) {
		calls++
		return projectSandboxExecResponse{}, &projectAssistantExecHTTPError{status: http.StatusBadRequest, detail: "invalid argv"}
	})
	if err == nil || calls != 1 {
		t.Fatalf("permanent start error = %v, attempts = %d, want one fail-fast attempt", err, calls)
	}
	if projectAssistantExecStartRetryable(&projectAssistantExecHTTPError{status: http.StatusUnauthorized, detail: "unauthorized"}) {
		t.Fatal("auth error was incorrectly marked retryable")
	}
}

func TestProjectAssistantExecCommandInputCheckpointRegistration(t *testing.T) {
	var encoded bytes.Buffer
	original := any(&projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}})
	if err := gob.NewEncoder(&encoded).Encode(&original); err != nil {
		t.Fatalf("encode registered exec input: %v", err)
	}
	var decoded any
	if err := gob.NewDecoder(&encoded).Decode(&decoded); err != nil {
		t.Fatalf("decode registered exec input: %v", err)
	}
	got, ok := decoded.(*projectAssistantExecCommandInput)
	if !ok || got.Component != "backend" || len(got.Argv) != 2 {
		t.Fatalf("decoded exec input = %#v (%T)", decoded, decoded)
	}
}

func TestProjectAssistantExecMetadataIsStructuredAndDoesNotExposeSecrets(t *testing.T) {
	metadata := projectAssistantExecMetadataForToolArguments(projectToolExecCommand, map[string]any{
		"component":      "backend",
		"argv":           []any{"go", "test", "--token", "super-secret"},
		"workdir":        "internal",
		"timeoutSeconds": float64(42),
	}, `{"status":"failed","summary":"Command failed in component \"backend\".","exitCode":2,"durationMs":123}`, "failed")
	if metadata == nil || metadata.Component != "backend" || metadata.Workdir != "internal" || metadata.TimeoutSeconds != 42 ||
		metadata.NetworkProfile != "application-runtime" || metadata.AuthorityProfile != "application-container" || metadata.WritebackPolicy != "runtime-workspace-only" || metadata.ExitCode == nil || *metadata.ExitCode != 2 {
		t.Fatalf("metadata = %#v", metadata)
	}
	if len(metadata.Argv) != 4 || metadata.Argv[3] != "[redacted]" {
		t.Fatalf("metadata argv = %#v, want credential value redacted", metadata.Argv)
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret") {
		t.Fatalf("metadata leaked argv secret: %s", raw)
	}
}

func TestProjectAssistantExecPublicProjectionRedactsOutputAndMapsTimeout(t *testing.T) {
	metadata := projectAssistantExecMetadataForToolArguments(projectToolExecCommand, map[string]any{
		"component": "workspace",
		"argv":      []any{"sh", "-c", "echo TOKEN=argv-secret"},
	}, `{"status":"timed_out","summary":"timeout token=summary-secret","exitCode":124,"durationMs":99,"stdout":["TOKEN=stdout-secret","Bearer bearer-secret-value","https://example.test/?api_key=query-secret","{\"token\":\"json-secret\"}","ghp_1234567890abcdefghijkl","AKIA1234567890ABCDEF"],"stderr":["OPENAI_API_KEY=env-secret"]}`, "failed")
	if metadata == nil {
		t.Fatal("exec metadata is nil")
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	public := string(encoded)
	for _, secret := range []string{"argv-secret", "summary-secret", "stdout-secret", "bearer-secret-value", "query-secret", "json-secret", "ghp_1234567890abcdefghijkl", "AKIA1234567890ABCDEF", "env-secret"} {
		if strings.Contains(public, secret) {
			t.Fatalf("public exec projection leaked %q: %s", secret, public)
		}
	}
	if !strings.Contains(metadata.Summary, "token=[redacted]") || !strings.Contains(strings.Join(metadata.Stdout, "\n"), "TOKEN=[redacted]") || !strings.Contains(strings.Join(metadata.Stderr, "\n"), "OPENAI_API_KEY=[redacted]") {
		t.Fatalf("public exec projection did not retain redaction markers: %#v", metadata)
	}
	if got := strings.Join(metadata.Stdout, "\n"); !strings.Contains(got, `{"token":"[redacted]"}`) {
		t.Fatalf("quoted JSON redaction = %q, want valid readable JSON field", got)
	}
	if metadata.Status != "timed_out" {
		t.Fatalf("nested exec status = %q, want timed_out", metadata.Status)
	}
	if metadata.OutputTruncated {
		t.Fatalf("secret redaction alone marked output truncated: %#v", metadata)
	}
	action := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:     "exec-timeout",
		Name:   projectToolExecCommand,
		Status: "timed_out",
		Exec:   metadata,
	})
	if action.Status != projectAssistantActionFeedStatusFailed || action.Severity != projectAssistantActionFeedSeverityError {
		t.Fatalf("timed-out public action = %#v, want failed/error", action)
	}
}

func TestProjectAssistantExecRequestUsesPersistentWorkspaceAuthority(t *testing.T) {
	raw, err := json.Marshal(projectSandboxExecRequest{
		Action:         "start",
		RequestID:      "request-1",
		Argv:           []string{"go", "test", "./..."},
		Workdir:        "internal",
		TimeoutSeconds: 30,
		SourceRevision: 7,
		SourceDigest:   "digest-7",
	})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["files"]; ok {
		t.Fatalf("persistent exec request unexpectedly carries source files: %s", raw)
	}
	if got, ok := wire["sourceRevision"].(float64); !ok || got != 7 {
		t.Fatalf("sourceRevision = %#v, want 7", wire["sourceRevision"])
	}
	if got, ok := wire["sourceDigest"].(string); !ok || got != "digest-7" {
		t.Fatalf("sourceDigest = %#v, want digest-7", wire["sourceDigest"])
	}
}

func TestProjectAssistantExecApprovalMetadataSurvivesMultiCallCheckpoint(t *testing.T) {
	state := projectAssistantCheckpointState{
		ToolCalls: []chatToolCall{
			{ID: "exec-1", Type: "function", Function: chatToolCallFunction{Name: projectToolExecCommand, Arguments: `{"component":"backend","argv":["go","test"]}`}},
			{ID: "exec-2", Type: "function", Function: chatToolCallFunction{Name: projectToolExecCommand, Arguments: `{"component":"frontend","argv":["npm","test"]}`}},
		},
		CurrentIndex: 1,
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored projectAssistantCheckpointState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	for index, call := range restored.ToolCalls {
		permission := projectAssistantPermissionForCall("permission-"+string(rune('1'+index)), call, projectAssistantToolSpec{Name: projectToolExecCommand})
		if permission.Exec == nil || permission.Exec.Component == "" || permission.Exec.AuthorityProfile != "application-container" || permission.Exec.NetworkProfile != "application-runtime" {
			t.Fatalf("checkpoint call %d permission metadata = %#v", index, permission)
		}
		if permission.Exec.Status != "permission_required" {
			t.Fatalf("checkpoint call %d permission status = %q", index, permission.Exec.Status)
		}
	}
}

func TestProjectAssistantExecActionFeedMergesTerminalResultsAcrossCheckpoints(t *testing.T) {
	calls := []struct {
		id        string
		component string
		argv      []string
		result    string
		exitCode  int
		duration  int64
	}{
		{
			id:        "exec-install",
			component: "frontend",
			argv:      []string{"npm", "install"},
			result:    `{"status":"succeeded","summary":"Command succeeded in component \"frontend\".","exitCode":0,"durationMs":742,"outputTruncated":true}`,
			exitCode:  0,
			duration:  742,
		},
		{
			id:        "exec-test",
			component: "backend",
			argv:      []string{"go", "test", "./..."},
			result:    `{"status":"failed","summary":"Command failed in component \"backend\".","exitCode":1,"durationMs":1834}`,
			exitCode:  1,
			duration:  1834,
		},
	}
	var events []projectToolCallStreamEvent
	for _, call := range calls {
		args := map[string]any{"component": call.component, "argv": call.argv}
		permission := projectToolCallStreamEvent{
			ID:        call.id,
			Name:      projectToolExecCommand,
			Status:    "permission_required",
			Arguments: projectEinoToolArgumentsString(args),
			Permission: &projectAssistantPermission{
				ToolCallID: call.id,
				ToolName:   projectToolExecCommand,
				Exec:       projectAssistantExecMetadataForToolArguments(projectToolExecCommand, args, "", "permission_required"),
			},
		}
		events = upsertProjectToolCallStreamEvent(events, permission)
		// The checkpoint callback intentionally carries only lifecycle data.
		events = upsertProjectToolCallStreamEvent(events, projectToolCallStreamEvent{
			ID:         call.id,
			Status:     "permission_required",
			Checkpoint: &projectAssistantCheckpoint{ID: "checkpoint-" + call.id},
		})
		terminal := projectToolCallStreamEvent{
			ID:        call.id,
			Name:      projectToolExecCommand,
			Status:    map[bool]string{true: "succeeded", false: "failed"}[call.exitCode == 0],
			Arguments: projectEinoToolArgumentsString(args),
			Summary:   "terminal result",
			Exec:      projectAssistantExecMetadataForToolArguments(projectToolExecCommand, args, call.result, map[bool]string{true: "succeeded", false: "failed"}[call.exitCode == 0]),
		}
		events = upsertProjectToolCallStreamEvent(events, terminal)
	}
	actions := projectAssistantActionFeedFromToolCalls(events)
	if len(actions) != len(calls) {
		t.Fatalf("actions = %#v, want %d terminal exec actions", actions, len(calls))
	}
	for index, call := range calls {
		action := actions[index]
		if action.Status != map[bool]string{true: "succeeded", false: "failed"}[call.exitCode == 0] {
			t.Fatalf("action %d status = %q, want terminal status", index, action.Status)
		}
		if action.Exec == nil {
			t.Fatalf("action %d lost exec metadata: %#v", index, action)
		}
		if action.Exec.Component != call.component || action.Exec.AuthorityProfile != "application-container" ||
			action.Exec.NetworkProfile != "application-runtime" || action.Exec.WritebackPolicy != "runtime-workspace-only" {
			t.Fatalf("action %d request disclosure = %#v", index, action.Exec)
		}
		if action.Exec.Status != map[bool]string{true: "succeeded", false: "failed"}[call.exitCode == 0] ||
			action.Exec.ExitCode == nil || *action.Exec.ExitCode != call.exitCode || action.Exec.DurationMS != call.duration {
			t.Fatalf("action %d terminal disclosure = %#v", index, action.Exec)
		}
		if call.exitCode == 0 && !action.Exec.OutputTruncated {
			t.Fatalf("action %d lost output truncation flag", index)
		}
	}

	// A terminal outer action must never leave a stale permission lifecycle in
	// the nested disclosure when an older checkpoint did not produce a fresh
	// terminal callback.
	stale := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID:     "exec-stale",
		Name:   projectToolExecCommand,
		Status: "permission_required",
		Exec:   projectAssistantExecMetadataForToolArguments(projectToolExecCommand, map[string]any{"component": "backend", "argv": []string{"go", "test"}}, "", "permission_required"),
	})
	stale.Status = projectAssistantActionFeedStatusSucceeded
	final := finalizeProjectAssistantActionFeed([]projectAssistantActionFeedItem{stale}, store.AssistantRunStatusCompleted)
	if len(final) != 1 || final[0].Exec == nil || final[0].Exec.Status != "succeeded" {
		t.Fatalf("finalized stale exec action = %#v, want succeeded nested status", final)
	}
	preserved := stale
	preserved.Exec = cloneProjectAssistantExecMetadata(stale.Exec)
	preserved.Exec.Status = "timed_out"
	final = finalizeProjectAssistantActionFeed([]projectAssistantActionFeedItem{preserved}, store.AssistantRunStatusCompleted)
	if len(final) != 1 || final[0].Exec == nil || final[0].Exec.Status != "timed_out" {
		t.Fatalf("finalized terminal exec action = %#v, want explicit timed_out status preserved", final)
	}
}
