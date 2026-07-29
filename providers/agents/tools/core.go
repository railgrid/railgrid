// Copyright 2026 The Faros Authors.
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
	"strings"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/channels"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/store"
)

// Core returns the core family: the agent managing itself — durable memory,
// self-scheduling (cron + one-shot wakeups), proactive notify, asking the
// user a question via the inbox, and (when configured) delegating a scoped
// task to another agent.
func Core(d Deps) []engine.Tool {
	out := coreTools(d)
	if d.Delegate != nil && len(d.Agent.Spec.Delegates) > 0 {
		out = append(out, engine.Tool{
			Name: "delegate",
			Desc: "Delegate a scoped task to another agent and get its answer. Allowed agents: " + strings.Join(d.Agent.Spec.Delegates, ", ") + ". Max 3 delegations per run; delegated agents cannot delegate further.",
			Params: map[string]engine.Param{
				"agent": {Type: "string", Desc: "target agent name", Required: true, Enum: d.Agent.Spec.Delegates},
				"task":  {Type: "string", Desc: "the self-contained task for the target agent", Required: true},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				target, task := argString(args, "agent"), argString(args, "task")
				if target == "" || task == "" {
					return "", fmt.Errorf("agent and task are required")
				}
				return d.Delegate(ctx, target, task)
			},
		})
	}
	return out
}

func coreTools(d Deps) []engine.Tool {
	return []engine.Tool{
		{
			Name: "wait",
			Desc: "Pause for a few seconds before your next step, when an action needs time to take effect before you can verify it. Applies to any slow operation — a physical device finishing its motion (a gate, door, or cover), a service or container restarting, a deployment or provisioning settling, a job/build/query making progress, a state change propagating. Prefer a verify loop: act → wait → re-check (read state, poll status, take a snapshot, list results) → if not yet at the target state, act/wait/re-check again, until confirmed. Max 60 seconds per call.",
			Params: map[string]engine.Param{
				"seconds": {Type: "integer", Desc: "how long to wait, 1–60 seconds", Required: true},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				secs := argInt(args, "seconds")
				if secs <= 0 {
					secs = 5
				}
				if secs > 60 {
					secs = 60
				}
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(time.Duration(secs) * time.Second):
				}
				return fmt.Sprintf("waited %d seconds", secs), nil
			},
		},
		{
			Name: "memory_save",
			Desc: "Save a durable memory note (title + body) you can recall in future conversations and runs.",
			Params: map[string]engine.Param{
				"title": {Type: "string", Desc: "short title", Required: true},
				"body":  {Type: "string", Desc: "the note content (markdown)", Required: true},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				title, body := argString(args, "title"), argString(args, "body")
				if title == "" || body == "" {
					return "", fmt.Errorf("title and body are required")
				}
				now := time.Now().UTC()
				if err := d.Store.PutMemory(ctx, d.Scope, store.Memory{
					ID: uuid.NewString(), AgentName: d.Agent.Name, Title: title, Body: clip(body, 8000),
					CreatedAt: now, UpdatedAt: now,
				}); err != nil {
					return "", err
				}
				return "memory saved: " + title, nil
			},
		},
		{
			Name: "memory_list",
			Desc: "List your saved memory notes (most recently updated first).",
			Exec: func(ctx context.Context, _ string) (string, error) {
				mems, err := d.Store.ListMemories(ctx, d.Scope, 50)
				if err != nil {
					return "", err
				}
				if len(mems) == 0 {
					return "no memories saved yet", nil
				}
				var b strings.Builder
				for _, m := range mems {
					fmt.Fprintf(&b, "## %s\n%s\n\n", m.Title, clip(m.Body, 1500))
				}
				return b.String(), nil
			},
		},
		{
			Name: "schedule_create",
			Desc: "Create a schedule for yourself: a recurring cron task or a one-shot wakeup. Cron is 5-field (minute hour day month weekday).",
			Params: map[string]engine.Param{
				"name":     {Type: "string", Desc: "lowercase identifier, e.g. daily-digest", Required: true},
				"type":     {Type: "string", Desc: "schedule type", Required: true, Enum: []string{"cron", "wakeup"}},
				"schedule": {Type: "string", Desc: "cron expression (cron type), e.g. 0 8 * * *"},
				"timeZone": {Type: "string", Desc: "IANA time zone for the cron, e.g. Europe/Vilnius"},
				"runAt":    {Type: "string", Desc: "RFC3339 time (wakeup type), e.g. 2026-07-14T09:00:00Z"},
				"task":     {Type: "string", Desc: "the prompt to run when it fires", Required: true},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				name, typ, task := argString(args, "name"), argString(args, "type"), argString(args, "task")
				if name == "" || task == "" {
					return "", fmt.Errorf("name and task are required")
				}
				sched := &agentsv1alpha1.Schedule{
					ObjectMeta: metav1.ObjectMeta{Name: name},
					Spec: agentsv1alpha1.ScheduleSpec{
						AgentRef: d.Agent.Name, Type: typ, Task: task,
						Schedule: argString(args, "schedule"), TimeZone: argString(args, "timeZone"),
					},
				}
				switch typ {
				case agentsv1alpha1.ScheduleTypeCron:
					if sched.Spec.Schedule == "" {
						return "", fmt.Errorf("cron type needs a schedule expression")
					}
				case agentsv1alpha1.ScheduleTypeWakeup:
					t, perr := time.Parse(time.RFC3339, argString(args, "runAt"))
					if perr != nil {
						return "", fmt.Errorf("wakeup type needs a valid RFC3339 runAt: %v", perr)
					}
					mt := metav1.NewTime(t)
					sched.Spec.RunAt = &mt
				default:
					return "", fmt.Errorf("type must be cron or wakeup")
				}
				if err := d.CR.CreateSchedule(ctx, sched); err != nil {
					return "", err
				}
				return fmt.Sprintf("schedule %q created (%s)", name, typ), nil
			},
		},
		{
			Name: "schedules_list",
			Desc: "List your existing schedules with their cron expressions and status.",
			Exec: func(ctx context.Context, _ string) (string, error) {
				items, err := d.CR.ListSchedules(ctx)
				if err != nil {
					return "", err
				}
				var b strings.Builder
				for _, s := range items {
					if s.Spec.AgentRef != d.Agent.Name {
						continue
					}
					fmt.Fprintf(&b, "- %s (%s) %s %s suspend=%v next=%v\n",
						s.Name, s.Spec.Type, s.Spec.Schedule, s.Spec.TimeZone, s.Spec.Suspend, s.Status.NextRun)
				}
				if b.Len() == 0 {
					return "no schedules for this agent", nil
				}
				return b.String(), nil
			},
		},
		{
			Name: "notify",
			Desc: "Send a message to the user on their configured notification channel (Telegram/Slack/email). Use for important findings; don't spam.",
			Params: map[string]engine.Param{
				"message": {Type: "string", Desc: "the message to deliver", Required: true},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				msg := argString(args, "message")
				if msg == "" {
					return "", fmt.Errorf("message is required")
				}
				connName, hasChannel := d.Agent.Spec.ResolveChannelConnection("")
				if !hasChannel {
					return "", fmt.Errorf("no notify channel configured on this agent")
				}
				conn, err := d.CR.GetConnection(ctx, connName)
				if err != nil {
					return "", err
				}
				if err := channels.Send(ctx, channels.Message{
					Type: conn.Spec.Type, Token: d.connToken(ctx, connName),
					Target: conn.Spec.Channel, Config: conn.Spec.Config, Text: clip(msg, 3500),
				}); err != nil {
					return "", err
				}
				return "notification sent via " + connName, nil
			},
		},
		{
			Name: "ask",
			Desc: "Ask the user a question. It lands in their inbox (and channel); you will NOT get the answer in this run — finish what you can and mention you asked.",
			Params: map[string]engine.Param{
				"question": {Type: "string", Desc: "the question for the user", Required: true},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				q := argString(args, "question")
				if q == "" {
					return "", fmt.Errorf("question is required")
				}
				now := time.Now().UTC()
				item := store.InboxItem{
					ID: uuid.NewString(), AgentName: d.Agent.Name, Kind: store.InboxKindQuestion,
					State: store.InboxStatePending, Prompt: clip(q, 2000), CreatedAt: now, UpdatedAt: now,
				}
				if err := d.Store.AddInboxItem(ctx, d.Scope, item); err != nil {
					return "", err
				}
				// Best-effort channel delivery so the question reaches the user
				// where they live, not only the portal inbox.
				if connName, ok := d.Agent.Spec.ResolveChannelConnection(""); ok {
					if conn, err := d.CR.GetConnection(ctx, connName); err == nil {
						_ = channels.Send(ctx, channels.Message{
							Type: conn.Spec.Type, Token: d.connToken(ctx, connName),
							Target: conn.Spec.Channel, Config: conn.Spec.Config,
							Text: "❓ " + d.Agent.Name + " asks: " + clip(q, 3000),
						})
					}
				}
				return "question posted to the user's inbox", nil
			},
		},
	}
}
