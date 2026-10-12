// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package triggerfilter decides whether an inbound webhook delivery fires a
// Trigger, and says why not when it does not.
//
// It is the one implementation of spec.filter. The webhook handler runs it on
// every delivery, the triggers/evaluate verb runs it on a candidate filter
// against a recorded delivery so the portal's filter editor shows exactly
// what production would decide, and the golden tests pin the semantics. A
// second copy — in a handler, in TypeScript — would drift, and a filter that
// matches in the editor but not on the wire is worse than no editor.
//
// Supported keys:
//
//	eventType      — the platform's event header (X-GitHub-Event /
//	                 X-Event-Type / X-Event-Key) or a top-level "type"/
//	                 "action" field in the JSON body
//	action         — the top-level "action" field in the JSON body (GitHub's
//	                 opened / synchronize / closed / …)
//	repository     — the "repository.full_name" field in the JSON body
//	                 (owner/repo, as GitHub sends it)
//	match          — substring that must appear in the raw payload
//	header.<name>  — exact match on a request header
//
// eventType, action and repository take a comma-separated list and pass when
// any entry matches, case-insensitively, so one trigger can listen to several
// events. Unknown keys are ignored rather than failing closed, so a filter
// written for a future source never silently blocks every event; the verdict
// marks them so the editor can warn.
package triggerfilter

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// EventTypeHeaders are the request headers, in precedence order, a delivery's
// event type is read from before the body is consulted.
var EventTypeHeaders = []string{"X-GitHub-Event", "X-Event-Type", "X-Event-Key"}

// Key is the verdict on one filter entry.
type Key struct {
	// Key is the spec.filter key as written.
	Key string `json:"key"`
	// Want is the value the filter asked for.
	Want string `json:"want"`
	// Got is what the delivery carried at that coordinate, as the matcher saw
	// it. For "match" it is empty: the payload is the coordinate.
	Got string `json:"got,omitempty"`
	// Matched reports whether this entry passed.
	Matched bool `json:"matched"`
	// Known is false for a key the matcher does not understand. Such a key
	// never blocks a delivery, which is why the editor should say so.
	Known bool `json:"known"`
	// Reason is the one-line explanation of a failed entry.
	Reason string `json:"reason,omitempty"`
}

// Extracted is what the matcher read off a delivery, independent of any
// filter: the coordinates a filter can be written against.
type Extracted struct {
	EventType  string `json:"eventType,omitempty"`
	Action     string `json:"action,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// Verdict is the result of evaluating one filter against one delivery.
type Verdict struct {
	// Matched is true when every known, non-empty entry passed.
	Matched bool `json:"matched"`
	// Reason is the first failing entry's reason — the string the webhook
	// handler acks a filtered delivery with.
	Reason string `json:"reason,omitempty"`
	// Keys holds every entry's verdict, sorted by key so the output is stable.
	Keys []Key `json:"keys"`
	// Extracted is what the delivery carried at the well-known coordinates.
	Extracted Extracted `json:"extracted"`
}

// Evaluate runs filter against a delivery's headers and body.
func Evaluate(filter map[string]string, headers http.Header, body []byte) Verdict {
	v := Verdict{Matched: true, Keys: []Key{}, Extracted: Extract(headers, body)}
	keys := make([]string, 0, len(filter))
	for k := range filter {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		want := strings.TrimSpace(filter[key])
		if want == "" {
			continue
		}
		entry := Key{Key: key, Want: want, Known: true, Matched: true}
		switch {
		case key == "eventType":
			entry.Got = v.Extracted.EventType
			if !anyOf(want, entry.Got) {
				entry.Matched, entry.Reason = false, "eventType does not match "+want
			}
		case key == "action":
			entry.Got = v.Extracted.Action
			if !anyOf(want, entry.Got) {
				entry.Matched, entry.Reason = false, "action does not match "+want
			}
		case key == "repository":
			entry.Got = v.Extracted.Repository
			if !anyOf(want, entry.Got) {
				entry.Matched, entry.Reason = false, "repository does not match "+want
			}
		case key == "match":
			if !strings.Contains(string(body), want) {
				entry.Matched, entry.Reason = false, "payload does not contain "+want
			}
		case strings.HasPrefix(key, "header."):
			entry.Got = headers.Get(strings.TrimPrefix(key, "header."))
			if !strings.EqualFold(entry.Got, want) {
				entry.Matched, entry.Reason = false, key+" does not match"
			}
		default:
			entry.Known = false
			entry.Reason = key + " is not a filter key and is ignored"
		}
		if !entry.Matched && v.Matched {
			v.Matched, v.Reason = false, entry.Reason
		}
		v.Keys = append(v.Keys, entry)
	}
	return v
}

// KnownKeys names the filter keys the matcher understands, for messages that
// list them. "header.<name>" stands for every header key.
var KnownKeys = []string{"eventType", "action", "repository", "match", "header.<name>"}

// Known reports whether the matcher understands a filter key. It is the same
// rule Evaluate's switch applies, so the warning and the matcher agree.
func Known(key string) bool {
	switch key {
	case "eventType", "action", "repository", "match":
		return true
	}
	return strings.HasPrefix(key, "header.")
}

// UnknownKeys returns the keys of filter the matcher does not understand,
// sorted. The matcher ignores them at delivery time — fail-open, so a filter
// written for a future source never blocks every event — which is exactly why
// everything that writes a filter asks this first: a typo is otherwise a
// filter that silently lets everything through. An entry with a blank value
// counts too; Evaluate skips it, but the key is still not one anybody meant.
func UnknownKeys(filter map[string]string) []string {
	var out []string
	for key := range filter {
		if !Known(key) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// Allows is the webhook handler's question: does this delivery fire? It
// returns the reason when it does not.
func Allows(filter map[string]string, headers http.Header, body []byte) (string, bool) {
	if len(filter) == 0 {
		return "", true
	}
	v := Evaluate(filter, headers, body)
	return v.Reason, v.Matched
}

// Extract reads the well-known coordinates off a delivery.
func Extract(headers http.Header, body []byte) Extracted {
	return Extracted{
		EventType:  EventType(headers, body),
		Action:     BodyField(body, "action"),
		Repository: BodyField(body, "repository", "full_name"),
	}
}

// EventType resolves the delivery's event type from the conventional platform
// headers, falling back to a "type"/"action" field in the JSON body.
func EventType(headers http.Header, body []byte) string {
	for _, h := range EventTypeHeaders {
		if v := strings.TrimSpace(headers.Get(h)); v != "" {
			return v
		}
	}
	if v := BodyField(body, "type"); v != "" {
		return v
	}
	return BodyField(body, "action")
}

// BodyField reads one string at the given path of the JSON payload; a missing
// field, a non-string or a non-JSON body all read as "".
func BodyField(body []byte, path ...string) string {
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	var cur any = doc
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = obj[key]
	}
	s, _ := cur.(string)
	return strings.TrimSpace(s)
}

// anyOf reports whether got equals any comma-separated entry of want,
// ignoring case and surrounding whitespace. Blank entries never match, so a
// trailing comma does not turn the filter into a wildcard.
func anyOf(want, got string) bool {
	for _, entry := range strings.Split(want, ",") {
		entry = strings.TrimSpace(entry)
		if entry != "" && strings.EqualFold(entry, got) {
			return true
		}
	}
	return false
}
