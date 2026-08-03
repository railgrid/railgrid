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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	r := httptest.NewRequest(http.MethodPost, PathPrefix+"clusters/ws/applications/app/components/backend/exec", strings.NewReader(string(raw)))
	r.Header.Set("Authorization", "Bearer caller-token")
	r.Header.Set("Idempotency-Key", "run-1")
	return r
}

func newExecHandler(t *testing.T, executor *fakeExecutor, authorizer *fakeExecAuthorizer, development *fakeDevelopmentGetter) *Handler {
	t.Helper()
	instance := &fakeInstanceGetter{instance: &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "app"},
		"status": map[string]any{
			"runtimeNamespace": "tenant-app",
			"controlSecretRef": map[string]any{"name": "app-control", "namespace": "tenant-app"},
			"components": map[string]any{"backend": map[string]any{
				"controlServiceRef": map[string]any{"name": "app-backend-control", "namespace": "tenant-app"},
			}},
		},
	}}}
	return NewHandler(instance, &fakeContractGetter{contract: execContract()}, &fakeRuntime{}, WithExec(executor, authorizer), WithDevelopmentGetter(development))
}

func TestHandlerExecStartAuthorizesAndPassesPlatformDevelopment(t *testing.T) {
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
	h := NewHandler(&fakeInstanceGetter{instance: &unstructured.Unstructured{}}, &fakeContractGetter{contract: execContract()}, &fakeRuntime{}, WithExec(&fakeExecutor{}, nil), WithDevelopmentGetter(&fakeDevelopmentGetter{component: &infrav1alpha1.TemplateDevelopmentComponent{}}))
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

func TestWriteExecAuthorizationError(t *testing.T) {
	w := httptest.NewRecorder()
	writeExecAuthorizationError(w, errors.New("policy denied"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
