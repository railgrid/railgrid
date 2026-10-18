// Copyright 2026 The Railgrid Authors.
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
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/railgrid/provider-app-studio/store"
)

// bindProjectAssistantStartModelAudit fixes the selected registry entry to the
// durable run before worker startup. Credentials remain in the workspace
// Secret; the audit stores only stable, non-secret logical and revision IDs.
func bindProjectAssistantStartModelAudit(run *store.AssistantRun, modelID, modelRevisionID string) error {
	modelID = strings.TrimSpace(modelID)
	modelRevisionID = strings.TrimSpace(modelRevisionID)
	if run == nil || modelID == "" {
		return nil
	}
	var audit projectAssistantRunAudit
	if len(run.Audit) > 0 {
		if err := json.Unmarshal(run.Audit, &audit); err != nil {
			return fmt.Errorf("decode assistant model run audit: %w", err)
		}
	}
	if audit.ModelID != "" && audit.ModelID != modelID {
		return fmt.Errorf("%w: client request ID was already used with a different model", store.ErrAssistantRunConflict)
	}
	audit.ModelID = modelID
	if audit.ModelRevisionID != "" && audit.ModelRevisionID != modelRevisionID {
		return fmt.Errorf("%w: client request ID was already used with a different model revision", store.ErrAssistantRunConflict)
	}
	audit.ModelRevisionID = modelRevisionID
	raw, err := json.Marshal(audit)
	if err != nil {
		return fmt.Errorf("encode assistant model run audit: %w", err)
	}
	run.Audit = raw
	return nil
}

func projectAssistantModelReferenceFromRunAudit(run store.AssistantRun) (string, string) {
	var audit projectAssistantRunAudit
	if len(run.Audit) == 0 || json.Unmarshal(run.Audit, &audit) != nil {
		return "", ""
	}
	return strings.TrimSpace(audit.ModelID), strings.TrimSpace(audit.ModelRevisionID)
}

func projectAssistantModelIDFromRunAudit(run store.AssistantRun) string {
	modelID, _ := projectAssistantModelReferenceFromRunAudit(run)
	return modelID
}

func projectAssistantModelRevisionIDFromRunAudit(run store.AssistantRun) string {
	_, revisionID := projectAssistantModelReferenceFromRunAudit(run)
	return revisionID
}

func validateProjectAssistantStartModelSelection(run store.AssistantRun, modelID string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	if projectAssistantModelIDFromRunAudit(run) != modelID {
		return fmt.Errorf("%w: client request ID was already used with a different model", store.ErrAssistantRunConflict)
	}
	return nil
}

func cloneProjectAssistantAuditReasoning(reasoning *projectAssistantAuditReasoning) *projectAssistantAuditReasoning {
	if reasoning == nil {
		return nil
	}
	clone := *reasoning
	if reasoning.RequestedEffort != nil {
		requested := *reasoning.RequestedEffort
		clone.RequestedEffort = &requested
	}
	return &clone
}

func projectAssistantReasoningFromRunAudit(run store.AssistantRun) *projectAssistantAuditReasoning {
	var audit projectAssistantRunAudit
	if len(run.Audit) == 0 || json.Unmarshal(run.Audit, &audit) != nil {
		return nil
	}
	return cloneProjectAssistantAuditReasoning(audit.Reasoning)
}

func bindProjectAssistantStartReasoningAudit(run *store.AssistantRun, reasoning *projectAssistantAuditReasoning) error {
	if run == nil {
		return errors.New("bind assistant reasoning snapshot: run is required")
	}
	var audit projectAssistantRunAudit
	if len(run.Audit) > 0 {
		if err := json.Unmarshal(run.Audit, &audit); err != nil {
			return fmt.Errorf("decode assistant reasoning run audit: %w", err)
		}
	}
	if reasoning == nil {
		audit.Reasoning = nil
	} else {
		if audit.Reasoning != nil && !reflect.DeepEqual(audit.Reasoning, reasoning) {
			return fmt.Errorf("%w: client request ID was already used with a different reasoning choice", store.ErrAssistantRunConflict)
		}
		audit.Reasoning = cloneProjectAssistantAuditReasoning(reasoning)
	}
	raw, err := json.Marshal(audit)
	if err != nil {
		return fmt.Errorf("encode assistant reasoning run audit: %w", err)
	}
	run.Audit = raw
	return nil
}

func validateProjectAssistantExplicitReasoningReplay(run store.AssistantRun, requested *string) error {
	if requested == nil {
		return nil
	}
	stored := projectAssistantReasoningFromRunAudit(run)
	if stored == nil || stored.RequestedEffort == nil || *stored.RequestedEffort != *requested {
		return fmt.Errorf("%w: client request ID was already used with a different reasoning choice", store.ErrAssistantRunConflict)
	}
	return nil
}

// applyProjectAssistantReasoningSnapshot restores the immutable reasoning
// configuration for a worker or resume. An absent snapshot is an older run and
// deliberately clears settings that may have been added to the saved model
// since that run was created.
func applyProjectAssistantReasoningSnapshot(settings *projectLLMSettings, run store.AssistantRun) {
	restoreProjectAssistantReasoning(settings, projectAssistantReasoningFromRunAudit(run))
}

func restoreProjectAssistantReasoning(settings *projectLLMSettings, reasoning *projectAssistantAuditReasoning) {
	if settings == nil {
		return
	}
	if reasoning == nil {
		settings.ReasoningEffort = ""
		settings.ReasoningTransport = ""
		settings.ReasoningEffectiveEffort = nil
		return
	}
	settings.ReasoningEffort = reasoning.ConfiguredEffort
	settings.ReasoningTransport = reasoning.Transport
	effectiveEffort := reasoning.EffectiveEffort
	settings.ReasoningEffectiveEffort = &effectiveEffort
}
