/*
Copyright 2025 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"strings"
	"testing"
)

func TestDatabricksComponentReleaseContract(t *testing.T) {
	component, ok := components["databricks"]
	if !ok {
		t.Fatal("databricks component is not registered")
	}
	if component.prefix != "providers/databricks/v" {
		t.Fatalf("databricks tag prefix = %q, want providers/databricks/v", component.prefix)
	}
	if component.triggers == "" {
		t.Fatal("databricks release contract has no downstream trigger description")
	}
	for i, name := range componentOrder {
		if name == "databricks" {
			return
		}
		if i == len(componentOrder)-1 {
			t.Fatal("databricks component is not in componentOrder")
		}
	}
}

// TestTagSet covers the shapes git actually emits: `git tag -l` prints bare
// names, `git ls-remote --tags` prints "<sha>\trefs/tags/<name>" and repeats
// annotated tags with a "^{}" suffix for the dereferenced commit.
func TestTagSet(t *testing.T) {
	tests := []struct {
		name string
		out  string
		trim string
		want []string
		miss []string
	}{
		{
			name: "git tag -l",
			out:  "v0.1.0\nproviders/kuery/v0.0.24\n\n  providers/code/v0.0.27  \n",
			want: []string{"v0.1.0", "providers/kuery/v0.0.24", "providers/code/v0.0.27"},
			miss: []string{"", "v0.2.0"},
		},
		{
			name: "ls-remote strips sha and deref suffix",
			out: "3f9bdca\trefs/tags/provider-sdk/v0.0.1\n" +
				"b01a9a1\trefs/tags/v0.1.0\n" +
				"6a153a7\trefs/tags/v0.1.0^{}\n",
			trim: "refs/tags/",
			want: []string{"provider-sdk/v0.0.1", "v0.1.0"},
			miss: []string{"v0.1.0^{}", "refs/tags/v0.1.0"},
		},
		{
			name: "non-tag refs are ignored",
			out:  "3f9bdca\trefs/heads/main\nb01a9a1\trefs/tags/v0.1.0\n",
			trim: "refs/tags/",
			want: []string{"v0.1.0"},
			miss: []string{"refs/heads/main", "main"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tagSet(tc.out, tc.trim)
			for _, w := range tc.want {
				if !got[w] {
					t.Errorf("tagSet missing %q; got %v", w, got)
				}
			}
			for _, m := range tc.miss {
				if got[m] {
					t.Errorf("tagSet unexpectedly contains %q", m)
				}
			}
			if len(got) != len(tc.want) {
				t.Errorf("tagSet size = %d, want %d (%v)", len(got), len(tc.want), got)
			}
		})
	}
}

// TestMultipleComponentTargets covers the argument parsing that lets a run name
// several components: the pseudo-targets stay exclusive, and anything that is
// not a component is rejected before a tag is created.
func TestMultipleComponentTargets(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"all is exclusive", []string{"all", "quickstart", "--dry-run"}, "`all` already covers every component"},
		{"current is exclusive", []string{"current", "hub"}, "`current` prints every component's latest tag"},
		{"unknown component", []string{"quickstart", "bogus", "--dry-run"}, `unknown component "bogus"`},
		{"flags only", []string{"--dry-run"}, "no component given"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.args)
			if err == nil {
				t.Fatalf("run(%q) = nil, want error containing %q", tc.args, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run(%q) = %v, want error containing %q", tc.args, err, tc.want)
			}
		})
	}
}

func TestLinearReleaseComponent(t *testing.T) {
	component, ok := components["linear"]
	if !ok || component.prefix != "providers/linear/v" {
		t.Fatalf("linear release component: %+v exists=%v", component, ok)
	}
	found := false
	for _, name := range componentOrder {
		if name == "linear" {
			found = true
		}
	}
	if !found {
		t.Fatal("linear absent from release component order")
	}
}
