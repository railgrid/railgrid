// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package modelcredential

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
)

// harnessCredential is the same object shape as credential() with a harness
// provider and, deliberately, NO baseURL: a harness identity has no endpoint,
// and the CRD's CEL rule only requires one for a chat provider.
func harnessCredential(provider string) *agentsv1alpha1.ModelCredential {
	cred := credential()
	cred.Spec.Provider = provider
	cred.Spec.BaseURL = ""
	cred.Spec.Model = ""
	return cred
}

// refuseProbe fails the test if it is ever called. A harness identity has no
// endpoint: GET /models against one would be a call to nowhere with a credential
// no chat API would accept.
func refuseProbe(t *testing.T) Prober {
	t.Helper()
	return func(_ context.Context, baseURL, _ string) ([]string, time.Duration, error) {
		t.Errorf("the reconciler probed %q for a harness credential; harness identities have no endpoint", baseURL)
		return nil, 0, nil
	}
}

func TestHarnessCredentialIsReadyWithoutAProbe(t *testing.T) {
	for _, tc := range []struct {
		name       string
		provider   string
		data       map[string][]byte
		wantModels []string
	}{{
		name:       "claude-code with a setup token",
		provider:   agentsv1alpha1.ModelProviderClaudeCode,
		data:       map[string][]byte{"oauthToken": []byte("sk-ant-oat-abc")},
		wantModels: []string{"opus", "sonnet", "haiku"},
	}, {
		name:       "claude-code with an API key",
		provider:   agentsv1alpha1.ModelProviderClaudeCode,
		data:       map[string][]byte{"apiKey": []byte("sk-ant-api-abc")},
		wantModels: []string{"opus", "sonnet", "haiku"},
	}, {
		// Codex publishes no alias table, so status.models is left EMPTY rather
		// than filled with a guess.
		name:     "codex with a login session",
		provider: agentsv1alpha1.ModelProviderCodex,
		data:     map[string][]byte{"auth.json": []byte(`{"tokens":{"access_token":"a"}}`)},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			r, c := newReconciler(t, refuseProbe(t), harnessCredential(tc.provider), secret(true, tc.data))
			reconcileOnce(t, r)
			got := read(t, c)

			if cond := condition(t, got, agentsv1alpha1.ConditionSecretResolved); cond.Status != metav1.ConditionTrue {
				t.Fatalf("SecretResolved = %s (%s): %s", cond.Status, cond.Reason, cond.Message)
			}
			// Reachable is True and says WHY it did not call anything, so Ready
			// stays the one conjunction every reader consults.
			reachable := condition(t, got, agentsv1alpha1.ConditionReachable)
			if reachable.Status != metav1.ConditionTrue {
				t.Fatalf("Reachable = %s (%s): %s", reachable.Status, reachable.Reason, reachable.Message)
			}
			if !strings.Contains(reachable.Message, "no endpoint") {
				t.Fatalf("Reachable does not explain that there is nothing to call: %q", reachable.Message)
			}
			if cond := condition(t, got, agentsv1alpha1.ConditionReady); cond.Status != metav1.ConditionTrue {
				t.Fatalf("Ready = %s (%s): %s", cond.Status, cond.Reason, cond.Message)
			}
			if !slices.Equal(got.Status.Models, tc.wantModels) {
				t.Fatalf("status.models = %v, want %v", got.Status.Models, tc.wantModels)
			}
			// Nothing was probed, so nothing may claim a probe happened.
			if got.Status.LastProbeTime != nil {
				t.Fatalf("status.lastProbeTime was stamped for a credential that was never probed")
			}
		})
	}
}

// The refusal the task calls out: both keys present is a REFUSAL rather than a
// precedence decision, and it lands on the object as SecretResolved=False.
func TestClaudeCodeCredentialWithBothKeysIsNotReady(t *testing.T) {
	r, c := newReconciler(t, refuseProbe(t),
		harnessCredential(agentsv1alpha1.ModelProviderClaudeCode),
		secret(true, map[string][]byte{
			"oauthToken": []byte("sk-ant-oat-abc"),
			"apiKey":     []byte("sk-ant-api-abc"),
		}))
	reconcileOnce(t, r)
	got := read(t, c)

	resolved := condition(t, got, agentsv1alpha1.ConditionSecretResolved)
	if resolved.Status != metav1.ConditionFalse || resolved.Reason != agentsv1alpha1.ReasonSecretIncomplete {
		t.Fatalf("SecretResolved = %s (%s), want False/%s", resolved.Status, resolved.Reason, agentsv1alpha1.ReasonSecretIncomplete)
	}
	if !strings.Contains(resolved.Message, "exactly one") {
		t.Fatalf("the message does not say what to do about it: %q", resolved.Message)
	}
	if cond := condition(t, got, agentsv1alpha1.ConditionReady); cond.Status != metav1.ConditionFalse {
		t.Fatalf("Ready = %s, want False: a credential with two conflicting keys is not usable", cond.Status)
	}
}

// A Codex Secret must hold a JSON object. Every other top-level JSON shape is
// refused through SecretResolved without copying credential bytes into status.
func TestCodexCredentialWithNonObjectSessionIsNotReady(t *testing.T) {
	for _, tc := range []struct {
		name  string
		auth  string
		leaks string
	}{{
		name: "null",
		auth: `null`,
	}, {
		name: "array",
		auth: `[]`,
	}, {
		name:  "string",
		auth:  `"session-secret-marker"`,
		leaks: "session-secret-marker",
	}, {
		name: "number",
		auth: `42`,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			r, c := newReconciler(t, refuseProbe(t),
				harnessCredential(agentsv1alpha1.ModelProviderCodex),
				secret(true, map[string][]byte{"auth.json": []byte(tc.auth)}))
			reconcileOnce(t, r)
			got := read(t, c)

			resolved := condition(t, got, agentsv1alpha1.ConditionSecretResolved)
			if resolved.Status != metav1.ConditionFalse || resolved.Reason != agentsv1alpha1.ReasonSecretIncomplete {
				t.Fatalf("SecretResolved = %s (%s), want False/%s", resolved.Status, resolved.Reason, agentsv1alpha1.ReasonSecretIncomplete)
			}
			if !strings.Contains(resolved.Message, "JSON object") {
				t.Fatalf("the message does not describe the required auth.json shape: %q", resolved.Message)
			}
			if tc.leaks != "" && strings.Contains(resolved.Message, tc.leaks) {
				t.Fatalf("the status message leaked auth.json contents: %q", resolved.Message)
			}
			if cond := condition(t, got, agentsv1alpha1.ConditionReady); cond.Status != metav1.ConditionFalse {
				t.Fatalf("Ready = %s, want False for a non-object session", cond.Status)
			}
		})
	}
}

// Malformed JSON is refused as incomplete too, and the diagnostic names the
// key rather than echoing the supplied bytes.
func TestCodexCredentialWithUnparseableSessionIsNotReady(t *testing.T) {
	r, c := newReconciler(t, refuseProbe(t),
		harnessCredential(agentsv1alpha1.ModelProviderCodex),
		secret(true, map[string][]byte{"auth.json": []byte(`{"tokens":`)}))
	reconcileOnce(t, r)
	got := read(t, c)

	resolved := condition(t, got, agentsv1alpha1.ConditionSecretResolved)
	if resolved.Status != metav1.ConditionFalse || resolved.Reason != agentsv1alpha1.ReasonSecretIncomplete {
		t.Fatalf("SecretResolved = %s (%s), want False/%s", resolved.Status, resolved.Reason, agentsv1alpha1.ReasonSecretIncomplete)
	}
	if !strings.Contains(resolved.Message, llm.ProviderCodex) && !strings.Contains(resolved.Message, "auth.json") {
		t.Fatalf("the message does not name the key to fix: %q", resolved.Message)
	}
	if cond := condition(t, got, agentsv1alpha1.ConditionReady); cond.Status == metav1.ConditionTrue {
		t.Fatal("a credential with an unparseable session file must not be Ready")
	}
}
