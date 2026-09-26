// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engagement

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// catalogEntry is the slice of kuery's CatalogEntry these tests reason about:
// spec.requires, the ONE list of everything kuery needs that it does not own.
// It replaced spec.apiExport.permissionClaims and spec.dependencies[].composes,
// which could disagree with each other, so there is exactly one declaration to
// assert against.
type catalogEntry struct {
	Spec struct {
		Requires []requirement `json:"requires"`
	} `json:"spec"`
}

type requirement struct {
	Provider  string `json:"provider"`
	Group     string `json:"group"`
	Resources []struct {
		Name  string   `json:"name"`
		Verbs []string `json:"verbs"`
	} `json:"resources"`
}

// Where the list and the watch went: kuery's generated APIExport carries a
// permission claim on the edges provider's clusters, and that claim carries NO
// identityHash. kcp resolves an unpinned first-party claim per CONSUMER
// workspace when a cluster-scoped PermissionClaimPolicy pairs the claiming
// export's group with the claimed one — which is the property spec.requires was
// invented for, and the reason reading edges no longer needs a credential.
//
// A hash appearing here would silently pin every consumer to ONE edges
// provider, so its absence is asserted rather than assumed.
func TestGeneratedAPIExportClaimsTheEdgesClusters(t *testing.T) {
	var export struct {
		Spec struct {
			PermissionClaims []struct {
				Group        string   `json:"group"`
				Resource     string   `json:"resource"`
				Verbs        []string `json:"verbs"`
				IdentityHash string   `json:"identityHash"`
			} `json:"permissionClaims"`
		} `json:"spec"`
	}
	for _, path := range [][]string{
		{"config", "kcp", "apiexport-kuery.providers.railgrid.ai.yaml"},
		{"deploy", "chart", "files", "apiexport.yaml"},
	} {
		if err := yaml.Unmarshal([]byte(readFixture(t, path...)), &export); err != nil {
			t.Fatalf("decode %v: %v", path, err)
		}
		var found bool
		for _, claim := range export.Spec.PermissionClaims {
			if claim.Group != edgesAPIGroup || claim.Resource != edgesResource {
				continue
			}
			found = true
			if claim.IdentityHash != "" {
				t.Fatalf("%v pins identityHash %q on the edges claim; it must resolve per consumer workspace", path, claim.IdentityHash)
			}
			for _, want := range []string{"get", "list", "watch"} {
				if !slices.Contains(claim.Verbs, want) {
					t.Fatalf("%v claims %v on %s; the edge reconciler needs %q", path, claim.Verbs, edgesResource, want)
				}
			}
		}
		if !found {
			t.Fatalf("%v carries no claim on %s/%s; kuery would see no edges in any workspace", path, edgesAPIGroup, edgesResource)
		}
	}
}

// The manifest and the chart's copy are two renderings of one declaration, and
// the chart's is the one that reaches production. A requirement that drifts
// between them is a consent prompt that does not match what the hub will mint,
// and an export that drifts is a coordinate kcp routes in one deployment and
// not the other.
//
// Both sections are compared as text rather than decoded, because the chart is
// a Helm template: neither spec.export nor spec.requires carries a Helm
// expression, so the two blocks must be character-identical once comments,
// blank lines and the leading indentation are removed. Comparing the whole
// block (rather than probing for substrings) is what makes an EXTRA entry a
// failure too.
func TestChartDeclaresTheSameExportAndRequirements(t *testing.T) {
	manifest := readFixture(t, "manifest.yaml")
	chart := readFixture(t, "deploy", "chart", "templates", "catalogentry.yaml")

	for _, section := range []struct {
		key  string
		want string
	}{
		{
			key: "export:",
			want: strings.Join([]string{
				`export:`,
				`  name: "kuery.providers.railgrid.ai"`,
				`  resources:`,
				`    - name: savedviews`,
				`      apiVersion: "kuery.providers.railgrid.ai/v1alpha1"`,
				`      kind: SavedView`,
				`      verbs:`,
				`        - name: run`,
				`          description: "Run the saved view's query and return its result."`,
				`          readOnly: true`,
			}, "\n"),
		},
		{
			// Exactly two entries, and no more: the edges requirement the
			// engagement controller lives on (read-only on the kind, and the
			// kubernetesclusters/k8s coordinate with NO verbs, because the verb
			// is the capability and the generated claim spells every verb), plus
			// the built-in review API the proxied subresource gate runs through
			// the export virtual workspace. Anything else here — above all
			// serviceaccounts, secrets, clusterroles or clusterrolebindings — is
			// a contract violation rather than a design choice
			// (docs/provider-connectivity-contract.md §"Scoped identities"): a
			// provider asks the hub for an identity, it does not mint one.
			key: "requires:",
			want: strings.Join([]string{
				`requires:`,
				`  - provider: edges`,
				`    group: edges.railgrid.ai`,
				`    resources:`,
				`      - name: kubernetesclusters`,
				`        verbs: [get, list, watch]`,
				`      - name: kubernetesclusters/k8s`,
				`  - group: authorization.k8s.io`,
				`    resources:`,
				`      - name: subjectaccessreviews`,
				`        verbs: [create]`,
			}, "\n"),
		},
	} {
		for _, source := range []struct {
			where string
			text  string
		}{{"manifest.yaml", manifest}, {"deploy/chart/templates/catalogentry.yaml", chart}} {
			got := yamlBlock(t, source.where, source.text, section.key)
			if got != section.want {
				t.Errorf("%s spec.%s reads\n%s\n\nwant\n%s", source.where, section.key, got, section.want)
			}
		}
	}
}

// The declaration decodes, and what it declares is what the engagement
// controller needs: the edges requirement is a dependency edge (it names the
// provider), it is on the group edges providers SERVE rather than an APIExport
// name, and it carries no core-group entry — kuery claims no core type at all.
func TestManifestRequiresOnlyEdgesAndTheReviewAPI(t *testing.T) {
	var entry catalogEntry
	if err := yaml.Unmarshal([]byte(readFixture(t, "manifest.yaml")), &entry); err != nil {
		t.Fatalf("decode manifest.yaml: %v", err)
	}
	if len(entry.Spec.Requires) != 2 {
		t.Fatalf("manifest.yaml declares %d requirement(s): %+v; want exactly the edges entry and the review API", len(entry.Spec.Requires), entry.Spec.Requires)
	}
	edges, review := entry.Spec.Requires[0], entry.Spec.Requires[1]

	if edges.Provider != "edges" {
		t.Errorf("the %s requirement names provider %q; without it the hub would enable kuery in a workspace with no edges provider", edgesAPIGroup, edges.Provider)
	}
	if edges.Group != edgesAPIGroup {
		t.Errorf("the edges requirement is on group %q, want %q — the group the provider SERVES, not its APIExport name", edges.Group, edgesAPIGroup)
	}
	wantResources := map[string][]string{
		edgesResource:               {"get", "list", "watch"},
		edgesResource + "/" + "k8s": nil,
	}
	if len(edges.Resources) != len(wantResources) {
		t.Fatalf("the edges requirement claims %+v; want %v", edges.Resources, wantResources)
	}
	for _, resource := range edges.Resources {
		want, ok := wantResources[resource.Name]
		if !ok {
			t.Errorf("the edges requirement claims %q, which the engagement controller does not use", resource.Name)
			continue
		}
		if !slices.Equal(resource.Verbs, want) {
			// A "<resource>/<verb>" coordinate must carry no verbs at all: the
			// verb IS the capability, and the generated claim spells every one.
			t.Errorf("the edges requirement claims %s with verbs %v, want %v", resource.Name, resource.Verbs, want)
		}
	}

	if review.Provider != "" {
		t.Errorf("the %s requirement names provider %q; no provider serves a platform builtin and nothing has to be enabled first", review.Group, review.Provider)
	}
	if review.Group != "authorization.k8s.io" || len(review.Resources) != 1 ||
		review.Resources[0].Name != "subjectaccessreviews" || !slices.Equal(review.Resources[0].Verbs, []string{"create"}) {
		t.Errorf("manifest.yaml declares %+v; want exactly authorization.k8s.io subjectaccessreviews [create], the review API the proxied subresource gate needs", review)
	}
}

// yamlBlock returns the block introduced by key, with comment-only lines, blank
// lines and key's own indentation removed, so a manifest section and the chart's
// deeper-indented copy of it compare as the same text.
func yamlBlock(t *testing.T, where, text, key string) string {
	t.Helper()
	var out []string
	indent := -1
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		at := len(line) - len(strings.TrimLeft(line, " "))
		if indent < 0 {
			if trimmed == key {
				indent = at
				out = append(out, key)
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if at <= indent {
			break
		}
		out = append(out, line[indent:])
	}
	if indent < 0 {
		t.Fatalf("%s declares no spec.%s", where, key)
	}
	return strings.Join(out, "\n")
}

func readFixture(t *testing.T, parts ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{".."}, parts...)...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(raw)
}
