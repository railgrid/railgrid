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
	"testing"
	"time"
)

func TestProjectAssistantResearchPhraseRequested(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"do some research on tinder ux", true},
		{"run a deep research pass over competitors", true},
		{"deep-research this market", true},
		{"Research the best auth libraries", true},
		{"RESEARCH: swipe patterns", true},
		{"do a deep reseach on docs page again", true},
		{"resarch the competitors", true},
		{"deep reserach please", true},
		{"researh this", true},
		{"run a few researches on pricing", true},
		{"investigate the swipe UX market", true},
		{"investgate what competitors charge", true},
		{"open an investigation into onboarding patterns", true},
		{"do a deep dive on the Faros docs", true},
		{"Deep-Dive: kcp vs vcluster", true},
		{"deepdive into oauth device flow", true},
		{"dig into how Vercel prices previews", true},
		{"dig deeper on the pricing page", true},
		{"give me a competitor analysis", true},
		{"competitive landscape scan for MCP gateways", true},
		{"market overview of internal developer platforms", true},
		{"write a literature review on agent memory", true},
		{"lit review on retrieval eval", true},
		{"find out everything about Runlayer", true},
		{"find out more about agentgateway", true},
		{"fact-check these deck claims", true},
		{"do due diligence on the vendor", true},
		{"get some background on kcp", true},
		{"gather sources for the pricing slide", true},
		{"I was researching this earlier", false},
		{"ask the researcher agent", false},
		{"we researched it last week", false},
		{"I am investigating the failing test", false},
		{"look into the login bug", false},
		{"find out why the build fails", false},
		{"dive into the code", false},
		{"reach out to the team", false},
		{"search the docs", false},
		{"fix the login bug", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := projectAssistantResearchPhraseRequested(tc.text); got != tc.want {
			t.Errorf("projectAssistantResearchPhraseRequested(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestProjectAssistantLatestUserMessage(t *testing.T) {
	conversation := []chatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "research swipe UIs"},
		{Role: "tool", Content: "tool output"},
	}
	if got := projectAssistantLatestUserMessage(conversation); got != "research swipe UIs" {
		t.Fatalf("latest user message = %q", got)
	}
	if got := projectAssistantLatestUserMessage(nil); got != "" {
		t.Fatalf("latest user message of empty conversation = %q", got)
	}
}

func TestProjectAssistantResearchAgentNames(t *testing.T) {
	raw := `{"agents":[
		{"name":"researcher","phase":"Ready"},
		{"name":"paused","phase":"Suspended"},
		{"name":"flagged","phase":"Ready","suspendedReason":"budget exhausted"},
		{"name":"fresh"},
		{"name":"  "}
	]}`
	got := projectAssistantResearchAgentNames(raw)
	want := []string{"researcher", "fresh"}
	if len(got) != len(want) {
		t.Fatalf("agent names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("agent names = %v, want %v", got, want)
		}
	}
	if names := projectAssistantResearchAgentNames("not json"); names != nil {
		t.Fatalf("malformed payload should yield nil, got %v", names)
	}
}

type researchCapabilityFakePort struct {
	listAgentsResult string
	listAgentsErr    error
	invoked          []string
}

func (p *researchCapabilityFakePort) DiscoverMCP(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, bool, error) {
	return nil, false, nil
}

func (p *researchCapabilityFakePort) Invoke(_ context.Context, tool projectAssistantTool, _ projectAssistantToolCallRequest) (string, error) {
	p.invoked = append(p.invoked, tool.Spec().Name)
	return p.listAgentsResult, p.listAgentsErr
}

func researchCapabilityMCPTool(name string) projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{Name: name, Description: "test", Risk: projectAssistantToolRiskRead},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) { return "", nil },
	}
}

func researchCapabilityAgentsTools() []projectAssistantTool {
	return []projectAssistantTool{
		researchCapabilityMCPTool(projectToolAgentsRunAgent),
		researchCapabilityMCPTool(projectToolAgentsGetRun),
		researchCapabilityMCPTool(projectToolAgentsListRuns),
		researchCapabilityMCPTool(projectToolAgentsListAgents),
	}
}

func researchCapabilityRequest(port projectAssistantToolPort, mode projectAssistantCollaborationMode, message string) projectAssistantRunRequest {
	return projectAssistantRunRequest{
		ToolPort:          port,
		CollaborationMode: mode,
		Conversation:      []chatMessage{{Role: "user", Content: message}},
	}
}

func TestProjectAssistantResearchCapabilityPrompt(t *testing.T) {
	activeAgents := `{"agents":[{"name":"researcher","phase":"Ready"}]}`

	t.Run("activates when phrase, tools, and active agents align", func(t *testing.T) {
		port := &researchCapabilityFakePort{listAgentsResult: activeAgents}
		req := researchCapabilityRequest(port, projectAssistantCollaborationModeDefault, "please research swipe-based UX")
		prompt := projectAssistantResearchCapabilityPrompt(context.Background(), req, researchCapabilityAgentsTools())
		if !strings.Contains(prompt, "researcher") || !strings.Contains(prompt, projectToolAgentsRunAgent) {
			t.Fatalf("prompt should name the agent and the run tool, got %q", prompt)
		}
		if len(port.invoked) != 1 || port.invoked[0] != projectToolAgentsListAgents {
			t.Fatalf("expected exactly one list_agents call, got %v", port.invoked)
		}
	})

	t.Run("silent without the research phrase", func(t *testing.T) {
		port := &researchCapabilityFakePort{listAgentsResult: activeAgents}
		req := researchCapabilityRequest(port, projectAssistantCollaborationModeDefault, "fix the login button")
		if prompt := projectAssistantResearchCapabilityPrompt(context.Background(), req, researchCapabilityAgentsTools()); prompt != "" {
			t.Fatalf("expected no prompt, got %q", prompt)
		}
		if len(port.invoked) != 0 {
			t.Fatalf("list_agents must not be called without the phrase, got %v", port.invoked)
		}
	})

	t.Run("silent when agents run tools are not federated", func(t *testing.T) {
		port := &researchCapabilityFakePort{listAgentsResult: activeAgents}
		req := researchCapabilityRequest(port, projectAssistantCollaborationModeDefault, "research this")
		tools := []projectAssistantTool{researchCapabilityMCPTool(projectToolInfrastructureListTemplates)}
		if prompt := projectAssistantResearchCapabilityPrompt(context.Background(), req, tools); prompt != "" {
			t.Fatalf("expected no prompt, got %q", prompt)
		}
	})

	t.Run("silent without active agents", func(t *testing.T) {
		port := &researchCapabilityFakePort{listAgentsResult: `{"agents":[{"name":"paused","phase":"Suspended"}]}`}
		req := researchCapabilityRequest(port, projectAssistantCollaborationModeDefault, "research this")
		if prompt := projectAssistantResearchCapabilityPrompt(context.Background(), req, researchCapabilityAgentsTools()); prompt != "" {
			t.Fatalf("expected no prompt, got %q", prompt)
		}
	})

	t.Run("silent on list_agents transport failure", func(t *testing.T) {
		port := &researchCapabilityFakePort{listAgentsErr: errors.New("boom")}
		req := researchCapabilityRequest(port, projectAssistantCollaborationModeDefault, "research this")
		if prompt := projectAssistantResearchCapabilityPrompt(context.Background(), req, researchCapabilityAgentsTools()); prompt != "" {
			t.Fatalf("expected no prompt, got %q", prompt)
		}
	})

	t.Run("silent in read-only collaboration modes", func(t *testing.T) {
		port := &researchCapabilityFakePort{listAgentsResult: activeAgents}
		req := researchCapabilityRequest(port, projectAssistantCollaborationModePlan, "research this")
		if prompt := projectAssistantResearchCapabilityPrompt(context.Background(), req, researchCapabilityAgentsTools()); prompt != "" {
			t.Fatalf("expected no prompt in plan mode, got %q", prompt)
		}
	})
}

func TestProjectAssistantMCPToolCallTimeout(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
		want time.Duration
	}{
		{"run_agent max wait gets margin", projectToolAgentsRunAgent, map[string]any{"wait": float64(120)}, 150 * time.Second},
		{"get_run max wait gets margin", projectToolAgentsGetRun, map[string]any{"wait": float64(300)}, 330 * time.Second},
		{"short wait keeps the default floor", projectToolAgentsRunAgent, map[string]any{"wait": float64(10)}, projectMCPCallTimeout},
		{"absent wait keeps the default", projectToolAgentsRunAgent, map[string]any{}, projectMCPCallTimeout},
		{"absurd wait is capped", projectToolAgentsGetRun, map[string]any{"wait": float64(100000)}, projectAssistantMCPWaitTimeoutCap},
		{"non-agents tools keep the default", projectToolInfrastructureProvision, map[string]any{"wait": float64(300)}, projectMCPCallTimeout},
		{"non-numeric wait keeps the default", projectToolAgentsRunAgent, map[string]any{"wait": "300"}, projectMCPCallTimeout},
	}
	for _, tc := range cases {
		if got := projectAssistantMCPToolCallTimeout(tc.tool, tc.args); got != tc.want {
			t.Errorf("%s: timeout = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProjectAssistantMCPToolSpecAllowsAgentsRunTools(t *testing.T) {
	cases := []struct {
		name string
		risk projectAssistantToolRisk
	}{
		{projectToolAgentsRunAgent, projectAssistantToolRiskRuntime},
		{projectToolAgentsGetRun, projectAssistantToolRiskRead},
		{projectToolAgentsListRuns, projectAssistantToolRiskRead},
		{projectToolAgentsListAgents, projectAssistantToolRiskRead},
	}
	for _, tc := range cases {
		spec, ok := projectAssistantMCPToolSpec(projectMCPTool{Name: tc.name, Description: "test"})
		if !ok {
			t.Fatalf("%s should pass the aggregate MCP allowlist", tc.name)
		}
		if spec.Risk != tc.risk {
			t.Fatalf("%s risk = %v, want %v", tc.name, spec.Risk, tc.risk)
		}
	}
}

func TestProjectAssistantResearchConversation(t *testing.T) {
	req := researchCapabilityRequest(nil, projectAssistantCollaborationModeDefault, "fix the login button")
	if got := projectAssistantResearchConversation(req, nil); len(got) != 1 || got[0].Content != "fix the login button" {
		t.Fatalf("nil run state must fall back to the request conversation, got %#v", got)
	}
	state := newProjectEinoAssistantRunState()
	if got := projectAssistantResearchConversation(req, state); len(got) != 1 || got[0].Content != "fix the login button" {
		t.Fatalf("run state without model messages must fall back to the request conversation, got %#v", got)
	}
	state.RecordModelInput(req.Conversation)
	state.RecordSteeringInput("do a deep research for https://faros.sh/docs/")
	got := projectAssistantResearchConversation(req, state)
	if latest := projectAssistantLatestUserMessage(got); latest != "do a deep research for https://faros.sh/docs/" {
		t.Fatalf("steered user message must be the latest, got %q from %#v", latest, got)
	}
}

// researchCapabilityDiscoveryPort federates the agents run tools on discovery
// and answers list_agents, so a full tool-discovery pass can exercise the
// research capability end to end.
type researchCapabilityDiscoveryPort struct {
	researchCapabilityFakePort
}

func (p *researchCapabilityDiscoveryPort) DiscoverMCP(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, bool, error) {
	return researchCapabilityAgentsTools(), false, nil
}

func TestProjectEinoAssistantRefreshToolDiscoveryActivatesResearchForSteeredMessage(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup}
	port := &researchCapabilityDiscoveryPort{researchCapabilityFakePort{listAgentsResult: `{"agents":[{"name":"researcher","phase":"Ready"}]}`}}
	req := projectAssistantRunRequest{
		ToolPort:          port,
		CollaborationMode: projectAssistantCollaborationModeDefault,
		TurnPolicy:        projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		Conversation:      []chatMessage{{Role: "user", Content: "fix the login button"}},
	}
	state := newProjectEinoAssistantRunState()

	first := projectEinoAssistantRefreshToolDiscovery(context.Background(), server, req, state)
	if strings.Contains(first.Prompt, "Research delegation capability") {
		t.Fatalf("start-of-run discovery must not activate research for %q: %q", req.Conversation[0].Content, first.Prompt)
	}
	if len(port.invoked) != 0 {
		t.Fatalf("list_agents must not be called without the phrase, got %v", port.invoked)
	}

	// The user steers a research request into the running turn. The request
	// conversation is frozen at run start; only the run state sees the message.
	state.RecordModelInput(req.Conversation)
	state.RecordSteeringInput("do a deep research for https://faros.sh/docs/")
	second := projectEinoAssistantRefreshToolDiscovery(context.Background(), server, req, state)
	if !strings.Contains(second.Prompt, "Research delegation capability") || !strings.Contains(second.Prompt, "researcher") {
		t.Fatalf("steered research request must activate delegation, got prompt %q", second.Prompt)
	}
	if len(port.invoked) != 1 || port.invoked[0] != projectToolAgentsListAgents {
		t.Fatalf("expected exactly one list_agents call on the steered refresh, got %v", port.invoked)
	}
}
