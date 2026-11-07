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

package codex

import (
	"encoding/json"
	"errors"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// Bound ancestry retained from thread/started, including announcements whose
// parent has not arrived yet. Unrelated threads cannot grow this without bound.
const maxChildThreads = 256

type childThread struct {
	parentID string
	turnID   string
}

// routeThreadEvent separates app-server's multiplexed thread stream from the
// one root session the runner owns. Only the thread/start or thread/resume
// RESPONSE establishes a new root; an unsolicited notification cannot pin it.
func (s *runState) routeThreadEvent(msg wireMessage) (bool, error) {
	id := firstNonEmpty(threadIDFromParams(msg.Params), threadID(msg.Params))
	if msg.Method == "thread/started" {
		id = threadID(msg.Params)
		s.rememberChild(msg.Params)
	}
	if id == "" || id == s.sessionID {
		return false, nil
	}
	if !s.isChild(id) {
		// A different thread's notification is not evidence that our turn failed.
		// Never approve its requests or forward its payload as our conversation.
		if hasID(msg.ID) {
			return true, s.rejectThreadRequest(msg)
		}
		return true, nil
	}
	child := s.children[id]
	switch msg.Method {
	case "turn/started":
		child.turnID = turnID(msg.Params)
	case "turn/completed":
		// Child completion, failure and auth errors are progress, not root outcomes.
		if turnID(msg.Params) == child.turnID {
			child.turnID = ""
		}
	}
	if isApprovalRequest(msg.Method) && hasID(msg.ID) {
		if child.turnID == "" {
			return true, s.rejectThreadRequest(msg)
		}
		return true, s.handleThreadApprovalRequest(msg, id, child.turnID)
	}
	// Product questions currently stop the process and resume the ROOT. That
	// cannot deliver an answer to a child's pending RPC. Refuse explicitly so
	// the harness can report it to the parent instead of parking the wrong turn.
	if hasID(msg.ID) {
		return true, s.rejectThreadRequest(msg)
	}
	data := boundedData(msg.Params)
	if isAuthNotification(msg.Method) || authData(msg.Params) {
		data = authEventData(msg.Params)
	}
	payload, err := json.Marshal(struct {
		Method   string          `json:"method"`
		ThreadID string          `json:"threadId"`
		TurnID   string          `json:"turnId,omitempty"`
		Params   json.RawMessage `json:"params"`
	}{msg.Method, id, turnIDFromParams(msg.Params, turnID(msg.Params)), data})
	if err != nil {
		return true, err
	}
	return true, s.emitEvent(harness.Event{
		Type: "subagent_progress", SessionID: s.sessionID, TurnID: s.turnID,
		Data: boundedData(payload),
	})
}

func (s *runState) rememberChild(data json.RawMessage) {
	if len(data) > maxEventData {
		return
	}
	var params struct {
		Thread struct {
			ID       string          `json:"id"`
			ParentID string          `json:"parentThreadId"`
			Source   json.RawMessage `json:"source"`
		} `json:"thread"`
	}
	if json.Unmarshal(data, &params) != nil {
		return
	}
	thread := params.Thread
	// source is a tagged union: ordinary sources and internal review/compact
	// subagents can be strings. They must not invalidate parentThreadId.
	var source struct {
		SubAgent struct {
			Spawn struct {
				ParentID string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subAgent"`
	}
	_ = json.Unmarshal(thread.Source, &source)
	sourceParent := source.SubAgent.Spawn.ParentID
	if thread.ParentID != "" && sourceParent != "" && thread.ParentID != sourceParent {
		return
	}
	parent := firstNonEmpty(thread.ParentID, sourceParent)
	if thread.ID == "" || parent == "" || thread.ID == parent || thread.ID == s.sessionID ||
		len(thread.ID) > maxClarificationField || len(parent) > maxClarificationField {
		return
	}
	if s.children == nil {
		s.children = make(map[string]*childThread)
	}
	// Do not permit later announcements to reparent a known thread.
	if s.children[thread.ID] == nil && len(s.children) < maxChildThreads {
		s.children[thread.ID] = &childThread{parentID: parent}
	}
}

func (s *runState) isChild(id string) bool {
	if s.sessionID == "" {
		return false
	}
	// Bounded traversal also rejects cycles and allows announcements to arrive
	// grandchild first without trusting them until the chain reaches our root.
	for range len(s.children) {
		child := s.children[id]
		if child == nil {
			return false
		}
		if child.parentID == s.sessionID {
			return true
		}
		id = child.parentID
	}
	return false
}

func (s *runState) rejectThreadRequest(msg wireMessage) error {
	if s.conn == nil {
		return errors.New("codex thread request has no app-server connection")
	}
	if codexApprovalTool(msg.Method) != "" {
		return s.conn.respond(msg.ID, map[string]string{"decision": "decline"})
	}
	return s.conn.respondError(msg.ID, -32602, "railgrid cannot route this request to the active root turn")
}
