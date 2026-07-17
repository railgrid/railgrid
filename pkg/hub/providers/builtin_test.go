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

// Since the mcp + edge providers were extracted into standalone
// out-of-process packages, no first-party package registers a builtin
// via init() anymore. These tests therefore register their own fixture
// specs (guarded against duplicate registration since the registry is a
// process-global shared with the rest of the package's tests) and
// exercise the public resolver contract — empty-selects-all, explicit
// sets, unknown-name rejection, dedup, and Requires validation —
// against that fixture set rather than any real provider.

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/faroshq/faros-kedge/pkg/hub/providers"
)

// Fixture builtin names, prefixed to avoid colliding with any real
// provider (or the app-studio fixture in controller_test.go).
const (
	fixtureAggregator = "test-aggregator" // pure aggregator, no Requires
	fixtureEdgeA      = "test-edge-a"     // standalone, no deps
	fixtureEdgeB      = "test-edge-b"     // standalone, declares a child
	fixtureNeedsA     = "test-needs-a"    // Requires fixtureEdgeA
)

var registerFixturesOnce sync.Once

func registerFixtures(t *testing.T) {
	t.Helper()
	registerFixturesOnce.Do(func() {
		providers.RegisterBuiltin(providers.BuiltinSpec{Name: fixtureAggregator})
		providers.RegisterBuiltin(providers.BuiltinSpec{Name: fixtureEdgeA})
		providers.RegisterBuiltin(providers.BuiltinSpec{
			Name:     fixtureEdgeB,
			Children: []providers.BuiltinChild{{DisplayName: "Workloads"}},
		})
		providers.RegisterBuiltin(providers.BuiltinSpec{
			Name:     fixtureNeedsA,
			Requires: []string{fixtureEdgeA},
		})
	})
}

func TestResolveEnabledBuiltins(t *testing.T) {
	registerFixtures(t)

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

	t.Run("pure aggregator alone is allowed (no Requires)", func(t *testing.T) {
		got, err := providers.ResolveEnabledBuiltins([]string{fixtureAggregator})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].Name != fixtureAggregator {
			t.Fatalf("expected just %s, got %+v", fixtureAggregator, got)
		}
	})

	t.Run("standalone entry alone is fine (no deps of its own)", func(t *testing.T) {
		if _, err := providers.ResolveEnabledBuiltins([]string{fixtureEdgeA}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("multiple independent entries resolve", func(t *testing.T) {
		got, err := providers.ResolveEnabledBuiltins([]string{fixtureAggregator, fixtureEdgeA, fixtureEdgeB})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var names []string
		for _, e := range got {
			names = append(names, e.Name)
		}
		for _, want := range []string{fixtureAggregator, fixtureEdgeA, fixtureEdgeB} {
			if !slices.Contains(names, want) {
				t.Errorf("missing %s in resolved set: %v", want, names)
			}
		}
	})

	t.Run("satisfied Requires resolves", func(t *testing.T) {
		if _, err := providers.ResolveEnabledBuiltins([]string{fixtureNeedsA, fixtureEdgeA}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("missing Requires is rejected", func(t *testing.T) {
		_, err := providers.ResolveEnabledBuiltins([]string{fixtureNeedsA})
		if err == nil {
			t.Fatal("expected dependency-violation error, got nil")
		}
		if !strings.Contains(err.Error(), fixtureNeedsA) || !strings.Contains(err.Error(), fixtureEdgeA) {
			t.Errorf("error should name the dependent and its missing dep, got: %v", err)
		}
	})

	t.Run("unknown name rejected with a hint", func(t *testing.T) {
		_, err := providers.ResolveEnabledBuiltins([]string{fixtureEdgeA, "typo-here"})
		if err == nil {
			t.Fatal("expected unknown-name error, got nil")
		}
		if !strings.Contains(err.Error(), "typo-here") || !strings.Contains(err.Error(), "known:") {
			t.Errorf("error should name the typo and list known, got: %v", err)
		}
	})

	t.Run("duplicates are deduped", func(t *testing.T) {
		got, err := providers.ResolveEnabledBuiltins([]string{fixtureEdgeA, fixtureEdgeA})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(got))
		}
	})

	t.Run("builtin declares a child", func(t *testing.T) {
		spec, ok := providers.BuiltinByName(fixtureEdgeB)
		if !ok {
			t.Fatalf("%s not registered", fixtureEdgeB)
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
