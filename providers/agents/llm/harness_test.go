// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

func secretWith(data map[string]string) *corev1.Secret {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "railgrid-agents-model-harness"}, Data: map[string][]byte{}}
	for k, v := range data {
		sec.Data[k] = []byte(v)
	}
	return sec
}

func TestReadHarnessSecretShapes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		data     map[string]string
		wantKind string
		// wantProblem is a substring the refusal must name, so the test asserts
		// the reader is told WHICH key to fix rather than merely that something
		// is wrong.
		wantProblem string
	}{{
		name:     "claude-code with a setup token",
		provider: ProviderClaudeCode,
		data:     map[string]string{"oauthToken": "sk-ant-oat-abc"},
		wantKind: "claude-oauth",
	}, {
		name:     "claude-code with an API key",
		provider: ProviderClaudeCode,
		data:     map[string]string{"apiKey": "sk-ant-api-abc"},
		wantKind: "claude-apikey",
	}, {
		// The one the task calls out: both present is a REFUSAL, not a
		// precedence decision. The two are injected into the harness
		// differently, so silently picking one would mean the credential a
		// person rotated is not necessarily the one their turns bill against.
		name:        "claude-code with both keys is refused, not resolved by precedence",
		provider:    ProviderClaudeCode,
		data:        map[string]string{"oauthToken": "sk-ant-oat-abc", "apiKey": "sk-ant-api-abc"},
		wantProblem: "exactly one",
	}, {
		name:        "claude-code with neither key",
		provider:    ProviderClaudeCode,
		data:        map[string]string{"token": "nope"},
		wantProblem: "oauthToken",
	}, {
		name:     "codex with a login session",
		provider: ProviderCodex,
		data:     map[string]string{"auth.json": `{"tokens":{"access_token":"a"}}`},
		wantKind: "codex-auth",
	}, {
		name:        "codex with an unparseable session file",
		provider:    ProviderCodex,
		data:        map[string]string{"auth.json": `{"tokens":`},
		wantProblem: "JSON object",
	}, {
		name:        "codex with null instead of an object",
		provider:    ProviderCodex,
		data:        map[string]string{"auth.json": `null`},
		wantProblem: "JSON object",
	}, {
		name:        "codex with an array instead of an object",
		provider:    ProviderCodex,
		data:        map[string]string{"auth.json": `[]`},
		wantProblem: "JSON object",
	}, {
		name:        "codex with a string instead of an object",
		provider:    ProviderCodex,
		data:        map[string]string{"auth.json": `"session-secret-marker"`},
		wantProblem: "JSON object",
	}, {
		name:        "codex with a number instead of an object",
		provider:    ProviderCodex,
		data:        map[string]string{"auth.json": `42`},
		wantProblem: "JSON object",
	}, {
		name:        "codex with no session file",
		provider:    ProviderCodex,
		data:        map[string]string{"apiKey": "sk-abc"},
		wantProblem: "auth.json",
	}, {
		name:        "a chat endpoint is not a harness identity",
		provider:    ProviderOpenAI,
		data:        map[string]string{"apiKey": "sk-abc"},
		wantProblem: "not a harness identity",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			identity, problem := ReadHarnessSecret(tc.provider, secretWith(tc.data))
			if tc.wantProblem != "" {
				if problem == "" {
					t.Fatalf("expected a refusal, got identity kind %q", identity.Kind)
				}
				if !strings.Contains(string(problem), tc.wantProblem) {
					t.Fatalf("refusal %q does not name %q", problem, tc.wantProblem)
				}
				if strings.Contains(string(problem), "session-secret-marker") {
					t.Fatalf("refusal leaked auth.json contents: %q", problem)
				}
				if !identity.Empty() {
					t.Fatal("a refused secret must yield no identity")
				}
				return
			}
			if problem != "" {
				t.Fatalf("unexpected refusal: %s", problem)
			}
			if identity.Kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", identity.Kind, tc.wantKind)
			}
			if identity.Value == "" {
				t.Fatal("the identity carries no value")
			}
		})
	}
}

// A harness identity has no printable form that is not a live credential.
func TestHarnessIdentityStringRedacts(t *testing.T) {
	identity := HarnessIdentity{Kind: "claude-oauth", Value: "sk-ant-oat-secret"}
	if strings.Contains(identity.String(), "secret") {
		t.Fatalf("String() leaked the credential: %s", identity.String())
	}
}

// BuildModel must refuse a harness identity by NAME, before it gets as far as
// the key or the model id: a `claude setup-token` value posted to
// /chat/completions comes back as an upstream 401, and the reader then looks for
// a rotated key instead of a mis-pointed agent.
func TestBuildModelRefusesHarnessProviders(t *testing.T) {
	for _, provider := range []string{ProviderClaudeCode, ProviderCodex} {
		t.Run(provider, func(t *testing.T) {
			// Deliberately complete in every other respect, so a refusal cannot
			// be the empty-key or empty-model check firing instead.
			_, err := BuildModel(context.Background(), Profile{
				Provider: provider, BaseURL: "https://api.anthropic.com/v1",
				Model: "sonnet", APIKey: "sk-ant-oat-abc",
			})
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !errors.Is(err, ErrHarnessCredential) {
				t.Fatalf("error %v does not match ErrHarnessCredential", err)
			}
			if !strings.Contains(err.Error(), "spec.backend.harness.credentialRef") {
				t.Fatalf("the refusal does not say where to point the agent instead: %v", err)
			}
		})
	}
}

// The two names of a harness come from the one table in pkg/runner/harness, and
// they differ only for Claude Code — which is exactly why assuming they are
// equal has cost two bugs.
func TestHarnessNamesComeFromTheOneTable(t *testing.T) {
	if got := HarnessSelector(ProviderClaudeCode); got != "claude" {
		t.Fatalf("claude-code selector = %q, want claude", got)
	}
	if got := HarnessAdvertisedName(ProviderClaudeCode); got != "claude-code" {
		t.Fatalf("claude-code advertised name = %q, want claude-code", got)
	}
	if got := HarnessAdvertisedName(ProviderCodex); got != "codex" {
		t.Fatalf("codex advertised name = %q, want codex", got)
	}
	// A chat endpoint implies no harness at all, and says so with "" rather than
	// guessing one.
	if got := HarnessSelector(ProviderOpenAI); got != "" {
		t.Fatalf("openai selector = %q, want empty", got)
	}
	if !agentsv1alpha1.IsHarnessProvider(ProviderCodex) || agentsv1alpha1.IsHarnessProvider(ProviderOpenAICompatible) {
		t.Fatal("IsHarnessProvider disagrees with the provider constants")
	}
}
