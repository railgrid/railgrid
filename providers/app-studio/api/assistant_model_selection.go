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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/faroshq/provider-app-studio/store"
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
