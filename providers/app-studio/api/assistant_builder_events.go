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

	"github.com/google/uuid"
)

const (
	projectBuilderEventPlanReady        = "plan_ready"
	projectBuilderEventPlanApproved     = "plan_approved"
	projectBuilderEventWorkspaceChanged = "workspace_changed"
)

type projectBuilderEventView struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

func newProjectBuilderEventID() string {
	return "evt-" + uuid.NewString()
}

func projectAssistantBuilderEventView(
	eventType string,
) *projectBuilderEventView {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return nil
	}
	return &projectBuilderEventView{
		ID:   newProjectBuilderEventID(),
		Type: eventType,
	}
}

func emitProjectAssistantBuilderEvent(callbacks projectAssistantStreamCallbacks, view *projectBuilderEventView) {
	if view == nil || callbacks.OnAssistantEvent == nil {
		return
	}
	callbacks.OnAssistantEvent(projectAssistantEvent{
		Type:         projectAssistantEventBuilderEvent,
		BuilderEvent: view,
	})
}
