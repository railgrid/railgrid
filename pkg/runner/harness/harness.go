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

// Package harness defines the public execution seam for runner adapters.
package harness

import (
	"context"
	"encoding/json"
)

// Info describes an adapter's executable and readiness state.
type Info struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Ready   bool     `json:"ready"`
	Reasons []string `json:"reasons,omitempty"`
}

// Launch identifies one worker attempt and the thread turn it should execute.
type Launch struct {
	AttemptID    string `json:"attemptID"`
	Workdir      string `json:"workdir"`
	SessionID    string `json:"sessionID,omitempty"`
	Instructions string `json:"instructions"`
	Model        string `json:"model,omitempty"`
}

// Event is a bounded adapter event forwarded to the runner state engine.
type Event struct {
	Type          string          `json:"type"`
	SessionID     string          `json:"sessionID,omitempty"`
	TurnID        string          `json:"turnID,omitempty"`
	Message       string          `json:"message,omitempty"`
	Data          json.RawMessage `json:"data,omitempty"`
	Clarification *Clarification  `json:"clarification,omitempty"`
}

// Clarification is a bounded product question that can be answered by a
// caller before resuming the existing harness session.
type Clarification struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Result is the terminal state of one adapter launch.
type Result struct {
	Phase         string         `json:"phase"`
	SessionID     string         `json:"sessionID,omitempty"`
	Blocker       string         `json:"blocker,omitempty"`
	Clarification *Clarification `json:"clarification,omitempty"`
}

// Emit receives adapter events. Implementations must treat an error as a
// terminal failure and stop producing events for the launch.
type Emit func(Event) error

// Adapter probes its executable and runs one isolated agent turn.
type Adapter interface {
	Probe(context.Context) (Info, error)
	Run(context.Context, Launch, Emit) (Result, error)
}
