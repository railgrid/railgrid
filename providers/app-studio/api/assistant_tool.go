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
	"fmt"
	"net/http"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

type projectAssistantToolRisk string
type projectAssistantToolBundle string

const (
	projectAssistantToolRiskRead    projectAssistantToolRisk = "read"
	projectAssistantToolRiskInput   projectAssistantToolRisk = "input"
	projectAssistantToolRiskPlan    projectAssistantToolRisk = "plan"
	projectAssistantToolRiskWrite   projectAssistantToolRisk = "write"
	projectAssistantToolRiskCommit  projectAssistantToolRisk = "commit"
	projectAssistantToolRiskRuntime projectAssistantToolRisk = "runtime"
)

const (
	projectAssistantToolBundleWorkflow       projectAssistantToolBundle = "workflow"
	projectAssistantToolBundleWorkspaceRead  projectAssistantToolBundle = "workspace_read"
	projectAssistantToolBundleEdit           projectAssistantToolBundle = "edit"
	projectAssistantToolBundleRepo           projectAssistantToolBundle = "repo"
	projectAssistantToolBundleRuntime        projectAssistantToolBundle = "runtime"
	projectAssistantToolBundleInfrastructure projectAssistantToolBundle = "infrastructure"
	projectAssistantToolBundleCollaboration  projectAssistantToolBundle = "collaboration"
)

type projectAssistantToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Risk        projectAssistantToolRisk
	// ParallelSafe is an explicit server-owned contract. The zero value is
	// exclusive, and effectful tools remain exclusive even if misconfigured.
	ParallelSafe bool
}

func (s projectAssistantToolSpec) chatTool() chatTool {
	return chatTool{
		Type: "function",
		Function: chatToolFunction{
			Name:        s.Name,
			Description: s.Description,
			Parameters:  s.Parameters,
		},
	}
}

func projectAssistantToolBundleForSpec(spec projectAssistantToolSpec) projectAssistantToolBundle {
	switch spec.Name {
	case projectToolInfrastructureListTemplates,
		projectToolInfrastructureDescribeTemplate,
		projectToolInfrastructureProvision,
		projectToolInfrastructureListInstances,
		projectToolInfrastructureGetInstance:
		return projectAssistantToolBundleInfrastructure
	}
	switch projectToolBaseName(spec.Name) {
	case projectToolPlanProjectChanges, projectToolCheckProjectReadiness, projectToolPrepareProjectDeployment, projectToolInspectDevelopmentTemplates, projectToolCheckProjectBuild, projectToolGetBuildLogs:
		return projectAssistantToolBundleWorkflow
	case projectToolGetRuntimeStatus, projectToolGetPreviewURL, projectToolInspectDevelopmentPreview, projectToolGetPreviewConsoleLogs,
		projectToolGetRuntimeLogs, projectToolVerifyDevelopmentRuntime, projectToolRestartRuntime, projectToolSetRuntimeEnv, projectToolPromoteProject, projectToolRebuildProject:
		return projectAssistantToolBundleRuntime
	case projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep:
		return projectAssistantToolBundleWorkspaceRead
	case projectToolApplyPatch:
		return projectAssistantToolBundleEdit
	case projectToolCommitProjectFiles, projectToolCommitFiles:
		return projectAssistantToolBundleRepo
	case projectToolAskFollowUp, projectToolDefineInitialProjectPlan:
		return projectAssistantToolBundleCollaboration
	// Template selection shapes the development ENVIRONMENT, not workspace
	// files — it must be callable from the requirements-interview turns
	// (exploration/debugging), not only full implementation turns, or the
	// model narrates a choice it cannot make.
	case projectToolSelectTemplate:
		return projectAssistantToolBundleWorkflow
	}
	switch spec.Risk {
	case projectAssistantToolRiskPlan:
		return projectAssistantToolBundleWorkflow
	case projectAssistantToolRiskRead:
		return projectAssistantToolBundleWorkspaceRead
	case projectAssistantToolRiskWrite:
		return projectAssistantToolBundleEdit
	case projectAssistantToolRiskCommit:
		return projectAssistantToolBundleRepo
	case projectAssistantToolRiskRuntime:
		return projectAssistantToolBundleRuntime
	case projectAssistantToolRiskInput:
		return projectAssistantToolBundleCollaboration
	}
	return projectAssistantToolBundleWorkflow
}

type projectAssistantToolCallRequest struct {
	Identity             identity
	Project              *aiv1alpha1.Project
	Repository           *ProjectRepositoryView
	WorkspaceScope       workspace.Scope
	ProjectRepositoryRef string
	MCPEndpoint          string
	HTTPRequest          *http.Request
	SessionSnapshot      *projectEinoAssistantSessionSnapshot
	AssistantRunID       string
	InitialBuild         bool
	RunState             *projectEinoAssistantRunState
	Arguments            map[string]any
}

func refreshProjectToolSnapshot(current, updated *aiv1alpha1.Project) {
	if current == nil || updated == nil || current == updated {
		return
	}
	updated.DeepCopyInto(current)
}

type projectAssistantTool interface {
	Spec() projectAssistantToolSpec
	Call(context.Context, projectAssistantToolCallRequest) (string, error)
}

type projectAssistantToolFunc struct {
	spec projectAssistantToolSpec
	call func(context.Context, projectAssistantToolCallRequest) (string, error)
}

func (t projectAssistantToolFunc) Spec() projectAssistantToolSpec {
	return t.spec
}

func (t projectAssistantToolFunc) Call(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
	if t.call == nil {
		return "", fmt.Errorf("project assistant tool %q is not callable", t.spec.Name)
	}
	return t.call(ctx, req)
}

func projectAssistantToolJSONResult(out any, err error) (string, error) {
	raw, encodeErr := json.Marshal(out)
	if encodeErr != nil {
		if err != nil {
			return "", errors.Join(err, fmt.Errorf("encode local tool result: %w", encodeErr))
		}
		return "", fmt.Errorf("encode local tool result: %w", encodeErr)
	}
	if err != nil {
		// Preserve a concrete partial result alongside its error. Contextual
		// patch rollback can fail after changing files, and the execution layer
		// must see those paths so it can invalidate stale reads and retain the
		// actual durable dirty-workspace state.
		return string(raw), err
	}
	return string(raw), nil
}
