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
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/railgrid/provider-app-studio/tenant"
	"github.com/railgrid/provider-app-studio/workspace"
)

type assistantExecReviewRoundTripper func(*http.Request) (*http.Response, error)

func (f assistantExecReviewRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func setAssistantExecReviewTransport(runCtx projectAssistantWorkflowRunContext, roundTrip assistantExecReviewRoundTripper) {
	runCtx.Server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: roundTrip}
	}
}

func assistantExecReviewResponse(request *http.Request, status int, payload string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(payload)),
		Request:    request,
	}
}

func setAssistantExecReviewInstanceUID(t *testing.T, runCtx projectAssistantWorkflowRunContext) {
	t.Helper()
	resource := runCtx.Client.Dynamic().Resource(tenant.InfrastructureInstancesResource.GVR)
	instance, err := resource.Get(context.Background(), projectTemplateInstanceName(runCtx.Project), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get development instance for exec review test: %v", err)
	}
	instance.SetUID(types.UID("review-instance-uid"))
	if _, err := resource.Update(context.Background(), instance, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("set development instance UID for exec review test: %v", err)
	}
}

func TestProjectAssistantExecPrestartWorkdirRejectionDoesNotMarkSourceStale(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ConfigureSandboxCapability(CodingSandboxEligibility{Reason: "coding sandbox mode is off"}, nil)
	runCtx, files, scope := newAppStudioSharedExecTestContext(t, state, nil)
	setAssistantExecReviewInstanceUID(t, runCtx)
	var actions []string
	setAssistantExecReviewTransport(runCtx, func(request *http.Request) (*http.Response, error) {
		var payload projectSandboxExecRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		actions = append(actions, payload.Action)
		if payload.Action != "start" {
			t.Errorf("unexpected pre-start rejection action %q", payload.Action)
		}
		if _, err := files.PutFile(context.Background(), scope, workspace.PutOptions{Path: "api/main.go", Data: []byte("package main // changed while start was rejected\n")}); err != nil {
			t.Errorf("change source while rejecting start: %v", err)
		}
		return assistantExecReviewResponse(request, http.StatusBadRequest, `{"rejectionCode":"invalid_workdir","error":"selected directory cannot be opened"}`), nil
	})

	initialRevision, err := files.SourceRevision(context.Background(), scope)
	if err != nil {
		t.Fatalf("read initial source revision: %v", err)
	}
	result, err := execProjectAssistantCommand(runCtx)(context.Background(), &projectAssistantExecCommandInput{
		Component: "backend",
		Argv:      []string{"go", "test", "./..."},
		Workdir:   "missing",
	})
	if err != nil || result == nil {
		t.Fatalf("pre-start rejection = %#v, err=%v", result, err)
	}
	if result.Status != "blocked" || result.SourceStale || result.SyncStatus != "succeeded" || result.SourceRevision != initialRevision {
		t.Fatalf("pre-start rejection receipt = %#v; want blocked, unchanged sync evidence, and no stale claim", result)
	}
	if len(result.Blockers) == 0 || !strings.Contains(strings.Join(result.Blockers, " "), "The command did not start") || strings.Contains(result.Summary, "output is unverified") {
		t.Fatalf("pre-start rejection disclosure = %#v; want a clear did-not-start blocker only", result)
	}
	if strings.Join(actions, ",") != "start" {
		t.Fatalf("pre-start rejection actions = %v; want only start", actions)
	}
	currentRevision, err := files.SourceRevision(context.Background(), scope)
	if err != nil || currentRevision != initialRevision+1 {
		t.Fatalf("current source revision = %d, err=%v; want source change after captured revision %d", currentRevision, err, initialRevision)
	}
}

func TestProjectAssistantExecMalformedTypedRejectionIsNotClassified(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ConfigureSandboxCapability(CodingSandboxEligibility{Reason: "coding sandbox mode is off"}, nil)
	runCtx, _, _ := newAppStudioSharedExecTestContext(t, state, nil)
	setAssistantExecReviewInstanceUID(t, runCtx)
	setAssistantExecReviewTransport(runCtx, func(request *http.Request) (*http.Response, error) {
		return assistantExecReviewResponse(request, http.StatusBadRequest, `{"rejectionCode":"invalid_workdir","error":`), nil
	})

	result, err := execProjectAssistantCommand(runCtx)(context.Background(), &projectAssistantExecCommandInput{
		Component: "backend",
		Argv:      []string{"go", "test", "./..."},
		Workdir:   "missing",
	})
	if err != nil || result == nil {
		t.Fatalf("malformed typed rejection = %#v, err=%v", result, err)
	}
	if result.Status != "error" || len(result.Blockers) != 0 {
		t.Fatalf("malformed typed rejection was classified as a pre-start blocker: %#v", result)
	}
}

func TestProjectAssistantExecTypedRetryRejectionAfterAmbiguousStartRemainsUncertain(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ConfigureSandboxCapability(CodingSandboxEligibility{Reason: "coding sandbox mode is off"}, nil)
	runCtx, _, _ := newAppStudioSharedExecTestContext(t, state, nil)
	setAssistantExecReviewInstanceUID(t, runCtx)
	var startAttempts int
	var actions []projectSandboxExecRequest
	setAssistantExecReviewTransport(runCtx, func(request *http.Request) (*http.Response, error) {
		var payload projectSandboxExecRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		actions = append(actions, payload)
		switch payload.Action {
		case "start":
			startAttempts++
			if startAttempts == 1 {
				return assistantExecReviewResponse(request, http.StatusServiceUnavailable, `{"error":"upstream unavailable"}`), nil
			}
			return assistantExecReviewResponse(request, http.StatusBadRequest, `{"rejectionCode":"invalid_workdir","error":"selected directory cannot be opened"}`), nil
		case "cancel":
			return assistantExecReviewResponse(request, http.StatusOK, `{"state":"canceled","requestID":"`+payload.RequestID+`"}`), nil
		default:
			return assistantExecReviewResponse(request, http.StatusInternalServerError, `{"error":"unexpected action"}`), nil
		}
	})

	result, err := execProjectAssistantCommand(runCtx)(context.Background(), &projectAssistantExecCommandInput{
		Component: "backend",
		Argv:      []string{"go", "test", "./..."},
		Workdir:   "missing",
	})
	if err != nil || result == nil {
		t.Fatalf("ambiguous start followed by typed rejection = %#v, err=%v", result, err)
	}
	if result.Status != "error" || len(result.Blockers) != 0 || result.SessionID != "" || result.ExitCode != nil || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("ambiguous START was treated as definitive rejection or execution receipt: %#v", result)
	}
	if !strings.Contains(result.Summary, "could not be confirmed") || strings.Contains(result.Summary, "The command did not start") || result.SourceStale {
		t.Fatalf("ambiguous START summary/evidence = %#v; want uncertain start with no false pre-start or stale claim", result)
	}
	if startAttempts != 2 || len(actions) != 3 || actions[0].Action != "start" || actions[1].Action != "start" || actions[2].Action != "cancel" {
		t.Fatalf("ambiguous START actions = %#v, attempts=%d; want retry then request-scoped cancel", actions, startAttempts)
	}
	if actions[0].RequestID == "" || actions[0].RequestID != actions[1].RequestID || actions[1].RequestID != actions[2].RequestID {
		t.Fatalf("ambiguous START request IDs = %#v; want one idempotency/request ID including cancel", actions)
	}
}

func TestProjectAssistantExecStartWithoutSessionDoesNotClaimOutputWasRunOrStale(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ConfigureSandboxCapability(CodingSandboxEligibility{Reason: "coding sandbox mode is off"}, nil)
	runCtx, files, scope := newAppStudioSharedExecTestContext(t, state, nil)
	setAssistantExecReviewInstanceUID(t, runCtx)
	var actions []string
	var requestID string
	setAssistantExecReviewTransport(runCtx, func(request *http.Request) (*http.Response, error) {
		var payload projectSandboxExecRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		actions = append(actions, payload.Action)
		switch payload.Action {
		case "start":
			requestID = payload.RequestID
			if _, err := files.PutFile(context.Background(), scope, workspace.PutOptions{Path: "api/main.go", Data: []byte("package main // start acknowledgement lost its session\n")}); err != nil {
				return nil, err
			}
			return assistantExecReviewResponse(request, http.StatusOK, `{"state":"running","requestID":"`+payload.RequestID+`"}`), nil
		case "cancel":
			return assistantExecReviewResponse(request, http.StatusOK, `{"state":"canceled","requestID":"`+payload.RequestID+`"}`), nil
		default:
			return assistantExecReviewResponse(request, http.StatusInternalServerError, `{"error":"unexpected action"}`), nil
		}
	})

	initialRevision, err := files.SourceRevision(context.Background(), scope)
	if err != nil {
		t.Fatalf("read initial source revision: %v", err)
	}
	result, err := execProjectAssistantCommand(runCtx)(context.Background(), &projectAssistantExecCommandInput{
		Component: "backend",
		Argv:      []string{"go", "test", "./..."},
	})
	if err != nil || result == nil {
		t.Fatalf("start without session = %#v, err=%v", result, err)
	}
	if result.Status != "error" || result.SessionID != "" || result.ExitCode != nil || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("uncertain start outcome = %#v; want an error without session or fabricated execution receipt", result)
	}
	if result.SourceStale || result.SyncStatus != "succeeded" || result.SourceRevision != initialRevision {
		t.Fatalf("uncertain start source evidence = %#v; want captured evidence without a stale-output claim", result)
	}
	if strings.Contains(strings.ToLower(result.Summary), "output is unverified") || strings.Contains(strings.ToLower(result.Summary), "command ran") {
		t.Fatalf("uncertain start summary claims command output: %q", result.Summary)
	}
	if requestID == "" || strings.Join(actions, ",") != "start,cancel" {
		t.Fatalf("uncertain start requests = %v (request ID %q); want idempotent start followed by scoped cancel", actions, requestID)
	}
	currentRevision, err := files.SourceRevision(context.Background(), scope)
	if err != nil || currentRevision != initialRevision+1 {
		t.Fatalf("current source revision = %d, err=%v; want source change after captured revision %d", currentRevision, err, initialRevision)
	}
}

func TestProjectAssistantExecWorkdirClassificationRequiresTypedRejection(t *testing.T) {
	args := &projectAssistantExecCommandInput{Component: "backend", Workdir: "missing", Argv: []string{"go", "test"}}
	component := projectTemplateComponent{WorkspacePath: "api"}

	if _, ok := projectAssistantExecWorkdirRejection(
		&projectAssistantExecHTTPError{status: http.StatusBadRequest, detail: "workdir missing: no such directory"},
		args, "backend", component, 8, "source-digest", "succeeded",
	); ok {
		t.Fatal("untyped HTTP prose was accepted as proof of a pre-start workdir rejection")
	}
	if _, ok := projectAssistantExecWorkdirExecutionFailure(
		projectSandboxExecResponse{State: "failed", Stderr: "stateless executor rejected request: workdir missing: no such directory"},
		args, "backend", component, 8, "source-digest", "succeeded",
	); ok {
		t.Fatal("command-controlled stderr was accepted as proof of a pre-start workdir rejection")
	}

	httpResult, ok := projectAssistantExecWorkdirRejection(
		&projectAssistantExecHTTPError{status: http.StatusBadRequest, detail: "selected directory cannot be opened", rejectionCode: "invalid_workdir"},
		args, "backend", component, 8, "source-digest", "succeeded",
	)
	if !ok || httpResult.Status != "blocked" || httpResult.ExitCode != nil || !strings.Contains(strings.Join(httpResult.Blockers, " "), "selected directory cannot be opened") {
		t.Fatalf("typed HTTP workdir rejection = %#v, classified=%t; want pre-start blocker from arbitrary error detail", httpResult, ok)
	}

	terminalResult, ok := projectAssistantExecWorkdirExecutionFailure(
		projectSandboxExecResponse{State: "error", RejectionCode: "invalid_workdir", Stderr: "requested folder could not be opened"},
		args, "backend", component, 8, "source-digest", "succeeded",
	)
	if !ok || terminalResult.Status != "blocked" || terminalResult.ExitCode != nil || !strings.Contains(strings.Join(terminalResult.Blockers, " "), "requested folder could not be opened") {
		t.Fatalf("typed terminal workdir rejection = %#v, classified=%t; want wording-independent pre-start blocker", terminalResult, ok)
	}
	if _, ok := projectAssistantExecWorkdirExecutionFailure(
		projectSandboxExecResponse{State: "failed", RejectionCode: "invalid_request", Stderr: "workdir invalid"},
		args, "backend", component, 8, "source-digest", "succeeded",
	); ok {
		t.Fatal("a different typed request error was classified as an invalid workdir")
	}
	exitCode := 2
	if _, ok := projectAssistantExecWorkdirExecutionFailure(
		projectSandboxExecResponse{State: "failed", RejectionCode: "invalid_workdir", ExitCode: &exitCode, Stderr: "requested folder could not be opened"},
		args, "backend", component, 8, "source-digest", "succeeded",
	); ok {
		t.Fatal("a terminal result with an exit code was classified as a pre-start workdir rejection")
	}
}
