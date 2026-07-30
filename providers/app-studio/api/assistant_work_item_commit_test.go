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
	"encoding/json"
	"errors"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

// TestEinoAssistantEngineWorkItemCommitRequestRetiresGrant exercises the
// production ownership path.  Unlike the legacy engine harness, the plan
// belongs to a WorkItem and each user decision first claims its paused run.
// A commit request must retire that grant before it becomes pending permission.
func TestEinoAssistantEngineWorkItemCommitRequestRetiresGrant(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	registry := server.projectAssistantToolRegistry()
	planTool, ok := registry.Get(projectToolRequestProjectPlanApproval)
	if !ok {
		t.Fatal("request_project_plan_approval tool missing")
	}
	writeTool, ok := registry.Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	verifyTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        projectToolVerifyDevelopmentRuntime,
			Description: "Verify the development runtime.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Risk:        projectAssistantToolRiskRead,
		},
		result: `{"status":"ready"}`,
	}
	commitTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        projectToolCommitProjectFiles,
			Description: "Commit project files.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Risk:        projectAssistantToolRiskCommit,
		},
		result: `{"status":"committed"}`,
	}
	chatModel := &planWriteCommitWriteEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{
				newProjectEinoAssistantServerTool(server, planTool, req, state),
				newProjectEinoAssistantServerTool(server, writeTool, req, state),
				newProjectEinoAssistantServerTool(server, verifyTool, req, state),
				newProjectEinoAssistantServerTool(server, commitTool, req, state),
			}, nil
		},
	}

	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1", user: "alice"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	scope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	started, err := server.startProjectAssistantBuildRunDurably(ctx, scope, id.user, "Build the app shell", "build-1", func(store.AssistantRun, store.Message, bool) error { return nil })
	if err != nil {
		t.Fatalf("start durable build: %v", err)
	}
	req := projectAssistantRunRequest{
		Identity:       id,
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		Workspace:      workspaces,
		MessageScope:   scope,
		AssistantRun:   &started.Run,
	}

	err = runProjectAssistantWorkItemCommitEngineForTest(t, server, engine, scope, started.Run, started.Assistant, req, nil)
	planPermission := requireProjectAssistantPermissionForWorkItemCommitTest(t, err, projectToolRequestProjectPlanApproval)
	planRun := loadProjectAssistantRunAndCheckpointForWorkItemCommitTest(t, messages, scope, planPermission.RunID)
	planRun.run = claimProjectAssistantRunForWorkItemCommitTest(t, server, messages, scope, planRun.run, planPermission.RequestID)

	err = runProjectAssistantWorkItemCommitEngineForTest(t, server, engine, scope, planRun.run, started.Assistant, projectAssistantRunRequest{
		Identity: id, Project: project, WorkspaceScope: req.WorkspaceScope, Workspace: workspaces, MessageScope: scope, AssistantRun: &planRun.run,
	}, &projectAssistantWorkItemCommitResume{request: projectAssistantResumeRequest{RequestID: planPermission.RequestID, Decision: string(projectAssistantPermissionAllow)}, checkpoint: planRun.checkpoint})
	commitPermission := requireProjectAssistantPermissionForWorkItemCommitTest(t, err, projectToolCommitProjectFiles)
	if commitTool.calls != 0 {
		t.Fatalf("commit calls = %d, want no commit before approval", commitTool.calls)
	}

	commitRun := loadProjectAssistantRunAndCheckpointForWorkItemCommitTest(t, messages, scope, commitPermission.RunID)
	if commitRun.run.Status != store.AssistantRunStatusPendingPermission {
		t.Fatalf("commit run status = %q, want pending_permission", commitRun.run.Status)
	}
	if commitRun.checkpoint.ApprovedPlan != nil {
		t.Fatalf("commit checkpoint retained approved plan: %#v", commitRun.checkpoint.ApprovedPlan)
	}
	item, err := messages.GetAssistantWorkItem(ctx, scope, started.Run.WorkItemID)
	if err != nil {
		t.Fatalf("GetAssistantWorkItem: %v", err)
	}
	if !projectAssistantWorkItemPlanGrantClearedForCommitTest(item.PlanGrant) {
		t.Fatalf("work item plan grant = %s, want cleared before commit approval", item.PlanGrant)
	}
	if item.GrantRevision == "" || item.GrantRevision != commitRun.run.ExpectedGrantRevision {
		t.Fatalf("work item/run tombstone revisions = %q/%q, want shared non-empty revision", item.GrantRevision, commitRun.run.ExpectedGrantRevision)
	}
	commitRun.run = claimProjectAssistantRunForWorkItemCommitTest(t, server, messages, scope, commitRun.run, commitPermission.RequestID)
	err = runProjectAssistantWorkItemCommitEngineForTest(t, server, engine, scope, commitRun.run, started.Assistant, projectAssistantRunRequest{
		Identity: id, Project: project, WorkspaceScope: req.WorkspaceScope, Workspace: workspaces, MessageScope: scope, AssistantRun: &commitRun.run,
	}, &projectAssistantWorkItemCommitResume{request: projectAssistantResumeRequest{RequestID: commitPermission.RequestID, Decision: string(projectAssistantPermissionAllow)}, checkpoint: commitRun.checkpoint})
	writePermission := requireProjectAssistantPermissionForWorkItemCommitTest(t, err, projectToolWriteFile)
	if commitTool.calls != 1 {
		t.Fatalf("commit calls = %d, want exactly one approved commit", commitTool.calls)
	}
	if writePermission.RunID != started.Run.ID {
		t.Fatalf("post-commit write run = %q, want original WorkItem run %q", writePermission.RunID, started.Run.ID)
	}
	read, err := workspaces.ReadFile(ctx, req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if read.Content != "approved plan write\n" {
		t.Fatalf("post-commit write bypassed fresh approval: %q", read.Content)
	}
}

type projectAssistantWorkItemCommitCheckpoint struct {
	run        store.AssistantRun
	checkpoint projectAssistantCheckpointState
}

type projectAssistantWorkItemCommitResume struct {
	request    projectAssistantResumeRequest
	checkpoint projectAssistantCheckpointState
}

func runProjectAssistantWorkItemCommitEngineForTest(
	t *testing.T,
	server *Server,
	engine projectEinoAssistantEngine,
	scope store.Scope,
	run store.AssistantRun,
	message store.Message,
	req projectAssistantRunRequest,
	resume *projectAssistantWorkItemCommitResume,
) error {
	t.Helper()
	done := make(chan error, 1)
	if req.ToolPort == nil {
		req.ToolPort = projectAssistantDirectToolPort{}
	}
	if err := server.projectAssistantSupervisor().Start(context.Background(), scope, run, message, func(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator) {
		workerReq := req
		workerRun := run
		workerReq.AssistantRun = &workerRun
		if resume == nil {
			_, err := engine.StreamProjectAssistant(ctx, workerReq)
			done <- err
			return
		}
		_, err := engine.ResumeProjectAssistant(ctx, workerReq, resume.request, resume.checkpoint)
		done <- err
	}); err != nil {
		t.Fatalf("start supervised assistant run: %v", err)
	}
	return <-done
}

func loadProjectAssistantRunAndCheckpointForWorkItemCommitTest(t *testing.T, messages store.Store, scope store.Scope, runID string) projectAssistantWorkItemCommitCheckpoint {
	t.Helper()
	run, err := messages.GetAssistantRun(context.Background(), scope, runID)
	if err != nil {
		t.Fatalf("GetAssistantRun(%s): %v", runID, err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	return projectAssistantWorkItemCommitCheckpoint{run: run, checkpoint: checkpoint}
}

func claimProjectAssistantRunForWorkItemCommitTest(t *testing.T, server *Server, messages store.Store, scope store.Scope, run store.AssistantRun, requestID string) store.AssistantRun {
	t.Helper()
	if accumulator := server.projectAssistantSupervisor().accumulatorFor(scope, run.ID); accumulator != nil {
		claimed, err := accumulator.ClaimPending(context.Background(), requestID)
		if err != nil {
			t.Fatalf("ClaimPending(%s): %v", run.ID, err)
		}
		return claimed
	}
	claimed, err := messages.ClaimAssistantRun(context.Background(), scope, run.ID, requestID, time.Now().UTC())
	if err != nil {
		t.Fatalf("ClaimAssistantRun(%s): %v", run.ID, err)
	}
	return claimed
}

func projectAssistantWorkItemPlanGrantClearedForCommitTest(grant json.RawMessage) bool {
	return len(grant) == 0 || string(grant) == "{}"
}

func requireProjectAssistantPermissionForWorkItemCommitTest(t *testing.T, err error, wantTool string) *projectAssistantPermissionRequiredError {
	t.Helper()
	var permission *projectAssistantPermissionRequiredError
	if !errors.As(err, &permission) {
		t.Fatalf("assistant error = %v, want permission for %s", err, wantTool)
	}
	if permission.ToolName != wantTool {
		t.Fatalf("permission tool = %q, want %q", permission.ToolName, wantTool)
	}
	return permission
}
