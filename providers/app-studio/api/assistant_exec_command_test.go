/*
Copyright 2026 The Railgrid Authors.

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
	"crypto/sha256"
	"encoding/base64"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/hubmcp"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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
		{name: "accepts 4096 byte argv token", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{strings.Repeat("x", 4096)}}, wantWork: "", wantTime: projectAssistantExecDefaultTimeout},
		{name: "rejects 4097 byte argv token with precise guidance", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{strings.Repeat("x", 4097)}}, wantErr: "argv token 1 is 4097 UTF-8 bytes; maximum is 4096. Use an existing workspace script"},
		{name: "counts unicode argv bytes", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{strings.Repeat("雪", 1366)}}, wantErr: "argv token 1 is 4098 UTF-8 bytes; maximum is 4096"},
		{name: "rejects empty argv token", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{""}}, wantErr: "argv token 1 is empty (0 UTF-8 bytes); provide a non-empty token"},
		{name: "rejects NUL argv token", input: &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go\x00test"}}, wantErr: "argv token 1 contains NUL (7 UTF-8 bytes); remove the NUL byte"},
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
	for _, want := range []string{"user-authorized", "compiler, test, lint, or read-only diagnostic", "argv tokens", "4096 UTF-8 bytes", "existing workspace script", "must not edit project source", "shared project development component", "stale and does not verify the newer revision"} {
		if !strings.Contains(spec.Description, want) {
			t.Errorf("live exec_command description missing %q: %s", want, spec.Description)
		}
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
	for _, want := range []string{"user-authorized", "read-only diagnostic", "Go, Node.js, and Python", "has no public preview", "4096 UTF-8 bytes", "existing workspace script", "256 UTF-8 bytes", "MUST NOT change source files", "gofmt -d", "never gofmt -w"} {
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
	argv, ok := properties["argv"].(map[string]any)
	if !ok {
		t.Fatalf("sandbox argv schema = %#v", properties["argv"])
	}
	items, ok := argv["items"].(map[string]any)
	if !ok || items["maxLength"] != float64(projectAssistantExecMaxArgBytes) || !strings.Contains(projectToolString(items["description"]), "4096 UTF-8 bytes") {
		t.Fatalf("sandbox argv item schema = %#v, want a 4096 UTF-8 byte cap", argv["items"])
	}
	workdir, ok := properties["workdir"].(map[string]any)
	if !ok || workdir["maxLength"] != float64(projectAssistantExecMaxWorkdir) || !strings.Contains(projectToolString(workdir["description"]), "256 UTF-8 bytes") {
		t.Fatalf("sandbox workdir schema = %#v, want the existing 256 UTF-8 byte cap", properties["workdir"])
	}

	generated, err := info.ToJSONSchema()
	if err != nil {
		t.Fatalf("generate sandbox exec schema: %v", err)
	}
	generatedComponent, ok := generated.Properties.Get("component")
	if !ok || len(generatedComponent.Enum) != 1 || generatedComponent.Enum[0] != projectAssistantRunSandboxWorkspaceVerb {
		t.Fatalf("generated sandbox component schema = %#v, want workspace enum", generatedComponent)
	}
}

type sharedExecTestHandler func(*http.Request, projectSandboxExecRequest, *workspace.FileStore, workspace.Scope) projectSandboxExecResponse

func newSharedExecTestContext(t *testing.T, state *projectEinoAssistantRunState, handler sharedExecTestHandler) (projectAssistantWorkflowRunContext, *workspace.FileStore, workspace.Scope) {
	t.Helper()
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "shop", ProjectUID: "project-uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "api/main.go", Content: "package main\n"}}); err != nil {
		t.Fatalf("seed shared runtime source: %v", err)
	}
	if state == nil {
		state = newProjectEinoAssistantRunState()
	}
	state.RecordSourceMutation()
	revision := state.BeginDevelopmentSyncForCurrentMutation()
	state.CompleteDevelopmentSync(revision, nil)
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "shop", UID: "project-uid"}, Spec: aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"}}}
	applicationGVR := schema.GroupVersionResource{Group: "infrastructure.railgrid.ai", Version: "v1alpha1", Resource: "instances"}
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
		"kind":       "Instance",
		"metadata":   map[string]any{"name": projectTemplateInstanceName(project)},
	}}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		asclient.ProjectGVR:  "ProjectList",
		templateResource.GVR: "TemplateList",
		applicationGVR:       "InstanceList",
	}, runtimeProjectObject(t, project), applicationTemplateObject(), instance)
	server := testDataPlaneServer()
	server.workspaces = files
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			var payload projectSandboxExecRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				return nil, fmt.Errorf("decode shared exec payload: %w", err)
			}
			response := projectSandboxExecResponse{State: "failed"}
			if handler != nil {
				response = handler(request, payload, files, scope)
			}
			body, err := json.Marshal(response)
			if err != nil {
				return nil, fmt.Errorf("encode shared exec response: %w", err)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
		})}
	}
	return projectAssistantWorkflowRunContext{
		Server: server, Project: project, WorkspaceScope: scope, Workspace: files, AssistantRunID: "run-shared-exec", RunState: state,
		Identity: identity{clusterID: "rgl3jcl2cfl3xa5p", user: "alice"}, Client: asclient.NewFromDynamic(dynamicClient),
	}, files, scope
}

func TestProjectAssistantExecCommandUsesSharedRuntimeWhenPrivateSandboxIsOff(t *testing.T) {
	var setupCalls atomic.Int32
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	state.ConfigureSandboxCapability(CodingSandboxEligibility{Reason: "coding sandbox mode is off"}, func(context.Context) (*projectAssistantRunSandbox, func(), error) {
		setupCalls.Add(1)
		return nil, nil, errors.New("private sandbox setup must not run while mode is off")
	})
	var requests []projectSandboxExecRequest
	var requestIDs []string
	runCtx, files, scope := newSharedExecTestContext(t, state, func(request *http.Request, payload projectSandboxExecRequest, _ *workspace.FileStore, _ workspace.Scope) projectSandboxExecResponse {
		if got := request.URL.Query().Get("component"); got != "backend" {
			t.Errorf("exec component = %q, want backend", got)
		}
		if key := request.Header.Get("Idempotency-Key"); key == "" || key != payload.RequestID {
			t.Errorf("Idempotency-Key = %q, request ID = %q; want same non-empty key", key, payload.RequestID)
		}
		requests = append(requests, payload)
		requestIDs = append(requestIDs, payload.RequestID)
		switch payload.Action {
		case "start":
			return projectSandboxExecResponse{SessionID: "shared-session", State: "running"}
		case "poll":
			exitCode := 0
			return projectSandboxExecResponse{SessionID: "shared-session", State: "succeeded", ExitCode: &exitCode, Stdout: "SHARED_BUILD_OK"}
		default:
			t.Errorf("unexpected shared exec action %q", payload.Action)
			return projectSandboxExecResponse{State: "failed"}
		}
	})
	initialRevision, err := files.SourceRevision(context.Background(), scope)
	if err != nil {
		t.Fatalf("read initial source revision: %v", err)
	}
	result, err := execProjectAssistantCommand(runCtx)(context.Background(), &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"npm", "run", "build"}})
	if err != nil || result == nil || result.Status != "succeeded" {
		t.Fatalf("shared exec result = %#v, err=%v; want succeeded", result, err)
	}
	if setupCalls.Load() != 0 {
		t.Fatalf("private sandbox setup calls = %d, want none while mode is off", setupCalls.Load())
	}
	if len(requests) != 2 || requests[0].Action != "start" || requests[1].Action != "poll" || requestIDs[0] != requestIDs[1] {
		t.Fatalf("shared exec requests = %#v, ids=%#v; want one start and poll with the same idempotency key", requests, requestIDs)
	}
	if requests[0].SourceRevision != result.SourceRevision || requests[0].SourceDigest != result.SourceDigest || result.SourceRevision != initialRevision || result.SourceDigest == "" {
		t.Fatalf("shared exec receipt/request = %#v / %#v, want the source snapshot dispatched", result, requests[0])
	}
	if got := strings.Join(result.Stdout, "\n"); got != "SHARED_BUILD_OK" || result.ExitCode == nil || *result.ExitCode != 0 || result.SourceStale {
		t.Fatalf("shared exec output/status = %#v, want unchanged verified success", result)
	}
}

func TestProjectAssistantExecCommandDoesNotFallbackWhenPrivateSandboxIsUnavailable(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	state.ConfigureSandboxCapability(CodingSandboxEligibility{Eligible: true}, func(context.Context) (*projectAssistantRunSandbox, func(), error) {
		return nil, nil, nil
	})
	var sharedCalls atomic.Int32
	runCtx, _, _ := newSharedExecTestContext(t, state, func(_ *http.Request, _ projectSandboxExecRequest, _ *workspace.FileStore, _ workspace.Scope) projectSandboxExecResponse {
		sharedCalls.Add(1)
		return projectSandboxExecResponse{State: "succeeded"}
	})

	result, err := execProjectAssistantCommand(runCtx)(context.Background(), &projectAssistantExecCommandInput{
		Component: projectAssistantRunSandboxWorkspaceVerb,
		Argv:      []string{"go", "test", "./..."},
	})
	if err != nil || result == nil || result.Status != "blocked" || !strings.Contains(result.Summary, "private coding sandbox is unavailable") {
		t.Fatalf("private sandbox unavailable result = %#v, err=%v; want blocked private-runtime result", result, err)
	}
	if sharedCalls.Load() != 0 {
		t.Fatalf("shared runtime calls = %d, want none when private mode is explicitly enabled", sharedCalls.Load())
	}
}

func TestProjectAssistantExecMarkSourceStaleRetainsRequestContextAfterCancellation(t *testing.T) {
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "shop", ProjectUID: "project-uid"}
	ledger := workspace.NewMemoryLedger()
	files := workspace.NewFileStore(t.TempDir())
	files.SetLedger(nil)
	ctx := workspace.ContextWithLedger(context.Background(), ledger)
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "api/main.go", Content: "package main\n"}}); err != nil {
		t.Fatalf("seed project source: %v", err)
	}
	revision, err := files.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatalf("read source revision: %v", err)
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	result := projectAssistantExecMarkSourceStale(canceledCtx, projectAssistantWorkflowRunContext{Workspace: files, WorkspaceScope: scope}, &projectAssistantExecCommandResult{
		Status: "succeeded", SourceRevision: revision,
	})
	if result.SourceStale || result.Status != "succeeded" {
		t.Fatalf("source check after cancellation = %#v, want unchanged verified result using the request ledger", result)
	}
}

func TestProjectAssistantExecCommandMarksSharedResultsStaleWhenSourceChanges(t *testing.T) {
	for _, test := range []struct {
		name       string
		state      string
		exitCode   int
		wantStatus string
	}{
		{name: "successful output is unverified", state: "succeeded", exitCode: 0, wantStatus: "stale"},
		{name: "command failure remains failed", state: "failed", exitCode: 2, wantStatus: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newProjectEinoAssistantRunState()
			state.ConfigureSandboxCapability(CodingSandboxEligibility{Reason: "coding sandbox mode is off"}, nil)
			var starts atomic.Int32
			runCtx, files, scope := newSharedExecTestContext(t, state, func(_ *http.Request, payload projectSandboxExecRequest, files *workspace.FileStore, scope workspace.Scope) projectSandboxExecResponse {
				switch payload.Action {
				case "start":
					starts.Add(1)
					return projectSandboxExecResponse{SessionID: "shared-session", State: "running"}
				case "poll":
					if _, err := files.PutFile(context.Background(), scope, workspace.PutOptions{Path: "api/main.go", Data: []byte("package main // changed during command\n")}); err != nil {
						t.Errorf("change source during command: %v", err)
					}
					exitCode := test.exitCode
					return projectSandboxExecResponse{SessionID: "shared-session", State: test.state, ExitCode: &exitCode, Stdout: "BUILD_OUTPUT", Stderr: "BUILD_DIAGNOSTIC"}
				default:
					t.Errorf("unexpected shared exec action %q", payload.Action)
					return projectSandboxExecResponse{State: "failed"}
				}
			})
			initialRevision, err := files.SourceRevision(context.Background(), scope)
			if err != nil {
				t.Fatalf("read initial source revision: %v", err)
			}
			result, err := execProjectAssistantCommand(runCtx)(context.Background(), &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"npm", "run", "build"}})
			if err != nil || result == nil {
				t.Fatalf("shared exec result = %#v, err=%v", result, err)
			}
			if result.Status != test.wantStatus || !result.SourceStale || result.SyncStatus != "stale" {
				t.Fatalf("stale shared exec status = %#v, want status=%q with sourceStale=true", result, test.wantStatus)
			}
			if result.SourceRevision != initialRevision || result.SourceDigest == "" || result.ExitCode == nil || *result.ExitCode != test.exitCode || strings.Join(result.Stdout, "\n") != "BUILD_OUTPUT" || strings.Join(result.Stderr, "\n") != "BUILD_DIAGNOSTIC" {
				t.Fatalf("stale result lost dispatch receipt or command outcome: %#v", result)
			}
			if !strings.Contains(result.Summary, fmt.Sprintf("changed from revision %d to %d", initialRevision, initialRevision+1)) || !strings.Contains(result.Summary, "unverified for current source") {
				t.Fatalf("stale result summary = %q, want explicit source-change explanation", result.Summary)
			}
			if starts.Load() != 1 {
				t.Fatalf("START requests = %d, want one attempt for this command call", starts.Load())
			}
			currentRevision, err := files.SourceRevision(context.Background(), scope)
			if err != nil || currentRevision != initialRevision+1 {
				t.Fatalf("current source revision = %d, err=%v, want newer revision %d", currentRevision, err, initialRevision+1)
			}
			metadata := projectAssistantExecMetadataForToolArguments(projectToolExecCommand, map[string]any{
				"component": "backend", "argv": []string{"npm", "run", "build"},
			}, mustJSON(t, result), "succeeded")
			if metadata == nil || !metadata.SourceStale || metadata.SourceRevision != initialRevision || metadata.Status != test.wantStatus || metadata.ExitCode == nil || *metadata.ExitCode != test.exitCode {
				t.Fatalf("public stale exec metadata = %#v", metadata)
			}
			action := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{ID: "exec-stale", Name: projectToolExecCommand, Status: "succeeded", Exec: metadata})
			if action.Exec == nil || action.Exec.Status != test.wantStatus || !action.Exec.SourceStale {
				t.Fatalf("action feed stale disclosure = %#v, want terminal source-stale evidence", action.Exec)
			}
		})
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test result: %v", err)
	}
	return string(encoded)
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
			metadata: projectAssistantRunSandboxMetadata{Status: "active", RunID: "run-lazy-exec", RemoteRevision: 7, RemoteDigest: "sha256:remote"},
		}, func() {}, nil
	})
	tool, err := newProjectAssistantExecCommandGraphTool(projectAssistantWorkflowRunContext{RunState: state})
	if err != nil {
		t.Fatalf("create exec tool: %v", err)
	}
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("read exec tool info: %v", err)
	}
	for _, want := range []string{"private sandbox starts without the hosted preview's installed packages", "install only from the existing lockfile", "npm ci", "without changing package.json or the lockfile", "rerun the original check once"} {
		if !strings.Contains(info.Desc, want) {
			t.Errorf("eligible exec description missing %q: %s", want, info.Desc)
		}
	}
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
	requests := append([]projectSandboxExecRequest(nil), client.execRequests...)
	client.mu.Unlock()
	if setupCalls != 1 || execCalls != 2 {
		t.Fatalf("lazy exec setup calls=%d exec calls=%d, want one setup and two commands", setupCalls, execCalls)
	}
	for index, request := range requests {
		if request.Action != "start" || request.SourceRevision != 7 || request.SourceDigest != "sha256:remote" {
			t.Fatalf("lazy exec request %d = %#v, want command pinned to run snapshot 7/sha256:remote", index, request)
		}
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
	if result == nil || result.Status != "canceled" || result.Summary != "Command canceled before the private coding sandbox was ready." {
		t.Fatalf("canceled setup result = %#v, want canceled command", result)
	}
}

func TestProjectAssistantExecCommandPresentationWithoutRunSandboxUsesSharedRuntime(t *testing.T) {
	tool, err := newProjectAssistantExecCommandGraphTool(projectAssistantWorkflowRunContext{})
	if err != nil {
		t.Fatalf("create project exec tool: %v", err)
	}
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("read project exec tool info: %v", err)
	}
	if !strings.Contains(info.Desc, "shared project development component") || !strings.Contains(info.Desc, "source revision changes") || strings.Contains(info.Desc, "private per-run sandbox") {
		t.Fatalf("exec description without run state = %q, want shared-runtime guidance", info.Desc)
	}
	generated, err := info.ToJSONSchema()
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
			RunID:          "run",
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
	if fake.execs[0].SourceRevision != 1 || fake.execs[0].SourceDigest != "sha256:source" {
		t.Fatalf("workspace command fence = %#v, want the private run snapshot", fake.execs[0])
	}
}

func TestProjectAssistantRunSandboxExecRejectsWrongRunOrMissingFence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata projectAssistantRunSandboxMetadata
	}{
		{name: "wrong run", metadata: projectAssistantRunSandboxMetadata{Status: "active", RunID: "sibling-run", RemoteRevision: 4, RemoteDigest: "sha256:sibling"}},
		{name: "missing source fence", metadata: projectAssistantRunSandboxMetadata{Status: "active", RunID: "run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &assistantRunSandboxExecGateFake{}
			sandbox := &projectAssistantRunSandbox{
				client: fake,
				target: projectDevelopmentSyncTargetInfo{Components: map[string]projectTemplateComponent{
					projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."},
				}},
				metadata: tc.metadata,
			}
			result, err := execProjectAssistantRunSandboxCommand(context.Background(), projectAssistantWorkflowRunContext{AssistantRunID: "run"}, sandbox, &projectAssistantExecCommandInput{
				Component: projectAssistantRunSandboxWorkspaceVerb,
				Argv:      []string{"go", "test", "./..."},
			})
			if err != nil || result == nil || result.Status != "blocked" || len(fake.execs) != 0 {
				t.Fatalf("exec result = %#v, err=%v, exec calls=%d; want blocked before dispatch", result, err, len(fake.execs))
			}
		})
	}
}

func TestProjectAssistantRunSandboxExecCleansUpAmbiguousStart(t *testing.T) {
	for _, tc := range []struct {
		name         string
		execError    error
		execResponse projectSandboxExecResponse
		wantCancel   bool
	}{
		{name: "ambiguous transport failure", execError: errors.New("connection reset after worker accepted START"), wantCancel: true},
		{name: "successful response without session ID", execResponse: projectSandboxExecResponse{State: "running"}, wantCancel: true},
		{name: "definitive client rejection", execError: &projectAssistantExecHTTPError{status: http.StatusBadRequest, detail: "invalid argv"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &sandboxClientFake{execErr: tc.execError, execResponse: tc.execResponse}
			sandbox := &projectAssistantRunSandbox{
				client: client,
				target: projectDevelopmentSyncTargetInfo{Components: map[string]projectTemplateComponent{
					projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."},
				}},
				metadata: projectAssistantRunSandboxMetadata{Status: "active", RunID: "run-cleanup", SourceRevision: 1, SourceDigest: "sha256:source", RemoteRevision: 1, RemoteDigest: "sha256:source"},
			}
			result, err := execProjectAssistantRunSandboxCommand(context.Background(), projectAssistantWorkflowRunContext{AssistantRunID: "run-cleanup"}, sandbox, &projectAssistantExecCommandInput{
				Component: projectAssistantRunSandboxWorkspaceVerb,
				Argv:      []string{"go", "test", "./..."},
			})
			if err != nil || result == nil || result.Status != "error" {
				t.Fatalf("exec result = %#v, err=%v; want an error result", result, err)
			}
			client.mu.Lock()
			requests := append([]projectSandboxExecRequest(nil), client.execRequests...)
			contextErrors := append([]error(nil), client.execContextErrors...)
			hasDeadline := append([]bool(nil), client.execHasDeadline...)
			client.mu.Unlock()
			wantCalls := 1
			if tc.wantCancel {
				wantCalls++
			}
			if len(requests) != wantCalls || requests[0].Action != "start" || requests[0].RequestID == "" {
				t.Fatalf("exec requests = %#v, want one START and cleanup=%t", requests, tc.wantCancel)
			}
			if tc.wantCancel {
				if requests[1].Action != "cancel" || requests[1].RequestID != requests[0].RequestID || requests[1].SessionID != "" {
					t.Fatalf("cleanup request = %#v, want request-ID-only cancel for START %q", requests[1], requests[0].RequestID)
				}
				if contextErrors[1] != nil || !hasDeadline[1] {
					t.Fatalf("cleanup context error/deadline = %v/%t, want detached bounded context", contextErrors[1], hasDeadline[1])
				}
			}
		})
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
	digest, revision, err := projectAssistantExecSnapshot(context.Background(), projectAssistantWorkflowRunContext{
		Workspace:      files,
		WorkspaceScope: scope,
	}, projectTemplateComponent{WorkspacePath: "backend"}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" || revision == 0 {
		t.Fatalf("snapshot digest = %q, revision=%d", digest, revision)
	}
	wantDigest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "package main\n"}})
	if digest != wantDigest {
		t.Fatalf("snapshot digest = %q, component source digest = %q", digest, wantDigest)
	}
}

func TestProjectAssistantExecSnapshotSelectsComponentInLargeWorkspace(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	input := make([]workspace.File, 0, 440)
	for i := range 420 {
		input = append(input, workspace.File{Path: fmt.Sprintf("docs/page-%03d.txt", i), Content: "unrelated\n"})
	}
	want := make([]projectSandboxSyncFile, 0, 20)
	for i := range 20 {
		name := fmt.Sprintf("main-%02d.go", i)
		content := fmt.Sprintf("package main // %d\n", i)
		input = append(input, workspace.File{Path: "backend/" + name, Content: content})
		want = append(want, projectSandboxSyncFile{Path: name, Content: content})
	}
	if err := files.ApplyFiles(ctx, scope, input); err != nil {
		t.Fatal(err)
	}

	digest, revision, err := projectAssistantExecSnapshot(ctx, projectAssistantWorkflowRunContext{
		Workspace:      files,
		WorkspaceScope: scope,
	}, projectTemplateComponent{WorkspacePath: "backend"}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if revision == 0 || digest != projectSandboxSyncDigest(want) {
		t.Fatalf("snapshot revision = %d, digest = %q, want nonzero revision and component digest %q", revision, digest, projectSandboxSyncDigest(want))
	}
}

func TestProjectAssistantExecSnapshotMirrorsBinarySync(t *testing.T) {
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	model := testPNG(workspace.MaxWriteBytes + 100)
	for _, file := range []workspace.PutOptions{{Path: "web/index.html", Data: []byte("<!doctype html>\n")}, {Path: "web/public/jeep.glb", Data: model}} {
		if _, err := files.PutFile(context.Background(), scope, file); err != nil {
			t.Fatal(err)
		}
	}
	runCtx := projectAssistantWorkflowRunContext{Workspace: files, WorkspaceScope: scope}
	text := []projectSandboxSyncFile{{Path: "index.html", Content: "<!doctype html>\n"}}
	// An agent without base64 sync never received the binary: the exec
	// digest covers text only (and no longer fails on the binary).
	digest, _, err := projectAssistantExecSnapshot(context.Background(), runCtx, projectTemplateComponent{WorkspacePath: "web"}, 0, func() bool { return false })
	if err != nil || digest != projectSandboxSyncDigest(text) {
		t.Fatalf("text-only exec digest = %q, %v", digest, err)
	}
	// An agent with base64 sync holds the binary: the digest covers its bytes,
	// matching the development sync digest.
	withBinary := append(text, projectSandboxSyncFile{Path: "public/jeep.glb", Content: base64.StdEncoding.EncodeToString(model), Encoding: "base64"})
	digest, _, err = projectAssistantExecSnapshot(context.Background(), runCtx, projectTemplateComponent{WorkspacePath: "web"}, 0, func() bool { return true })
	if err != nil || digest != projectSandboxSyncDigest(withBinary) {
		t.Fatalf("binary exec digest = %q, %v", digest, err)
	}
}

func TestProjectAssistantExecSnapshotOmitsBinariesDroppedBySyncBundleLimit(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	image := make([]byte, hubmcp.BinaryFileMaxBytes)
	image[0] = 0
	for _, file := range []workspace.PutOptions{
		{Path: "backend/public/a.bin", Data: image},
		{Path: "backend/public/b.bin", Data: image},
		{Path: "backend/main.go", Data: []byte("package main\n")},
	} {
		if _, err := files.PutFile(ctx, scope, file); err != nil {
			t.Fatal(err)
		}
	}

	digest, _, err := projectAssistantExecSnapshot(ctx, projectAssistantWorkflowRunContext{
		Workspace: files, WorkspaceScope: scope,
	}, projectTemplateComponent{WorkspacePath: "backend"}, 0, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	// Development sync preserves the sorted first binary and skips the second
	// because two 25 MiB binaries exceed its 48 MiB decoded bundle limit.
	// The live agent's digest therefore covers a.bin and main.go only.
	hash := sha256.New()
	for _, entry := range []struct {
		path string
		data []byte
	}{{"main.go", []byte("package main\n")}, {"public/a.bin", image}} {
		_, _ = hash.Write([]byte(entry.path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(entry.data)
		_, _ = hash.Write([]byte{0})
	}
	wantDigest := hex.EncodeToString(hash.Sum(nil))
	if digest != wantDigest {
		t.Fatalf("exec digest = %q, want exact bundle-bounded sync digest %q", digest, wantDigest)
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
		tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders,
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
	_, _, err := projectAssistantExecSnapshot(context.Background(), projectAssistantWorkflowRunContext{
		Workspace:      files,
		WorkspaceScope: scope,
		RunState:       runState,
	}, projectTemplateComponent{WorkspacePath: "backend"}, 0, nil)
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

func TestProjectAssistantExecStartFailureCleanupDecision(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "transport error", err: errors.New("connection reset"), want: true},
		{name: "canceled request", err: context.Canceled, want: true},
		{name: "request timeout", err: &projectAssistantExecHTTPError{status: http.StatusRequestTimeout}, want: true},
		{name: "server error", err: &projectAssistantExecHTTPError{status: http.StatusBadGateway}, want: true},
		{name: "bad request", err: &projectAssistantExecHTTPError{status: http.StatusBadRequest}, want: false},
		{name: "idempotency conflict", err: &projectAssistantExecHTTPError{status: http.StatusConflict}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectAssistantExecStartMayHaveBeenAccepted(tc.err); got != tc.want {
				t.Fatalf("projectAssistantExecStartMayHaveBeenAccepted(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

func TestProjectAssistantCancelExecByRequestIDUsesDetachedBoundedCancel(t *testing.T) {
	var got projectSandboxExecRequest
	called := false
	projectAssistantCancelExecByRequestID("request-1", func(ctx context.Context, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
		called = true
		got = request
		if ctx.Err() != nil {
			t.Fatalf("cleanup context already canceled: %v", ctx.Err())
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > projectAssistantExecCancelTimeout {
			t.Fatalf("cleanup deadline = %v (present=%t), want a live bounded timeout", deadline, ok)
		}
		return projectSandboxExecResponse{}, nil
	})
	if !called || got.Action != "cancel" || got.RequestID != "request-1" || got.SessionID != "" {
		t.Fatalf("cleanup request = %#v (called=%t), want request-ID-only cancel", got, called)
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
	}, `{"status":"failed","summary":"Command failed in component \"backend\".","exitCode":2,"durationMs":123,"sourceRevision":7,"sourceDigest":"sha256:run-snapshot","syncStatus":"succeeded"}`, "failed")
	if metadata == nil || metadata.Component != "backend" || metadata.Workdir != "internal" || metadata.TimeoutSeconds != 42 ||
		metadata.NetworkProfile != "application-runtime" || metadata.AuthorityProfile != "application-container" || metadata.WritebackPolicy != "runtime-workspace-only" || metadata.ExitCode == nil || *metadata.ExitCode != 2 ||
		metadata.SourceRevision != 7 || metadata.SourceDigest != "sha256:run-snapshot" || metadata.SyncStatus != "succeeded" {
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
	}, `{"status":"timed_out","summary":"timeout token=summary-secret","exitCode":124,"durationMs":99,"sourceRevision":14,"sourceDigest":"sha256:private-snapshot","syncStatus":"succeeded","stdout":["TOKEN=stdout-secret","Bearer bearer-secret-value","https://example.test/?api_key=query-secret","{\"token\":\"json-secret\"}","ghp_1234567890abcdefghijkl","AKIA1234567890ABCDEF"],"stderr":["OPENAI_API_KEY=env-secret"]}`, "failed")
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
	if metadata.SourceRevision != 14 || metadata.SourceDigest != "sha256:private-snapshot" || metadata.SyncStatus != "succeeded" {
		t.Fatalf("nested exec source receipt = %#v, want the dispatch snapshot fence", metadata)
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

func TestProjectAssistantExecMetadataMergeKeepsDispatchSourceFence(t *testing.T) {
	merged := mergeProjectAssistantExecMetadata(
		&projectAssistantExecMetadata{Status: "running", SourceRevision: 8, SourceDigest: "sha256:dispatch", SyncStatus: "succeeded"},
		&projectAssistantExecMetadata{Status: "succeeded", SourceRevision: 9, SourceDigest: "sha256:later", SyncStatus: "changed"},
	)
	if merged.SourceRevision != 8 || merged.SourceDigest != "sha256:dispatch" || merged.SyncStatus != "succeeded" {
		t.Fatalf("merged exec receipt = %#v, want the immutable dispatch snapshot", merged)
	}

	filled := mergeProjectAssistantExecMetadata(
		&projectAssistantExecMetadata{Status: "running"},
		&projectAssistantExecMetadata{Status: "succeeded", SourceRevision: 9, SourceDigest: "sha256:dispatch", SyncStatus: "succeeded"},
	)
	if filled.SourceRevision != 9 || filled.SourceDigest != "sha256:dispatch" || filled.SyncStatus != "succeeded" {
		t.Fatalf("filled exec receipt = %#v, want terminal dispatch snapshot", filled)
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
			result:    `{"status":"succeeded","summary":"Command succeeded in component \"frontend\".","exitCode":0,"durationMs":742,"outputTruncated":true,"sourceRevision":11,"sourceDigest":"sha256:thread-a"}`,
			exitCode:  0,
			duration:  742,
		},
		{
			id:        "exec-test",
			component: "backend",
			argv:      []string{"go", "test", "./..."},
			result:    `{"status":"failed","summary":"Command failed in component \"backend\".","exitCode":1,"durationMs":1834,"sourceRevision":12,"sourceDigest":"sha256:thread-b"}`,
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
		if action.Exec.SourceRevision != uint64(11+index) || action.Exec.SourceDigest != []string{"sha256:thread-a", "sha256:thread-b"}[index] {
			t.Fatalf("action %d source receipt = %#v, want its immutable dispatch snapshot", index, action.Exec)
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

// A coordinator can durably accept START even when its response never reaches
// App Studio. Retrying must recover that session, not launch another command.
func TestProjectAssistantExecStartRecoversLostAcceptedResponse(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var launches atomic.Int32
	var firstRequest projectSandboxExecRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got projectSandboxExecRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if calls.Add(1) == 1 {
			firstRequest = got
			launches.Add(1)
			<-r.Context().Done() // accepted, but response lost until attempt deadline
			return
		}
		if !reflect.DeepEqual(got, firstRequest) {
			t.Errorf("retry changed command identity or source evidence: %#v != %#v", got, firstRequest)
		}
		_ = json.NewEncoder(w).Encode(projectSandboxExecResponse{SessionID: "accepted-session", State: "succeeded"})
	}))
	defer server.Close()
	request := projectSandboxExecRequest{Action: "start", RequestID: "lost-response", Argv: []string{"npm", "run", "build"}, SourceRevision: 9, SourceDigest: "source-digest"}
	response, err := retryProjectAssistantExecStart(context.Background(), request, func(ctx context.Context, got projectSandboxExecRequest) (projectSandboxExecResponse, error) {
		body, _ := json.Marshal(got)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(body))
		if err != nil {
			return projectSandboxExecResponse{}, err
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			return projectSandboxExecResponse{}, err
		}
		defer func() { _ = resp.Body.Close() }()
		var result projectSandboxExecResponse
		err = json.NewDecoder(resp.Body).Decode(&result)
		return result, err
	})
	if err != nil || response.SessionID != "accepted-session" || calls.Load() != 2 || launches.Load() != 1 {
		t.Fatalf("result=%#v error=%v requests=%d launches=%d", response, err, calls.Load(), launches.Load())
	}
}

func TestProjectAssistantExecStartCallerCancellationStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := retryProjectAssistantExecStart(ctx, projectSandboxExecRequest{Action: "start", RequestID: "cancel"}, func(ctx context.Context, _ projectSandboxExecRequest) (projectSandboxExecResponse, error) {
		calls++
		cancel()
		<-ctx.Done()
		return projectSandboxExecResponse{}, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("error=%v attempts=%d", err, calls)
	}
}

func TestProjectAssistantExecStartTimeoutDiagnosticSurvivesProjection(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://internal.invalid/" + strings.Repeat("long-route/", 40), Err: context.DeadlineExceeded}
	summary := projectAssistantExecStartFailureSummary(err)
	if len(summary) > 240 || !strings.Contains(summary, "startup deadline") || strings.Contains(summary, "internal.invalid") {
		t.Fatalf("timeout diagnostic lost its cause: %q", summary)
	}
}
