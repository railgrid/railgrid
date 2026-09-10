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

package dataplane

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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

type fakeDevelopmentGetter struct {
	component *infrav1alpha1.TemplateDevelopmentComponent
	err       error
}

func (f *fakeDevelopmentGetter) DevelopmentFor(context.Context, string, string) (*infrav1alpha1.TemplateDevelopmentComponent, error) {
	return f.component, f.err
}

type fakeExecAuthorizer struct {
	got ExecAuthorization
	err error
}

func (f *fakeExecAuthorizer) AuthorizeExec(_ context.Context, authorization ExecAuthorization) error {
	f.got = authorization
	return f.err
}

type fakeExecutor struct {
	startCall  ExecCall
	pollCall   ExecCall
	cancelCall ExecCall
	result     ExecResult
	err        error
}

func (f *fakeExecutor) Start(_ context.Context, call ExecCall) (ExecResult, error) {
	f.startCall = call
	return f.result, f.err
}
func (f *fakeExecutor) Poll(_ context.Context, call ExecCall) (ExecResult, error) {
	f.pollCall = call
	return f.result, f.err
}
func (f *fakeExecutor) Cancel(_ context.Context, call ExecCall) (ExecResult, error) {
	f.cancelCall = call
	return f.result, f.err
}

func execContract() *infrav1alpha1.TemplateDataPlane {
	return &infrav1alpha1.TemplateDataPlane{
		RuntimeNamespacePath: "status.runtimeNamespace",
		TokenSecretPath:      "status.controlSecretRef",
		Components: map[string]infrav1alpha1.TemplateDataPlaneComponent{
			"backend": {
				Endpoints: map[string]infrav1alpha1.TemplateDataPlaneEndpoint{
					"sync": {
						ServicePath: "status.components.backend.controlServiceRef",
						Port:        "control", UpstreamPath: "/sync", Methods: []string{http.MethodPost},
					},
				},
				Exec: &infrav1alpha1.TemplateDataPlaneExec{
					MaxTimeoutSeconds: 30,
					MaxOutputBytes:    8,
				},
			},
		},
	}
}

func execRequest(t *testing.T, action ExecAction) *http.Request {
	t.Helper()
	body := ExecRequest{
		Action:         action,
		SessionID:      "session-1",
		SourceRevision: 1,
		SourceDigest:   "sha256:source",
		Argv:           []string{"go", "test", "./..."},
	}
	if action == ExecActionStart {
		body.SessionID = ""
		body.RequestID = "run-1"
	} else {
		body.SourceRevision = 0
		body.SourceDigest = ""
		body.Argv = nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, PathPrefix+"clusters/ws/instances/app/components/backend/exec", strings.NewReader(string(raw)))
	r.Header.Set("Authorization", "Bearer caller-token")
	r.Header.Set("Idempotency-Key", "run-1")
	return r
}

func newExecHandler(t *testing.T, executor *fakeExecutor, authorizer *fakeExecAuthorizer, development *fakeDevelopmentGetter) *Handler {
	t.Helper()
	instance := &fakeInstanceGetter{instance: execInstance()}
	return NewHandler(instance, &fakeContractGetter{contract: execContract()}, &fakeRuntime{}, WithExec(executor, authorizer), WithDevelopmentGetter(development))
}

func execInstance() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "app", "generation": int64(2)},
		"spec":     map[string]any{"template": infrav1alpha1.UniversalCodingSandboxTemplateName},
		"status": map[string]any{
			"farosNetworkPhase":  infrav1alpha1.FarosNetworkPhaseRuntime,
			"phase":              "Ready",
			"observedGeneration": int64(2),
			"conditions":         []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(2)}},
			"runtimeNamespace":   "ws-default",
			"controlSecretRef":   map[string]any{"name": "app-control", "namespace": "ws-default"},
			"components": map[string]any{"backend": map[string]any{
				"controlServiceRef": map[string]any{"name": "app-backend-control", "namespace": "ws-default"},
			}},
		},
	}}
}

func TestHandlerExecDeniesSetupPhaseBeforeExecutorOrAuthorizer(t *testing.T) {
	executor := &fakeExecutor{}
	authorizer := &fakeExecAuthorizer{}
	h := newExecHandler(t, executor, authorizer, &fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}})
	instance := h.instances.(*fakeInstanceGetter).instance
	status := instance.Object["status"].(map[string]any)
	status[infrav1alpha1.FarosNetworkPhaseStatusField] = infrav1alpha1.FarosNetworkPhaseSetup
	status["conditions"] = []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(2)}}
	status["phase"] = "Ready"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, execRequest(t, ExecActionStart))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %q", rec.Code, rec.Body.String())
	}
	if executor.startCall.Request.Action != "" {
		t.Fatalf("executor was called for setup-phase Instance: %+v", executor.startCall)
	}
	if authorizer.got.Component != "" {
		t.Fatalf("authorizer was called for setup-phase Instance: %+v", authorizer.got)
	}
}

func TestHandlerExecDeniesRuntimePhaseUntilInstanceReady(t *testing.T) {
	h := newExecHandler(t, &fakeExecutor{}, &fakeExecAuthorizer{}, &fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}})
	instance := h.instances.(*fakeInstanceGetter).instance
	instance.Object["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "Ready", "status": "False"}}
	instance.Object["status"].(map[string]any)["phase"] = "Pending"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, execRequest(t, ExecActionStart))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %q", rec.Code, rec.Body.String())
	}
}

func TestHandlerExecIgnoresTamperedTenantSpecPhase(t *testing.T) {
	h := newExecHandler(t, &fakeExecutor{}, &fakeExecAuthorizer{}, &fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}})
	instance := h.instances.(*fakeInstanceGetter).instance
	status := instance.Object["status"].(map[string]any)
	status[infrav1alpha1.FarosNetworkPhaseStatusField] = infrav1alpha1.FarosNetworkPhaseSetup
	status["conditions"] = []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(2)}}
	status["phase"] = "Ready"
	instance.Object["spec"].(map[string]any)["values"] = map[string]any{
		infrav1alpha1.FarosNetworkPhaseField: infrav1alpha1.FarosNetworkPhaseRuntime,
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, execRequest(t, ExecActionStart))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 despite forged tenant spec phase; body %q", rec.Code, rec.Body.String())
	}
}

func TestHandlerExecDeniesStaleReadyMirrorDuringRuntimeConvergence(t *testing.T) {
	h := newExecHandler(t, &fakeExecutor{}, &fakeExecAuthorizer{}, &fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}})
	instance := h.instances.(*fakeInstanceGetter).instance
	status := instance.Object["status"].(map[string]any)
	status[infrav1alpha1.FarosNetworkPhaseStatusField] = infrav1alpha1.FarosNetworkPhaseRuntime
	status["phase"] = "Ready"
	status["observedGeneration"] = int64(2)
	status["conditions"] = []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(1)}}
	instance.Object["spec"].(map[string]any)["values"] = map[string]any{
		infrav1alpha1.FarosNetworkPhaseField: infrav1alpha1.FarosNetworkPhaseRuntime,
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, execRequest(t, ExecActionStart))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want stale Ready mirror denied; body %q", rec.Code, rec.Body.String())
	}
}

func TestHandlerExecDeniesStaleTenantStatusGeneration(t *testing.T) {
	h := newExecHandler(t, &fakeExecutor{}, &fakeExecAuthorizer{}, &fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}})
	instance := h.instances.(*fakeInstanceGetter).instance
	status := instance.Object["status"].(map[string]any)
	status["observedGeneration"] = int64(1)
	status["conditions"] = []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(1)}}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, execRequest(t, ExecActionStart))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want stale tenant status denied; body %q", rec.Code, rec.Body.String())
	}
}

func TestHandlerExecPreservesOrdinaryDevelopmentCompatibility(t *testing.T) {
	h := newExecHandler(t, &fakeExecutor{result: ExecResult{SessionID: "session-1", State: "running"}}, &fakeExecAuthorizer{}, &fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}})
	instance := h.instances.(*fakeInstanceGetter).instance
	instance.Object["spec"].(map[string]any)["template"] = "ordinary-development"
	instance.Object["status"].(map[string]any)[infrav1alpha1.FarosNetworkPhaseStatusField] = infrav1alpha1.FarosNetworkPhaseSetup

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, execRequest(t, ExecActionStart))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want ordinary development exec compatibility; body %q", rec.Code, rec.Body.String())
	}
}

func TestHandlerExecAllowsReadyRuntimeAndPassesPlatformDevelopment(t *testing.T) {
	executor := &fakeExecutor{result: ExecResult{SessionID: "session-1", State: "running", Stdout: "123456789"}}
	authorizer := &fakeExecAuthorizer{}
	development := &fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{WorkingDir: "/workspace/backend"}}
	h := newExecHandler(t, executor, authorizer, development)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, execRequest(t, ExecActionStart))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if executor.startCall.WorkingDir != "/workspace/backend" {
		t.Fatalf("executor working dir = %q", executor.startCall.WorkingDir)
	}
	if executor.startCall.IdempotencyKey != "run-1" || executor.startCall.Request.RequestID != "run-1" {
		t.Fatalf("idempotency = %q / %q", executor.startCall.IdempotencyKey, executor.startCall.Request.RequestID)
	}
	if authorizer.got.CallerToken != "caller-token" || authorizer.got.Component != "backend" {
		t.Fatalf("authorizer context = token %q component %q", authorizer.got.CallerToken, authorizer.got.Component)
	}
	if !strings.Contains(rec.Body.String(), `"truncated":true`) {
		t.Fatalf("result was not bounded: %s", rec.Body.String())
	}
}

func TestHandlerExecRecordsActivityAfterAuthorization(t *testing.T) {
	rt := &activityRuntime{fakeRuntime: &fakeRuntime{}}
	h := NewHandler(
		&fakeInstanceGetter{instance: execInstance()},
		&fakeContractGetter{contract: execContract()},
		rt,
		WithExec(&fakeExecutor{result: ExecResult{SessionID: "session-1", State: "running"}}, &fakeExecAuthorizer{}),
		WithDevelopmentGetter(&fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}}),
	)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, execRequest(t, ExecActionStart))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if rt.calls != 1 {
		t.Fatalf("activity calls = %d, want 1", rt.calls)
	}
}

func TestHandlerExecPollAndCancelDispatch(t *testing.T) {
	executor := &fakeExecutor{result: ExecResult{SessionID: "session-1", State: "canceled"}}
	authorizer := &fakeExecAuthorizer{}
	h := newExecHandler(t, executor, authorizer, &fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}})
	for _, action := range []ExecAction{ExecActionPoll, ExecActionCancel} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, execRequest(t, action))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body %q", action, rec.Code, rec.Body.String())
		}
	}
	if executor.pollCall.Request.Action != ExecActionPoll || executor.cancelCall.Request.Action != ExecActionCancel {
		t.Fatalf("dispatch actions = %q / %q", executor.pollCall.Request.Action, executor.cancelCall.Request.Action)
	}
}

func TestHandlerExecRejectsMissingIdempotencyAndTail(t *testing.T) {
	h := newExecHandler(t, &fakeExecutor{}, &fakeExecAuthorizer{}, &fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}})
	r := execRequest(t, ExecActionStart)
	r.Header.Del("Idempotency-Key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key status = %d, want 400", rec.Code)
	}
	r = execRequest(t, ExecActionStart)
	r.URL.Path += "/tail"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tail status = %d, want 400", rec.Code)
	}
}

func TestHandlerExecRequiresAuthorizer(t *testing.T) {
	h := NewHandler(&fakeInstanceGetter{instance: execInstance()}, &fakeContractGetter{contract: execContract()}, &fakeRuntime{}, WithExec(&fakeExecutor{}, nil), WithDevelopmentGetter(&fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, execRequest(t, ExecActionStart))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestDecodeExecRequestRequiresSourceRevision(t *testing.T) {
	body, err := json.Marshal(ExecRequest{Action: ExecActionStart, RequestID: "key", Argv: []string{"true"}, SourceDigest: "sha"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	r.Header.Set("Idempotency-Key", "key")
	w := httptest.NewRecorder()
	if _, _, err := decodeExecRequest(w, r, &infrav1alpha1.TemplateDataPlaneExec{}); err == nil || !strings.Contains(err.Error(), "sourceRevision is required") {
		t.Fatalf("decodeExecRequest error = %v, want missing sourceRevision", err)
	}
}

func TestDecodeExecRequestRejectsRetiredSourceSnapshot(t *testing.T) {
	body := `{"action":"start","requestID":"key","sourceRevision":1,"sourceDigest":"sha","argv":["true"],"files":[]}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Idempotency-Key", "key")
	w := httptest.NewRecorder()
	if _, _, err := decodeExecRequest(w, r, &infrav1alpha1.TemplateDataPlaneExec{}); err == nil || !strings.Contains(err.Error(), `unknown field "files"`) {
		t.Fatalf("decodeExecRequest error = %v, want retired files field rejection", err)
	}
}

// scriptedExecutor returns a fixed start result and then walks a list of poll
// results, repeating the last one once the script is exhausted.
type scriptedExecutor struct {
	start     ExecResult
	polls     []ExecResult
	startCall ExecCall
	pollCalls []ExecCall
}

func (s *scriptedExecutor) Start(_ context.Context, call ExecCall) (ExecResult, error) {
	s.startCall = call
	return s.start, nil
}

func (s *scriptedExecutor) Poll(_ context.Context, call ExecCall) (ExecResult, error) {
	s.pollCalls = append(s.pollCalls, call)
	if len(s.polls) == 0 {
		return s.start, nil
	}
	index := min(len(s.pollCalls)-1, len(s.polls)-1)
	return s.polls[index], nil
}

func (s *scriptedExecutor) Cancel(context.Context, ExecCall) (ExecResult, error) {
	return ExecResult{}, errors.New("unexpected cancel")
}

// fastExecRun shortens the run poll cadence and wait budget for a test.
func fastExecRun(t *testing.T, budget time.Duration) {
	t.Helper()
	initial, maxDelay, waitBudget := execRunPollInitial, execRunPollMax, execRunWaitBudget
	execRunPollInitial, execRunPollMax = time.Millisecond, 2*time.Millisecond
	execRunWaitBudget = func(int32) time.Duration { return budget }
	t.Cleanup(func() { execRunPollInitial, execRunPollMax, execRunWaitBudget = initial, maxDelay, waitBudget })
}

func newScriptedExecHandler(executor Executor, rt *fakeRuntime) *Handler {
	return NewHandler(&fakeInstanceGetter{instance: execInstance()}, &fakeContractGetter{contract: execContract()}, rt,
		WithExec(executor, &fakeExecAuthorizer{}),
		WithDevelopmentGetter(&fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}}))
}

func postExec(t *testing.T, h *Handler, body, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, PathPrefix+"clusters/ws/instances/app/components/backend/exec", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer caller-token")
	if idempotencyKey != "" {
		r.Header.Set("Idempotency-Key", idempotencyKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func decodeExecResult(t *testing.T, rec *httptest.ResponseRecorder) ExecResult {
	t.Helper()
	var result ExecResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode exec result %q: %v", rec.Body.String(), err)
	}
	return result
}

const testSourceDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

func TestHandlerExecRunPollsUntilTerminal(t *testing.T) {
	fastExecRun(t, 5*time.Second)
	exitCode := int32(3)
	executor := &scriptedExecutor{
		start: ExecResult{SessionID: "session-1", RequestID: "run-1", State: "queued"},
		polls: []ExecResult{
			{SessionID: "session-1", RequestID: "run-1", State: "running"},
			{SessionID: "session-1", RequestID: "run-1", State: "running"},
			{SessionID: "session-1", RequestID: "run-1", State: "failed", ExitCode: &exitCode, Stdout: "out", Stderr: "err"},
		},
	}
	h := newScriptedExecHandler(executor, &fakeRuntime{})
	rec := postExec(t, h, `{"action":"run","argv":["go","test"],"sourceRevision":5,"sourceDigest":"`+testSourceDigest+`"}`, "run-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	got := decodeExecResult(t, rec)
	if got.State != "failed" || got.ExitCode == nil || *got.ExitCode != 3 || got.SessionID != "session-1" {
		t.Fatalf("run result = %+v, want terminal failed result with exit code 3", got)
	}
	if got.SourceRevision != 5 || got.SourceDigest != testSourceDigest {
		t.Fatalf("run result source = %d/%q, want the executed revision", got.SourceRevision, got.SourceDigest)
	}
	if executor.startCall.Request.Action != ExecActionStart || executor.startCall.IdempotencyKey != "run-1" {
		t.Fatalf("start call = action %q key %q, want start with the caller key", executor.startCall.Request.Action, executor.startCall.IdempotencyKey)
	}
	if len(executor.pollCalls) != 3 {
		t.Fatalf("poll calls = %d, want 3", len(executor.pollCalls))
	}
	for _, call := range executor.pollCalls {
		if call.Request.Action != ExecActionPoll || call.Request.SessionID != "session-1" || call.Request.RequestID != "run-1" || len(call.Request.Argv) != 0 {
			t.Fatalf("poll request = %+v, want a session-bound poll", call.Request)
		}
	}
}

func TestHandlerExecRunReturnsRunningSessionAtWaitBudget(t *testing.T) {
	fastExecRun(t, 20*time.Millisecond)
	executor := &scriptedExecutor{
		start: ExecResult{SessionID: "session-1", State: "queued"},
		polls: []ExecResult{{State: "running", Stdout: "partial"}},
	}
	h := newScriptedExecHandler(executor, &fakeRuntime{})
	rec := postExec(t, h, `{"action":"run","argv":["sleep","60"],"sourceRevision":1,"sourceDigest":"`+testSourceDigest+`"}`, "run-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	got := decodeExecResult(t, rec)
	if got.State != "running" || got.SessionID != "session-1" || got.RequestID != "run-1" {
		t.Fatalf("run result at wait budget = %+v, want the running session so the caller can poll", got)
	}
	if len(executor.pollCalls) == 0 {
		t.Fatal("run never polled before the wait budget expired")
	}
}

func TestExecRunWaitBudgetAddsSlackAndStaysBelowProxyTimeouts(t *testing.T) {
	if got := execRunWaitBudget(30); got != 40*time.Second {
		t.Fatalf("budget(30s) = %s, want 40s", got)
	}
	if got := execRunWaitBudget(ExecMaxTimeoutSeconds); got != execRunMaxWait {
		t.Fatalf("budget(%ds) = %s, want cap %s", ExecMaxTimeoutSeconds, got, execRunMaxWait)
	}
}

func TestHandlerExecRunGeneratesIdempotencyKeyButStartRequiresOne(t *testing.T) {
	fastExecRun(t, time.Second)
	executor := &scriptedExecutor{start: ExecResult{SessionID: "session-1", State: "succeeded"}}
	h := newScriptedExecHandler(executor, &fakeRuntime{})
	body := `{"action":"%s","argv":["true"],"sourceRevision":1,"sourceDigest":"` + testSourceDigest + `"}`

	rec := postExec(t, h, fmt.Sprintf(body, ExecActionRun), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("run without key status = %d, body %q", rec.Code, rec.Body.String())
	}
	key := executor.startCall.IdempotencyKey
	if len(key) != 32 || strings.Trim(key, "0123456789abcdef") != "" || executor.startCall.Request.RequestID != key {
		t.Fatalf("generated key = %q / requestID %q, want a 128-bit hex key used as requestID", key, executor.startCall.Request.RequestID)
	}
	if got := decodeExecResult(t, rec); got.RequestID != key {
		t.Fatalf("run result requestID = %q, want generated key %q", got.RequestID, key)
	}
	if len(executor.pollCalls) != 0 {
		t.Fatalf("terminal start was polled %d times", len(executor.pollCalls))
	}

	executor.startCall = ExecCall{}
	rec = postExec(t, h, fmt.Sprintf(body, ExecActionStart), "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Idempotency-Key is required") {
		t.Fatalf("start without key status = %d body %q, want 400", rec.Code, rec.Body.String())
	}
	if executor.startCall.Request.Action != "" {
		t.Fatal("start without key reached the executor")
	}
}

// statusUpstream serves the dev agent /status through the fake runtime and
// records what the provider sent.
func statusUpstream(t *testing.T, status int, body string) (*fakeRuntime, *string, *string) {
	t.Helper()
	var gotPath, gotToken string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get(controlTokenHeader)
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	return &fakeRuntime{host: upstream.URL, token: "control-token"}, &gotPath, &gotToken
}

func TestHandlerExecDefaultsSourceToAppliedComponentRevision(t *testing.T) {
	fastExecRun(t, time.Second)
	for _, action := range []ExecAction{ExecActionStart, ExecActionRun} {
		t.Run(string(action), func(t *testing.T) {
			rt, gotPath, gotToken := statusUpstream(t, http.StatusOK, `{"running":true,"sourceRevision":4,"sourceDigest":"`+testSourceDigest+`"}`)
			executor := &scriptedExecutor{start: ExecResult{SessionID: "session-1", State: "succeeded"}}
			h := newScriptedExecHandler(executor, rt)
			rec := postExec(t, h, `{"action":"`+string(action)+`","argv":["true"]}`, "run-1")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
			}
			if want := "/api/v1/namespaces/ws-default/services/app-backend-control:control/proxy/status"; *gotPath != want {
				t.Fatalf("status path = %q, want %q", *gotPath, want)
			}
			if *gotToken != "control-token" {
				t.Fatalf("status control token = %q", *gotToken)
			}
			if executor.startCall.Request.SourceRevision != 4 || executor.startCall.Request.SourceDigest != testSourceDigest {
				t.Fatalf("executor source = %d/%q, want applied revision", executor.startCall.Request.SourceRevision, executor.startCall.Request.SourceDigest)
			}
			if got := decodeExecResult(t, rec); got.SourceRevision != 4 || got.SourceDigest != testSourceDigest {
				t.Fatalf("result source = %d/%q, want applied revision", got.SourceRevision, got.SourceDigest)
			}
		})
	}
}

func TestHandlerExecWithoutAppliedRevisionTellsCallerToSync(t *testing.T) {
	rt, _, _ := statusUpstream(t, http.StatusOK, `{"running":true}`)
	executor := &scriptedExecutor{}
	h := newScriptedExecHandler(executor, rt)
	rec := postExec(t, h, `{"action":"start","argv":["true"]}`, "run-1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %q, want 400", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"sourceRevision is required for start", "sync"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("body %q missing %q", rec.Body.String(), want)
		}
	}
	if executor.startCall.Request.Action != "" {
		t.Fatal("executor was called without an applied revision")
	}
}

func TestHandlerExecSurfacesComponentStatusFailure(t *testing.T) {
	rt, _, _ := statusUpstream(t, http.StatusBadGateway, "runtime supervisor unavailable")
	executor := &scriptedExecutor{}
	h := newScriptedExecHandler(executor, rt)
	rec := postExec(t, h, `{"action":"run","argv":["true"]}`, "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "runtime supervisor unavailable") {
		t.Fatalf("status = %d body %q, want 502 with the status failure", rec.Code, rec.Body.String())
	}
	if executor.startCall.Request.Action != "" {
		t.Fatal("executor was called after status resolution failed")
	}
}

func TestDecodeExecRequestSourceEvidenceMustBeWholeOrOmitted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "both omitted", body: `{"action":"start","argv":["true"]}`},
		{name: "run both omitted", body: `{"action":"run","argv":["true"]}`},
		{name: "revision only", body: `{"action":"start","argv":["true"],"sourceRevision":2}`, wantErr: "sourceDigest is required"},
		{name: "digest only", body: `{"action":"run","argv":["true"],"sourceDigest":"sha"}`, wantErr: "sourceRevision is required"},
		{name: "run rejects sessionID", body: `{"action":"run","argv":["true"],"sessionID":"s"}`, wantErr: "sessionID is not accepted for run"},
		{name: "run needs argv", body: `{"action":"run"}`, wantErr: "run argv must contain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			r.Header.Set("Idempotency-Key", "key")
			_, _, err := decodeExecRequest(httptest.NewRecorder(), r, &infrav1alpha1.TemplateDataPlaneExec{})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("decodeExecRequest error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("decodeExecRequest error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestDecodeExecRequestRunUsesRequestIDAsKey(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"action":"run","requestID":"body-key","argv":["true"]}`))
	req, key, err := decodeExecRequest(httptest.NewRecorder(), r, &infrav1alpha1.TemplateDataPlaneExec{})
	if err != nil {
		t.Fatal(err)
	}
	if key != "body-key" || req.RequestID != "body-key" {
		t.Fatalf("key/requestID = %q/%q, want body requestID", key, req.RequestID)
	}
}

func TestWriteExecAuthorizationError(t *testing.T) {
	w := httptest.NewRecorder()
	writeExecAuthorizationError(w, errors.New("policy denied"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
