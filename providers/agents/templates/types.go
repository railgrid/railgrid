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

// Package templates is the agent template catalog: curated starting points a
// user instantiates into an ordinary Agent (plus the Schedules it needs) by
// wiring in only what the template cannot know about their workspace — which
// model credential, which channel, which machine.
//
// A template is shipped content, not tenant state. The built-in set lives in
// builtin/*.yaml, embedded into the provider binary for the MCP tools and
// rendered into the portal bundle as agent-templates.json by
// internal/gentemplates. The YAML is shaped as an AgentTemplate manifest in
// the agents.railgrid.ai group so the same files become instances of that
// kind when tenant-owned templates land (docs/roadmap/agents-templates.md),
// but no such kind is served today: nothing here is read from or written to
// kcp.
package templates

import (
	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// APIVersion and Kind are the manifest coordinates every template file carries.
const (
	APIVersion = "agents.railgrid.ai/v1alpha1"
	Kind       = "AgentTemplate"
)

// Template is one agent template, in the shape of a Kubernetes manifest.
type Template struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
}

// Metadata is the manifest's metadata; only the name is meaningful.
type Metadata struct {
	Name string `json:"name"`
}

// Spec describes what the template produces and what it needs wired in.
type Spec struct {
	// DisplayName is the card title in the picker and the created agent's
	// default display name.
	DisplayName string `json:"displayName"`
	// Description is one or two sentences on what the agent does.
	Description string `json:"description,omitempty"`
	// Category groups cards in the picker: one of Categories.
	Category string `json:"category"`
	// SuggestedName is the agent name the picker proposes; the user may change
	// it. A DNS label.
	SuggestedName string `json:"suggestedName,omitempty"`
	// Parameters are the template's declared inputs. Every {{key}} in the
	// template's text is one of these, and each one is used somewhere.
	Parameters []Parameter `json:"parameters,omitempty"`
	// Wiring lists the workspace objects the template needs: each slot maps
	// onto exactly one Agent spec field and is never free text.
	Wiring []WiringSlot `json:"wiring,omitempty"`
	// Agent is the produced agent's spec with the wired fields left empty.
	Agent agentsv1alpha1.AgentSpec `json:"agent"`
	// Schedules are created alongside the agent, agentRef filled in and names
	// prefixed with the agent's name.
	Schedules []TemplateSchedule `json:"schedules,omitempty"`
}

// Parameter types.
const (
	ParameterString = "string"
	ParameterText   = "text"
	ParameterList   = "list"
)

// Parameter is one declared input substituted into the template's text.
type Parameter struct {
	// Key is the placeholder name: {{key}}.
	Key string `json:"key"`
	// Label is the form label.
	Label string `json:"label"`
	// Type is string (one line), text (multi-line) or list (one item per
	// line, rendered as a markdown bullet list).
	Type string `json:"type"`
	// Required refuses instantiation without a value (after Default).
	Required bool `json:"required,omitempty"`
	// Default fills the parameter when the caller gives nothing.
	Default string `json:"default,omitempty"`
	// Description is the form hint.
	Description string `json:"description,omitempty"`
}

// Wiring slot kinds. Each one names the Agent field it fills.
const (
	// SlotModelCredential fills spec.backend.model.credentials.chat. Model
	// backend only.
	SlotModelCredential = "modelCredential"
	// SlotChannel fills spec.channels with one primary channel backed by the
	// named messaging Connection.
	SlotChannel = "channel"
	// SlotConnection appends a tool Connection of ConnectionType to both
	// tool grants' connections. Model backend only.
	SlotConnection = "connection"
	// SlotEdge fills spec.backend.harness.edgeRef. Harness backend only; the
	// value is "<Kind>/<name>".
	SlotEdge = "edge"
	// SlotHarnessCredential fills spec.backend.harness.credentialRef. Harness
	// backend only.
	SlotHarnessCredential = "harnessCredential"
	// SlotGitHubConnection fills spec.backend.harness.githubConnectionRef.
	// Harness backend only.
	SlotGitHubConnection = "githubConnection"
)

// WiringSlot is one workspace object the template needs.
type WiringSlot struct {
	// Key identifies the slot in Inputs.Wiring. Built-ins use the slot kind
	// when one of a kind is enough; a template with two connection slots keys
	// them apart ("websearch", "github").
	Key string `json:"key"`
	// Slot is the slot kind: one of the Slot* constants.
	Slot string `json:"slot"`
	// ConnectionType restricts a SlotConnection to Connections of that
	// spec.type (github, mcp, websearch, edges, http). Required for
	// SlotConnection, rejected elsewhere.
	ConnectionType string `json:"connectionType,omitempty"`
	// Required refuses instantiation without a value.
	Required bool `json:"required,omitempty"`
	// Description says what the slot is for, in the form.
	Description string `json:"description,omitempty"`
}

// TemplateSchedule is one Schedule the template creates with the agent. Its
// spec is a ScheduleSpec with agentRef left empty.
type TemplateSchedule struct {
	// Name is suffixed onto the agent name: <agent>-<name>.
	Name string `json:"name"`
	// Spec is the schedule's spec with agentRef empty. Only cron schedules are
	// allowed in a template; a wakeup is a one-shot and a heartbeat's checklist
	// is standing state the agent owns, not a starting point.
	Spec agentsv1alpha1.ScheduleSpec `json:"spec"`
}

// Categories are the picker's groups, in display order.
var Categories = []string{"engineering", "research", "operations", "assistant"}

// Inputs is everything a caller supplies to instantiate a template.
type Inputs struct {
	// Name is the agent's object name. Required.
	Name string `json:"name"`
	// DisplayName overrides the template's displayName when set.
	DisplayName string `json:"displayName,omitempty"`
	// Parameters are values by parameter key. A list parameter is one item
	// per line.
	Parameters map[string]string `json:"parameters,omitempty"`
	// Wiring are workspace object names by slot key. An edge slot's value is
	// "<Kind>/<name>".
	Wiring map[string]string `json:"wiring,omitempty"`
}

// Result is what instantiation produced, ready to create: the agent first,
// then its schedules.
type Result struct {
	Agent     *agentsv1alpha1.Agent
	Schedules []*agentsv1alpha1.Schedule
}
