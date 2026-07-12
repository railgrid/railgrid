// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package llm resolves a tenant's model credentials and builds Eino chat
// models. Credentials live in a Secret (kedge-agents-llm) in the tenant
// workspace, holding one or more named profiles; agents map run purposes
// (chat, background, compaction) to profile names.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"

	corev1 "k8s.io/api/core/v1"
)

// SecretGetter is the minimal tenant-Secret read surface this package needs.
// The agents client satisfies it; keeping the dependency an interface makes
// this whole package provider-agnostic and portable into provider-sdk for
// re-sharing with other providers (e.g. app-studio's own model wiring).
type SecretGetter interface {
	GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error)
}

const (
	// SecretName is the legacy single-credential Secret (kept for back-compat
	// with LoadProfiles). New code uses named credentials — see
	// ModelCredentialPrefix.
	SecretName = "kedge-agents-llm"
	// SecretNamespace is the namespace credential Secrets live in.
	SecretNamespace = "default"

	// ModelCredentialPrefix is the Secret-name prefix for a named model
	// credential: kedge-agents-model-<name>. Each credential is its own Secret
	// so it can be shared/assigned independently, and agents reference one by
	// name (Agent.spec.models["chat"]).
	ModelCredentialPrefix = "kedge-agents-model-"

	// DefaultProfile is used when an agent does not map a purpose to a profile.
	DefaultProfile = "chat"

	ProviderOpenAICompatible = "openai-compatible"
	ProviderGoogle           = "google"
)

// CredentialSecretName returns the Secret name for a named model credential.
func CredentialSecretName(name string) string { return ModelCredentialPrefix + name }

// LoadCredential reads a single named model credential Secret
// (kedge-agents-model-<name>) and returns its profile. The Secret carries flat
// keys: provider, baseURL, model, apiKey.
func LoadCredential(ctx context.Context, c SecretGetter, name string) (Profile, error) {
	sec, err := c.GetSecret(ctx, SecretNamespace, CredentialSecretName(name))
	if err != nil {
		return Profile{}, err
	}
	get := func(k string) string {
		if v, ok := sec.Data[k]; ok {
			return strings.TrimSpace(string(v))
		}
		if v, ok := sec.StringData[k]; ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	p := Profile{Provider: get("provider"), BaseURL: get("baseURL"), Model: get("model"), APIKey: get("apiKey")}
	if p.Model == "" && p.APIKey == "" {
		return Profile{}, ErrNotConfigured
	}
	return p.normalized(), nil
}

// ErrNotConfigured means the tenant has no usable model credentials.
var ErrNotConfigured = errors.New("no model credentials configured — set up the agents LLM Secret for this workspace")

// Profile is one named set of model credentials.
type Profile struct {
	Provider string `json:"provider,omitempty"`
	BaseURL  string `json:"baseURL,omitempty"`
	Model    string `json:"model,omitempty"`
	APIKey   string `json:"apiKey,omitempty"`
}

func (p Profile) normalized() Profile {
	p.Provider = strings.TrimSpace(p.Provider)
	if p.Provider == "" {
		p.Provider = ProviderOpenAICompatible
	}
	p.BaseURL = strings.TrimSpace(p.BaseURL)
	if p.BaseURL == "" && p.Provider == ProviderOpenAICompatible {
		p.BaseURL = "https://api.openai.com/v1"
	}
	p.Model = strings.TrimSpace(p.Model)
	p.APIKey = strings.TrimSpace(p.APIKey)
	return p
}

// Profiles is a set of named profiles plus the raw single-profile fallback.
type Profiles map[string]Profile

// Resolve returns the profile for a purpose, falling back to the default
// profile and then to any single configured profile.
func (ps Profiles) Resolve(purpose string) (Profile, bool) {
	if purpose != "" {
		if p, ok := ps[purpose]; ok {
			return p.normalized(), true
		}
	}
	if p, ok := ps[DefaultProfile]; ok {
		return p.normalized(), true
	}
	// Single-profile fallback: if exactly one profile exists, use it.
	if len(ps) == 1 {
		for _, p := range ps {
			return p.normalized(), true
		}
	}
	return Profile{}, false
}

// LoadProfiles reads and parses the tenant's model-credentials Secret. Two
// shapes are accepted: a "profiles" key holding a JSON object of named
// profiles, or flat keys (provider/baseURL/model/apiKey) describing one default
// profile — the app-studio-compatible single-credential form.
func LoadProfiles(ctx context.Context, c SecretGetter) (Profiles, error) {
	sec, err := c.GetSecret(ctx, SecretNamespace, SecretName)
	if err != nil {
		return nil, err
	}
	return parseProfiles(sec)
}

func parseProfiles(sec *corev1.Secret) (Profiles, error) {
	get := func(key string) string {
		if v, ok := sec.Data[key]; ok {
			return strings.TrimSpace(string(v))
		}
		if v, ok := sec.StringData[key]; ok {
			return strings.TrimSpace(v)
		}
		return ""
	}

	if raw := get("profiles"); raw != "" {
		var out Profiles
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, fmt.Errorf("parsing profiles JSON in %s Secret: %w", SecretName, err)
		}
		if len(out) == 0 {
			return nil, ErrNotConfigured
		}
		return out, nil
	}

	// Flat single-profile fallback.
	p := Profile{
		Provider: get("provider"),
		BaseURL:  get("baseURL"),
		Model:    get("model"),
		APIKey:   get("apiKey"),
	}
	if p.Model == "" && p.APIKey == "" {
		return nil, ErrNotConfigured
	}
	return Profiles{DefaultProfile: p}, nil
}

// BuildModel constructs an Eino chat model from a profile. Only the
// OpenAI-compatible provider is supported in this milestone; Gemini and other
// providers are added later.
func BuildModel(ctx context.Context, p Profile) (einomodel.BaseChatModel, error) {
	p = p.normalized()
	if p.APIKey == "" {
		return nil, ErrNotConfigured
	}
	if p.Model == "" {
		return nil, fmt.Errorf("profile is missing a model name")
	}
	switch p.Provider {
	case ProviderOpenAICompatible, "openai", "":
		cfg := &openaimodel.ChatModelConfig{
			APIKey:     p.APIKey,
			BaseURL:    strings.TrimRight(p.BaseURL, "/"),
			Model:      p.Model,
			HTTPClient: &http.Client{},
		}
		if modelSupportsTemperature(p.Model) {
			t := float32(0.2)
			cfg.Temperature = &t
		}
		m, err := openaimodel.NewChatModel(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("create OpenAI-compatible chat model: %w", err)
		}
		return m, nil
	default:
		return nil, fmt.Errorf("provider %q is not supported yet (use %q)", p.Provider, ProviderOpenAICompatible)
	}
}

// modelSupportsTemperature reports whether the model accepts a custom sampling
// temperature. OpenAI's GPT-5 family and o-series reasoning models fix it.
func modelSupportsTemperature(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true
	}
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	switch {
	case strings.HasPrefix(m, "gpt-5"), strings.HasPrefix(m, "gpt5"):
		return false
	case strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"):
		return false
	}
	return true
}
