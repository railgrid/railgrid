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
	"time"
)

// projectAssistantInitialCreationPlan is the narrow authorization implied by
// an explicit prompt submitted to create a new Project. It stays in the
// Eino run/checkpoint only; unlike a user-approved plan it is never written to
// the cross-turn grant store. Permission policy limits it to source edits and
// always requires separate template-selection and commit approval.
func projectAssistantInitialCreationPlan(goal ...string) projectAssistantApprovedPlan {
	initialGoal := ""
	if len(goal) > 0 {
		initialGoal = strings.TrimSpace(goal[0])
	}
	return normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Goal:         initialGoal,
		Summary:      "Initial project creation prompt authorizes source edits for this run.",
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		// Initial creation does not know the generated project paths yet.
		AllowAllWrites: true,
		ApprovedAt:     time.Now().UTC(),
		ApprovalTool:   "project_create_prompt",
		RunLocal:       true,
	})
}

// mergeProjectAssistantApprovedPlans is used only after a separate direct
// user approval. Model-authored plan restatements never call this helper.
func mergeProjectAssistantApprovedPlans(existing, next projectAssistantApprovedPlan) projectAssistantApprovedPlan {
	merged := next
	merged.TargetPaths = normalizeProjectAssistantStringList(append(append([]string(nil), existing.TargetPaths...), next.TargetPaths...))
	merged.Capabilities = normalizeProjectAssistantStringList(append(append([]string(nil), existing.Capabilities...), next.Capabilities...))
	if existing.Version > merged.Version {
		merged.Version = existing.Version
	}
	merged.AllowAllWrites = existing.AllowAllWrites || next.AllowAllWrites
	merged.RunLocal = existing.RunLocal || next.RunLocal
	return merged
}
