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
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/tenant"
)

func templateTestClient(connections ...map[string]any) *agentsclient.Client {
	objs := make([]runtime.Object, 0, len(connections))
	for _, spec := range connections {
		objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": agentsv1alpha1.SchemeGroupVersion.String(),
			"kind":       "Connection",
			"metadata":   map[string]any{"name": spec["name"]},
			"spec":       map[string]any{"type": spec["type"]},
		}})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		agentsclient.AgentGVR:      "AgentList",
		agentsclient.ScheduleGVR:   "ScheduleList",
		agentsclient.ConnectionGVR: "ConnectionList",
	}, objs...)
	return agentsclient.NewFromScope(tenant.NewScopeFromDynamic("workspace-cluster", dyn))
}

// TestCreateAgentFromTemplate is the templated create end to end over a fake
// workspace: the agent lands with the template's prompt, grants and wiring,
// the schedule lands pointing at it, and the request's explicit fields win.
func TestCreateAgentFromTemplate(t *testing.T) {
	s := newMCPTestServer(t)
	c := templateTestClient(
		map[string]any{"name": "tg", "type": "telegram"},
		map[string]any{"name": "searx", "type": "websearch"},
	)
	req := &createAgentRequest{
		Name:       "daily",
		Template:   "news-reporter",
		Parameters: map[string]string{"topics": "kcp\nkubernetes"},
		Wiring:     map[string]string{"modelCredential": "openai", "channel": "tg", "websearch": "searx"},
		Autonomy:   "auto",
		BudgetUSD:  "5",
	}
	a, report, err := s.createAgentFromRequest(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status.Phase != agentsv1alpha1.AgentPhaseReady {
		t.Errorf("phase = %q", a.Status.Phase)
	}
	if !strings.Contains(a.Spec.SystemPrompt, "- kcp\n- kubernetes") {
		t.Errorf("prompt not rendered: %q", a.Spec.SystemPrompt)
	}
	if a.Spec.Autonomy != "auto" {
		t.Errorf("override lost: autonomy = %q", a.Spec.Autonomy)
	}
	if a.Spec.Budget == nil || a.Spec.Budget.USDLimit != "5" {
		t.Errorf("budget override lost: %+v", a.Spec.Budget)
	}
	if got := a.Spec.ModelCredentialFor("chat"); got != "openai" {
		t.Errorf("chat credential = %q", got)
	}
	if len(a.Spec.Tools.Interactive.Connections) != 1 || a.Spec.Tools.Interactive.Connections[0] != "searx" {
		t.Errorf("connection slot not applied: %+v", a.Spec.Tools)
	}
	if len(report.Schedules) != 1 || report.Schedules[0] != "daily-daily-digest" || len(report.Warnings) != 0 {
		t.Fatalf("report = %+v", report)
	}
	sched, err := c.Schedules().Get(context.Background(), "daily-daily-digest", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sched.Spec.AgentRef != "daily" || sched.Spec.Type != "cron" || sched.Spec.Schedule != "0 8 * * 1-5" || sched.Spec.ChannelRef != "primary" {
		t.Errorf("schedule = %+v", sched.Spec)
	}
	stored, err := c.Agents().Get(context.Background(), "daily", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Spec.Channels) != 1 || stored.Spec.Channels[0].ConnectionRef != "tg" {
		t.Errorf("stored channels = %+v", stored.Spec.Channels)
	}
}

// TestCreateAgentFromTemplateFlatFieldsFillSlots: modelCredential and
// channels are the same thing as the slots, so a caller who knows create_agent
// already does not need the wiring map for them.
func TestCreateAgentFromTemplateFlatFieldsFillSlots(t *testing.T) {
	req := &createAgentRequest{
		Name:            "helper",
		Template:        "workspace-assistant",
		ModelCredential: "openai",
		Channels:        []channelInput{{Name: "primary", ConnectionRef: "slack", Primary: true}},
	}
	res, err := agentFromTemplateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Agent.Spec.ModelCredentialFor("chat"); got != "openai" {
		t.Errorf("chat credential = %q", got)
	}
	if len(res.Agent.Spec.Channels) != 1 || res.Agent.Spec.Channels[0].ConnectionRef != "slack" {
		t.Errorf("channels = %+v", res.Agent.Spec.Channels)
	}
	if res.Agent.Spec.DisplayName != "Workspace assistant" {
		t.Errorf("displayName = %q", res.Agent.Spec.DisplayName)
	}
	if !strings.Contains(res.Agent.Spec.SystemPrompt, "a small team using this Railgrid workspace") {
		t.Error("parameter default not applied")
	}
}

func TestCreateAgentFromTemplateRejects(t *testing.T) {
	cases := []struct {
		name string
		req  createAgentRequest
		want string
	}{
		{"unknown template", createAgentRequest{Name: "a", Template: "nope"}, `unknown template "nope"`},
		{"missing parameter", createAgentRequest{Name: "a", Template: "news-reporter", Wiring: map[string]string{"modelCredential": "m", "channel": "c"}}, `parameter "topics"`},
		{"missing wiring", createAgentRequest{Name: "a", Template: "news-reporter", Parameters: map[string]string{"topics": "x"}}, `wiring "modelCredential"`},
		{"families on harness template", createAgentRequest{Name: "a", Template: "code-reviewer",
			Parameters:          map[string]string{"repositories": "o/r"},
			Wiring:              map[string]string{"edge": "LinuxServer/box", "harnessCredential": "claude", "githubConnection": "gh"},
			InteractiveFamilies: []string{"web"}}, "harness-backed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := agentFromTemplateRequest(&tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if code, _ := errorClass(err); code != 400 {
				t.Errorf("status = %d, want 400", code)
			}
		})
	}
}

// TestVerifyTemplateWiring is the type check Instantiate cannot do: a slot
// wired to a connection of the wrong type, or to none at all, is refused
// before anything is created.
func TestVerifyTemplateWiring(t *testing.T) {
	s := newMCPTestServer(t)
	c := templateTestClient(
		map[string]any{"name": "gh", "type": "github"},
		map[string]any{"name": "tg", "type": "telegram"},
	)
	base := func() *createAgentRequest {
		return &createAgentRequest{Name: "r", Template: "news-reporter",
			Parameters: map[string]string{"topics": "x"},
			Wiring:     map[string]string{"modelCredential": "m", "channel": "tg"}}
	}
	wrongType := base()
	wrongType.Wiring["websearch"] = "gh"
	if _, _, err := s.createAgentFromRequest(context.Background(), c, wrongType); err == nil || !strings.Contains(err.Error(), "needs a websearch connection") {
		t.Errorf("github connection accepted in websearch slot: %v", err)
	}
	missing := base()
	missing.Wiring["websearch"] = "ghost"
	if _, _, err := s.createAgentFromRequest(context.Background(), c, missing); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing connection accepted: %v", err)
	}
	notChannel := base()
	notChannel.Wiring["channel"] = "gh"
	if _, _, err := s.createAgentFromRequest(context.Background(), c, notChannel); err == nil || !strings.Contains(err.Error(), "messaging connection") {
		t.Errorf("github connection accepted as channel: %v", err)
	}
	if _, err := c.Agents().Get(context.Background(), "r", metav1.GetOptions{}); err == nil {
		t.Error("an agent was created despite refused wiring")
	}
}

// TestAgentTemplateMCPTools asserts the catalog is advertised and readable
// without a tenant client, and that create_agent's schema carries the
// template fields.
func TestAgentTemplateMCPTools(t *testing.T) {
	result := mcpRPC(t, "/mcp", "tools/list", map[string]any{})
	var out struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]any `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range out.Tools {
		seen[tool.Name] = true
		if tool.Name == "create_agent" {
			for _, field := range []string{"template", "parameters", "wiring"} {
				if _, ok := tool.InputSchema.Properties[field]; !ok {
					t.Errorf("create_agent schema lacks %q", field)
				}
			}
		}
	}
	for _, want := range []string{"list_agent_templates", "get_agent_template"} {
		if !seen[want] {
			t.Errorf("tool %q not advertised", want)
		}
	}

	listed := mcpRPC(t, "/mcp", "tools/call", map[string]any{"name": "list_agent_templates", "arguments": map[string]any{}})
	var call struct {
		StructuredContent listAgentTemplatesOutput `json:"structuredContent"`
		IsError           bool                     `json:"isError"`
	}
	if err := json.Unmarshal(listed, &call); err != nil {
		t.Fatal(err)
	}
	if call.IsError || len(call.StructuredContent.Templates) < 4 {
		t.Fatalf("list_agent_templates = %s", listed)
	}
	names := map[string]string{}
	for _, tmpl := range call.StructuredContent.Templates {
		names[tmpl.Name] = tmpl.Backend
	}
	if names["code-reviewer"] != "harness" || names["news-reporter"] != "model" {
		t.Errorf("backends = %v", names)
	}

	got := mcpRPC(t, "/mcp", "tools/call", map[string]any{"name": "get_agent_template", "arguments": map[string]any{"name": "researcher"}})
	var one struct {
		StructuredContent getAgentTemplateOutput `json:"structuredContent"`
		IsError           bool                   `json:"isError"`
	}
	if err := json.Unmarshal(got, &one); err != nil {
		t.Fatal(err)
	}
	if one.IsError || one.StructuredContent.SystemPrompt == "" {
		t.Fatalf("get_agent_template = %s", got)
	}
}
