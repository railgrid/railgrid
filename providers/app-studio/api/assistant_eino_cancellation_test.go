// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// cancellationProbeSandboxClient keeps an execution session open until its
// request context is canceled. It makes the bridge between the supervisor
// context and the Eino tool context observable without a real data plane.
type cancellationProbeSandboxClient struct {
	started      chan struct{}
	pollStarted  chan struct{}
	pollReturned chan struct{}
	cancelCalled chan struct{}
	startedOnce  sync.Once
	pollOnce     sync.Once
	returnedOnce sync.Once
	cancelOnce   sync.Once
}

func (c *cancellationProbeSandboxClient) Workspace(context.Context, identity, dataPlaneRef, projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	return projectAssistantSandboxWorkspaceResponse{}, nil
}

func (c *cancellationProbeSandboxClient) Exec(ctx context.Context, _ identity, _ dataPlaneRef, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	switch request.Action {
	case "start":
		c.startedOnce.Do(func() { close(c.started) })
		return projectSandboxExecResponse{SessionID: "session-cancel-probe", State: "running"}, nil
	case "poll":
		c.pollOnce.Do(func() { close(c.pollStarted) })
		<-ctx.Done()
		c.returnedOnce.Do(func() { close(c.pollReturned) })
		return projectSandboxExecResponse{}, ctx.Err()
	case "cancel":
		c.cancelOnce.Do(func() { close(c.cancelCalled) })
		return projectSandboxExecResponse{SessionID: request.SessionID, State: "canceled"}, nil
	default:
		return projectSandboxExecResponse{}, errors.New("unexpected sandbox exec action")
	}
}

var _ projectAssistantSandboxClient = (*cancellationProbeSandboxClient)(nil)

func TestProjectEinoAssistantSandboxExecFollowsSupervisorCancellation(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "eino-cancellation-bridge")
	h.req.TurnProfile = projectAssistantTurnProfileImplementation
	h.req.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	h.server.ConfigureCodingSandbox(CodingSandboxConfig{Mode: CodingSandboxModeForce, DevelopmentMode: true, ReplicaCount: 1})

	probe := &cancellationProbeSandboxClient{
		started:      make(chan struct{}),
		pollStarted:  make(chan struct{}),
		pollReturned: make(chan struct{}),
		cancelCalled: make(chan struct{}),
	}
	h.server.runSandboxSetupFactory = func(_ context.Context, _ projectAssistantRunRequest, state *projectEinoAssistantRunState, _ *projectAssistantSandboxCheckpoint) (*projectAssistantRunSandbox, func(), error) {
		return &projectAssistantRunSandbox{
			client:   probe,
			runState: state,
			target: projectDevelopmentSyncTargetInfo{Components: map[string]projectTemplateComponent{
				projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."},
			}},
			metadata: projectAssistantRunSandboxMetadata{
				Status:         "active",
				RemoteRevision: 1,
				RemoteDigest:   "sha256:cancel-probe",
			},
		}, func() {}, nil
	}

	var eventsMu sync.Mutex
	var events []projectToolCallStreamEvent
	h.req.StreamCallbacks.OnToolCall = func(event projectToolCallStreamEvent) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	}
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{{
		Message: schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "exec-cancel-probe",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolExecCommand,
				Arguments: `{"component":"workspace","argv":["sleep","45"]}`,
			},
		}}),
	}}}
	engine := projectEinoAssistantEngine{
		server: h.server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: newProjectEinoAssistantToolsFactory(h.server),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type runResult struct{ err error }
	resultCh := make(chan runResult, 1)
	go func() {
		_, err := engine.StreamProjectAssistant(ctx, h.req)
		resultCh <- runResult{err: err}
	}()

	select {
	case <-probe.started:
	case <-time.After(time.Second):
		t.Fatal("sandbox exec did not start")
	}
	select {
	case <-probe.pollStarted:
	case <-time.After(time.Second):
		t.Fatal("sandbox exec did not enter polling")
	}

	canceledAt := time.Now()
	cancel()
	select {
	case <-probe.cancelCalled:
		if elapsed := time.Since(canceledAt); elapsed > time.Second {
			t.Fatalf("remote cancel took %s, want under one second", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("remote sandbox session was not canceled promptly")
	}
	select {
	case <-probe.pollReturned:
	case <-time.After(time.Second):
		t.Fatal("sandbox poll did not unwind after cancellation")
	}
	select {
	case result := <-resultCh:
		if result.err == nil || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("assistant result error = %v, want context.Canceled", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("assistant turn did not finish after cancellation")
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	var canceledEvent *projectToolCallStreamEvent
	for index := range events {
		if events[index].Name == projectToolExecCommand && events[index].Status == "canceled" {
			canceledEvent = &events[index]
		}
	}
	if canceledEvent == nil || canceledEvent.Exec == nil || canceledEvent.Exec.Status != "canceled" {
		t.Fatalf("tool events = %#v, want a terminal canceled exec event", events)
	}
}
