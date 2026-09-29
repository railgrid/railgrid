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

// CredentialKind names which harness identity a launch carries, and therefore
// how the adapter hands it to its child. The kinds are harness-specific on
// purpose: an adapter that is given a credential it cannot use must refuse it
// rather than guess.
type CredentialKind string

const (
	// CredentialClaudeOAuth is a `claude setup-token` value, injected as
	// CLAUDE_CODE_OAUTH_TOKEN.
	CredentialClaudeOAuth CredentialKind = "claude-oauth"
	// CredentialClaudeAPIKey is an Anthropic API key, injected as
	// ANTHROPIC_API_KEY.
	CredentialClaudeAPIKey CredentialKind = "claude-apikey"
	// CredentialCodexAuth is a Codex login session file, materialized as the
	// home's auth.json for the duration of one launch.
	CredentialCodexAuth CredentialKind = "codex-auth"
)

// Credential is the caller's harness identity for ONE launch.
//
// It is dispatch data, not enrollment: the coordinator sends it with every
// start and resume, the runner keeps it in memory for the life of the attempt
// and never writes it to durable state, and an adapter puts it in front of its
// child for exactly one turn. That is what makes a runner a shared machine
// rather than a shared billable identity — nothing on the host authenticates
// to a model provider on its own.
//
// It carries no json tags because it must never be serialized: every wire
// shape that could hold one (the runner's StartRequest, its durable journal)
// strips it before anything is written down.
type Credential struct {
	Kind  CredentialKind
	Value string
}

// Empty reports whether c carries nothing to inject.
func (c Credential) Empty() bool { return c.Kind == "" || c.Value == "" }

// Launch identifies one worker attempt and the thread turn it should execute.
//
// SessionID, when set, is the harness session this turn continues. It may be a
// session an earlier ATTEMPT created — that is how a conversation spans turns —
// so an adapter must resume it rather than assume it owns it.
type Launch struct {
	AttemptID    string `json:"attemptID"`
	Workdir      string `json:"workdir"`
	SessionID    string `json:"sessionID,omitempty"`
	Instructions string `json:"instructions"`
	Model        string `json:"model,omitempty"`
	// Credential is the identity this turn runs as. It is never serialized.
	Credential Credential `json:"-"`
	// Permissions, when set, is how this launch reaches a human for a tool call
	// the permission mode does not pre-approve. nil means there is nobody to
	// ask, and an adapter must then keep its harness from prompting at all —
	// which is what every caller that predates this field gets, unchanged.
	Permissions PermissionAsker `json:"-"`
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
