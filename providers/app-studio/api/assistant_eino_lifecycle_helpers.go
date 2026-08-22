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
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

const (
	projectEinoAssistantWriteTodosTool = "write_todos"
	projectEinoAssistantToolSearchTool = "tool_search"

	projectEinoAssistantTodoProgressMaxItems      = 50
	projectEinoAssistantTodoProgressMaxInputBytes = 64 * 1024
	projectEinoAssistantTodoProgressMaxLabelBytes = 120

	// Repeated action tracking is retained for durable telemetry/replay context;
	// it is informational and never terminates a model turn.
	projectEinoAssistantRepeatedActionLimit = 100

	// A failed mutation gets one complete-reread repair attempt. If that
	// repair also fails at the same source revision and canonical target, stop
	// before asking the model for another sample.
	projectEinoAssistantMutationRecoveryFailureLimit       = 2
	projectEinoAssistantMaxTrackedMutationRecoveryAttempts = 128
)

type projectEinoAssistantNoProgressError struct {
	ToolName         string
	Calls            int
	Limit            int
	SourceRevision   uint64
	VerifiedRevision uint64
	Target           string
	RecoveryBlocked  bool
}

func (e *projectEinoAssistantNoProgressError) Error() string {
	if e == nil {
		return errProjectAssistantNoProgress.Error()
	}
	if e.RecoveryBlocked {
		target := strings.TrimSpace(e.Target)
		if target == "" {
			target = "the same mutation target"
		}
		return fmt.Sprintf("%s: recovery_blocked after %d failed %s mutations for %s at source revision %d", errProjectAssistantNoProgress, e.Calls, projectToolBaseName(e.ToolName), target, e.SourceRevision)
	}
	if toolName := projectToolBaseName(e.ToolName); toolName != "" {
		return fmt.Sprintf("%s: repeated %s %d times", errProjectAssistantNoProgress, toolName, e.Limit)
	}
	return fmt.Sprintf("%s: completed %d consecutive model turns without new progress", errProjectAssistantNoProgress, e.Limit)
}

func (e *projectEinoAssistantNoProgressError) Unwrap() error {
	return errProjectAssistantNoProgress
}

// projectEinoAssistantRecoveryBlockedError is emitted by the lifecycle
// boundary after the second failed mutation for one target/revision. It wraps
// the existing no-progress sentinel so audit and terminal classification stay
// compatible while callers can distinguish the recovery-specific reason.
type projectEinoAssistantRecoveryBlockedError struct {
	projectEinoAssistantNoProgressError
}

func newProjectEinoAssistantRecoveryBlockedError(
	attempt projectAssistantMutationRecoveryAttempt,
	verifiedRevision uint64,
) error {
	return &projectEinoAssistantRecoveryBlockedError{
		projectEinoAssistantNoProgressError: projectEinoAssistantNoProgressError{
			ToolName:         attempt.Operation,
			Calls:            attempt.Failures,
			Limit:            projectEinoAssistantMutationRecoveryFailureLimit,
			SourceRevision:   attempt.SourceRevision,
			VerifiedRevision: verifiedRevision,
			Target:           attempt.Target,
			RecoveryBlocked:  true,
		},
	}
}

func (e *projectEinoAssistantRecoveryBlockedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return &e.projectEinoAssistantNoProgressError
}

func projectEinoAssistantProgressApplies(req projectAssistantRunRequest, runState *projectEinoAssistantRunState) bool {
	if runState != nil {
		return projectAssistantTurnProfileAllowsMutation(runState.TurnPolicy().profile)
	}
	profile := req.TurnPolicy.profile
	if strings.TrimSpace(string(profile)) == "" {
		profile = req.TurnProfile
	}
	return projectAssistantTurnProfileAllowsMutation(profile)
}

func projectEinoAssistantSuccessfulToolContent(content string) bool {
	content = strings.ToLower(strings.TrimSpace(content))
	for _, prefix := range []string{
		"tool call failed:",
		"tool call denied:",
		"tool call skipped: waiting for approval",
		"permission denied:",
	} {
		if strings.HasPrefix(content, prefix) {
			return false
		}
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(content), &decoded); err == nil {
		switch strings.ToLower(projectToolString(decoded["status"])) {
		case "failed", "partial_failure", "error", "timed_out":
			return false
		}
	}
	return true
}

// projectAssistantToolResultDisposition is the single compatibility adapter
// from provider/model text into App Studio's typed durable settlement. No
// lifecycle, replay, or terminal consumer should reinterpret result prose.
func projectAssistantToolResultDisposition(name, content string, invokeErr error) projectAssistantToolDisposition {
	if invokeErr != nil || !projectEinoAssistantSuccessfulToolContent(content) {
		return projectAssistantToolDispositionFailed
	}
	if projectEinoAssistantCommitTool(name) && projectToolCallResultStatus(name, content) != "succeeded" {
		return projectAssistantToolDispositionFailed
	}
	decoded := map[string]any{}
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &decoded) == nil {
		effectful := projectAssistantToolNameHasEffect(name)
		for _, field := range []string{"status", "phase", "outcome"} {
			switch strings.ToLower(projectToolString(decoded[field])) {
			case "error", "failed", "failure", "partial_failure", "timed_out":
				return projectAssistantToolDispositionFailed
			case "blocked", "cancelled", "canceled", "denied", "not_configured", "not_ready", "rejected", "unavailable":
				if effectful {
					return projectAssistantToolDispositionFailed
				}
			}
		}
	}
	return projectAssistantToolDispositionSucceeded
}

func projectAssistantToolNameHasEffect(name string) bool {
	name = projectToolBaseName(name)
	if spec, ok := projectAssistantWorkflowToolSpec(name); ok {
		return projectAssistantToolHasEffect(spec)
	}
	if spec, ok := projectAssistantLocalToolRegistry(nil).Spec(name); ok {
		return projectAssistantToolHasEffect(spec)
	}
	if spec, ok := projectAssistantMCPToolSpec(projectMCPTool{Name: name}); ok {
		return projectAssistantToolHasEffect(spec)
	}
	return false
}

func projectEinoAssistantVerificationContentReady(content string) bool {
	var result projectAssistantRuntimeVerificationResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &result); err != nil {
		return false
	}
	return projectEinoAssistantRuntimeVerificationDisposition(result) == projectEinoAssistantVerificationReadyDisposition
}

func projectEinoAssistantTemplateBootstrapAllowed(project *aiv1alpha1.Project) bool {
	return project != nil && (project.Spec.Template == nil || strings.TrimSpace(project.Spec.Template.Name) == "")
}

func projectEinoAssistantTodoProgressLabel(value string) string {
	value = projectEinoAssistantSafeText(strings.Join(strings.Fields(value), " "))
	if len(value) <= projectEinoAssistantTodoProgressMaxLabelBytes {
		return value
	}
	end := projectEinoAssistantTodoProgressMaxLabelBytes - 3
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return strings.TrimSpace(value[:end]) + "..."
}
