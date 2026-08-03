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
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

type persistentRuntimeRoundTripper struct {
	mu                sync.Mutex
	request           *http.Request
	records           map[string]execWorkerRequest
	lostFirstResponse bool
	calls             int
}

func (t *persistentRuntimeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var request execWorkerRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	t.request = r.Clone(r.Context())
	if t.records == nil {
		t.records = map[string]execWorkerRequest{}
	}
	if request.Action == ExecActionStart {
		if previous, found := t.records[request.SessionID]; found && previous.Fingerprint != request.Fingerprint {
			return workerResponse(r, http.StatusConflict, `idempotency conflict`), nil
		}
		t.records[request.SessionID] = request
		if t.lostFirstResponse {
			t.lostFirstResponse = false
			return nil, errors.New("connection reset after worker persisted start")
		}
	}
	exit := int32(0)
	result := ExecResult{SessionID: request.SessionID, RequestID: request.RequestID, State: "succeeded", ExitCode: &exit, Stdout: "ok\n"}
	body, _ := json.Marshal(result)
	return workerResponse(r, http.StatusOK, string(body)), nil
}

func workerResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}
}

type persistentTestRuntime struct {
	transport *persistentRuntimeRoundTripper
}

func (r *persistentTestRuntime) Host() string                          { return "https://runtime.example" }
func (r *persistentTestRuntime) Transport() (http.RoundTripper, error) { return r.transport, nil }
func (r *persistentTestRuntime) ControlToken(context.Context, string, string) (string, error) {
	return "control-token", nil
}

func persistentTestCall() ExecCall {
	return ExecCall{
		Workspace: "ws", Resource: "applications", Name: "app", Component: "backend",
		Capability: &infrav1alpha1.TemplateDataPlaneExec{MaxTimeoutSeconds: 5, MaxOutputBytes: 128},
		WorkingDir: "/workspace", RuntimeNamespace: "runtime", CallerKey: "caller",
		ControlTarget: ResolvedTarget{
			ServiceNamespace: "runtime", ServiceName: "app-dev-backend-control", ServicePort: "exec",
			TokenSecretNamespace: "runtime", TokenSecretName: "app-control", UpstreamPath: "/exec",
		},
		Request: ExecRequest{
			Action: ExecActionStart, RequestID: "request-1", Argv: []string{"go", "test", "./..."},
			SourceRevision: 4, SourceDigest: strings.Repeat("a", 64),
		},
		IdempotencyKey: "request-1",
	}
}

func TestPersistentExecutorIsReplicaSafeAndRetriesLostStartResponse(t *testing.T) {
	transport := &persistentRuntimeRoundTripper{lostFirstResponse: true}
	runtime := &persistentTestRuntime{transport: transport}
	first, err := NewPersistentExecutor(runtime)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPersistentExecutor(runtime)
	if err != nil {
		t.Fatal(err)
	}
	call := persistentTestCall()
	started, err := first.Start(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := second.Start(context.Background(), call)
	if err != nil || duplicate.SessionID != started.SessionID {
		t.Fatalf("cross-replica duplicate start = %#v, %v", duplicate, err)
	}
	poll := call
	poll.Request = ExecRequest{Action: ExecActionPoll, SessionID: started.SessionID, RequestID: "request-1"}
	result, err := second.Poll(context.Background(), poll)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "succeeded" || result.ExitCode == nil || *result.ExitCode != 0 || result.Stdout != "ok\n" {
		t.Fatalf("result = %#v", result)
	}
	if transport.calls != 4 { // lost+retry, duplicate start, poll
		t.Fatalf("worker calls = %d, want 4", transport.calls)
	}
	if got, want := transport.request.URL.Path, "/api/v1/namespaces/runtime/services/app-dev-backend-control:exec/proxy/exec"; got != want {
		t.Fatalf("worker proxy path = %q, want %q", got, want)
	}
	if got := transport.request.Header.Get(controlTokenHeader); got != "control-token" {
		t.Fatalf("worker control token = %q", got)
	}
}

func TestPersistentExecutorChangedFingerprintConflicts(t *testing.T) {
	transport := &persistentRuntimeRoundTripper{}
	executor, _ := NewPersistentExecutor(&persistentTestRuntime{transport: transport})
	call := persistentTestCall()
	if _, err := executor.Start(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	call.Request.Argv = []string{"go", "version"}
	if _, err := executor.Start(context.Background(), call); err == nil {
		t.Fatal("changed request fingerprint was accepted")
	}
}
