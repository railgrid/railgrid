// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package kcp

import (
	"sort"
	"strings"
)

// AcceptedClaimScope is the effective intersection of the user's current
// decision and the access kcp actually applied, never the mutable declaration.
type AcceptedClaimScope struct {
	Group    string                `json:"group,omitempty"`
	Resource string                `json:"resource"`
	Verbs    []string              `json:"verbs"`
	Selector AcceptedClaimSelector `json:"selector"`
}

type AcceptedClaimSelector struct {
	MatchAll    bool              `json:"matchAll,omitempty"`
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

func effectiveAcceptedClaims(spec, applied, exported []any) []AcceptedClaimScope {
	index := func(raw []any) map[ClaimRef]map[string]any {
		out := map[ClaimRef]map[string]any{}
		for _, value := range raw {
			claim, ok := value.(map[string]any)
			if !ok {
				continue
			}
			group, _ := claim["group"].(string)
			resource, _ := claim["resource"].(string)
			ref := ClaimRef{Group: group, Resource: resource}
			if _, duplicate := out[ref]; duplicate {
				out[ref] = nil
				continue
			}
			out[ref] = claim
		}
		return out
	}
	appliedByRef, exportedByRef := index(applied), index(exported)
	var out []AcceptedClaimScope
	for ref, accepted := range index(spec) {
		active, current := appliedByRef[ref], exportedByRef[ref]
		if accepted == nil || active == nil || current == nil || accepted["state"] != "Accepted" || ref.Resource == "" || strings.Contains(ref.Resource, "*") {
			continue
		}
		identityFree := true
		for _, claim := range []map[string]any{accepted, active, current} {
			if identity, exists := claim["identityHash"]; exists && identity != "" {
				identityFree = false
			}
		}
		if !identityFree {
			continue
		}
		verbs := intersectClaimVerbs(intersectClaimVerbs(claimVerbs(accepted), claimVerbs(active)), claimVerbs(current))
		if len(verbs) == 0 {
			continue
		}
		consentSelector, ok := acceptedSelector(accepted["selector"])
		if !ok {
			continue
		}
		appliedSelector, ok := acceptedSelector(active["selector"])
		if !ok {
			continue
		}
		selector, ok := intersectClaimSelectors(consentSelector, appliedSelector)
		if !ok {
			continue
		}
		out = append(out, AcceptedClaimScope{Group: ref.Group, Resource: ref.Resource, Verbs: verbs, Selector: selector})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Resource < out[j].Resource
	})
	return out
}

func claimVerbs(claim map[string]any) []string {
	raw, ok := claim["verbs"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, item := range raw {
		verb, ok := item.(string)
		if !ok || verb == "" {
			return nil
		}
		set[verb] = true
	}
	out := make([]string, 0, len(set))
	for verb := range set {
		out = append(out, verb)
	}
	sort.Strings(out)
	return out
}

func intersectClaimVerbs(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	left, right := map[string]bool{}, map[string]bool{}
	for _, verb := range a {
		left[verb] = true
	}
	for _, verb := range b {
		right[verb] = true
	}
	if left["*"] {
		return b
	}
	if right["*"] {
		return a
	}
	var out []string
	for _, verb := range a {
		if right[verb] {
			out = append(out, verb)
		}
	}
	return out
}

func acceptedSelector(raw any) (AcceptedClaimSelector, bool) {
	selector, ok := raw.(map[string]any)
	if !ok || len(selector) != 1 {
		return AcceptedClaimSelector{}, false
	}
	if selector["matchAll"] == true {
		return AcceptedClaimSelector{MatchAll: true}, true
	}
	labels, ok := selector["matchLabels"].(map[string]any)
	if !ok || len(labels) == 0 {
		return AcceptedClaimSelector{}, false
	}
	out := map[string]string{}
	for key, value := range labels {
		text, ok := value.(string)
		if !ok || key == "" {
			return AcceptedClaimSelector{}, false
		}
		out[key] = text
	}
	return AcceptedClaimSelector{MatchLabels: out}, true
}

func intersectClaimSelectors(a, b AcceptedClaimSelector) (AcceptedClaimSelector, bool) {
	if a.MatchAll {
		return b, true
	}
	if b.MatchAll {
		return a, true
	}
	labels := map[string]string{}
	for key, value := range a.MatchLabels {
		labels[key] = value
	}
	for key, value := range b.MatchLabels {
		if prior, exists := labels[key]; exists && prior != value {
			return AcceptedClaimSelector{}, false
		}
		labels[key] = value
	}
	return AcceptedClaimSelector{MatchLabels: labels}, len(labels) > 0
}
