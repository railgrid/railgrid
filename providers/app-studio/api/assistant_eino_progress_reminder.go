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
	projectEinoAssistantProgressReminderSilenceModelCalls = 3
	projectEinoAssistantProgressReminderMaxAttempts       = 3
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
		"User-visible progress is overdue. report_progress is available. Call it now with one concise completed outcome and your next direction or blocker, then continue working. Context: %s. This reminder is advisory and non-blocking; do not force a tool choice, stop, or wait.",
		context,
	)
}

func projectEinoAssistantPlanPhaseTransition(previous, next projectAssistantPlanSnapshot) bool {
	if len(next.Steps) == 0 {
		return false
	}
	if len(previous.Steps) == 0 {
		for _, step := range next.Steps {
			if step.Status == "in_progress" || step.Status == "completed" {
				return true
			}
		}
		return false
	}
	if len(previous.Steps) != len(next.Steps) {
		return true
	}
	for index := range next.Steps {
		if strings.TrimSpace(previous.Steps[index].Status) != strings.TrimSpace(next.Steps[index].Status) {
			return true
		}
	}
	return false
}
