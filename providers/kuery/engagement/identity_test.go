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
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// fakeIdentityHub stands in for the hub identity service: it records every
// request and hands back a numbered token, so a test can tell a refresh from
// a re-mint and read back the rules that were asked for.
type catalogEntry struct {
	Spec struct {
		APIExport *struct {
			PermissionClaims []map[string]any `json:"permissionClaims"`
		} `json:"apiExport"`
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

// Where the list and the watch went: kuery's generated APIExport carries a
// permission claim on the edges provider's clusters, and that claim carries NO
// identityHash. kcp resolves an unpinned first-party claim per CONSUMER
// workspace when a cluster-scoped PermissionClaimPolicy pairs the claiming
// export's group with the claimed one — which is the property the composition
// was invented for, and the reason reading edges no longer needs a credential.
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
// the chart's is the one that reaches production. A composition that drifts
// between them is a consent prompt that does not match what the hub will mint.
func TestChartDeclaresTheSameComposition(t *testing.T) {
	chart := readFixture(t, "deploy", "chart", "templates", "catalogentry.yaml")
	for _, want := range []string{
		"      dependencies:",
		"        - name: edges",
		"          composes:",
		"            - group: edges.railgrid.ai",
		"              resource: kubernetesclusters",
		`              verbs: ["get", "list", "watch"]`,
	} {
		if !strings.Contains(chart, want) {
			t.Fatalf("the chart's CatalogEntry is missing %q; it must declare the same composition as manifest.yaml", want)
		}
	}
	// And neither may grow a hand-written DATA claim: the one data claim kuery
	// carries is DERIVED from the composition above by apiexportgen and lands
	// in the generated APIExport. The single claim the chart may declare is the
	// review API the proxied subresource gate runs through the export virtual
	// workspace (authorization.k8s.io/subjectaccessreviews, create), which kcp
	// serves there only for an export that claims it. Comments are stripped
	// first — the ones explaining the claim naturally name the types involved.
	// The chart is a Helm template, so it is scanned line by line rather than
	// decoded.
	// Only the permissionClaims block is inspected: the data-plane verbs name
	// kuery's own resources and the composition is asserted above.
	sawReview := false
	claimsIndent := -1
	for _, line := range strings.Split(chart, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case trimmed == "permissionClaims:":
			claimsIndent = indent
			continue
		case claimsIndent >= 0 && indent <= claimsIndent:
			claimsIndent = -1
		}
		if claimsIndent < 0 {
			continue
		}
		if resource, ok := strings.CutPrefix(strings.TrimPrefix(trimmed, "- "), "resource: "); ok {
			if resource = strings.Trim(resource, `"`); resource != "subjectaccessreviews" {
				t.Fatalf("the chart's CatalogEntry hand-writes a claim on %q; kuery's only hand-written claim is the subjectaccessreviews review API", resource)
			}
			sawReview = true
		}
	}
	if !sawReview {
		t.Fatal("the chart's CatalogEntry lacks the subjectaccessreviews claim the subresource gate needs")
	}
}

// assertOnlyTheReviewClaim fails unless claims is exactly the one claim a
// provider with custom subresources must hold: create on
// authorization.k8s.io/subjectaccessreviews. Anything else — above all a claim
// on serviceaccounts, secrets, clusterroles or clusterrolebindings — is a
// contract violation for kuery.
func assertOnlyTheReviewClaim(t *testing.T, where string, claims []map[string]any) {
	t.Helper()
	if len(claims) != 1 {
		t.Fatalf("%s hand-writes claims %v; kuery's only hand-written claim is the subjectaccessreviews review API, its data claim is derived from its composition", where, claims)
	}
	c := claims[0]
	verbs, _ := c["verbs"].([]any)
	if c["group"] != "authorization.k8s.io" || c["resource"] != "subjectaccessreviews" || len(verbs) != 1 || verbs[0] != "create" || c["tenantScoped"] != true {
		t.Fatalf("%s hand-writes claim %v; want exactly {authorization.k8s.io subjectaccessreviews [create] tenantScoped}", where, c)
	}
}

// The manifest hand-writes exactly one claim, and it is not about data: the
// review API the proxied subresource gate runs through the export virtual
// workspace. The edges claim in the generated APIExport is derived from the
// composition (see the test above); what must never appear here is a claim on
// serviceaccounts, secrets, clusterroles or clusterrolebindings, which is a
// contract violation rather than a design choice
// (docs/provider-connectivity-contract.md §"Scoped identities") — a provider
// asks the hub for an identity, it does not mint one.
func TestManifestClaimsOnlyTheReviewAPI(t *testing.T) {
	var entry catalogEntry
	if err := yaml.Unmarshal([]byte(readFixture(t, "manifest.yaml")), &entry); err != nil {
		t.Fatalf("decode manifest.yaml: %v", err)
	}
	if entry.Spec.APIExport == nil {
		t.Fatal("manifest.yaml declares no apiExport")
	}
	assertOnlyTheReviewClaim(t, "manifest.yaml", entry.Spec.APIExport.PermissionClaims)
}

func declaredComposition(t *testing.T) map[string]string {
	t.Helper()
	var entry catalogEntry
	if err := yaml.Unmarshal([]byte(readFixture(t, "manifest.yaml")), &entry); err != nil {
		t.Fatalf("decode manifest.yaml: %v", err)
	}
	out := map[string]string{}
	for _, dependency := range entry.Spec.Dependencies {
		for _, composed := range dependency.Composes {
			if composed.Group == "" {
				t.Fatalf("dependency %q composes a core-group resource: %+v", dependency.Name, composed)
			}
			out[composed.Group+"/"+composed.Resource] = normalizeVerbs(composed.Verbs)
		}
	}
	if len(out) == 0 {
		t.Fatal("manifest.yaml declares no composition; the engagement identity would be refused in every workspace")
	}
	return out
}

func readFixture(t *testing.T, parts ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{".."}, parts...)...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(raw)
}

func normalizeVerbs(verbs []string) string {
	copied := append([]string(nil), verbs...)
	sort.Strings(copied)
	out := copied[:0]
	for i, verb := range copied {
		if i == 0 || verb != copied[i-1] {
			out = append(out, verb)
		}
	}
	return strings.Join(out, ",")
}
