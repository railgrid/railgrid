// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package llm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestResponsesOriginNeedsProviderSecretForOfflineKeyVerification(t *testing.T) {
	profile := Profile{
		BaseURL: "https://gateway.example/v1", Model: "gpt-6-luna", APIKey: "weak-gateway-key",
		ResponsesOriginKey: []byte("provider-held-signing-secret"),
	}
	model, err := NewResponsesModel(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	origin := model.(interface{ ResponsesOrigin() string }).ResponsesOrigin()
	mac := hmac.New(sha256.New, profile.ResponsesOriginKey)
	_, _ = mac.Write([]byte(strings.Join([]string{
		"railgrid-agents-responses-origin:v1", profile.BaseURL, profile.Model, profile.APIKey,
	}, "\x00")))
	if want := fmt.Sprintf("hmac-sha256:v1:%x", mac.Sum(nil)); origin != want {
		t.Fatalf("origin = %q, want keyed fingerprint %q", origin, want)
	}
	legacy := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(strings.Join([]string{profile.BaseURL, profile.Model, profile.APIKey}, "\x00"))))
	if origin == legacy {
		t.Fatal("stored origin is still an offline API-key verifier")
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), string(profile.ResponsesOriginKey)) || strings.Contains(origin, profile.APIKey) {
		t.Fatal("private origin signing material escaped into serialization")
	}

	// Without durable provider configuration, independently built ephemeral
	// models still use a random shared process secret, never an unkeyed hash.
	profile.ResponsesOriginKey = nil
	first, err := NewResponsesModel(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewResponsesModel(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	ephemeralOrigin := first.(interface{ ResponsesOrigin() string }).ResponsesOrigin()
	if !strings.HasPrefix(ephemeralOrigin, "hmac-sha256:v1:") || ephemeralOrigin == origin || ephemeralOrigin == legacy ||
		ephemeralOrigin != second.(interface{ ResponsesOrigin() string }).ResponsesOrigin() {
		t.Fatal("ephemeral models did not share a private process-keyed origin")
	}
}

func TestResponsesOriginRetainsCredentialChangeGuard(t *testing.T) {
	profile := Profile{
		BaseURL: "https://gateway.example/v1", Model: "gpt-6-luna", APIKey: "same-account-key",
		ResponsesOriginKey: []byte("shared-provider-signing-secret"),
	}
	first, err := NewResponsesModel(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	origin := first.(interface{ ResponsesOrigin() string }).ResponsesOrigin()
	for _, test := range []struct {
		name   string
		change func(*Profile)
		match  bool
	}{
		{name: "same credential reconstructed on another replica", change: func(*Profile) {}, match: true},
		{name: "API key rotated", change: func(p *Profile) { p.APIKey = "replacement-account-key" }},
		{name: "endpoint changed", change: func(p *Profile) { p.BaseURL = "https://another.example/v1" }},
		{name: "model changed", change: func(p *Profile) { p.Model = "gpt-6.1-sol" }},
		{name: "provider signing key rotated", change: func(p *Profile) { p.ResponsesOriginKey = []byte("another-provider-key") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := profile
			test.change(&changed)
			rebuilt, err := NewResponsesModel(t.Context(), changed)
			if err != nil {
				t.Fatal(err)
			}
			if got := ModelHasResponsesOrigin(rebuilt, origin); got != test.match {
				t.Fatalf("rebuilt model origin matches = %v, want %v", got, test.match)
			}
			bound, err := rebuilt.(einomodel.ToolCallingChatModel).WithTools([]*schema.ToolInfo{{Name: "wait"}})
			if err != nil {
				t.Fatal(err)
			}
			if got := ModelHasResponsesOrigin(bound, origin); got != test.match {
				t.Fatalf("bound model origin matches = %v, want %v", got, test.match)
			}
		})
	}
	if ModelHasResponsesOrigin(first, "") {
		t.Fatal("an absent origin matched an active continuation")
	}
}
