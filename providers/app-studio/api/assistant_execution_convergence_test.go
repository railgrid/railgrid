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
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantExecutionGateSerializesEffectsAgainstReads(t *testing.T) {
	executionContext := &projectAssistantExecutionContext{}
	middleware := projectEinoAssistantToolBatchAdmissionMiddleware(newProjectEinoAssistantRunState(), executionContext).(*projectEinoAssistantToolBatchMiddleware)
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	effectStarted := make(chan struct{})
	read, err := middleware.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...einotool.Option) (string, error) {
		close(readStarted)
		<-releaseRead
		return "read", nil
	}, &adk.ToolContext{Name: projectToolGetRuntimeStatus})
	if err != nil {
		t.Fatal(err)
	}
	effect, err := middleware.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...einotool.Option) (string, error) {
		close(effectStarted)
		return "effect", nil
	}, &adk.ToolContext{Name: projectToolEditFile})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = read(context.Background(), "")
	}()
	<-readStarted
	go func() {
		defer wg.Done()
		_, _ = effect(context.Background(), "")
	}()
	select {
	case <-effectStarted:
		t.Fatal("effect overlapped a running parallel-safe read")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseRead)
	select {
	case <-effectStarted:
	case <-time.After(time.Second):
		t.Fatal("effect did not start after the read released the gate")
	}
	wg.Wait()
}

type projectAssistantSnapshotCapturePort struct {
	project *aiv1alpha1.Project
}

func (*projectAssistantSnapshotCapturePort) DiscoverMCP(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, bool, error) {
	return nil, false, nil
}

func (p *projectAssistantSnapshotCapturePort) Invoke(_ context.Context, _ projectAssistantTool, req projectAssistantToolCallRequest) (string, error) {
	p.project = req.Project
	return `{"status":"ready"}`, nil
}

func TestProjectAssistantToolDispatchUsesPublishedSampleSnapshot(t *testing.T) {
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-snapshot")
	port := &projectAssistantSnapshotCapturePort{}
	projectV1 := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", ResourceVersion: "1"}}
	projectV2 := projectV1.DeepCopy()
	projectV2.ResourceVersion = "2"
	req := projectAssistantRunRequestWithExecutionContext(projectAssistantRunRequest{
		Project:      projectV1,
		ToolPort:     port,
		MessageScope: scope,
		eventLedger:  newProjectAssistantRunEventLedger(messageStore, scope, "run-snapshot"),
	})
	tool := projectEinoAssistantTool{
		tool:     projectAssistantToolFunc{spec: projectAssistantToolSpec{Name: "snapshot_read", Risk: projectAssistantToolRiskRead}},
		req:      req,
		runState: newProjectEinoAssistantRunState(),
	}
	current := req.currentExecutionRequest()
	current.Project = projectV2
	current.publishExecutionRequest()
	tool.req = tool.req.currentExecutionRequest()
	if _, err := tool.invokeAllowedTool(context.Background(), "snapshot-call", tool.tool.Spec(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if port.project == nil || port.project.ResourceVersion != "2" {
		t.Fatalf("dispatched project = %#v, want published resourceVersion 2", port.project)
	}
}

func TestProjectAssistantSupervisorAdmitMutationRequiresBoundActor(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-actor", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, ClientRequestID: "request", UserMessageID: "user", ActiveMessageID: "assistant", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user", Role: "user", ActorID: "owner", Content: "change it", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if err := bindProjectAssistantStartRequest(&run, user.ActorID, user.Content); err != nil {
		t.Fatal(err)
	}
	created, err := messages.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := newProjectAssistantSupervisor(context.Background(), messages)
	if _, err := supervisor.Attach(scope, created, assistant); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.AdmitMutation(ctx, scope, created.ID, "owner"); err != nil {
		t.Fatalf("owner admission: %v", err)
	}
	for _, actor := range []string{"", "other"} {
		if err := supervisor.AdmitMutation(ctx, scope, created.ID, actor); !errors.Is(err, store.ErrAssistantRunConflict) {
			t.Fatalf("actor %q admission = %v, want conflict", actor, err)
		}
	}
}

func TestAssistantRunLedgerPersistsTypedDisposition(t *testing.T) {
	ctx := context.Background()
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-disposition")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-disposition")
	effect := projectAssistantToolSpec{Name: projectToolRestartRuntime, Risk: projectAssistantToolRiskRuntime}
	decision, err := ledger.BeginToolCall(ctx, "blocked", effect, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := ledger.FinishToolCall(ctx, decision.Token, `{"status":"blocked"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failed || outcome.Succeeded() || outcome.Disposition != projectAssistantToolDispositionFailed {
		t.Fatalf("blocked effect outcome = %#v, want typed semantic failure without transport failure", outcome)
	}

	read := projectAssistantToolSpec{Name: projectToolGetRuntimeStatus, Risk: projectAssistantToolRiskRead}
	decision, err = ledger.BeginToolCall(ctx, "not-ready-read", read, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err = ledger.FinishToolCall(ctx, decision.Token, `{"status":"not_ready"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Succeeded() {
		t.Fatalf("not-ready read outcome = %#v, want successful observation", outcome)
	}
}
