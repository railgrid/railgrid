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
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/internal/triggerfilter"
)

// SelfConfigAccess is the tenant-resource surface the self-configuration
// tools write through. It is optional on CRAccess, so test fakes and
// read-only accessors need not implement it; the family is absent when the
// run's accessor cannot write.
type SelfConfigAccess interface {
	UpdateAgent(ctx context.Context, a *agentsv1alpha1.Agent) error
	CreateTrigger(ctx context.Context, t *agentsv1alpha1.Trigger) error
	GetTrigger(ctx context.Context, name string) (*agentsv1alpha1.Trigger, error)
	UpdateTrigger(ctx context.Context, t *agentsv1alpha1.Trigger) error
	DeleteTrigger(ctx context.Context, name string) error
	ListTriggers(ctx context.Context) ([]agentsv1alpha1.Trigger, error)
	ListWebhooks(ctx context.Context) ([]agentsv1alpha1.Webhook, error)
}

// SelfConfigMutatingTools are the self-configuration tools that change
// something. spec.selfConfiguration.approval gates exactly these; the read
// tools never wait for the user.
var SelfConfigMutatingTools = []string{
	"self_prompt_update", "self_memory_update",
	"trigger_create", "trigger_update", "trigger_delete",
}

// SelfConfigToolNames is every tool the family can return.
var SelfConfigToolNames = append([]string{"self_config_get", "triggers_list", "webhooks_list"}, SelfConfigMutatingTools...)

// SelfConfig returns the self-configuration family: the agent reading and
// changing its own prompt, triggers and memory settings, each tool present
// only for a scope spec.selfConfiguration grants. Changes are written to the
// Agent and Trigger objects and take effect from the agent's next run; the
// run making them keeps the configuration it started with.
func SelfConfig(d Deps) []engine.Tool {
	spec := &d.Agent.Spec
	access, ok := d.CR.(SelfConfigAccess)
	if !ok || spec.SelfConfiguration == nil || !spec.SelfConfiguration.Enabled {
		return nil
	}
	out := []engine.Tool{d.selfConfigGetTool()}
	if spec.SelfConfigures(agentsv1alpha1.SelfConfigScopePrompt) {
		out = append(out, d.selfPromptUpdateTool(access))
	}
	if spec.SelfConfigures(agentsv1alpha1.SelfConfigScopeMemory) {
		out = append(out, d.selfMemoryUpdateTool(access))
	}
	if spec.SelfConfigures(agentsv1alpha1.SelfConfigScopeTriggers) {
		out = append(out, d.triggerTools(access)...)
	}
	return out
}

// selfScopes lists the scopes this agent may change, for the tool output.
func selfScopes(spec *agentsv1alpha1.AgentSpec) []string {
	var out []string
	for _, s := range []string{agentsv1alpha1.SelfConfigScopePrompt, agentsv1alpha1.SelfConfigScopeTriggers, agentsv1alpha1.SelfConfigScopeMemory} {
		if spec.SelfConfigures(s) {
			out = append(out, s)
		}
	}
	return out
}

func (d Deps) selfConfigGetTool() engine.Tool {
	return engine.Tool{
		Name: "self_config_get",
		Desc: "Read your own current configuration as it is stored now — system prompt, description, memory settings — and which parts of it you are allowed to change. Read it before changing anything: another run, or the user, may have changed it since this run started.",
		Exec: func(ctx context.Context, _ string) (string, error) {
			a, err := d.CR.GetAgent(ctx, d.Agent.Name)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "You may change: %s\n", strings.Join(selfScopes(&d.Agent.Spec), ", "))
			fmt.Fprintf(&b, "Changes need the user's approval: %s\n", d.Agent.Spec.SelfConfigApproval())
			b.WriteString("You may never change your tools, autonomy, budget, limits, backend, delegates or these self-configuration settings.\n\n")
			fmt.Fprintf(&b, "description: %s\n", a.Spec.Description)
			enabled := a.Spec.Memory.Enabled == nil || *a.Spec.Memory.Enabled
			fmt.Fprintf(&b, "memory: enabled=%v maxNotes=%d\n", enabled, a.Spec.Memory.MaxNotes)
			fmt.Fprintf(&b, "systemPrompt (%d chars):\n%s\n", len(a.Spec.SystemPrompt), a.Spec.SystemPrompt)
			return b.String(), nil
		},
	}
}

// updateSelf re-reads the agent, applies mutate, and writes it back, retrying
// on a conflicting write. mutate returns the fields it changed; none is an
// error, so a call that changes nothing says so instead of claiming success.
func (d Deps) updateSelf(ctx context.Context, access SelfConfigAccess, mutate func(*agentsv1alpha1.Agent) ([]string, error)) ([]string, error) {
	var changed []string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		a, err := d.CR.GetAgent(ctx, d.Agent.Name)
		if err != nil {
			return err
		}
		if changed, err = mutate(a); err != nil {
			return err
		}
		if len(changed) == 0 {
			return nil
		}
		stampRun(&a.ObjectMeta, d.RunID)
		return access.UpdateAgent(ctx, a)
	})
	return changed, err
}

func stampRun(m *metav1.ObjectMeta, runID string) {
	if runID == "" {
		return
	}
	if m.Annotations == nil {
		m.Annotations = map[string]string{}
	}
	m.Annotations[agentsv1alpha1.SelfConfiguredByRunAnnotation] = runID
}

// maxSystemPrompt and maxDescription mirror the CRD's MaxLength markers, so
// an over-long prompt is refused with a message the model can act on rather
// than an apiserver validation error.
const (
	maxSystemPrompt = 32768
	maxDescription  = 2048
)

func (d Deps) selfPromptUpdateTool(access SelfConfigAccess) engine.Tool {
	return engine.Tool{
		Name: "self_prompt_update",
		Desc: "Change your own standing instructions. Pass systemPrompt to replace the whole prompt, or append to add a paragraph to its end (e.g. a lesson learned or a user preference to remember). description replaces your one-line summary. Read self_config_get first. Takes effect from your next run.",
		Params: map[string]engine.Param{
			"systemPrompt": {Type: "string", Desc: "the complete new system prompt (replaces the current one)"},
			"append":       {Type: "string", Desc: "text to add as a new paragraph at the end of the current system prompt"},
			"description":  {Type: "string", Desc: "the new one-line description of what you are for"},
		},
		Exec: func(ctx context.Context, argsJSON string) (string, error) {
			args, err := parseArgs(argsJSON)
			if err != nil {
				return "", err
			}
			_, hasReplace := args["systemPrompt"]
			appendText := strings.TrimSpace(argString(args, "append"))
			if hasReplace && appendText != "" {
				return "", fmt.Errorf("pass systemPrompt or append, not both")
			}
			changed, err := d.updateSelf(ctx, access, func(a *agentsv1alpha1.Agent) ([]string, error) {
				var changed []string
				prompt := a.Spec.SystemPrompt
				switch {
				case hasReplace:
					prompt = argString(args, "systemPrompt")
				case appendText != "":
					prompt = strings.TrimRight(prompt, "\n")
					if prompt != "" {
						prompt += "\n\n"
					}
					prompt += appendText
				}
				if len(prompt) > maxSystemPrompt {
					return nil, fmt.Errorf("the system prompt would be %d characters; the limit is %d — shorten it", len(prompt), maxSystemPrompt)
				}
				if prompt != a.Spec.SystemPrompt {
					a.Spec.SystemPrompt = prompt
					changed = append(changed, "systemPrompt")
				}
				if _, ok := args["description"]; ok {
					desc := strings.TrimSpace(argString(args, "description"))
					if len(desc) > maxDescription {
						return nil, fmt.Errorf("description is %d characters; the limit is %d", len(desc), maxDescription)
					}
					if desc != a.Spec.Description {
						a.Spec.Description = desc
						changed = append(changed, "description")
					}
				}
				return changed, nil
			})
			if err != nil {
				return "", err
			}
			if len(changed) == 0 {
				return "nothing changed — pass systemPrompt, append or description with a new value", nil
			}
			return fmt.Sprintf("updated %s; takes effect from your next run", strings.Join(changed, ", ")), nil
		},
	}
}

// maxMemoryNotes bounds spec.memory.maxNotes as the agent may set it.
const maxMemoryNotes = 500

func (d Deps) selfMemoryUpdateTool(access SelfConfigAccess) engine.Tool {
	return engine.Tool{
		Name: "self_memory_update",
		Desc: fmt.Sprintf("Change your long-term memory settings: turn memory on or off, or change how many notes you keep (1–%d). Takes effect from your next run.", maxMemoryNotes),
		Params: map[string]engine.Param{
			"enabled":  {Type: "boolean", Desc: "true keeps long-term memory, false stops using it"},
			"maxNotes": {Type: "integer", Desc: fmt.Sprintf("how many notes to keep, 1–%d", maxMemoryNotes)},
		},
		Exec: func(ctx context.Context, argsJSON string) (string, error) {
			args, err := parseArgs(argsJSON)
			if err != nil {
				return "", err
			}
			changed, err := d.updateSelf(ctx, access, func(a *agentsv1alpha1.Agent) ([]string, error) {
				var changed []string
				if _, ok := args["enabled"]; ok {
					v := argBool(args, "enabled")
					if a.Spec.Memory.Enabled == nil || *a.Spec.Memory.Enabled != v {
						a.Spec.Memory.Enabled = &v
						changed = append(changed, fmt.Sprintf("enabled=%v", v))
					}
				}
				if _, ok := args["maxNotes"]; ok {
					n := argInt(args, "maxNotes")
					if n < 1 || n > maxMemoryNotes {
						return nil, fmt.Errorf("maxNotes must be between 1 and %d", maxMemoryNotes)
					}
					if int(a.Spec.Memory.MaxNotes) != n {
						a.Spec.Memory.MaxNotes = int32(n)
						changed = append(changed, fmt.Sprintf("maxNotes=%d", n))
					}
				}
				return changed, nil
			})
			if err != nil {
				return "", err
			}
			if len(changed) == 0 {
				return "nothing changed — pass enabled or maxNotes with a new value", nil
			}
			return fmt.Sprintf("memory updated (%s); takes effect from your next run", strings.Join(changed, ", ")), nil
		},
	}
}

// ownTrigger resolves a trigger by name and refuses it unless it drives the
// running agent, for the same reason ownSchedule does: triggers are
// cluster-scoped in the tenant workspace, and these tools are about the agent
// managing itself.
func (d Deps) ownTrigger(ctx context.Context, access SelfConfigAccess, name string) (*agentsv1alpha1.Trigger, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	t, err := access.GetTrigger(ctx, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("no trigger named %q — use triggers_list to see your triggers", name)
		}
		return nil, err
	}
	if t.Spec.AgentRef != d.Agent.Name {
		return nil, fmt.Errorf("trigger %q belongs to agent %q, not you", name, t.Spec.AgentRef)
	}
	return t, nil
}

// argFilter reads the filter argument: an object of string values. present is
// false when the argument was not passed at all.
func argFilter(args map[string]any) (filter map[string]string, present bool, err error) {
	raw, ok := args["filter"]
	if !ok {
		return nil, false, nil
	}
	if raw == nil {
		return nil, true, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, true, fmt.Errorf("filter must be an object of string values, e.g. {\"eventType\": \"pull_request\", \"action\": \"opened\"}")
	}
	filter = map[string]string{}
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, true, fmt.Errorf("filter key %q must be a string", k)
		}
		filter[k] = s
	}
	if unknown := triggerfilter.UnknownKeys(filter); len(unknown) > 0 {
		return nil, true, fmt.Errorf("unknown filter key %s (known: %s)", strings.Join(unknown, ", "), strings.Join(triggerfilter.KnownKeys, ", "))
	}
	if len(filter) == 0 {
		filter = nil
	}
	return filter, true, nil
}

// checkChannelRef refuses a channel the agent does not have, which would
// otherwise post the trigger's output nowhere.
func (d Deps) checkChannelRef(ref string) error {
	if ref == "" {
		return nil
	}
	var names []string
	for _, ch := range d.Agent.Spec.Channels {
		if ch.Name == ref {
			return nil
		}
		names = append(names, ch.Name)
	}
	if len(names) == 0 {
		return fmt.Errorf("you have no channels; leave channelRef empty")
	}
	return fmt.Errorf("you have no channel %q (yours: %s)", ref, strings.Join(names, ", "))
}

const filterDesc = `which deliveries fire the trigger; every key must pass. Keys: eventType (event header or body type/action), action (body action, e.g. opened), repository (owner/repo), match (substring of the raw payload), header.<name>. eventType, action and repository take a comma-separated any-of list. Example: {"eventType": "pull_request", "action": "opened,reopened"}`

func (d Deps) triggerTools(access SelfConfigAccess) []engine.Tool {
	return []engine.Tool{
		{
			Name: "webhooks_list",
			Desc: "List the webhooks in this workspace that a trigger can route from. You can route from a webhook but not create one — ask the user if the one you need does not exist.",
			Exec: func(ctx context.Context, _ string) (string, error) {
				items, err := access.ListWebhooks(ctx)
				if err != nil {
					return "", err
				}
				if len(items) == 0 {
					return "no webhooks in this workspace — ask the user to create one", nil
				}
				var b strings.Builder
				for _, w := range items {
					fmt.Fprintf(&b, "- %s (source %s)", w.Name, w.Spec.Source)
					if w.Spec.DisplayName != "" {
						fmt.Fprintf(&b, ": %s", w.Spec.DisplayName)
					}
					b.WriteString("\n")
				}
				return b.String(), nil
			},
		},
		{
			Name: "triggers_list",
			Desc: "List your triggers: the webhook each routes from, its filter, task, channel and whether it is paused.",
			Exec: func(ctx context.Context, _ string) (string, error) {
				items, err := access.ListTriggers(ctx)
				if err != nil {
					return "", err
				}
				var b strings.Builder
				for _, t := range items {
					if t.Spec.AgentRef != d.Agent.Name {
						continue
					}
					fmt.Fprintf(&b, "- %s webhook=%s filter=%s channel=%s suspend=%v\n  task: %s\n",
						t.Name, t.Spec.WebhookRef, formatFilter(t.Spec.Filter), t.Spec.ChannelRef, t.Spec.Suspend, clip(t.Spec.Task, 500))
				}
				if b.Len() == 0 {
					return "you have no triggers", nil
				}
				return b.String(), nil
			},
		},
		{
			Name: "trigger_create",
			Desc: "Create a NEW trigger for yourself: run a task whenever a webhook receives a delivery that passes the filter. Use webhooks_list for the webhook name. To change a trigger that exists, use trigger_update.",
			Params: map[string]engine.Param{
				"name":       {Type: "string", Desc: "lowercase identifier, e.g. pr-review", Required: true},
				"webhookRef": {Type: "string", Desc: "the webhook to route from, as shown by webhooks_list", Required: true},
				"task":       {Type: "string", Desc: "the prompt to run when it fires; the delivery's payload is given to the run", Required: true},
				"filter":     {Type: "object", Desc: filterDesc},
				"channelRef": {Type: "string", Desc: "one of your channel names to post output to; empty means your primary channel"},
				"suspend":    {Type: "boolean", Desc: "true creates it paused"},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				name := strings.TrimSpace(argString(args, "name"))
				webhook := strings.TrimSpace(argString(args, "webhookRef"))
				task := argString(args, "task")
				if name == "" || webhook == "" || strings.TrimSpace(task) == "" {
					return "", fmt.Errorf("name, webhookRef and task are required")
				}
				filter, _, err := argFilter(args)
				if err != nil {
					return "", err
				}
				channel := strings.TrimSpace(argString(args, "channelRef"))
				if err := d.checkChannelRef(channel); err != nil {
					return "", err
				}
				t := &agentsv1alpha1.Trigger{
					ObjectMeta: metav1.ObjectMeta{Name: name},
					Spec: agentsv1alpha1.TriggerSpec{
						AgentRef: d.Agent.Name, WebhookRef: webhook, Filter: filter,
						Task: task, ChannelRef: channel, Suspend: argBool(args, "suspend"),
					},
				}
				stampRun(&t.ObjectMeta, d.RunID)
				if err := access.CreateTrigger(ctx, t); err != nil {
					if apierrors.IsAlreadyExists(err) {
						return "", fmt.Errorf("a trigger named %q already exists — use trigger_update to change it", name)
					}
					return "", err
				}
				return fmt.Sprintf("trigger %q created on webhook %q", name, webhook), nil
			},
		},
		{
			Name: "trigger_update",
			Desc: "Change one of your triggers in place: its task, filter, webhook, channel, or pause/resume it. Only the fields you pass change; filter replaces the whole filter (pass {} to fire on every delivery). Use triggers_list first.",
			Params: map[string]engine.Param{
				"name":       {Type: "string", Desc: "the trigger's name, as shown by triggers_list", Required: true},
				"task":       {Type: "string", Desc: "new prompt to run when it fires"},
				"filter":     {Type: "object", Desc: "the complete new filter — " + filterDesc},
				"webhookRef": {Type: "string", Desc: "a different webhook to route from"},
				"channelRef": {Type: "string", Desc: "one of your channel names, or empty for your primary channel"},
				"suspend":    {Type: "boolean", Desc: "true pauses the trigger, false resumes it"},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				filter, hasFilter, err := argFilter(args)
				if err != nil {
					return "", err
				}
				var changed []string
				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					t, err := d.ownTrigger(ctx, access, argString(args, "name"))
					if err != nil {
						return err
					}
					changed = changed[:0]
					if _, ok := args["task"]; ok {
						if strings.TrimSpace(argString(args, "task")) == "" {
							return fmt.Errorf("task cannot be empty — pause the trigger with suspend=true instead")
						}
						t.Spec.Task = argString(args, "task")
						changed = append(changed, "task")
					}
					if hasFilter {
						t.Spec.Filter = filter
						changed = append(changed, "filter="+formatFilter(filter))
					}
					if _, ok := args["webhookRef"]; ok {
						ref := strings.TrimSpace(argString(args, "webhookRef"))
						if ref == "" {
							return fmt.Errorf("webhookRef cannot be empty: a trigger routes from a webhook")
						}
						t.Spec.WebhookRef = ref
						changed = append(changed, "webhookRef="+ref)
					}
					if _, ok := args["channelRef"]; ok {
						ref := strings.TrimSpace(argString(args, "channelRef"))
						if err := d.checkChannelRef(ref); err != nil {
							return err
						}
						t.Spec.ChannelRef = ref
						changed = append(changed, "channelRef="+ref)
					}
					if _, ok := args["suspend"]; ok {
						t.Spec.Suspend = argBool(args, "suspend")
						changed = append(changed, fmt.Sprintf("suspend=%v", t.Spec.Suspend))
					}
					if len(changed) == 0 {
						return nil
					}
					stampRun(&t.ObjectMeta, d.RunID)
					return access.UpdateTrigger(ctx, t)
				})
				if err != nil {
					return "", err
				}
				if len(changed) == 0 {
					return "", fmt.Errorf("nothing to update — pass at least one of task, filter, webhookRef, channelRef, suspend")
				}
				return fmt.Sprintf("trigger %q updated (%s)", strings.TrimSpace(argString(args, "name")), strings.Join(changed, ", ")), nil
			},
		},
		{
			Name: "trigger_delete",
			Desc: "Delete one of your triggers permanently. To stop it only temporarily, prefer trigger_update with suspend=true.",
			Params: map[string]engine.Param{
				"name": {Type: "string", Desc: "the trigger's name, as shown by triggers_list", Required: true},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				t, err := d.ownTrigger(ctx, access, argString(args, "name"))
				if err != nil {
					return "", err
				}
				if err := access.DeleteTrigger(ctx, t.Name); err != nil {
					return "", err
				}
				return fmt.Sprintf("trigger %q deleted", t.Name), nil
			},
		},
	}
}

func formatFilter(f map[string]string) string {
	if len(f) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+f[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
