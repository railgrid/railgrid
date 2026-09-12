/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/faroshq/faros/pkg/runner/harness"
)

func TestAuthFailureEventsExcludeCredentialPayloads(t *testing.T) {
	const secret = "refresh-token-secret-that-must-not-escape"
	params := json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","reason":"expired","accessToken":"refresh-token-secret-that-must-not-escape","refreshToken":"refresh-token-secret-that-must-not-escape"}`)

	t.Run("native auth notification", func(t *testing.T) {
		var event harness.Event
		state := &runState{emit: func(got harness.Event) error {
			event = got
			return nil
		}}
		if err := state.handle(wireMessage{Method: "account/chatgptAuthTokens/refresh", Params: params}); err == nil || !needsInput(err) {
			t.Fatalf("auth notification error = %v, want needs-input", err)
		}
		assertAuthEventSafe(t, event, secret)
	})

	t.Run("auth RPC response", func(t *testing.T) {
		var event harness.Event
		result := authResult(&runState{sessionID: "thread-1", turnID: "turn-1"}, &rpcError{
			Code:    401,
			Message: "authentication required",
			Data:    params,
		}, func(got harness.Event) error {
			event = got
			return nil
		})
		if result.Phase != "needs_input" {
			t.Fatalf("auth RPC result = %+v, want needs_input", result)
		}
		assertAuthEventSafe(t, event, secret)
	})
}

func assertAuthEventSafe(t *testing.T, event harness.Event, secret string) {
	t.Helper()
	if event.Type != "auth_failure" {
		t.Fatalf("auth event type = %q, want auth_failure", event.Type)
	}
	if bytes.Contains(event.Data, []byte(secret)) {
		t.Fatalf("auth event exposed credential payload: %s", event.Data)
	}
	var safe map[string]any
	if err := json.Unmarshal(event.Data, &safe); err != nil {
		t.Fatalf("auth event data = %q, want bounded safe metadata: %v", event.Data, err)
	}
	if safe["reason"] != "expired" {
		t.Fatalf("auth event lost safe reason metadata: %v", safe)
	}
}

func TestAuthFailureEventsRedactNestedCredentialFields(t *testing.T) {
	secrets := []string{"nested-client-secret", "nested-api-key", "nested-bearer-token"}
	params := json.RawMessage(`{"reason":"expired","details":{"CLIENT_SECRET":"nested-client-secret","items":[{"api-key":"nested-api-key","Authorization":"nested-bearer-token"}]},"safe":"retain"}`)
	var event harness.Event
	state := &runState{emit: func(got harness.Event) error {
		event = got
		return nil
	}}
	if err := state.handle(wireMessage{Method: "account/chatgptAuthTokens/refresh", Params: params}); err == nil || !needsInput(err) {
		t.Fatalf("auth notification error = %v, want needs-input", err)
	}
	for _, secret := range secrets {
		if bytes.Contains(event.Data, []byte(secret)) {
			t.Fatalf("nested auth event exposed %q: %s", secret, event.Data)
		}
	}
	var safe map[string]any
	if err := json.Unmarshal(event.Data, &safe); err != nil {
		t.Fatalf("nested auth event data = %q: %v", event.Data, err)
	}
	if safe["reason"] != "expired" || safe["safe"] != "retain" {
		t.Fatalf("safe auth metadata changed: %v", safe)
	}
}

func TestTurnCompletedAuthFailureRedactsCredentialFields(t *testing.T) {
	const secret = "turn-refresh-token-secret"
	params := json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":"authentication required","accessToken":"turn-access-token-secret","refreshToken":"turn-refresh-token-secret"}}}`)
	var event harness.Event
	state := &runState{emit: func(got harness.Event) error {
		event = got
		return nil
	}}
	if err := state.handle(wireMessage{Method: "turn/completed", Params: params}); err != nil {
		t.Fatalf("turn completion error = %v", err)
	}
	if state.phase != "needs_input" || event.Type != "turn_completed" {
		t.Fatalf("turn auth state phase=%q event=%+v, want needs_input turn_completed", state.phase, event)
	}
	if bytes.Contains(event.Data, []byte(secret)) || bytes.Contains(event.Data, []byte("turn-access-token-secret")) {
		t.Fatalf("turn completion exposed auth credentials: %s", event.Data)
	}
	var safe map[string]any
	if err := json.Unmarshal(event.Data, &safe); err != nil {
		t.Fatalf("turn completion data = %q: %v", event.Data, err)
	}
	turn, ok := safe["turn"].(map[string]any)
	if !ok || turn["status"] != "failed" {
		t.Fatalf("turn completion lost safe status metadata: %v", safe)
	}
}

func TestAuthFailureEventsMalformedAndOversizedPayloadsStayBounded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params json.RawMessage
		check  func(*testing.T, []byte)
	}{
		{
			name:   "malformed",
			params: json.RawMessage(`{"reason":"expired"`),
			check: func(t *testing.T, data []byte) {
				if string(data) != `{"redacted":true}` {
					t.Fatalf("malformed auth data = %s, want redacted marker", data)
				}
			},
		},
		{
			name:   "oversized",
			params: json.RawMessage(`{"reason":"expired","token":"` + strings.Repeat("x", maxEventData) + `"}`),
			check: func(t *testing.T, data []byte) {
				if !bytes.Contains(data, []byte(`"truncated":true`)) || bytes.Contains(data, []byte(strings.Repeat("x", 32))) {
					t.Fatalf("oversized auth data = %s, want bounded truncation without payload", data)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var event harness.Event
			state := &runState{emit: func(got harness.Event) error {
				event = got
				return nil
			}}
			if err := state.handle(wireMessage{Method: "account/chatgptAuthTokens/refresh", Params: tc.params}); err == nil || !needsInput(err) {
				t.Fatalf("auth notification error = %v, want needs-input", err)
			}
			if event.Type != "auth_failure" {
				t.Fatalf("auth event type = %q, want auth_failure", event.Type)
			}
			tc.check(t, event.Data)
		})
	}
}
