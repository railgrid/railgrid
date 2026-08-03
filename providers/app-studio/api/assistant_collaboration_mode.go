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
	"strings"

	"github.com/faroshq/provider-app-studio/store"
)

// projectAssistantCollaborationMode is the server-owned execution policy for a
// complete turn. Review is a separate, read-only execution, analogous to
// Codex's review/start operation; it is not an automatic completion gate.
type projectAssistantCollaborationMode string

const (
	projectAssistantCollaborationModeDefault projectAssistantCollaborationMode = "default"
	projectAssistantCollaborationModePlan    projectAssistantCollaborationMode = "plan"
	projectAssistantCollaborationModeReview  projectAssistantCollaborationMode = "review"
)

const projectAssistantReviewDedicatedRouteMessage = "review runs must use the dedicated /assistant/threads/{thread}/reviews endpoint"

func projectAssistantCollaborationModeForRun(run store.AssistantRun) (projectAssistantCollaborationMode, bool) {
	mode := projectAssistantCollaborationMode(strings.ToLower(strings.TrimSpace(string(run.Mode))))
	switch mode {
	case projectAssistantCollaborationModeDefault,
		projectAssistantCollaborationModePlan,
		projectAssistantCollaborationModeReview:
		return mode, true
	default:
		return "", false
	}
}

// publicAssistantThreadTurnMode validates the generic thread-turn creation
// contract. Review is intentionally omitted: callers must use the dedicated
// review endpoint so that the target and read-only intent are explicit.
func (r assistantThreadTurnCreateRequest) publicAssistantThreadTurnMode() (store.AssistantRunMode, error) {
	mode := store.AssistantRunMode(strings.ToLower(strings.TrimSpace(string(r.CollaborationMode))))
	if mode == "" {
		mode = store.AssistantRunModeDefault
	}
	switch mode {
	case store.AssistantRunModeDefault, store.AssistantRunModePlan:
		return mode, nil
	case store.AssistantRunModeReview:
		return "", newValidationError(projectAssistantReviewDedicatedRouteMessage)
	default:
		return "", newValidationError("collaborationMode must be default or plan")
	}
}

func projectAssistantCollaborationModeReadOnly(mode projectAssistantCollaborationMode) bool {
	return mode == projectAssistantCollaborationModePlan || mode == projectAssistantCollaborationModeReview
}

func projectAssistantToolsForCollaborationMode(tools []projectAssistantTool, mode projectAssistantCollaborationMode) []projectAssistantTool {
	out := make([]projectAssistantTool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		spec := tool.Spec()
		if mode == projectAssistantCollaborationModeDefault && spec.Risk == projectAssistantToolRiskInput {
			continue
		}
		if projectAssistantCollaborationModeReadOnly(mode) && projectAssistantToolHasEffect(spec) {
			continue
		}
		out = append(out, tool)
	}
	return out
}
