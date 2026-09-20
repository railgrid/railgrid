/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package crossprovider

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// catalogEntry is the slice of the CatalogEntry this test cares about.
type catalogEntry struct {
	Spec struct {
		Dependencies []struct {
			Name     string `json:"name"`
			Composes []struct {
				Group    string   `json:"group"`
				Resource string   `json:"resource"`
				Verbs    []string `json:"verbs"`
			} `json:"composes"`
		} `json:"dependencies"`
	} `json:"spec"`
}

// The rules this package builds and the composition the CatalogEntry declares
// are one contract with two spellings. The hub admits a rule only when a
// declared composition covers it, so a rule built here that the manifest does
// not declare is an identity request refused in full — and a verb declared but
// never asked for is a grant nobody needed.
//
// The comparison is on the UNION of verbs across both rule shapes, because the
// split into collection and object rules is an RBAC mechanic (resourceNames do
// not apply to a collection request), not part of the declaration.
func TestCompositionRulesMatchTheManifest(t *testing.T) {
	declared := declaredComposition(t)

	built := map[string][]string{}
	add := func(rule rbacv1.PolicyRule) {
		key := rule.APIGroups[0] + "/" + rule.Resources[0]
		built[key] = append(built[key], rule.Verbs...)
	}
	add(InstanceCollectionRule())
	add(RepositoryCollectionRule())
	add(RepositoryCommitCollectionRule())
	for _, build := range []func([]string) (rbacv1.PolicyRule, bool){
		InstanceObjectRule, RepositoryObjectRule, RepositoryCommitObjectRule,
	} {
		rule, ok := build([]string{"x"})
		if !ok {
			t.Fatal("object rule was not built for a non-empty name list")
		}
		add(rule)
	}

	for key, verbs := range built {
		want, ok := declared[key]
		if !ok {
			t.Fatalf("%s is composed in Go but not declared in manifest.yaml", key)
		}
		if got := normalize(verbs); got != want {
			t.Fatalf("%s: Go composes [%s], the manifest declares [%s]", key, got, want)
		}
	}
	for key := range declared {
		if _, ok := built[key]; !ok {
			t.Fatalf("%s is declared in manifest.yaml but nothing composes it", key)
		}
	}
}

// An object rule with no names is no rule at all: a rule with `delete` and an
// empty resourceNames would authorize deleting every instance in the
// workspace, which is the opposite of what name scoping is for.
func TestObjectRulesRefuseToBeUnnamed(t *testing.T) {
	for name, build := range map[string]func([]string) (rbacv1.PolicyRule, bool){
		"instances":         InstanceObjectRule,
		"repositories":      RepositoryObjectRule,
		"repositorycommits": RepositoryCommitObjectRule,
	} {
		if _, ok := build(nil); ok {
			t.Fatalf("%s object rule was built without names", name)
		}
		if _, ok := build([]string{}); ok {
			t.Fatalf("%s object rule was built from an empty name list", name)
		}
	}
}

// Collection verbs never carry names, because RBAC ignores resourceNames on a
// collection request: a named `list` authorizes nothing at all.
func TestCollectionRulesAreUnnamed(t *testing.T) {
	for _, rule := range []rbacv1.PolicyRule{
		InstanceCollectionRule(),
		RepositoryCollectionRule(),
		RepositoryCommitCollectionRule(),
	} {
		if len(rule.ResourceNames) != 0 {
			t.Fatalf("collection rule is name-scoped: %#v", rule)
		}
		for _, verb := range rule.Verbs {
			switch verb {
			case "create", "list", "watch":
			default:
				t.Fatalf("%q is not a collection verb: %#v", verb, rule)
			}
		}
	}
}

func declaredComposition(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifest.yaml"))
	if err != nil {
		t.Fatalf("read manifest.yaml: %v", err)
	}
	var entry catalogEntry
	if err := yaml.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("decode manifest.yaml: %v", err)
	}
	out := map[string]string{}
	for _, dependency := range entry.Spec.Dependencies {
		for _, composed := range dependency.Composes {
			if composed.Group == "" {
				t.Fatalf("dependency %q composes a core-group resource: %+v", dependency.Name, composed)
			}
			out[composed.Group+"/"+composed.Resource] = normalize(composed.Verbs)
		}
	}
	if len(out) == 0 {
		t.Fatal("manifest.yaml declares no composition; the reconcilers would be granted nothing")
	}
	return out
}

func normalize(verbs []string) string {
	seen := map[string]bool{}
	out := make([]string, 0, len(verbs))
	for _, verb := range verbs {
		if seen[verb] {
			continue
		}
		seen[verb] = true
		out = append(out, verb)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}
