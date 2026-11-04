// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// selfCR is an in-memory SelfConfigAccess: one agent, its triggers and the
// workspace's webhooks.
type selfCR struct {
	*scheduleCR
	agent     *agentsv1alpha1.Agent
	triggers  map[string]*agentsv1alpha1.Trigger
	webhooks  []agentsv1alpha1.Webhook
	conflicts int // UpdateAgent calls to fail with a conflict before succeeding
	writes    int
}

var triggerResource = schema.GroupResource{Group: "agents.railgrid.ai", Resource: "triggers"}

func newSelfCR(agent *agentsv1alpha1.Agent, triggers ...*agentsv1alpha1.Trigger) *selfCR {
	cr := &selfCR{scheduleCR: newScheduleCR(), agent: agent, triggers: map[string]*agentsv1alpha1.Trigger{}}
	for _, t := range triggers {
		cr.triggers[t.Name] = t
	}
	return cr
}

func (c *selfCR) GetAgent(context.Context, string) (*agentsv1alpha1.Agent, error) {
	return c.agent.DeepCopy(), nil
}

func (c *selfCR) UpdateAgent(_ context.Context, a *agentsv1alpha1.Agent) error {
	if c.conflicts > 0 {
		c.conflicts--
		return apierrors.NewConflict(schema.GroupResource{Resource: "agents"}, a.Name, nil)
	}
	c.writes++
	c.agent = a
	return nil
}

func (c *selfCR) CreateTrigger(_ context.Context, t *agentsv1alpha1.Trigger) error {
	if _, ok := c.triggers[t.Name]; ok {
		return apierrors.NewAlreadyExists(triggerResource, t.Name)
	}
	c.triggers[t.Name] = t
	return nil
}

func (c *selfCR) GetTrigger(_ context.Context, name string) (*agentsv1alpha1.Trigger, error) {
	t, ok := c.triggers[name]
	if !ok {
		return nil, apierrors.NewNotFound(triggerResource, name)
	}
	return t.DeepCopy(), nil
}

func (c *selfCR) UpdateTrigger(_ context.Context, t *agentsv1alpha1.Trigger) error {
	c.triggers[t.Name] = t
	return nil
}

func (c *selfCR) DeleteTrigger(_ context.Context, name string) error {
	delete(c.triggers, name)
	return nil
}

func (c *selfCR) ListTriggers(context.Context) ([]agentsv1alpha1.Trigger, error) {
	out := []agentsv1alpha1.Trigger{}
	for _, t := range c.triggers {
		out = append(out, *t)
	}
	return out, nil
}

func (c *selfCR) ListWebhooks(context.Context) ([]agentsv1alpha1.Webhook, error) {
	return c.webhooks, nil
}

func selfAgent(sc *agentsv1alpha1.AgentSelfConfiguration) *agentsv1alpha1.Agent {
	return &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "railgrid"},
		Spec: agentsv1alpha1.AgentSpec{
			DisplayName: "Railgrid", SystemPrompt: "You review pull requests.",
			Channels:          []agentsv1alpha1.AgentChannel{{Name: "ops", ConnectionRef: "slack"}},
			SelfConfiguration: sc,
		},
	}
}

func selfDeps(cr *selfCR) Deps {
	return Deps{Agent: cr.agent.DeepCopy(), CR: cr, RunID: "run-1"}
}

func selfTool(t *testing.T, d Deps, name string) func(context.Context, string) (string, error) {
	t.Helper()
	for _, tool := range SelfConfig(d) {
		if tool.Name == name {
			return tool.Exec
		}
	}
	t.Fatalf("self-configuration family has no %q tool", name)
	return nil
}

func toolNames(d Deps) []string {
	var out []string
	for _, tool := range SelfConfig(d) {
		out = append(out, tool.Name)
	}
	return out
}

func TestSelfConfigToolsFollowScopes(t *testing.T) {
	enabled := func(scopes ...string) *agentsv1alpha1.AgentSelfConfiguration {
		sc := &agentsv1alpha1.AgentSelfConfiguration{Enabled: true}
		for _, s := range scopes {
			sc.Scopes = append(sc.Scopes, agentsv1alpha1.AgentSelfConfigurationScope{Name: s})
		}
		return sc
	}
	tests := []struct {
		name string
		sc   *agentsv1alpha1.AgentSelfConfiguration
		want []string
	}{
		{"unset", nil, nil},
		{"disabled", &agentsv1alpha1.AgentSelfConfiguration{}, nil},
		{"every scope", enabled(), SelfConfigToolNames},
		{"prompt only", enabled(agentsv1alpha1.SelfConfigScopePrompt), []string{"self_config_get", "self_prompt_update"}},
		{"triggers only", enabled(agentsv1alpha1.SelfConfigScopeTriggers), []string{
			"self_config_get", "webhooks_list", "triggers_list", "trigger_create", "trigger_update", "trigger_delete"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolNames(selfDeps(newSelfCR(selfAgent(tt.sc))))
			if !sameSet(got, tt.want) {
				t.Fatalf("tools = %v, want %v", got, tt.want)
			}
		})
	}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// An accessor that cannot write (a read-only or test accessor) yields no
// family at all, rather than tools that fail on every call.
func TestSelfConfigAbsentWithoutWritableAccess(t *testing.T) {
	d := Deps{Agent: selfAgent(&agentsv1alpha1.AgentSelfConfiguration{Enabled: true}), CR: newScheduleCR()}
	if got := SelfConfig(d); got != nil {
		t.Fatalf("SelfConfig over a read-only accessor = %d tools, want none", len(got))
	}
}

func TestSelfPromptAppend(t *testing.T) {
	cr := newSelfCR(selfAgent(&agentsv1alpha1.AgentSelfConfiguration{Enabled: true}))
	cr.agent.Spec.Memory.MaxNotes = 7
	cr.conflicts = 1 // another writer got there first; the tool re-reads and retries
	out, err := selfTool(t, selfDeps(cr), "self_prompt_update")(context.Background(), `{"append":"Always link the PR."}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "You review pull requests.\n\nAlways link the PR."; cr.agent.Spec.SystemPrompt != want {
		t.Fatalf("systemPrompt = %q, want %q", cr.agent.Spec.SystemPrompt, want)
	}
	if cr.agent.Spec.Memory.MaxNotes != 7 {
		t.Fatalf("a prompt change rewrote memory: maxNotes = %d", cr.agent.Spec.Memory.MaxNotes)
	}
	if got := cr.agent.Annotations[agentsv1alpha1.SelfConfiguredByRunAnnotation]; got != "run-1" {
		t.Fatalf("self-configured-by-run = %q, want run-1", got)
	}
	if !strings.Contains(out, "next run") {
		t.Fatalf("result %q does not say when the change applies", out)
	}
}

func TestSelfPromptRefusals(t *testing.T) {
	cr := newSelfCR(selfAgent(&agentsv1alpha1.AgentSelfConfiguration{Enabled: true}))
	exec := selfTool(t, selfDeps(cr), "self_prompt_update")
	for name, args := range map[string]string{
		"both modes": `{"systemPrompt":"x","append":"y"}`,
		"too long":   `{"systemPrompt":"` + strings.Repeat("x", maxSystemPrompt+1) + `"}`,
	} {
		if _, err := exec(context.Background(), args); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	out, err := exec(context.Background(), `{"systemPrompt":"You review pull requests."}`)
	if err != nil || !strings.Contains(out, "nothing changed") {
		t.Fatalf("an unchanged prompt = (%q, %v), want a nothing-changed answer", out, err)
	}
	if cr.writes != 0 {
		t.Fatalf("refused or no-op calls wrote the agent %d times", cr.writes)
	}
}

func TestSelfMemoryUpdate(t *testing.T) {
	cr := newSelfCR(selfAgent(&agentsv1alpha1.AgentSelfConfiguration{Enabled: true}))
	exec := selfTool(t, selfDeps(cr), "self_memory_update")
	if _, err := exec(context.Background(), `{"maxNotes":0}`); err == nil {
		t.Fatal("maxNotes 0 accepted")
	}
	if _, err := exec(context.Background(), `{"enabled":false,"maxNotes":40}`); err != nil {
		t.Fatal(err)
	}
	if m := cr.agent.Spec.Memory; m.Enabled == nil || *m.Enabled || m.MaxNotes != 40 {
		t.Fatalf("memory = %+v, want disabled with 40 notes", m)
	}
}

func TestTriggerCreateOwnsAndValidates(t *testing.T) {
	cr := newSelfCR(selfAgent(&agentsv1alpha1.AgentSelfConfiguration{Enabled: true}))
	exec := selfTool(t, selfDeps(cr), "trigger_create")
	for name, args := range map[string]string{
		"unknown filter key": `{"name":"pr","webhookRef":"gh","task":"review","filter":{"evnetType":"pull_request"}}`,
		"non-string filter":  `{"name":"pr","webhookRef":"gh","task":"review","filter":{"action":["opened"]}}`,
		"unknown channel":    `{"name":"pr","webhookRef":"gh","task":"review","channelRef":"random"}`,
		"no task":            `{"name":"pr","webhookRef":"gh"}`,
	} {
		if _, err := exec(context.Background(), args); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if len(cr.triggers) != 0 {
		t.Fatalf("refused creates left %d triggers", len(cr.triggers))
	}
	// agentRef is not a parameter: a trigger the agent creates always drives itself.
	if _, err := exec(context.Background(), `{"name":"pr","webhookRef":"gh","task":"review","agentRef":"other","channelRef":"ops","filter":{"eventType":"pull_request","action":"opened"}}`); err != nil {
		t.Fatal(err)
	}
	got := cr.triggers["pr"]
	if got.Spec.AgentRef != "railgrid" || got.Spec.ChannelRef != "ops" || got.Spec.Filter["action"] != "opened" {
		t.Fatalf("created trigger = %+v", got.Spec)
	}
	if got.Annotations[agentsv1alpha1.SelfConfiguredByRunAnnotation] != "run-1" {
		t.Fatal("created trigger not stamped with the run")
	}
	if _, err := exec(context.Background(), `{"name":"pr","webhookRef":"gh","task":"again"}`); err == nil || !strings.Contains(err.Error(), "trigger_update") {
		t.Fatalf("duplicate create = %v, want a pointer to trigger_update", err)
	}
}

func TestTriggerUpdateAndDelete(t *testing.T) {
	mine := &agentsv1alpha1.Trigger{ObjectMeta: metav1.ObjectMeta{Name: "pr"}, Spec: agentsv1alpha1.TriggerSpec{
		AgentRef: "railgrid", WebhookRef: "gh", Task: "review", Filter: map[string]string{"eventType": "push"}}}
	theirs := &agentsv1alpha1.Trigger{ObjectMeta: metav1.ObjectMeta{Name: "deploy"}, Spec: agentsv1alpha1.TriggerSpec{
		AgentRef: "deployer", WebhookRef: "gh", Task: "deploy"}}
	cr := newSelfCR(selfAgent(&agentsv1alpha1.AgentSelfConfiguration{Enabled: true}), mine, theirs)
	d := selfDeps(cr)
	update, del := selfTool(t, d, "trigger_update"), selfTool(t, d, "trigger_delete")

	if _, err := update(context.Background(), `{"name":"deploy","suspend":true}`); err == nil {
		t.Fatal("updated another agent's trigger")
	}
	if _, err := del(context.Background(), `{"name":"deploy"}`); err == nil {
		t.Fatal("deleted another agent's trigger")
	}
	if _, err := update(context.Background(), `{"name":"pr"}`); err == nil {
		t.Fatal("an empty update succeeded")
	}
	if _, err := update(context.Background(), `{"name":"pr","filter":{"eventType":"pull_request"},"suspend":true}`); err != nil {
		t.Fatal(err)
	}
	if got := cr.triggers["pr"].Spec; !got.Suspend || got.Filter["eventType"] != "pull_request" || got.Task != "review" {
		t.Fatalf("updated trigger = %+v", got)
	}
	if _, err := update(context.Background(), `{"name":"pr","filter":{}}`); err != nil {
		t.Fatal(err)
	}
	if f := cr.triggers["pr"].Spec.Filter; f != nil {
		t.Fatalf("filter {} = %v, want cleared", f)
	}
	if _, err := del(context.Background(), `{"name":"pr"}`); err != nil {
		t.Fatal(err)
	}
	if _, ok := cr.triggers["pr"]; ok {
		t.Fatal("trigger not deleted")
	}
	if _, ok := cr.triggers["deploy"]; !ok {
		t.Fatal("another agent's trigger was touched")
	}
}

func TestTriggersListShowsOnlyOwn(t *testing.T) {
	cr := newSelfCR(selfAgent(&agentsv1alpha1.AgentSelfConfiguration{Enabled: true}),
		&agentsv1alpha1.Trigger{ObjectMeta: metav1.ObjectMeta{Name: "pr"}, Spec: agentsv1alpha1.TriggerSpec{AgentRef: "railgrid", WebhookRef: "gh", Task: "review"}},
		&agentsv1alpha1.Trigger{ObjectMeta: metav1.ObjectMeta{Name: "deploy"}, Spec: agentsv1alpha1.TriggerSpec{AgentRef: "deployer", WebhookRef: "gh", Task: "deploy"}})
	out, err := selfTool(t, selfDeps(cr), "triggers_list")(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pr webhook=gh") || strings.Contains(out, "deploy") {
		t.Fatalf("triggers_list = %q", out)
	}
}
