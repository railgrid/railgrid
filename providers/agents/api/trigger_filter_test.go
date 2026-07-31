// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTriggerFilterAllows(t *testing.T) {
	tests := []struct {
		name    string
		filter  map[string]string
		headers map[string]string
		body    string
		want    bool
	}{
		{
			name: "no filter fires on everything",
			body: `{"anything":true}`,
			want: true,
		},
		{
			name:    "eventType matches the GitHub header",
			filter:  map[string]string{"eventType": "pull_request"},
			headers: map[string]string{"X-GitHub-Event": "pull_request"},
			body:    `{}`,
			want:    true,
		},
		{
			name:    "eventType rejects a different event",
			filter:  map[string]string{"eventType": "pull_request"},
			headers: map[string]string{"X-GitHub-Event": "push"},
			body:    `{}`,
			want:    false,
		},
		{
			name:   "eventType falls back to the body type field",
			filter: map[string]string{"eventType": "deployment.created"},
			body:   `{"type":"deployment.created"}`,
			want:   true,
		},
		{
			name:   "eventType falls back to the body action field",
			filter: map[string]string{"eventType": "opened"},
			body:   `{"action":"opened"}`,
			want:   true,
		},
		{
			name:   "match requires the substring in the payload",
			filter: map[string]string{"match": "production"},
			body:   `{"env":"production"}`,
			want:   true,
		},
		{
			name:   "match rejects when absent",
			filter: map[string]string{"match": "production"},
			body:   `{"env":"staging"}`,
			want:   false,
		},
		{
			name:    "header.<name> matches a request header",
			filter:  map[string]string{"header.X-Env": "prod"},
			headers: map[string]string{"X-Env": "prod"},
			body:    `{}`,
			want:    true,
		},
		{
			name:    "header.<name> rejects a mismatch",
			filter:  map[string]string{"header.X-Env": "prod"},
			headers: map[string]string{"X-Env": "dev"},
			body:    `{}`,
			want:    false,
		},
		{
			name:    "every filter key must pass",
			filter:  map[string]string{"eventType": "push", "match": "main"},
			headers: map[string]string{"X-GitHub-Event": "push"},
			body:    `{"ref":"refs/heads/develop"}`,
			want:    false,
		},
		{
			// A filter written for a source we don't evaluate yet must not
			// silently swallow every event.
			name:   "unknown keys are ignored rather than failing closed",
			filter: map[string]string{"labels": "urgent"},
			body:   `{}`,
			want:   true,
		},
		{
			name:   "blank values are ignored",
			filter: map[string]string{"match": "   "},
			body:   `{}`,
			want:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/webhooks/triggers/c/n/t", strings.NewReader(tc.body))
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			reason, got := triggerFilterAllows(tc.filter, r, []byte(tc.body))
			if got != tc.want {
				t.Fatalf("allows = %v (reason %q), want %v", got, reason, tc.want)
			}
			if !got && reason == "" {
				t.Fatal("a rejected delivery must explain why")
			}
		})
	}
}
