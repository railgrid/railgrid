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
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	runnerharness "github.com/railgrid/railgrid/pkg/runner/harness"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// Harness identities: the other half of what a ModelCredential can be.
//
// A chat credential is an endpoint plus a bearer, and this package's job is to
// turn it into a chat model. A HARNESS credential is a login that a coding
// harness on somebody else's machine runs as, and this package's job is
// narrower and stricter: say whether the Secret holds a usable one, and hand it
// over as the exact credential kind the runner protocol names — never build
// anything from it, never call anything with it, never log it.
//
// It lives here rather than in backend/harness because two callers need the
// same answer and must not reach different ones: the ModelCredential reconciler,
// which decides SecretResolved from the key shape, and the dispatcher, which
// needs the value. A second implementation would be a second verdict, and the
// one that matters (the reconciler's) is the one a person reads.

// HarnessSelector is the name a person writes to select the harness a provider
// implies: "claude" for claude-code, "codex" for codex. It is the SELECTOR
// half of the two names a harness has — see pkg/runner/harness/names.go, where
// conflating the two has already cost two bugs — and it is what an edges
// runner Service is named after (<edge>-<selector>).
//
// An unrecognised provider returns "", which every caller reports rather than
// guessing a harness for it.
func HarnessSelector(provider string) string {
	switch strings.TrimSpace(provider) {
	case ProviderClaudeCode:
		return runnerharness.SelectorClaude
	case ProviderCodex:
		return runnerharness.SelectorCodex
	default:
		return ""
	}
}

// HarnessAdvertisedName is what the harness a provider implies calls itself in
// a runner's capabilities response, which is what StartRequest.RequiredHarness
// is matched against. Derived from the selector through the one name table, so
// this package never spells "claude-code" itself.
func HarnessAdvertisedName(provider string) string {
	selector := HarnessSelector(provider)
	if selector == "" {
		return ""
	}
	return runnerharness.AdvertisedName(selector)
}

// HarnessIdentity is one resolved harness login: which kind of credential it is,
// and the value itself.
//
// The value is DISPATCH DATA. It is held for the length of one turn, put on one
// StartRequest or ResumeRequest, and never persisted, never logged, and never
// placed in an event or a status field. String() exists so that a struct which
// ends up in a %v by accident cannot leak it.
type HarnessIdentity struct {
	// Kind is the runner-protocol credential kind: claude-oauth, claude-apikey
	// or codex-auth.
	Kind string
	// Value is the credential.
	Value string
}

// String redacts. A harness identity has no useful printable form, and the one
// it would have by default is a live credential.
func (h HarnessIdentity) String() string {
	if h.Kind == "" {
		return "harness identity(none)"
	}
	return "harness identity(" + h.Kind + ", redacted)"
}

// Empty reports whether there is nothing to dispatch with.
func (h HarnessIdentity) Empty() bool { return h.Kind == "" || h.Value == "" }

// HarnessSecretProblem is why a harness credential's Secret is not usable. It
// is a sentence naming the key to fix, not an error, because its one consumer
// writes it into a condition message.
type HarnessSecretProblem string

// ReadHarnessSecret resolves a harness identity from an already-read Secret.
//
// A problem is returned instead of an identity when the Secret's SHAPE is
// wrong, and the shape rules are deliberately strict:
//
//   - claude-code carries EXACTLY ONE of oauthToken or apiKey. Both present is
//     a refusal rather than a precedence decision: the two are injected into the
//     harness differently (CLAUDE_CODE_OAUTH_TOKEN vs ANTHROPIC_API_KEY), so
//     picking one silently would mean the credential a person rotated is not
//     necessarily the one their turns bill against.
//   - codex carries auth.json, and it must parse as JSON. A truncated or
//     half-pasted login file otherwise fails on the host, several minutes and
//     one machine away from the person who pasted it.
func ReadHarnessSecret(provider string, sec *corev1.Secret) (HarnessIdentity, HarnessSecretProblem) {
	read := func(key string) (string, bool) {
		if sec == nil {
			return "", false
		}
		if v, ok := sec.Data[key]; ok {
			value := strings.TrimSpace(string(v))
			return value, value != ""
		}
		if v, ok := sec.StringData[key]; ok {
			value := strings.TrimSpace(v)
			return value, value != ""
		}
		return "", false
	}

	switch strings.TrimSpace(provider) {
	case ProviderClaudeCode:
		oauth, hasOAuth := read(agentsv1alpha1.HarnessSecretKeyOAuthToken)
		apiKey, hasAPIKey := read(agentsv1alpha1.HarnessSecretKeyAPIKey)
		switch {
		case hasOAuth && hasAPIKey:
			return HarnessIdentity{}, HarnessSecretProblem(fmt.Sprintf(
				"secret %q carries both %q and %q; a %s credential is exactly one of them — they are injected into the harness differently, so remove the one this credential is not",
				secretName(sec), agentsv1alpha1.HarnessSecretKeyOAuthToken, agentsv1alpha1.HarnessSecretKeyAPIKey, ProviderClaudeCode))
		case hasOAuth:
			return HarnessIdentity{Kind: string(runnerharness.CredentialClaudeOAuth), Value: oauth}, ""
		case hasAPIKey:
			return HarnessIdentity{Kind: string(runnerharness.CredentialClaudeAPIKey), Value: apiKey}, ""
		default:
			return HarnessIdentity{}, HarnessSecretProblem(fmt.Sprintf(
				"secret %q has neither %q (a `claude setup-token` value) nor %q (an Anthropic API key); a %s credential needs exactly one",
				secretName(sec), agentsv1alpha1.HarnessSecretKeyOAuthToken, agentsv1alpha1.HarnessSecretKeyAPIKey, ProviderClaudeCode))
		}
	case ProviderCodex:
		auth, ok := read(agentsv1alpha1.HarnessSecretKeyCodexAuth)
		if !ok {
			return HarnessIdentity{}, HarnessSecretProblem(fmt.Sprintf(
				"secret %q has no %q key; a %s credential is the Codex login session file saved under that key",
				secretName(sec), agentsv1alpha1.HarnessSecretKeyCodexAuth, ProviderCodex))
		}
		if !json.Valid([]byte(auth)) {
			return HarnessIdentity{}, HarnessSecretProblem(fmt.Sprintf(
				"secret %q key %q is not valid JSON; it must be the Codex login session file verbatim",
				secretName(sec), agentsv1alpha1.HarnessSecretKeyCodexAuth))
		}
		return HarnessIdentity{Kind: string(runnerharness.CredentialCodexAuth), Value: auth}, ""
	default:
		return HarnessIdentity{}, HarnessSecretProblem(fmt.Sprintf("provider %q is not a harness identity", provider))
	}
}

// LoadHarnessIdentity resolves a harness ModelCredential to the identity a turn
// is dispatched with: name → ModelCredential → Secret, read with the identity
// behind c.
func LoadHarnessIdentity(ctx context.Context, c CredentialResolver, name string) (HarnessIdentity, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return HarnessIdentity{}, ErrNotConfigured
	}
	cred, err := c.GetModelCredential(ctx, name)
	if err != nil {
		return HarnessIdentity{}, err
	}
	if !agentsv1alpha1.IsHarnessProvider(cred.Spec.Provider) {
		return HarnessIdentity{}, fmt.Errorf("model credential %q has provider %q, which is a chat endpoint rather than a harness identity", name, cred.Spec.Provider)
	}
	sec, err := c.GetSecret(ctx, SecretNamespace, strings.TrimSpace(cred.Spec.SecretRef.Name))
	if err != nil {
		return HarnessIdentity{}, err
	}
	identity, problem := ReadHarnessSecret(cred.Spec.Provider, sec)
	if problem != "" {
		return HarnessIdentity{}, fmt.Errorf("model credential %q: %s", name, problem)
	}
	return identity, nil
}

func secretName(sec *corev1.Secret) string {
	if sec == nil {
		return ""
	}
	return sec.Name
}

// HarnessModelAliases are the model ids a harness accepts as a shorthand, per
// provider, recorded in ModelCredential.status.models so a portal's model
// picker has something to offer for a credential that can never be probed.
//
// It is a small fixed table rather than a discovery call because there is
// nothing to ask: the harness resolves an alias itself, on the host, and the
// only alternative to a list here is an empty picker and a free-text box. A
// provider not listed gets no aliases, which is the honest answer rather than a
// guess.
var harnessModelAliases = map[string][]string{ //nolint:gochecknoglobals // immutable alias table
	ProviderClaudeCode: {"opus", "sonnet", "haiku"},
	ProviderCodex:      nil,
}

// HarnessModelAliases returns the aliases for a harness provider, or nil.
func HarnessModelAliases(provider string) []string {
	aliases := harnessModelAliases[strings.TrimSpace(provider)]
	if len(aliases) == 0 {
		return nil
	}
	return append([]string(nil), aliases...)
}
