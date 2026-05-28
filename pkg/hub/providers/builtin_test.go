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

package providers_test

// The blank imports pull in the first-party provider packages, whose
// init() calls populate the registry. The test exercises the public
// resolver against that real canonical set rather than a fixture, since
// the dep relationships we want to guarantee (e.g. mcp requires
// kubernetes-edges + server-edges) are themselves part of the contract
// each manifest declares.

import (
	"slices"
	"strings"
	"testing"

	"github.com/faroshq/faros-kedge/pkg/hub/providers"

	_ "github.com/faroshq/faros-kedge/providers/kubernetesedges"
	_ "github.com/faroshq/faros-kedge/providers/mcp"
	_ "github.com/faroshq/faros-kedge/providers/serveredges"
)

func TestResolveEnabledBuiltins(t *testing.T) {
	t.Run("empty selects everything", func(t *testing.T) {
		got, err := providers.ResolveEnabledBuiltins(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != len(providers.AllBuiltins()) {
			t.Fatalf("got %d entries, want %d", len(got), len(providers.AllBuiltins()))
		}
	})

	t.Run("explicit full set succeeds", func(t *testing.T) {
		names := providers.BuiltinNames()
		got, err := providers.ResolveEnabledBuiltins(names)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != len(names) {
			t.Fatalf("got %d entries, want %d", len(got), len(names))
		}
	})

	t.Run("mcp alone fails on both missing deps", func(t *testing.T) {
		_, err := providers.ResolveEnabledBuiltins([]string{"mcp"})
		if err == nil {
			t.Fatal("expected dep-violation error, got nil")
		}
		// Both missing deps must be listed — users want one fix, not
		// iterative whack-a-mole.
		msg := err.Error()
		if !strings.Contains(msg, "mcp requires kubernetes-edges") {
			t.Errorf("error should name kubernetes-edges, got: %v", err)
		}
		if !strings.Contains(msg, "mcp requires server-edges") {
			t.Errorf("error should name server-edges, got: %v", err)
		}
	})

	t.Run("mcp + only one edge type still fails", func(t *testing.T) {
		_, err := providers.ResolveEnabledBuiltins([]string{"mcp", "server-edges"})
		if err == nil {
			t.Fatal("expected dep-violation error, got nil")
		}
		if !strings.Contains(err.Error(), "mcp requires kubernetes-edges") {
			t.Errorf("error should name the still-missing dep, got: %v", err)
		}
	})

	t.Run("mcp + both edge types satisfies the dep", func(t *testing.T) {
		got, err := providers.ResolveEnabledBuiltins([]string{"mcp", "kubernetes-edges", "server-edges"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var names []string
		for _, e := range got {
			names = append(names, e.Name)
		}
		for _, want := range []string{"mcp", "kubernetes-edges", "server-edges"} {
			if !slices.Contains(names, want) {
				t.Errorf("missing %s in resolved set: %v", want, names)
			}
		}
	})

	t.Run("server-edges alone is fine (no deps of its own)", func(t *testing.T) {
		if _, err := providers.ResolveEnabledBuiltins([]string{"server-edges"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("unknown name rejected with a hint", func(t *testing.T) {
		_, err := providers.ResolveEnabledBuiltins([]string{"server-edges", "typo-here"})
		if err == nil {
			t.Fatal("expected unknown-name error, got nil")
		}
		if !strings.Contains(err.Error(), "typo-here") || !strings.Contains(err.Error(), "known:") {
			t.Errorf("error should name the typo and list known, got: %v", err)
		}
	})

	t.Run("duplicates are deduped", func(t *testing.T) {
		got, err := providers.ResolveEnabledBuiltins([]string{"server-edges", "server-edges"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(got))
		}
	})

	t.Run("kubernetes-edges declares Workloads as a child", func(t *testing.T) {
		spec, ok := providers.BuiltinByName("kubernetes-edges")
		if !ok {
			t.Fatal("kubernetes-edges not registered")
		}
		var labels []string
		for _, c := range spec.Children {
			labels = append(labels, c.DisplayName)
		}
		if !slices.Contains(labels, "Workloads") {
			t.Errorf("expected Workloads child, got %v", labels)
		}
	})
}
