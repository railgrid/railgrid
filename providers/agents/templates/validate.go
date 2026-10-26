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

package templates

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// dnsLabelRE is the object-name rule every create form in the portal enforces.
var dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// parameterKeyRE is the placeholder grammar: an identifier.
var parameterKeyRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// connectionTypes a SlotConnection may ask for: the tool-side Connection
// types. Messaging types go through SlotChannel instead.
var connectionTypes = map[string]bool{
	agentsv1alpha1.ConnectionTypeGitHub:    true,
	agentsv1alpha1.ConnectionTypeMCP:       true,
	agentsv1alpha1.ConnectionTypeWebSearch: true,
	agentsv1alpha1.ConnectionTypeEdges:     true,
	agentsv1alpha1.ConnectionTypeHTTP:      true,
}

var slotKinds = map[string]bool{
	SlotModelCredential:   true,
	SlotChannel:           true,
	SlotConnection:        true,
	SlotEdge:              true,
	SlotHarnessCredential: true,
	SlotGitHubConnection:  true,
}

// Validate checks a template is well-formed and can only ever produce an
// agent the reconciler would accept. It is what TestBuiltinCatalog runs on
// every shipped file, so the rules are spelled out rather than left to the
// create path to discover.
func Validate(t Template) error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if t.APIVersion != APIVersion {
		fail("apiVersion must be %q, got %q", APIVersion, t.APIVersion)
	}
	if t.Kind != Kind {
		fail("kind must be %q, got %q", Kind, t.Kind)
	}
	if !dnsLabelRE.MatchString(t.Metadata.Name) {
		fail("metadata.name %q must be a DNS label", t.Metadata.Name)
	}
	if strings.TrimSpace(t.Spec.DisplayName) == "" {
		fail("spec.displayName is required")
	}
	if categoryRank(t.Spec.Category) >= len(Categories) {
		fail("spec.category %q must be one of %s", t.Spec.Category, strings.Join(Categories, ", "))
	}
	if t.Spec.SuggestedName != "" && !dnsLabelRE.MatchString(t.Spec.SuggestedName) {
		fail("spec.suggestedName %q must be a DNS label", t.Spec.SuggestedName)
	}

	// Parameters: well-formed, unique, every one used, every use declared.
	declared := map[string]bool{}
	for i, p := range t.Spec.Parameters {
		at := fmt.Sprintf("spec.parameters[%d]", i)
		if !parameterKeyRE.MatchString(p.Key) {
			fail("%s.key %q must be an identifier", at, p.Key)
		}
		if declared[p.Key] {
			fail("%s.key %q is declared twice", at, p.Key)
		}
		declared[p.Key] = true
		if strings.TrimSpace(p.Label) == "" {
			fail("%s.label is required", at)
		}
		switch p.Type {
		case ParameterString, ParameterText, ParameterList:
		default:
			fail("%s.type %q must be string, text or list", at, p.Type)
		}
	}
	used := map[string]bool{}
	for _, field := range textFields(&t) {
		for _, key := range placeholders(*field) {
			used[key] = true
			if !declared[key] {
				fail("{{%s}} is used but not declared in spec.parameters", key)
			}
		}
	}
	for _, p := range t.Spec.Parameters {
		if declared[p.Key] && !used[p.Key] {
			fail("spec.parameters %q is declared but never used", p.Key)
		}
	}

	// Wiring: known kinds, unique keys, one of each non-connection kind, and
	// the right set for the backend.
	keys := map[string]bool{}
	kinds := map[string]int{}
	for i, w := range t.Spec.Wiring {
		at := fmt.Sprintf("spec.wiring[%d]", i)
		if !parameterKeyRE.MatchString(w.Key) {
			fail("%s.key %q must be an identifier", at, w.Key)
		}
		if keys[w.Key] {
			fail("%s.key %q is declared twice", at, w.Key)
		}
		keys[w.Key] = true
		if !slotKinds[w.Slot] {
			fail("%s.slot %q is not a known slot kind", at, w.Slot)
			continue
		}
		kinds[w.Slot]++
		if w.Slot == SlotConnection {
			if !connectionTypes[w.ConnectionType] {
				fail("%s is a connection slot and needs a tool connectionType (github, mcp, websearch, edges, http), got %q", at, w.ConnectionType)
			}
		} else if w.ConnectionType != "" {
			fail("%s.connectionType is only meaningful on a connection slot", at)
		}
	}
	for kind, n := range kinds {
		if kind != SlotConnection && n > 1 {
			fail("spec.wiring declares %d %s slots; an agent has one", n, kind)
		}
	}

	spec := t.Spec.Agent
	harness := spec.BackendType() == agentsv1alpha1.AgentBackendHarness
	switch spec.BackendType() {
	case agentsv1alpha1.AgentBackendModel, agentsv1alpha1.AgentBackendHarness:
	default:
		fail("spec.agent.backend.type %q must be model or harness", spec.Backend.Type)
	}
	if harness {
		for _, need := range []string{SlotEdge, SlotHarnessCredential} {
			if kinds[need] == 0 {
				fail("a harness template must declare a %s wiring slot", need)
			}
		}
		for _, no := range []string{SlotModelCredential, SlotConnection} {
			if kinds[no] > 0 {
				fail("a harness template cannot declare a %s slot: the harness brings its own tools and model", no)
			}
		}
		if spec.Backend.Model != nil {
			fail("spec.agent.backend.model is rejected on a harness template")
		}
		if len(spec.Tools.Interactive.Families)+len(spec.Tools.Interactive.Connections)+len(spec.Tools.Interactive.Toolsets)+len(spec.Tools.Interactive.RequireApproval)+
			len(spec.Tools.Background.Families)+len(spec.Tools.Background.Connections)+len(spec.Tools.Background.Toolsets)+len(spec.Tools.Background.RequireApproval) > 0 {
			fail("spec.agent.tools is meaningless on a harness template; the harness brings its own tools")
		}
		if len(spec.Delegates) > 0 {
			fail("spec.agent.delegates is meaningless on a harness template")
		}
		if h := spec.Backend.Harness; h != nil {
			if h.EdgeRef.Kind != "" || h.EdgeRef.Name != "" {
				fail("spec.agent.backend.harness.edgeRef must be empty: it comes from the edge wiring slot")
			}
			if h.CredentialRef != "" {
				fail("spec.agent.backend.harness.credentialRef must be empty: it comes from the harnessCredential wiring slot")
			}
			if h.GitHubConnectionRef != "" {
				fail("spec.agent.backend.harness.githubConnectionRef must be empty: it comes from the githubConnection wiring slot")
			}
		}
	} else {
		if kinds[SlotModelCredential] == 0 {
			fail("a model template must declare a modelCredential wiring slot")
		}
		for _, no := range []string{SlotEdge, SlotHarnessCredential, SlotGitHubConnection} {
			if kinds[no] > 0 {
				fail("a model template cannot declare a %s slot; a GitHub connection is a connection slot of type github", no)
			}
		}
		if spec.Backend.Harness != nil {
			fail("spec.agent.backend.harness is rejected on a model template")
		}
		if m := spec.Backend.Model; m != nil && (len(m.Credentials) > 0 || len(m.Fallbacks) > 0) {
			fail("spec.agent.backend.model.credentials and fallbacks must be empty: they come from the modelCredential wiring slot")
		}
		for _, grant := range []struct {
			name string
			g    agentsv1alpha1.ToolGrant
		}{{"interactive", spec.Tools.Interactive}, {"background", spec.Tools.Background}} {
			if len(grant.g.Connections) > 0 {
				fail("spec.agent.tools.%s.connections must be empty: connections come from connection wiring slots", grant.name)
			}
			if len(grant.g.Families) > 0 {
				core := false
				for _, f := range grant.g.Families {
					if f == "core" {
						core = true
					}
					if !agentsv1alpha1.KnownToolFamilies[f] {
						fail("spec.agent.tools.%s.families names unknown family %q", grant.name, f)
					}
				}
				if !core {
					fail("spec.agent.tools.%s.families must include core", grant.name)
				}
			}
		}
	}
	if len(spec.Channels) > 0 {
		fail("spec.agent.channels must be empty: the channel comes from the channel wiring slot")
	}
	switch spec.Autonomy {
	case "", agentsv1alpha1.AutonomySuggest, agentsv1alpha1.AutonomyAsk, agentsv1alpha1.AutonomyAuto:
	default:
		fail("spec.agent.autonomy %q must be suggest, ask or auto", spec.Autonomy)
	}
	if spec.Budget != nil {
		switch spec.Budget.Window {
		case "", "day", "month":
		default:
			fail("spec.agent.budget.window %q must be day or month", spec.Budget.Window)
		}
	}

	// Schedules: cron only, named, unique, delivering to the one channel a
	// template can wire.
	schedNames := map[string]bool{}
	for i, s := range t.Spec.Schedules {
		at := fmt.Sprintf("spec.schedules[%d]", i)
		if !dnsLabelRE.MatchString(s.Name) {
			fail("%s.name %q must be a DNS label", at, s.Name)
		}
		if schedNames[s.Name] {
			fail("%s.name %q is declared twice", at, s.Name)
		}
		schedNames[s.Name] = true
		if s.Spec.AgentRef != "" {
			fail("%s.spec.agentRef must be empty: it is the created agent", at)
		}
		if s.Spec.Type != agentsv1alpha1.ScheduleTypeCron {
			fail("%s.spec.type must be cron", at)
		}
		if strings.TrimSpace(s.Spec.Schedule) == "" {
			fail("%s.spec.schedule is required", at)
		}
		if strings.TrimSpace(s.Spec.Task) == "" {
			fail("%s.spec.task is required", at)
		}
		if s.Spec.RunAt != nil || s.Spec.Checklist != "" {
			fail("%s carries wakeup or heartbeat fields on a cron schedule", at)
		}
		if s.Spec.ChannelRef != "" && s.Spec.ChannelRef != primaryChannelName {
			fail("%s.spec.channelRef %q must be %q or empty: a template wires one channel", at, s.Spec.ChannelRef, primaryChannelName)
		}
		if s.Spec.ChannelRef != "" && kinds[SlotChannel] == 0 {
			fail("%s delivers to a channel but the template declares no channel slot", at)
		}
	}

	return errors.Join(errs...)
}
