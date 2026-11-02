// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package kcp

import (
	"reflect"
	"testing"
)

func TestAcceptedClaimScopeNeverWidensPriorConsentOrAppliedAccess(t *testing.T) {
	all := func() map[string]any { return map[string]any{"matchAll": true} }
	labels := func(key, value string) map[string]any {
		return map[string]any{"matchLabels": map[string]any{key: value}}
	}
	claim := func(verbs []any, selector map[string]any) map[string]any {
		return map[string]any{"group": "code.railgrid.ai", "resource": "repositories", "state": "Accepted", "verbs": verbs, "selector": selector}
	}
	for _, scenario := range []string{"export widened", "applied stale broader", "applied narrower", "spec narrower selector", "applied narrower selector", "selectors intersect", "conflicting selectors", "unsupported selector", "missing selector", "spec identity", "applied identity", "export identity", "rejected", "no common verb", "wildcard", "duplicate spec"} {
		t.Run(scenario, func(t *testing.T) {
			spec := claim([]any{"get"}, all())
			applied := claim([]any{"get"}, all())
			exported := claim([]any{"get", "delete"}, all())
			want := []AcceptedClaimScope{{Group: "code.railgrid.ai", Resource: "repositories", Verbs: []string{"get"}, Selector: AcceptedClaimSelector{MatchAll: true}}}
			switch scenario {
			case "applied stale broader":
				applied["verbs"] = []any{"get", "delete"}
			case "applied narrower":
				spec["verbs"] = []any{"get", "delete"}
			case "spec narrower selector":
				spec["selector"] = labels("owner", "old")
				want[0].Selector = AcceptedClaimSelector{MatchLabels: map[string]string{"owner": "old"}}
			case "applied narrower selector":
				applied["selector"] = labels("owner", "old")
				want[0].Selector = AcceptedClaimSelector{MatchLabels: map[string]string{"owner": "old"}}
			case "selectors intersect":
				spec["selector"] = labels("owner", "old")
				applied["selector"] = labels("environment", "dev")
				want[0].Selector = AcceptedClaimSelector{MatchLabels: map[string]string{"owner": "old", "environment": "dev"}}
			case "conflicting selectors":
				spec["selector"] = labels("owner", "old")
				applied["selector"] = labels("owner", "new")
				want = nil
			case "unsupported selector":
				spec["selector"] = map[string]any{"matchExpressions": []any{map[string]any{"key": "owner", "operator": "Exists"}}}
				want = nil
			case "missing selector":
				delete(spec, "selector")
				want = nil
			case "spec identity":
				spec["identityHash"] = "foreign"
				want = nil
			case "applied identity":
				applied["identityHash"] = "foreign"
				want = nil
			case "export identity":
				exported["identityHash"] = "foreign"
				want = nil
			case "rejected":
				spec["state"] = "Rejected"
				want = nil
			case "no common verb":
				exported["verbs"] = []any{"delete"}
				want = nil
			case "wildcard":
				applied["verbs"] = []any{"*"}
				exported["verbs"] = []any{"*"}
			case "duplicate spec":
				want = nil
			}
			rawSpec := []any{spec}
			if scenario == "duplicate spec" {
				rawSpec = append(rawSpec, spec)
			}
			got := effectiveAcceptedClaims(rawSpec, []any{applied}, []any{exported})
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("scope=%+v want=%+v", got, want)
			}
		})
	}
}
