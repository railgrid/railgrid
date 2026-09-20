// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
)

// credResolver is the pair a credential resolves through: the object, and the
// Secret its spec.secretRef names. It records what was asked for, so a test
// can prove the resolution followed the OBJECT rather than a name convention.
type credResolver struct {
	cred      *agentsv1alpha1.ModelCredential
	secrets   map[string]*corev1.Secret
	requested []string
}

func (r *credResolver) GetModelCredential(_ context.Context, name string) (*agentsv1alpha1.ModelCredential, error) {
	r.requested = append(r.requested, "modelcredentials/"+name)
	if r.cred == nil || r.cred.Name != name {
		return nil, fmt.Errorf("model credential %q not found", name)
	}
	return r.cred, nil
}

func (r *credResolver) GetSecret(_ context.Context, namespace, name string) (*corev1.Secret, error) {
	r.requested = append(r.requested, namespace+"/"+name)
	if sec, ok := r.secrets[name]; ok {
		return sec, nil
	}
	return nil, fmt.Errorf("secret %q not found", name)
}

// A credential's key comes from the Secret the OBJECT points at, under the key
// the object names — not from a name convention and not from the object
// itself. The old model stored the endpoint configuration in the Secret's own
// keys under a fixed railgrid-agents-model-<name>, which is exactly what made
// it unvalidatable; this asserts the indirection that replaced it.
func TestLoadCredentialFollowsSecretRef(t *testing.T) {
	for _, tt := range []struct {
		name       string
		secretName string
		secretKey  string
		stored     map[string][]byte
		wantKey    string
		wantErr    bool
	}{
		{
			name:       "default key",
			secretName: "team-openai-key",
			stored:     map[string][]byte{"apiKey": []byte("stored-key")},
			wantKey:    "stored-key",
		},
		{
			name:       "explicit key",
			secretName: "team-openai-key",
			secretKey:  "token",
			stored:     map[string][]byte{"token": []byte("other-key"), "apiKey": []byte("wrong")},
			wantKey:    "other-key",
		},
		{
			name:       "key absent",
			secretName: "team-openai-key",
			stored:     map[string][]byte{"unrelated": []byte("x")},
			wantErr:    true,
		},
		{
			name:       "secret missing",
			secretName: "not-written-yet",
			wantErr:    true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &credResolver{
				cred: &agentsv1alpha1.ModelCredential{
					ObjectMeta: metav1.ObjectMeta{Name: "main"},
					Spec: agentsv1alpha1.ModelCredentialSpec{
						Provider:  llm.ProviderOpenAICompatible,
						BaseURL:   "https://api.openai.com/v1",
						Model:     "gpt-4o",
						SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: tt.secretName},
						SecretKey: tt.secretKey,
					},
				},
				secrets: map[string]*corev1.Secret{},
			}
			if tt.stored != nil {
				r.secrets["team-openai-key"] = &corev1.Secret{Data: tt.stored}
			}
			got, err := llm.LoadCredential(t.Context(), r, "main")
			if (err != nil) != tt.wantErr {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr {
				return
			}
			if got.APIKey != tt.wantKey {
				t.Fatalf("api key = %q, want %q", got.APIKey, tt.wantKey)
			}
			if got.Model != "gpt-4o" || got.BaseURL != "https://api.openai.com/v1" {
				t.Fatalf("endpoint came from somewhere other than the object: %#v", got)
			}
			// The Secret is read in namespace default, under the name the
			// OBJECT gave, which is the whole point of the indirection.
			if want := "default/" + tt.secretName; !strings.Contains(strings.Join(r.requested, " "), want) {
				t.Fatalf("resolution did not read %s: %v", want, r.requested)
			}
		})
	}
}

// An unknown credential name is an error, not an empty profile that fails
// later inside the model builder.
func TestLoadCredentialUnknownName(t *testing.T) {
	r := &credResolver{secrets: map[string]*corev1.Secret{}}
	if _, err := llm.LoadCredential(t.Context(), r, "nope"); err == nil {
		t.Fatal("expected an error for an unknown credential")
	}
}

func TestVerifyCredentialModelTestsChatNotCatalog(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer private-key" {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				var body struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "gpt-4o" {
					t.Errorf("wrong model: %s", body.Model)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
				} else {
					_, _ = w.Write([]byte(`{"error":{"message":"private-key rejected"}}`))
				}
			}))
			defer upstream.Close()
			result := verifyCredentialModel(t.Context(), llm.Profile{Model: "gpt-4o", BaseURL: upstream.URL, APIKey: "private-key"})
			if result.OK != (status == http.StatusOK) || calls != 1 {
				t.Fatalf("unexpected result %#v, %d calls", result, calls)
			}
			if strings.Contains(result.Error, "private-key") {
				t.Fatal("error exposed credential")
			}
		})
	}
}

// A probe failure is recorded bounded and without the body verbatim: an
// upstream can answer with megabytes, and it can echo the request headers the
// key travelled in.
func TestBoundedProbeError(t *testing.T) {
	long := &llm.ProbeError{Status: 500, Msg: strings.Repeat("x", 4000)}
	got := boundedProbeError(long)
	if len(got) != maxProbeErrorLength {
		t.Fatalf("bounded length = %d, want %d", len(got), maxProbeErrorLength)
	}
	if boundedProbeError(nil) != "" {
		t.Fatal("nil error must render empty")
	}
}

// The `test` verb's optional body is what lets the editor verify the model a
// person just picked BEFORE it is saved. Previously the only thing that ever
// exercised a pick was the first agent run, which is where gpt-5.3-codex's
// 404 surfaced — in a chat window, long after the choice.
func TestTestModelOverride(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		want     string
		wantCode int
		wantMsg  string
	}{
		{name: "no body at all probes the saved model", body: ""},
		{name: "empty object probes the saved model", body: `{}`},
		{name: "blank model probes the saved model", body: `{"model":"   "}`},
		{name: "a chat model overrides", body: `{"model":"gpt-4o-mini"}`, want: "gpt-4o-mini"},
		{name: "a chat model is trimmed", body: `{"model":"  gpt-4o  "}`, want: "gpt-4o"},
		{
			name: "a responses-only model is refused by name",
			body: `{"model":"gpt-5.3-codex"}`, wantCode: http.StatusBadRequest,
			wantMsg: "not usable for chat",
		},
		{
			name: "an embedding model is refused",
			body: `{"model":"text-embedding-3-small"}`, wantCode: http.StatusBadRequest,
			wantMsg: "not usable for chat",
		},
		{
			name:     "a model id longer than the field allows is refused",
			body:     `{"model":"` + strings.Repeat("a", maxTestModelID+1) + `"}`,
			wantCode: http.StatusBadRequest, wantMsg: "too long",
		},
		{
			name:     "an oversized body is refused before it is parsed",
			body:     `{"model":"` + strings.Repeat("a", maxTestRequestBody) + `"}`,
			wantCode: http.StatusBadRequest, wantMsg: "too large",
		},
		{
			name: "a body that is not JSON is refused",
			body: `model=gpt-4o`, wantCode: http.StatusBadRequest, wantMsg: "not valid JSON",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/modelcredentials/main/test", strings.NewReader(tc.body))
			got, ok := testModelOverride(w, r)
			if tc.wantCode != 0 {
				if ok {
					t.Fatalf("expected the request to be refused, got override %q", got)
				}
				if w.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
				}
				// Both shapes carry the sentence: `error` for a caller reading
				// the verb's result, `message` for the portal's error reader.
				var body rejectedTest
				if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.OK {
					t.Fatal("a refused probe must report ok=false")
				}
				if !strings.Contains(body.Error, tc.wantMsg) || !strings.Contains(body.Message, tc.wantMsg) {
					t.Fatalf("message %q / %q does not say %q", body.Error, body.Message, tc.wantMsg)
				}
				return
			}
			if !ok {
				t.Fatalf("request refused unexpectedly: %s", w.Body.String())
			}
			if got != tc.want {
				t.Fatalf("override = %q, want %q", got, tc.want)
			}
		})
	}
}
