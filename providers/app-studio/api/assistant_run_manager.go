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
	"errors"
	"strings"
	"sync"
	"time"
)

type projectAssistantTurnKind string

const (
	projectAssistantTurnMessage projectAssistantTurnKind = "message"
	projectAssistantTurnResume  projectAssistantTurnKind = "resume"
	projectAssistantTurnAbort   projectAssistantTurnKind = "abort"
)

var errProjectAssistantTurnPreempted = errors.New("assistant turn preempted")

// projectAssistantTurnItem is intentionally small and value-only so a later
// Eino TurnLoop checkpoint can persist queued work without serializing request,
// client, workspace, or tenant-authority handles.
type projectAssistantTurnItem struct {
	Kind               projectAssistantTurnKind `json:"kind"`
	OrgUUID            string                   `json:"orgUUID"`
	WorkspaceUUID      string                   `json:"workspaceUUID"`
	ProjectName        string                   `json:"projectName"`
	User               string                   `json:"user,omitempty"`
	RunID              string                   `json:"runID,omitempty"`
	RequestID          string                   `json:"requestID,omitempty"`
	AssistantMessageID string                   `json:"assistantMessageID,omitempty"`
	Decision           string                   `json:"decision,omitempty"`
	Answer             string                   `json:"answer,omitempty"`
	EditedArguments    map[string]any           `json:"editedArguments,omitempty"`
	CreatedAt          time.Time                `json:"createdAt"`
}

func newProjectAssistantTurnItem(kind projectAssistantTurnKind, id identity, projectName string) projectAssistantTurnItem {
	return projectAssistantTurnItem{
		Kind:          kind,
		OrgUUID:       strings.TrimSpace(id.orgUUID),
		WorkspaceUUID: strings.TrimSpace(id.workspaceUUID),
		ProjectName:   strings.TrimSpace(projectName),
		User:          strings.TrimSpace(id.user),
		CreatedAt:     time.Now().UTC(),
	}
}

func (i projectAssistantTurnItem) key() projectAssistantRunKey {
	return projectAssistantRunKey{
		OrgUUID:       strings.TrimSpace(i.OrgUUID),
		WorkspaceUUID: strings.TrimSpace(i.WorkspaceUUID),
		ProjectName:   strings.TrimSpace(i.ProjectName),
	}
}

type projectAssistantRunKey struct {
	OrgUUID       string
	WorkspaceUUID string
	ProjectName   string
}

func (k projectAssistantRunKey) valid() bool {
	return strings.TrimSpace(k.OrgUUID) != "" && strings.TrimSpace(k.WorkspaceUUID) != "" && strings.TrimSpace(k.ProjectName) != ""
}

type projectAssistantActiveTurn struct {
	item   projectAssistantTurnItem
	cancel context.CancelCauseFunc
	done   chan struct{}
}

type projectAssistantRunManager struct {
	mu     sync.Mutex
	active map[projectAssistantRunKey]*projectAssistantActiveTurn
}

func newProjectAssistantRunManager() *projectAssistantRunManager {
	return &projectAssistantRunManager{
		active: map[projectAssistantRunKey]*projectAssistantActiveTurn{},
	}
}

func (m *projectAssistantRunManager) Begin(ctx context.Context, item projectAssistantTurnItem) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	key := item.key()
	if m == nil || !key.valid() {
		return ctx, func() {}
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	active := &projectAssistantActiveTurn{
		item:   item,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	m.mu.Lock()
	if previous := m.active[key]; previous != nil {
		previous.cancel(errProjectAssistantTurnPreempted)
	}
	m.active[key] = active
	m.mu.Unlock()

	var once sync.Once
	return runCtx, func() {
		once.Do(func() {
			m.mu.Lock()
			if m.active[key] == active {
				delete(m.active, key)
			}
			m.mu.Unlock()
			cancel(nil)
			close(active.done)
		})
	}
}

func (m *projectAssistantRunManager) activeCount() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}
