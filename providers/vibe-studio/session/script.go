// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package session

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// TurnContext carries the per-request identity a turn acts with: the
// hub-verified tenant, workspace cluster, and the caller's bearer. Engines
// use it for caller-scoped reads (catalog, secrets) and runtime access; the
// scripted engine ignores it.
type TurnContext struct {
	Tenant    string
	ClusterID string
	Token     string
	SessionID string
	// OnDelta receives streamed assistant text as it is generated (studio
	// turns). Optional; engines call it best-effort, the host renders it as
	// the in-progress reply. Never persisted.
	OnDelta func(string)
	// OnActivity receives each completed tool call. The host persists these
	// as turn.activity events — the durable status trail of what the model
	// is doing.
	OnActivity func(ToolActivityData)
}

// Engine runs one model turn. ScriptedEngine is the deterministic no-LLM
// implementation (dev, tests, unconfigured tenants); the Eino engine drives
// real model turns behind the same interface.
type Engine interface {
	// IntakeTurn proposes a blueprint from the accumulated intent. answers is
	// nil on the first round.
	IntakeTurn(ctx context.Context, tc TurnContext, state SessionState, input string, answers map[string]string) (Blueprint, error)
	// StudioTurn answers a studio chat message.
	StudioTurn(ctx context.Context, tc TurnContext, state SessionState, input string) (string, error)
}

// ScriptedEngine is the deterministic Phase 0 engine. First intake round asks
// one template question; the second round (with answers) converges.
type ScriptedEngine struct {
	// Templates simulates the tenant catalog; blueprint recommendations are
	// restricted to it.
	Templates []string
}

// DefaultScriptedTemplates mirrors the seeded infra catalog subset the wizard
// vocabulary covers today.
var DefaultScriptedTemplates = []string{"application", "simple-webapp", "worker"}

func (s *ScriptedEngine) templates() []string {
	if len(s.Templates) > 0 {
		return s.Templates
	}
	return DefaultScriptedTemplates
}

const templateQuestionID = "template"

// IntakeTurn implements Engine deterministically: no answers yet → propose
// with one question; answers present → converge on the chosen template.
func (s *ScriptedEngine) IntakeTurn(_ context.Context, _ TurnContext, state SessionState, input string, answers map[string]string) (Blueprint, error) {
	templates := s.templates()
	intent := input
	if intent == "" && state.Blueprint != nil {
		intent = state.Blueprint.Summary
	}

	bp := Blueprint{
		Title:   deriveTitle(intent),
		Summary: strings.TrimSpace(intent),
		Values:  map[string]any{},
		Assumptions: []string{
			"single-tenant app, no custom domain yet",
		},
		SuccessCriteria: []string{
			"development preview serves the scaffold",
		},
	}

	if chosen, ok := answers[templateQuestionID]; ok {
		if !slices.Contains(templates, chosen) {
			return Blueprint{}, fmt.Errorf("template %q is not in the catalog %v", chosen, templates)
		}
		bp.Template = BlueprintTemplate{Name: chosen, Reason: "selected in the wizard"}
		return bp, nil
	}

	// First round: recommend the first catalog entry, ask to confirm.
	bp.Template = BlueprintTemplate{Name: templates[0], Reason: "default recommendation (scripted)"}
	opts := make([]QuestionOption, 0, len(templates))
	for i, t := range templates {
		opts = append(opts, QuestionOption{Label: t, Recommended: i == 0})
	}
	bp.Questions = []Question{{
		ID:      templateQuestionID,
		Text:    "Which template should back this app?",
		Options: opts,
	}}
	return bp, nil
}

// StudioTurn implements Engine. It cannot edit code — so instead of pretending
// to work, it says exactly why and how to enable the real engine. (A silent
// echo reads as a broken product; this reads as a missing setting.)
func (s *ScriptedEngine) StudioTurn(_ context.Context, _ TurnContext, _ SessionState, _ string) (string, error) {
	return "**No model is configured for this workspace, so I can't change the code yet.**\n\n" +
		"Add a Secret named `kedge-vibe-studio-llm` (or `kedge-projects-llm`) in the `default` " +
		"namespace of this workspace with keys `provider`, `baseURL`, `model`, and `apiKey`, " +
		"then send your message again. Everything else — the sandbox, preview, and repository — " +
		"is already running.", nil
}

// deriveTitle turns free-form intent into a short title.
func deriveTitle(intent string) string {
	words := strings.Fields(strings.TrimSpace(intent))
	if len(words) == 0 {
		return "New app"
	}
	if len(words) > 6 {
		words = words[:6]
	}
	return strings.Join(words, " ")
}
