// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/client"
)

// Per-tenant LLM settings: one Secret in the tenant workspace, read as the
// caller. Keys: provider | baseURL | model | apiKey.
//
// Two names are tried in order: vibe-studio's own, then app-studio's
// (faros-projects-llm) — vibe-studio replaces app-studio, so a workspace
// already configured for it works without re-entering the credential.
const llmSecretNamespace = "default"

var llmSecretNames = []string{"faros-vibe-studio-llm", "faros-projects-llm"}

// errLLMNotConfigured routes the turn to the scripted fallback engine.
var errLLMNotConfigured = errors.New("llm is not configured for this workspace")

type llmProfile struct {
	Provider string
	BaseURL  string
	Model    string
	APIKey   string
}

// resolveLLMProfile picks the model for a turn, in order:
//
//  1. the session's spec.modelRef (the per-project choice)
//  2. the Model annotated as the workspace default
//  3. the only Model, when exactly one is configured
//  4. the legacy single-Secret scheme (faros-vibe-studio-llm / -projects-llm)
//
// Returns the profile and the Model CR name it came from ("" for legacy).
func resolveLLMProfile(ctx context.Context, cl *client.Client, sessionID string) (llmProfile, string, error) {
	models, err := cl.ListModels(ctx)
	if err != nil {
		models = nil // Models CRD not bound yet — fall through to legacy.
	}
	byName := map[string]*vibev1alpha1.Model{}
	var chosen *vibev1alpha1.Model
	for i := range models {
		byName[models[i].Name] = &models[i]
		if models[i].Annotations[vibev1alpha1.ModelDefaultAnnotation] == "true" && chosen == nil {
			chosen = &models[i]
		}
	}
	if sessionID != "" {
		if sess, err := cl.GetSession(ctx, sessionID); err == nil && sess.Spec.ModelRef != nil {
			if m, ok := byName[sess.Spec.ModelRef.Name]; ok {
				chosen = m
			}
		}
	}
	if chosen == nil && len(models) == 1 {
		chosen = &models[0]
	}
	if chosen != nil {
		p, err := profileFromModel(ctx, cl, chosen)
		if err == nil {
			return p, chosen.Name, nil
		}
	}
	p, err := loadLLMProfile(ctx, cl)
	return p, "", err
}

// profileFromModel reads a Model CR's key Secret into a usable profile.
func profileFromModel(ctx context.Context, cl *client.Client, m *vibev1alpha1.Model) (llmProfile, error) {
	ns := m.Spec.SecretRef.Namespace
	if ns == "" {
		ns = llmSecretNamespace
	}
	key := m.Spec.SecretRef.Key
	if key == "" {
		key = "apiKey"
	}
	sec, err := cl.GetSecret(ctx, ns, m.Spec.SecretRef.Name)
	if err != nil {
		return llmProfile{}, errLLMNotConfigured
	}
	data, _, _ := unstructuredNestedStringMap(sec.Object, "data")
	raw, err := base64.StdEncoding.DecodeString(data[key])
	if err != nil || len(raw) == 0 {
		return llmProfile{}, errLLMNotConfigured
	}
	p := llmProfile{
		Provider: firstNonEmpty(m.Spec.Provider, "openai-compatible"),
		BaseURL:  firstNonEmpty(m.Spec.BaseURL, "https://api.openai.com/v1"),
		Model:    m.Spec.Model,
		APIKey:   string(raw),
	}
	if p.Model == "" {
		return llmProfile{}, errLLMNotConfigured
	}
	return p, nil
}

// loadLLMProfile reads the tenant's legacy single LLM Secret. No secret, or
// empty model+apiKey → errLLMNotConfigured (the caller falls back to the
// scripted engine).
func loadLLMProfile(ctx context.Context, cl *client.Client) (llmProfile, error) {
	var data map[string]string
	for _, name := range llmSecretNames {
		sec, err := cl.GetSecret(ctx, llmSecretNamespace, name)
		if err != nil {
			continue
		}
		if d, ok, _ := unstructuredNestedStringMap(sec.Object, "data"); ok {
			data = d
			break
		}
	}
	if data == nil {
		return llmProfile{}, errLLMNotConfigured
	}
	get := func(k string) string {
		raw, err := base64.StdEncoding.DecodeString(data[k])
		if err != nil {
			return ""
		}
		return string(raw)
	}
	p := llmProfile{
		Provider: get("provider"),
		BaseURL:  get("baseURL"),
		Model:    get("model"),
		APIKey:   get("apiKey"),
	}
	if p.Model == "" && p.APIKey == "" {
		return llmProfile{}, errLLMNotConfigured
	}
	if p.Provider == "" {
		p.Provider = "openai-compatible"
	}
	if p.BaseURL == "" {
		p.BaseURL = "https://api.openai.com/v1"
	}
	if p.Model == "" {
		p.Model = "gpt-5.4"
	}
	return p, nil
}

// buildChatModel constructs the Eino chat model for a profile. Only
// openai-compatible endpoints are supported (Anthropic/OpenAI/proxies).
func buildChatModel(ctx context.Context, p llmProfile) (einomodel.BaseChatModel, error) {
	switch p.Provider {
	case "openai-compatible", "openai", "":
		return openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
			APIKey:  p.APIKey,
			BaseURL: p.BaseURL,
			Model:   p.Model,
		})
	default:
		return nil, fmt.Errorf("unsupported llm provider %q (use openai-compatible)", p.Provider)
	}
}

// unstructuredNestedStringMap is a tiny helper over unstructured content.
func unstructuredNestedStringMap(obj map[string]any, fields ...string) (map[string]string, bool, error) {
	cur := any(obj)
	for _, f := range fields {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		cur = m[f]
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, false, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, true, nil
}
