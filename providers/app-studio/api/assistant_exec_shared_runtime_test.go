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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/tenant"
	"github.com/railgrid/provider-app-studio/workspace"
)

type sharedRuntimeExecRoundTripper func(*http.Request) (*http.Response, error)

func (f sharedRuntimeExecRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type sharedRuntimeExecLedger struct {
	delegate workspace.Ledger
	failRead atomic.Bool
}

func (l *sharedRuntimeExecLedger) Read(ctx context.Context, scope workspace.Scope) (workspace.LedgerRecord, error) {
	if l.failRead.Load() {
		return workspace.LedgerRecord{}, errors.New("injected workspace ledger read failure")
	}
	return l.delegate.Read(ctx, scope)
}

func (l *sharedRuntimeExecLedger) Update(ctx context.Context, scope workspace.Scope, mutate func(*workspace.LedgerRecord) (bool, error)) (workspace.LedgerRecord, error) {
	return l.delegate.Update(ctx, scope, mutate)
}

type sharedRuntimeExecFixture struct {
	server  *Server
	project *aiv1alpha1.Project
	client  *asclient.Client
	files   *workspace.FileStore
	scope   workspace.Scope
	ledger  *sharedRuntimeExecLedger
	baseCtx context.Context
}

func newSharedRuntimeExecFixture(t *testing.T, roundTrip sharedRuntimeExecRoundTripper) sharedRuntimeExecFixture {
	return newSharedRuntimeExecFixtureWithPersistedProjectUID(t, roundTrip, "project-uid")
}

func newSharedRuntimeExecFixtureWithPersistedProjectUID(t *testing.T, roundTrip sharedRuntimeExecRoundTripper, persistedUID string) sharedRuntimeExecFixture {
	t.Helper()
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"}}
	project.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: project.Name, ProjectUID: string(project.UID)}
	ledger := &sharedRuntimeExecLedger{delegate: workspace.NewMemoryLedger()}
	baseCtx := workspace.ContextWithLedger(context.Background(), ledger)
	files := workspace.NewFileStore(t.TempDir())
	if err := files.ApplyFiles(baseCtx, scope, []workspace.File{{Path: "api/main.go", Content: "package main\n"}}); err != nil {
		t.Fatalf("apply initial project source: %v", err)
	}

	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
		"kind":       "Instance",
		"metadata":   map[string]any{"name": projectTemplateInstanceName(project), "uid": "instance-uid"},
	}}
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		asclient.ProjectGVR:                        "ProjectList",
		templateResource.GVR:                       "TemplateList",
		tenant.InfrastructureInstancesResource.GVR: "InstanceList",
	}, runtimeProjectObject(t, func() *aiv1alpha1.Project {
		persisted := project.DeepCopy()
		persisted.UID = types.UID(persistedUID)
		return persisted
	}()), applicationTemplateObject(), instance)

	server := testDataPlaneServer()
	server.workspaces = files
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: roundTrip}
	}
	return sharedRuntimeExecFixture{
		server: server, project: project, client: asclient.NewFromDynamic(dynamicClient),
		files: files, scope: scope, ledger: ledger, baseCtx: baseCtx,
	}
}

func (f sharedRuntimeExecFixture) runContext(runID string) projectAssistantWorkflowRunContext {
	state := newProjectEinoAssistantRunState()
	syncRevision := state.BeginDevelopmentSyncForNextMutation()
	state.RecordSourceMutation()
	state.CompleteDevelopmentSync(syncRevision, nil)
	return projectAssistantWorkflowRunContext{
		Server: f.server, Client: f.client, Project: f.project,
		Identity:       identity{clusterID: "rgl3jcl2cfl3xa5p", user: "alice", orgUUID: f.scope.OrgUUID, workspaceUUID: f.scope.WorkspaceUUID},
		WorkspaceScope: f.scope, Workspace: f.files, AssistantRunID: runID, RunState: state,
	}
}

func sharedRuntimeExecHTTPResponse(request *http.Request, status int, value any) (*http.Response, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(string(body))), Request: request,
	}, nil
}

func decodeSharedRuntimeExecRequest(request *http.Request) (projectSandboxExecRequest, error) {
	defer func() { _ = request.Body.Close() }()
	var decoded projectSandboxExecRequest
	if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
		return projectSandboxExecRequest{}, err
	}
	return decoded, nil
}

func TestSharedRuntimeExecAllowsConcurrentCommandsForOneProject(t *testing.T) {
	starts := make(chan projectSandboxExecRequest, 2)
	polls := make(chan projectSandboxExecRequest, 2)
	releasePolls := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releasePolls) }) }
	defer release()
	fixture := newSharedRuntimeExecFixture(t, func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, map[string]any{"syncEncodings": []string{}})
		}
		decoded, err := decodeSharedRuntimeExecRequest(request)
		if err != nil {
			return nil, err
		}
		switch decoded.Action {
		case "start":
			starts <- decoded
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{RequestID: decoded.RequestID, SessionID: "session-" + decoded.RequestID, State: "running"})
		case "poll":
			polls <- decoded
			<-releasePolls
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{RequestID: decoded.RequestID, SessionID: decoded.SessionID, State: "succeeded", ExitCode: intPointer(0)})
		default:
			return sharedRuntimeExecHTTPResponse(request, http.StatusBadRequest, map[string]string{"error": "unexpected action"})
		}
	})

	type invocation struct {
		result *projectAssistantExecCommandResult
		err    error
	}
	results := make(chan invocation, 2)
	for _, runID := range []string{"run-one", "run-two"} {
		runCtx := fixture.runContext(runID)
		go func(runCtx projectAssistantWorkflowRunContext) {
			result, err := execProjectAssistantCommand(runCtx)(fixture.baseCtx, &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test", "./..."}})
			results <- invocation{result: result, err: err}
		}(runCtx)
	}

	var first, second projectSandboxExecRequest
	for i := 0; i < 2; i++ {
		select {
		case request := <-starts:
			if i == 0 {
				first = request
			} else {
				second = request
			}
		case <-time.After(5 * time.Second):
			t.Fatal("both same-project commands did not start")
		}
	}
	// START is serialized only through its final target check and HTTP request.
	// Both processes should still be active together while their polls wait.
	polled := make(map[string]bool, 2)
	for i := 0; i < 2; i++ {
		select {
		case request := <-polls:
			polled[request.RequestID] = true
		case <-time.After(5 * time.Second):
			t.Fatal("both command lifetimes did not overlap in POLL")
		}
	}
	if first.RequestID == "" || second.RequestID == "" || first.RequestID == second.RequestID {
		t.Fatalf("same-project START request IDs = %q and %q, want distinct non-empty IDs", first.RequestID, second.RequestID)
	}
	if first.SourceRevision == 0 || first.SourceRevision != second.SourceRevision || first.SourceDigest == "" || first.SourceDigest != second.SourceDigest {
		t.Fatalf("concurrent launch fences = (%d, %q), (%d, %q), want the same non-empty captured source", first.SourceRevision, first.SourceDigest, second.SourceRevision, second.SourceDigest)
	}
	if !polled[first.RequestID] || !polled[second.RequestID] {
		t.Fatalf("overlapping polls = %v, want both START request IDs", polled)
	}
	release()
	for i := 0; i < 2; i++ {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("shared runtime command: %v", got.err)
			}
			if got.result == nil || got.result.Status != "succeeded" {
				t.Fatalf("shared runtime command result = %#v, want succeeded", got.result)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent shared runtime command did not finish")
		}
	}
}

func TestSharedRuntimeExecCancellationTargetsOnlyItsCommand(t *testing.T) {
	firstRunID, secondRunID := "cancel-this-run", "keep-this-run"
	firstRequestID := projectAssistantExecRequestID(firstRunID, "")
	secondRequestID := projectAssistantExecRequestID(secondRunID, "")
	firstPoll := make(chan struct{}, 1)
	secondPoll := make(chan *http.Request, 1)
	cancelRequests := make(chan projectSandboxExecRequest, 2)
	releaseSecondPoll := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseSecondPoll) })
	fixture := newSharedRuntimeExecFixture(t, func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, map[string]any{"syncEncodings": []string{}})
		}
		decoded, err := decodeSharedRuntimeExecRequest(request)
		if err != nil {
			return nil, err
		}
		switch decoded.Action {
		case "start":
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{RequestID: decoded.RequestID, SessionID: "session-" + decoded.RequestID, State: "running"})
		case "poll":
			if decoded.RequestID == firstRequestID {
				firstPoll <- struct{}{}
				<-request.Context().Done()
				return nil, request.Context().Err()
			}
			if decoded.RequestID == secondRequestID {
				secondPoll <- request
				select {
				case <-releaseSecondPoll:
					return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{RequestID: decoded.RequestID, SessionID: decoded.SessionID, State: "succeeded", ExitCode: intPointer(0)})
				case <-request.Context().Done():
					return nil, request.Context().Err()
				}
			}
			return sharedRuntimeExecHTTPResponse(request, http.StatusBadRequest, map[string]string{"error": "unknown request ID"})
		case "cancel":
			cancelRequests <- decoded
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{RequestID: decoded.RequestID, SessionID: decoded.SessionID, State: "cancelled"})
		default:
			return sharedRuntimeExecHTTPResponse(request, http.StatusBadRequest, map[string]string{"error": "unexpected action"})
		}
	})

	type invocation struct {
		requestID string
		result    *projectAssistantExecCommandResult
		err       error
	}
	results := make(chan invocation, 2)
	firstCtx, cancelFirst := context.WithCancel(fixture.baseCtx)
	defer cancelFirst()
	firstRunCtx := fixture.runContext(firstRunID)
	secondRunCtx := fixture.runContext(secondRunID)
	go func() {
		result, err := execProjectAssistantCommand(firstRunCtx)(firstCtx, &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}})
		results <- invocation{requestID: firstRequestID, result: result, err: err}
	}()
	go func() {
		result, err := execProjectAssistantCommand(secondRunCtx)(fixture.baseCtx, &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}})
		results <- invocation{requestID: secondRequestID, result: result, err: err}
	}()

	select {
	case <-firstPoll:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellable command did not reach POLL")
	}
	var secondPollRequest *http.Request
	select {
	case secondPollRequest = <-secondPoll:
	case <-time.After(5 * time.Second):
		t.Fatal("independent command did not reach POLL")
	}
	cancelFirst()
	var cancel projectSandboxExecRequest
	select {
	case cancel = <-cancelRequests:
	case <-time.After(5 * time.Second):
		t.Fatal("canceling one command did not issue a remote cancel")
	}
	if cancel.RequestID != firstRequestID || cancel.SessionID != "session-"+firstRequestID {
		t.Fatalf("remote cancel = %#v, want only request %q/session %q", cancel, firstRequestID, "session-"+firstRequestID)
	}
	if err := secondPollRequest.Context().Err(); err != nil {
		t.Fatalf("canceling the first command also canceled the second poll: %v", err)
	}
	releaseOnce.Do(func() { close(releaseSecondPoll) })

	seen := map[string]*projectAssistantExecCommandResult{}
	for i := 0; i < 2; i++ {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("command %q: %v", got.requestID, got.err)
			}
			seen[got.requestID] = got.result
		case <-time.After(5 * time.Second):
			t.Fatal("commands did not finish after scoped cancellation")
		}
	}
	if got := seen[firstRequestID]; got == nil || got.Status != "outcome_unknown" || got.ExitCode != nil {
		t.Fatalf("canceled command result = %#v, want unresolved outcome without exit code", got)
	}
	if got := seen[secondRequestID]; got == nil || got.Status != "succeeded" {
		t.Fatalf("independent command result = %#v, want succeeded", got)
	}
	select {
	case extra := <-cancelRequests:
		t.Fatalf("cancellation escaped to another command: %#v", extra)
	default:
	}
}

func TestSharedRuntimeExecMarksChangedOrUnreadableSourceStale(t *testing.T) {
	for _, tc := range []struct {
		name             string
		makeUnverifiable bool
		wantSyncStatus   string
	}{
		{name: "newer revision", wantSyncStatus: "stale"},
		{name: "revision check fails", makeUnverifiable: true, wantSyncStatus: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fixture sharedRuntimeExecFixture
			var launched projectSandboxExecRequest
			var launchMu sync.Mutex
			fixture = newSharedRuntimeExecFixture(t, func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodGet {
					return sharedRuntimeExecHTTPResponse(request, http.StatusOK, map[string]any{"syncEncodings": []string{}})
				}
				decoded, err := decodeSharedRuntimeExecRequest(request)
				if err != nil {
					return nil, err
				}
				switch decoded.Action {
				case "start":
					launchMu.Lock()
					launched = decoded
					launchMu.Unlock()
					return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{RequestID: decoded.RequestID, SessionID: "session-" + decoded.RequestID, State: "running"})
				case "poll":
					if tc.makeUnverifiable {
						fixture.ledger.failRead.Store(true)
					} else {
						_, err := fixture.files.PutFile(request.Context(), fixture.scope, workspace.PutOptions{Path: "api/main.go", Data: []byte("package main // changed during execution\n")})
						if err != nil {
							return nil, err
						}
					}
					return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{RequestID: decoded.RequestID, SessionID: decoded.SessionID, State: "succeeded", ExitCode: intPointer(0)})
				default:
					return sharedRuntimeExecHTTPResponse(request, http.StatusBadRequest, map[string]string{"error": "unexpected action"})
				}
			})

			runCtx := fixture.runContext("run-source-fence")
			result, err := execProjectAssistantCommand(runCtx)(fixture.baseCtx, &projectAssistantExecCommandInput{Component: "backend", Argv: []string{"go", "test"}})
			if err != nil {
				t.Fatalf("shared runtime command: %v", err)
			}
			launchMu.Lock()
			start := launched
			launchMu.Unlock()
			if start.Action != "start" || start.SourceRevision == 0 || start.SourceDigest == "" {
				t.Fatalf("launch request did not carry the source fence: %#v", start)
			}
			if result == nil || result.Status != "stale" || !result.SourceStale || result.SyncStatus != tc.wantSyncStatus {
				t.Fatalf("stale command result = %#v, want status=stale sourceStale=true syncStatus=%q", result, tc.wantSyncStatus)
			}
			if result.SourceRevision != start.SourceRevision || result.SourceDigest != start.SourceDigest {
				t.Fatalf("result source receipt changed after dispatch: start=(%d,%q) result=(%d,%q)", start.SourceRevision, start.SourceDigest, result.SourceRevision, result.SourceDigest)
			}
			if !strings.Contains(result.Summary, "unverified") {
				t.Fatalf("stale result summary = %q, want it to say the output is unverified", result.Summary)
			}
		})
	}
}

func TestSharedRuntimeExecBlocksStartWhenProjectWasReplacedAfterSync(t *testing.T) {
	var starts atomic.Int32
	fixture := newSharedRuntimeExecFixtureWithPersistedProjectUID(t, func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, map[string]any{"syncEncodings": []string{}})
		}
		decoded, err := decodeSharedRuntimeExecRequest(request)
		if err != nil {
			return nil, err
		}
		if decoded.Action == "start" {
			starts.Add(1)
		}
		return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{SessionID: "unexpected-session", State: "succeeded", ExitCode: intPointer(0)})
	}, "replacement-uid")

	result, err := execProjectAssistantCommand(fixture.runContext("run-replaced-project"))(fixture.baseCtx, &projectAssistantExecCommandInput{
		Component: "backend",
		Argv:      []string{"go", "test"},
	})
	if err != nil {
		t.Fatalf("shared runtime command: %v", err)
	}
	if result == nil || result.Status != "blocked" {
		t.Fatalf("replaced project result = %#v, want blocked before dispatch", result)
	}
	if starts.Load() != 0 {
		t.Fatalf("START requests = %d, want no command against the replacement project", starts.Load())
	}
}

func intPointer(value int) *int { return &value }
