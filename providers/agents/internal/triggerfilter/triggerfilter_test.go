// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package triggerfilter

import (
	"net/http"
	"testing"
)

func headersOf(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}

func TestAllows(t *testing.T) {
	tests := []struct {
		name    string
		filter  map[string]string
		headers map[string]string
		body    string
		want    bool
	}{
		{name: "no filter fires on everything", body: `{"anything":true}`, want: true},
		{name: "eventType matches the GitHub header", filter: map[string]string{"eventType": "pull_request"}, headers: map[string]string{"X-GitHub-Event": "pull_request"}, body: `{}`, want: true},
		{name: "eventType rejects a different event", filter: map[string]string{"eventType": "pull_request"}, headers: map[string]string{"X-GitHub-Event": "push"}, body: `{}`, want: false},
		{name: "eventType falls back to the body type field", filter: map[string]string{"eventType": "deployment.created"}, body: `{"type":"deployment.created"}`, want: true},
		{name: "eventType falls back to the body action field", filter: map[string]string{"eventType": "opened"}, body: `{"action":"opened"}`, want: true},
		{name: "eventType accepts a comma-separated list", filter: map[string]string{"eventType": "pull_request, issues"}, headers: map[string]string{"X-GitHub-Event": "issues"}, body: `{}`, want: true},
		{name: "eventType list rejects an event outside it", filter: map[string]string{"eventType": "pull_request, issues"}, headers: map[string]string{"X-GitHub-Event": "push"}, body: `{}`, want: false},
		{name: "a trailing comma in the list is not a wildcard", filter: map[string]string{"eventType": "pull_request,"}, headers: map[string]string{"X-GitHub-Event": "push"}, body: `{}`, want: false},
		{name: "action matches the body action field, not the event header", filter: map[string]string{"eventType": "pull_request", "action": "opened,synchronize"}, headers: map[string]string{"X-GitHub-Event": "pull_request"}, body: `{"action": "synchronize"}`, want: true},
		{name: "action rejects a different body action", filter: map[string]string{"action": "opened"}, headers: map[string]string{"X-GitHub-Event": "pull_request"}, body: `{"action": "closed"}`, want: false},
		{name: "action rejects a payload without one", filter: map[string]string{"action": "opened"}, body: `{}`, want: false},
		{name: "repository matches repository.full_name case-insensitively", filter: map[string]string{"repository": "railgrid/railgrid, railgrid/providers"}, body: `{"repository": {"full_name": "Railgrid/Providers"}}`, want: true},
		{name: "repository rejects another repo", filter: map[string]string{"repository": "railgrid/railgrid"}, body: `{"repository": {"full_name": "someone/else"}}`, want: false},
		{name: "repository rejects a non-object repository field", filter: map[string]string{"repository": "railgrid/railgrid"}, body: `{"repository": "railgrid/railgrid"}`, want: false},
		{name: "match requires the substring in the payload", filter: map[string]string{"match": "production"}, body: `{"env":"production"}`, want: true},
		{name: "match rejects when absent", filter: map[string]string{"match": "production"}, body: `{"env":"staging"}`, want: false},
		{name: "header.<name> matches a request header", filter: map[string]string{"header.X-Env": "prod"}, headers: map[string]string{"X-Env": "prod"}, body: `{}`, want: true},
		{name: "header.<name> rejects a mismatch", filter: map[string]string{"header.X-Env": "prod"}, headers: map[string]string{"X-Env": "dev"}, body: `{}`, want: false},
		{name: "every filter key must pass", filter: map[string]string{"eventType": "push", "match": "main"}, headers: map[string]string{"X-GitHub-Event": "push"}, body: `{"ref":"refs/heads/develop"}`, want: false},
		// A filter written for a source we don't evaluate yet must not
		// silently swallow every event.
		{name: "unknown keys are ignored rather than failing closed", filter: map[string]string{"labels": "urgent"}, body: `{}`, want: true},
		{name: "blank values are ignored", filter: map[string]string{"match": "   "}, body: `{}`, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, got := Allows(tc.filter, headersOf(tc.headers), []byte(tc.body))
			if got != tc.want {
				t.Fatalf("allows = %v (reason %q), want %v", got, reason, tc.want)
			}
			if !got && reason == "" {
				t.Fatal("a rejected delivery must explain why")
			}
		})
	}
}

// The editor shows one verdict per key, with what the delivery carried at
// that coordinate, so an author sees "eventType: want pull_request, got push"
// rather than a bare "no match".
func TestEvaluateExplainsEveryKey(t *testing.T) {
	filter := map[string]string{
		"eventType":    "pull_request",
		"action":       "opened, reopened",
		"repository":   "railgrid/railgrid",
		"match":        "needs-review",
		"header.X-Env": "prod",
		"labels":       "urgent",
	}
	headers := headersOf(map[string]string{"X-GitHub-Event": "push", "X-Env": "prod"})
	body := []byte(`{"action":"opened","repository":{"full_name":"railgrid/railgrid"},"labels":["needs-review"]}`)

	v := Evaluate(filter, headers, body)
	if v.Matched {
		t.Fatal("eventType push must fail a pull_request filter")
	}
	if v.Reason != "eventType does not match pull_request" {
		t.Fatalf("reason = %q", v.Reason)
	}
	if v.Extracted != (Extracted{EventType: "push", Action: "opened", Repository: "railgrid/railgrid"}) {
		t.Fatalf("extracted = %+v", v.Extracted)
	}
	byKey := map[string]Key{}
	order := []string{}
	for _, k := range v.Keys {
		byKey[k.Key] = k
		order = append(order, k.Key)
	}
	if len(order) != 6 {
		t.Fatalf("keys = %v, want one per filter entry", order)
	}
	for i := 1; i < len(order); i++ {
		if order[i-1] > order[i] {
			t.Fatalf("keys are not sorted: %v", order)
		}
	}
	for key, want := range map[string]Key{
		"eventType":    {Matched: false, Known: true, Got: "push"},
		"action":       {Matched: true, Known: true, Got: "opened"},
		"repository":   {Matched: true, Known: true, Got: "railgrid/railgrid"},
		"match":        {Matched: true, Known: true},
		"header.X-Env": {Matched: true, Known: true, Got: "prod"},
		"labels":       {Matched: true, Known: false},
	} {
		got := byKey[key]
		if got.Matched != want.Matched || got.Known != want.Known || got.Got != want.Got {
			t.Errorf("%s: got {matched %v known %v got %q}, want {matched %v known %v got %q}", key, got.Matched, got.Known, got.Got, want.Matched, want.Known, want.Got)
		}
		if got.Want != filter[key] {
			t.Errorf("%s: want = %q, filter said %q", key, got.Want, filter[key])
		}
	}
	if byKey["labels"].Reason == "" {
		t.Error("an unknown key must say it is ignored")
	}
}

// An empty filter is a verdict too: it matches, with nothing to explain, so
// the editor can show "fires on every delivery" from the same call.
func TestEvaluateEmptyFilterMatches(t *testing.T) {
	v := Evaluate(nil, headersOf(nil), []byte(`{"type":"ping"}`))
	if !v.Matched || v.Reason != "" || len(v.Keys) != 0 {
		t.Fatalf("empty filter verdict = %+v", v)
	}
	if v.Extracted.EventType != "ping" {
		t.Fatalf("extracted eventType = %q, want ping from the body", v.Extracted.EventType)
	}
}

// UnknownKeys is the warning the fail-open matcher cannot give at delivery
// time: the reconciler and the MCP write tools use it to say a key is ignored.
func TestUnknownKeys(t *testing.T) {
	got := UnknownKeys(map[string]string{
		"eventType": "push", "action": "opened", "repository": "a/b", "match": "x",
		"header.X-Foo": "1", "evenType": "push", "Branch": "main", "repo": "a/b",
	})
	want := []string{"Branch", "evenType", "repo"}
	if len(got) != len(want) {
		t.Fatalf("UnknownKeys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("UnknownKeys = %v, want %v", got, want)
		}
	}
	if keys := UnknownKeys(nil); len(keys) != 0 {
		t.Fatalf("a nil filter has no unknown keys, got %v", keys)
	}
}
