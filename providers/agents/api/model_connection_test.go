// Copyright 2026 The Faros Authors.
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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroshq/provider-agents/llm"
	corev1 "k8s.io/api/core/v1"
)

type modelTestSecrets struct{ requested []string }

func (s *modelTestSecrets) GetSecret(_ context.Context, namespace, name string) (*corev1.Secret, error) {
	s.requested = append(s.requested, namespace+"/"+name)
	return &corev1.Secret{Data: map[string][]byte{"provider": []byte("openai-compatible"), "baseURL": []byte("https://api.openai.com/v1"), "apiKey": []byte("stored-key"), "model": []byte("gpt-4o")}}, nil
}
func TestResolveCredentialDraftPreservesEndpointAuthority(t *testing.T) {
	for _, tt := range []struct {
		name, endpoint, key string
		wantError           bool
	}{
		{"same endpoint", "https://api.openai.com/v1/", "", false},
		{"changed endpoint", "https://other.example/v1", "", true},
		{"explicit new key", "https://other.example/v1", "replacement", false},
		{"invalid endpoint", "file:///etc/passwd", "replacement", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			secrets := &modelTestSecrets{}
			got, err := resolveCredentialDraft(t.Context(), secrets, credentialDraft{ExistingName: "main", Profile: llm.Profile{BaseURL: tt.endpoint, Model: "changed-model", APIKey: tt.key}})
			if (err != nil) != tt.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if err == nil && got.Model != "changed-model" {
				t.Fatalf("draft model was not preserved: %#v", got)
			}
			if err == nil && tt.key == "" && got.APIKey != "stored-key" {
				t.Fatal("stored key was not reused")
			}
			if len(secrets.requested) > 0 && secrets.requested[0] != "default/faros-agents-model-main" {
				t.Fatalf("wrong secret: %v", secrets.requested)
			}
		})
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
