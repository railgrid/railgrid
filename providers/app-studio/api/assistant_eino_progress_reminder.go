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
	"fmt"
	"strings"
)

const (
	projectEinoAssistantProgressReminderPlan         = "plan_transition"
	projectEinoAssistantProgressReminderVerification = "verification_change"
	projectEinoAssistantProgressReminderSilence      = "model_silence"

	// Keep the cadence bounded by model samples rather than wall-clock timers:
	// Eino can be suspended at a permission/follow-up boundary, and a timer
	// would otherwise race the durable turn state.
	projectEinoAssistantProgressReminderSilenceModelCalls = 18
	projectEinoAssistantProgressReminderMaxAttempts       = 1
	projectEinoAssistantProgressReminderMaxAcceptedCount  = 1024
)

type projectEinoAssistantProgressReminder struct {
	Kind   string
	Detail string
}

func projectEinoAssistantProgressReminderKindValid(kind string) bool {
	switch strings.TrimSpace(kind) {
	case projectEinoAssistantProgressReminderPlan,
		projectEinoAssistantProgressReminderVerification,
		projectEinoAssistantProgressReminderSilence:
		return true
	default:
		return false
	}
}

func projectEinoAssistantProgressReminderInstruction(reminder projectEinoAssistantProgressReminder) string {
	detail := strings.TrimSpace(reminder.Detail)
	if len([]rune(detail)) > 240 {
		detail = string([]rune(detail)[:240])
	}
	var context string
	switch strings.TrimSpace(reminder.Kind) {
	case projectEinoAssistantProgressReminderPlan:
		context = "The task checklist just moved to a new phase"
	case projectEinoAssistantProgressReminderVerification:
		context = "Verification changed the direction of the work"
	case projectEinoAssistantProgressReminderSilence:
		context = "Several model calls have passed without an accepted progress update"
	default:
		context = "The work has reached a meaningful transition"
	}
	if detail != "" {
		context += ": " + detail
	}
	return fmt.Sprintf(
		"A user update may be useful for this substantial work. If meaningful progress has occurred and no recent update covers it, use report_progress once with a concise outcome and next direction or blocker; otherwise continue without adding a tool call. Checklist changes and simple edits alone do not need a separate update. Context: %s. This reminder is advisory and non-blocking; do not force a tool choice, stop, or wait.",
		context,
	)
}

func projectEinoAssistantPlanPhaseTransition(previous, next projectAssistantPlanSnapshot) bool {
	if len(previous.Steps) == 0 || len(next.Steps) == 0 {
		return false
	}
	activeStep := func(snapshot projectAssistantPlanSnapshot) (string, bool) {
		for _, step := range snapshot.Steps {
			if strings.TrimSpace(step.Status) == "in_progress" {
				return strings.TrimSpace(step.Content), true
			}
		}
		return "", false
	}
	previousActive, previousHasActive := activeStep(previous)
	nextActive, nextHasActive := activeStep(next)
	if !previousHasActive || !nextHasActive || previousActive == "" || nextActive == "" || previousActive == nextActive {
		return false
	}
	for _, step := range next.Steps {
		if strings.TrimSpace(step.Content) == previousActive && strings.TrimSpace(step.Status) == "completed" {
			return true
		}
	}
	return false
}
