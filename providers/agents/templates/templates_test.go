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
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// TestBuiltinCatalog is the gate every shipped template passes: it parses
// strictly, Validate accepts it, and it instantiates with the fixture inputs
// into an agent whose wired fields are all filled.
func TestBuiltinCatalog(t *testing.T) {
	all := Builtin()
	if len(all) == 0 {
		t.Fatal("no built-in templates")
	}
	want := map[string]bool{"code-reviewer": true, "news-reporter": true, "researcher": true, "workspace-assistant": true}
	for _, tmpl := range all {
		delete(want, tmpl.Metadata.Name)
		t.Run(tmpl.Metadata.Name, func(t *testing.T) {
			res, err := Instantiate(tmpl, FixtureInputs(tmpl))
			if err != nil {
				t.Fatal(err)
			}
			spec := res.Agent.Spec
			if spec.DisplayName != tmpl.Spec.DisplayName {
				t.Errorf("displayName = %q, want the template's %q", spec.DisplayName, tmpl.Spec.DisplayName)
			}
			if strings.Contains(spec.SystemPrompt, "{{") || strings.Contains(spec.Description, "{{") {
				t.Errorf("unrendered placeholder left in agent text:\n%s", spec.SystemPrompt)
			}
			for _, s := range res.Schedules {
				if s.Spec.AgentRef != res.Agent.Name {
					t.Errorf("schedule %s agentRef = %q, want %q", s.Name, s.Spec.AgentRef, res.Agent.Name)
				}
				if !strings.HasPrefix(s.Name, res.Agent.Name+"-") {
					t.Errorf("schedule %s is not prefixed with the agent name", s.Name)
				}
				if strings.Contains(s.Spec.Task, "{{") || strings.Contains(s.Spec.Schedule, "{{") {
					t.Errorf("unrendered placeholder left in schedule %s", s.Name)
				}
			}
			if spec.HarnessBacked() {
				h := spec.Harness()
				if h == nil || h.EdgeRef.Name == "" || h.CredentialRef == "" {
					t.Errorf("harness agent not fully wired: %+v", h)
				}
				if spec.Backend.Model != nil {
					t.Error("harness agent carries a model block")
				}
			} else {
				if spec.ModelCredentialFor(agentsv1alpha1.PurposeChat) == "" {
					t.Error("model agent has no chat credential after wiring")
				}
				if spec.Backend.Harness != nil {
					t.Error("model agent carries a harness block")
				}
			}
		})
	}
	for name := range want {
		t.Errorf("starter template %q is missing from the catalog", name)
	}
}

// TestInstantiateWiresEverySlotKind pins what each slot kind writes, with the
// news reporter (model) and the code reviewer (harness) as the two shapes.
func TestInstantiateWiresEverySlotKind(t *testing.T) {
	reporter, _ := Lookup("news-reporter")
	res, err := Instantiate(reporter, Inputs{
		Name:       "daily",
		Parameters: map[string]string{"topics": "kcp\n\n  kubernetes  \n"},
		Wiring:     map[string]string{"modelCredential": "openai", "channel": "tg", "websearch": "searx"},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := res.Agent.Spec
	if got := spec.ModelCredentialFor("chat"); got != "openai" {
		t.Errorf("chat credential = %q", got)
	}
	if len(spec.Channels) != 1 || spec.Channels[0].ConnectionRef != "tg" || !spec.Channels[0].Primary || spec.Channels[0].Name != "primary" {
		t.Errorf("channels = %+v", spec.Channels)
	}
	for _, grant := range []agentsv1alpha1.ToolGrant{spec.Tools.Interactive, spec.Tools.Background} {
		if len(grant.Connections) != 1 || grant.Connections[0] != "searx" {
			t.Errorf("connection slot not applied to grant: %+v", grant)
		}
	}
	if !strings.Contains(spec.SystemPrompt, "- kcp\n- kubernetes\n") {
		t.Errorf("list parameter not rendered as bullets:\n%s", spec.SystemPrompt)
	}
	if len(res.Schedules) != 1 || res.Schedules[0].Name != "daily-daily-digest" || res.Schedules[0].Spec.Schedule != "0 8 * * 1-5" {
		t.Errorf("schedule = %+v", res.Schedules)
	}
	if res.Schedules[0].Spec.ChannelRef != "primary" || res.Schedules[0].Spec.Type != "cron" {
		t.Errorf("schedule delivery = %+v", res.Schedules[0].Spec)
	}

	reviewer, _ := Lookup("code-reviewer")
	res, err = Instantiate(reviewer, Inputs{
		Name:        "rev",
		DisplayName: "PR reviewer",
		Parameters:  map[string]string{"repositories": "railgrid/railgrid"},
		Wiring:      map[string]string{"edge": "MacOSServer/mini", "harnessCredential": "claude", "githubConnection": "gh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec = res.Agent.Spec
	h := spec.Harness()
	if h == nil || h.EdgeRef.Kind != "MacOSServer" || h.EdgeRef.Name != "mini" || h.CredentialRef != "claude" || h.GitHubConnectionRef != "gh" || h.Workspace != "persistent" {
		t.Errorf("harness = %+v", h)
	}
	if spec.DisplayName != "PR reviewer" {
		t.Errorf("displayName override ignored: %q", spec.DisplayName)
	}
	if !strings.Contains(spec.SystemPrompt, "Correctness first") {
		t.Error("parameter default not applied")
	}
}

func TestInstantiateInputErrors(t *testing.T) {
	reporter, _ := Lookup("news-reporter")
	good := map[string]string{"modelCredential": "openai", "channel": "tg"}
	cases := []struct {
		name string
		in   Inputs
		want string
	}{
		{"no name", Inputs{Parameters: map[string]string{"topics": "x"}, Wiring: good}, "name is required"},
		{"bad name", Inputs{Name: "Bad_Name", Parameters: map[string]string{"topics": "x"}, Wiring: good}, "lowercase"},
		{"missing required parameter", Inputs{Name: "a", Wiring: good}, `parameter "topics"`},
		{"blank list is missing", Inputs{Name: "a", Parameters: map[string]string{"topics": " \n "}, Wiring: good}, `parameter "topics"`},
		{"undeclared parameter", Inputs{Name: "a", Parameters: map[string]string{"topics": "x", "nope": "y"}, Wiring: good}, `parameter "nope" is not declared`},
		{"missing required wiring", Inputs{Name: "a", Parameters: map[string]string{"topics": "x"}, Wiring: map[string]string{"modelCredential": "m"}}, `wiring "channel"`},
		{"undeclared wiring", Inputs{Name: "a", Parameters: map[string]string{"topics": "x"}, Wiring: map[string]string{"modelCredential": "m", "channel": "c", "edge": "LinuxServer/x"}}, `wiring "edge" is not declared`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Instantiate(reporter, tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			var ie *InputError
			if !errors.As(err, &ie) {
				t.Errorf("error is not an InputError: %T", err)
			}
		})
	}

	reviewer, _ := Lookup("code-reviewer")
	for _, edge := range []string{"dev-box", "KubernetesCluster/prod", "/x", "LinuxServer/"} {
		_, err := Instantiate(reviewer, Inputs{Name: "r", Parameters: map[string]string{"repositories": "a/b"},
			Wiring: map[string]string{"edge": edge, "harnessCredential": "c", "githubConnection": "g"}})
		if err == nil {
			t.Errorf("edge %q accepted", edge)
		}
	}
}

// TestValidateRejects spells out the authoring mistakes the catalog test
// would otherwise report as a wall of text.
func TestValidateRejects(t *testing.T) {
	base := func() Template {
		tmpl, _ := Lookup("news-reporter")
		return *deepCopyTemplate(&tmpl)
	}
	cases := []struct {
		name   string
		mutate func(*Template)
		want   string
	}{
		{"undeclared placeholder", func(t *Template) { t.Spec.Agent.SystemPrompt += " {{missing}}" }, "{{missing}} is used but not declared"},
		{"unused parameter", func(t *Template) {
			t.Spec.Parameters = append(t.Spec.Parameters, Parameter{Key: "spare", Label: "Spare", Type: "string"})
		}, `"spare" is declared but never used`},
		{"bad parameter type", func(t *Template) { t.Spec.Parameters[0].Type = "number" }, "must be string, text or list"},
		{"channels pre-wired", func(t *Template) {
			t.Spec.Agent.Channels = []agentsv1alpha1.AgentChannel{{Name: "primary", ConnectionRef: "x"}}
		}, "spec.agent.channels must be empty"},
		{"credential pre-wired", func(t *Template) { t.Spec.Agent.SetModelCredential("chat", "x") }, "credentials and fallbacks must be empty"},
		{"connections pre-wired", func(t *Template) { t.Spec.Agent.Tools.Interactive.Connections = []string{"x"} }, "connections must be empty"},
		{"unknown family", func(t *Template) { t.Spec.Agent.Tools.Interactive.Families = []string{"core", "teleport"} }, `unknown family "teleport"`},
		{"family without core", func(t *Template) { t.Spec.Agent.Tools.Interactive.Families = []string{"web"} }, "must include core"},
		{"model template without credential slot", func(t *Template) { t.Spec.Wiring = t.Spec.Wiring[1:] }, "must declare a modelCredential"},
		{"model template with edge slot", func(t *Template) {
			t.Spec.Wiring = append(t.Spec.Wiring, WiringSlot{Key: "edge", Slot: SlotEdge})
		}, "cannot declare a edge slot"},
		{"connection slot without type", func(t *Template) { t.Spec.Wiring[2].ConnectionType = "" }, "needs a tool connectionType"},
		{"connection slot with channel type", func(t *Template) { t.Spec.Wiring[2].ConnectionType = "telegram" }, "needs a tool connectionType"},
		{"duplicate slot key", func(t *Template) { t.Spec.Wiring[2].Key = "channel" }, `"channel" is declared twice`},
		{"two channel slots", func(t *Template) {
			t.Spec.Wiring = append(t.Spec.Wiring, WiringSlot{Key: "other", Slot: SlotChannel})
		}, "declares 2 channel slots"},
		{"wakeup schedule", func(t *Template) { t.Spec.Schedules[0].Spec.Type = "wakeup" }, "must be cron"},
		{"schedule agentRef set", func(t *Template) { t.Spec.Schedules[0].Spec.AgentRef = "x" }, "agentRef must be empty"},
		{"schedule to unknown channel", func(t *Template) { t.Spec.Schedules[0].Spec.ChannelRef = "news" }, `channelRef "news"`},
		{"schedule without task", func(t *Template) { t.Spec.Schedules[0].Spec.Task = "" }, "task is required"},
		{"bad category", func(t *Template) { t.Spec.Category = "misc" }, "spec.category"},
		{"bad autonomy", func(t *Template) { t.Spec.Agent.Autonomy = "yolo" }, "autonomy"},
		{"wrong kind", func(t *Template) { t.Kind = "Agent" }, "kind must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := base()
			tc.mutate(&tmpl)
			err := Validate(tmpl)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	harness := func() Template {
		tmpl, _ := Lookup("code-reviewer")
		return *deepCopyTemplate(&tmpl)
	}
	harnessCases := []struct {
		name   string
		mutate func(*Template)
		want   string
	}{
		{"tools on harness", func(t *Template) { t.Spec.Agent.Tools.Interactive.Families = []string{"core"} }, "tools is meaningless"},
		{"delegates on harness", func(t *Template) { t.Spec.Agent.Delegates = []string{"x"} }, "delegates is meaningless"},
		{"edge pre-wired", func(t *Template) { t.Spec.Agent.Backend.Harness.EdgeRef.Name = "x" }, "edgeRef must be empty"},
		{"no edge slot", func(t *Template) { t.Spec.Wiring = t.Spec.Wiring[1:] }, "must declare a edge"},
		{"connection slot on harness", func(t *Template) {
			t.Spec.Wiring = append(t.Spec.Wiring, WiringSlot{Key: "ws", Slot: SlotConnection, ConnectionType: "websearch"})
		}, "cannot declare a connection slot"},
	}
	for _, tc := range harnessCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := harness()
			tc.mutate(&tmpl)
			err := Validate(tmpl)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestParseIsStrict(t *testing.T) {
	_, err := Parse([]byte(`
apiVersion: agents.railgrid.ai/v1alpha1
kind: AgentTemplate
metadata: {name: x}
spec:
  displayName: X
  category: assistant
  wiring: [{key: modelCredential, slot: modelCredential, required: true}]
  agent:
    systemPromt: typo
`))
	if err == nil || !strings.Contains(err.Error(), "systemPromt") {
		t.Fatalf("unknown field accepted: %v", err)
	}
}

// TestAssetsAreFresh holds the portal copies to the YAML. Regenerate with
//
//	make gen-agents-templates
//
// (go run ./internal/gentemplates portal/public/agent-templates.json portal/src/test/fixtures/agent-templates).
func TestAssetsAreFresh(t *testing.T) {
	const (
		asset    = "../portal/public/agent-templates.json"
		fixtures = "../portal/src/test/fixtures/agent-templates"
	)
	want, err := CatalogJSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(asset)
	if err != nil {
		t.Fatalf("the portal's agent template asset is missing: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s is stale: run make gen-agents-templates", asset)
	}
	seen := map[string]bool{}
	for _, tmpl := range Builtin() {
		path := filepath.Join(fixtures, tmpl.Metadata.Name+".json")
		seen[tmpl.Metadata.Name+".json"] = true
		want, err := FixtureJSON(tmpl)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("fixture for %s is missing: run make gen-agents-templates", tmpl.Metadata.Name)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s is stale: run make gen-agents-templates", path)
		}
	}
	entries, err := os.ReadDir(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" && !seen[e.Name()] {
			t.Errorf("%s has no template behind it: run make gen-agents-templates", e.Name())
		}
	}
}

func deepCopyTemplate(t *Template) *Template {
	out := *t
	out.Spec.Parameters = append([]Parameter(nil), t.Spec.Parameters...)
	out.Spec.Wiring = append([]WiringSlot(nil), t.Spec.Wiring...)
	out.Spec.Agent = *t.Spec.Agent.DeepCopy()
	out.Spec.Schedules = deepCopySchedules(t.Spec.Schedules)
	return &out
}
