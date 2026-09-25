/*
Copyright 2026 The Railgrid Authors.

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

package apiexportgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseManifestReadsTheCatalogEntry(t *testing.T) {
	decl, err := LoadManifest(filepath.Join("testdata", "manifest.yaml"))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if decl.Name != "fixture.providers.railgrid.ai" {
		t.Errorf("name = %q, want fixture.providers.railgrid.ai", decl.Name)
	}
	if len(decl.PermissionClaims) != 3 {
		t.Fatalf("claims = %d, want 3", len(decl.PermissionClaims))
	}
	if got := decl.PermissionClaims[2]; got.Group != "rbac.authorization.k8s.io" || got.Resource != "clusterroles" {
		t.Errorf("third claim = %+v", got)
	}
	if !decl.PermissionClaims[0].TenantScoped {
		t.Error("tenantScoped was dropped while parsing")
	}
}

func TestParseManifestRejectsAManifestWithoutAnExport(t *testing.T) {
	if _, err := ParseManifest([]byte("kind: Secret\n")); err == nil || !strings.Contains(err.Error(), "no kind: CatalogEntry") {
		t.Fatalf("err = %v, want a missing-CatalogEntry error", err)
	}
	if _, err := ParseManifest([]byte("kind: CatalogEntry\nspec:\n  displayName: x\n")); err == nil || !strings.Contains(err.Error(), "spec.apiExport.name") {
		t.Fatalf("err = %v, want a missing-apiExport error", err)
	}
}

// The export shape is the contract with kcp: an empty group is omitted rather
// than written as "", tenantScoped never reaches the APIExport, and no
// identityHash is written (install stamps it per installation).
func TestExportClaimsUseTheKCPShape(t *testing.T) {
	claims := ExportClaims([]PermissionClaim{
		{Resource: "secrets", Verbs: []string{"get", "list"}, TenantScoped: true},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Verbs: []string{"get"}},
		{Resource: "namespaces"},
	})
	if len(claims) != 3 {
		t.Fatalf("claims = %d, want 3", len(claims))
	}
	first := claims[0].(map[string]any)
	if _, ok := first["group"]; ok {
		t.Errorf("empty group was written: %+v", first)
	}
	if _, ok := first["tenantScoped"]; ok {
		t.Errorf("tenantScoped leaked into the APIExport: %+v", first)
	}
	if _, ok := first["identityHash"]; ok {
		t.Errorf("identityHash must be left to install: %+v", first)
	}
	if got := first["verbs"].([]any); len(got) != 2 || got[0] != "get" {
		t.Errorf("verbs = %+v", got)
	}
	if got := claims[1].(map[string]any)["group"]; got != "rbac.authorization.k8s.io" {
		t.Errorf("group = %v", got)
	}
	if _, ok := claims[2].(map[string]any)["verbs"]; ok {
		t.Errorf("an empty verb list must be omitted: %+v", claims[2])
	}
}

func TestGenerateRenamesTheExportAndStampsTheClaims(t *testing.T) {
	content, err := Generate(Options{
		ManifestPath:     filepath.Join("testdata", "manifest.yaml"),
		APIGenExportPath: filepath.Join("testdata", "apigen-export.yaml"),
		SchemasDir:       filepath.Join("testdata", "schemas"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := string(content)
	want := Header + `apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: fixture.providers.railgrid.ai
spec:
  permissionClaims:
  - resource: secrets
    verbs:
    - get
    - list
    - watch
  - resource: serviceaccounts
    verbs:
    - get
    - create
  - group: rbac.authorization.k8s.io
    resource: clusterroles
    verbs:
    - get
    - create
  - group: edges.railgrid.ai
    resource: kubernetesclusters
    verbs:
    - get
    - list
    - watch
  resources:
  - group: fixture.railgrid.ai
    name: widgets
    schema: v260919-abc1234.widgets.fixture.railgrid.ai
    storage:
      crd: {}
`
	if got != want {
		t.Errorf("generated file mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// A schema that is NOT shipped (kuery's provider-private Engagement CRD) must
// not reach the export: apigen sees every CRD in config/crds, but only the
// chart's files/schemas are applied to the workspace and bindable by tenants.
func TestGenerateDropsResourcesTheProviderDoesNotShip(t *testing.T) {
	content, err := Generate(Options{
		ManifestPath:     filepath.Join("testdata", "manifest.yaml"),
		APIGenExportPath: filepath.Join("testdata", "apigen-export.yaml"),
		SchemasDir:       filepath.Join("testdata", "schemas"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(string(content), "engagements") {
		t.Errorf("a private CRD reached the APIExport:\n%s", content)
	}
}

// Without --schemas-dir every apigen resource is kept verbatim.
func TestGenerateKeepsEveryResourceWithoutASchemasDir(t *testing.T) {
	content, err := Generate(Options{
		ManifestPath:     filepath.Join("testdata", "manifest.yaml"),
		APIGenExportPath: filepath.Join("testdata", "apigen-export.yaml"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(content), "engagements") {
		t.Errorf("unfiltered generation dropped a resource:\n%s", content)
	}
}

// A shipped schema the export does not reference is a stale chart copy: the
// workspace would hold a schema no tenant can ever bind.
func TestGenerateFailsWhenAShippedSchemaIsUnreferenced(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orphan.yaml"), []byte("apiVersion: apis.kcp.io/v1alpha1\nkind: APIResourceSchema\nmetadata:\n  name: v1.orphans.fixture.railgrid.ai\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Generate(Options{
		ManifestPath:     filepath.Join("testdata", "manifest.yaml"),
		APIGenExportPath: filepath.Join("testdata", "apigen-export.yaml"),
		SchemasDir:       dir,
	})
	if err == nil || !strings.Contains(err.Error(), "v1.orphans.fixture.railgrid.ai") {
		t.Fatalf("err = %v, want an unreferenced-schema error", err)
	}
}

// infrastructure mints its schemas at runtime, so it generates an export with
// no resources at all; install merges that with the runtime writers' entries.
func TestGenerateWithoutApigenOutputYieldsAnEmptyResourceList(t *testing.T) {
	content, err := Generate(Options{ManifestPath: filepath.Join("testdata", "manifest.yaml")})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(content), "resources: []") {
		t.Errorf("want an empty resource list, got:\n%s", content)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	opts := Options{
		ManifestPath:     filepath.Join("testdata", "manifest.yaml"),
		APIGenExportPath: filepath.Join("testdata", "apigen-export.yaml"),
		SchemasDir:       filepath.Join("testdata", "schemas"),
	}
	first, err := Generate(opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := Generate(opts)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("run %d differs from the first", i)
		}
	}
}

func TestSchemaNamesAreSorted(t *testing.T) {
	names, err := SchemaNames(filepath.Join("testdata", "schemas"))
	if err != nil {
		t.Fatalf("SchemaNames: %v", err)
	}
	if len(names) != 1 || names[0] != "v260919-abc1234.widgets.fixture.railgrid.ai" {
		t.Errorf("names = %v", names)
	}
}

// TestExportClaimsRenderTheSelectorAsDefaultSelector pins the one translation
// this generator does on a claim's scope: the manifest calls it `selector`,
// kcp's APIExport calls it `defaultSelector`. Getting the name wrong is silent
// — kcp ignores an unknown field on a claim and serves the claim unscoped, so
// the provider would keep blanket access with a manifest that says otherwise.
func TestExportClaimsRenderTheSelectorAsDefaultSelector(t *testing.T) {
	claims := ExportClaims([]PermissionClaim{
		{
			Resource:     "secrets",
			Verbs:        []string{"get"},
			TenantScoped: true,
			Selector:     &PermissionClaimSelector{MatchLabels: map[string]string{"railgrid.ai/owner": "fixture"}},
		},
		{Resource: "configmaps", Verbs: []string{"get"}},
		{Resource: "namespaces", Verbs: []string{"get"}, Selector: &PermissionClaimSelector{}},
	})

	scoped := claims[0].(map[string]any)
	if _, ok := scoped["selector"]; ok {
		t.Errorf("the manifest spelling leaked into the APIExport: %+v", scoped)
	}
	selector, ok := scoped["defaultSelector"].(map[string]any)
	if !ok {
		t.Fatalf("defaultSelector = %+v, want a mapping", scoped["defaultSelector"])
	}
	matchLabels, ok := selector["matchLabels"].(map[string]any)
	if !ok {
		t.Fatalf("matchLabels = %+v, want a mapping", selector["matchLabels"])
	}
	if got := matchLabels["railgrid.ai/owner"]; got != "fixture" {
		t.Errorf("matchLabels[railgrid.ai/owner] = %v, want fixture", got)
	}

	// No selector, and an empty one, both mean "unscoped" — kcp reads an
	// absent defaultSelector as matchAll, and writing an empty mapping instead
	// would be a selector that matches nothing.
	if _, ok := claims[1].(map[string]any)["defaultSelector"]; ok {
		t.Errorf("an unscoped claim grew a defaultSelector: %+v", claims[1])
	}
	if _, ok := claims[2].(map[string]any)["defaultSelector"]; ok {
		t.Errorf("an empty selector must be omitted, not written: %+v", claims[2])
	}
}

// TestGenerateRoundTripsTheSelector checks the whole path from the manifest
// file to the rendered YAML, since the field has to survive both the
// CatalogEntry parse and the export marshal.
func TestGenerateRoundTripsTheSelector(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "manifest.yaml")
	if err := os.WriteFile(manifest, []byte(`apiVersion: providers.railgrid.ai/v1alpha1
kind: CatalogEntry
metadata:
  name: fixture
spec:
  apiExport:
    name: fixture.providers.railgrid.ai
    permissionClaims:
      - resource: secrets
        verbs: [get]
        tenantScoped: true
        selector:
          matchLabels:
            railgrid.ai/owner: fixture
`), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := Generate(Options{ManifestPath: manifest})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(content), "defaultSelector:") ||
		!strings.Contains(string(content), "railgrid.ai/owner: fixture") {
		t.Fatalf("the selector did not reach the generated export:\n%s", content)
	}
}

// --- composition claims (identity-agnostic, always on) ----------------------

func TestParseCompositionsFlattensDependenciesInManifestOrder(t *testing.T) {
	compositions, err := LoadCompositions(filepath.Join("testdata", "manifest.yaml"))
	if err != nil {
		t.Fatalf("LoadCompositions: %v", err)
	}
	if len(compositions) != 2 {
		t.Fatalf("compositions = %d, want 2: %+v", len(compositions), compositions)
	}
	if got := compositions[0]; got.Group != "edges.railgrid.ai" || got.Resource != "kubernetesclusters" || len(got.Verbs) != 3 {
		t.Errorf("first composition = %+v", got)
	}
	if got := compositions[1]; got.Group != "rbac.authorization.k8s.io" || got.Resource != "clusterroles" {
		t.Errorf("second composition = %+v", got)
	}
}

// A manifest with no dependencies at all must yield no compositions rather
// than an error: most providers compose nothing.
func TestParseCompositionsToleratesAManifestWithoutDependencies(t *testing.T) {
	compositions, err := ParseCompositions([]byte("kind: CatalogEntry\nspec:\n  apiExport:\n    name: x\n"))
	if err != nil {
		t.Fatalf("ParseCompositions: %v", err)
	}
	if len(compositions) != 0 {
		t.Errorf("compositions = %+v, want none", compositions)
	}
}

// There is no opt-in any more: the fixture manifest declares compositions, so
// the generated export claims them. Every composition the manifest does not
// already claim is appended AFTER the manifest's own claims, with its own verbs
// and NO identityHash — the field kcp resolves per consumer workspace. The
// exact bytes are pinned by TestGenerateRenamesTheExportAndStampsTheClaims;
// this checks the rule rather than the rendering.
func TestGenerateAlwaysAppendsOneClaimPerComposition(t *testing.T) {
	content, err := Generate(Options{
		ManifestPath:     filepath.Join("testdata", "manifest.yaml"),
		APIGenExportPath: filepath.Join("testdata", "apigen-export.yaml"),
		SchemasDir:       filepath.Join("testdata", "schemas"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := string(content)
	// The fixture composes kubernetesclusters.edges.railgrid.ai (new) and
	// clusterroles.rbac.authorization.k8s.io (already claimed by the manifest,
	// with a NARROWER verb set): one claim is appended, not two.
	if !strings.Contains(got, "resource: kubernetesclusters") {
		t.Errorf("the composition did not reach the export:\n%s", got)
	}
	if n := strings.Count(got, "resource: clusterroles"); n != 1 {
		t.Errorf("clusterroles was claimed %d time(s), want 1 (the manifest's own)", n)
	}
	if strings.Contains(got, "- delete") {
		t.Errorf("the composition widened the manifest's clusterroles claim:\n%s", got)
	}
	// The appended claim comes last, after every manifest claim.
	if strings.Index(got, "resource: kubernetesclusters") < strings.Index(got, "resource: clusterroles") {
		t.Errorf("a composition claim was written before the manifest's own:\n%s", got)
	}
	// The header prose mentions identityHash, so check the body only.
	if body := strings.TrimPrefix(got, Header); strings.Contains(body, "identityHash") {
		t.Errorf("an identityHash was written; the claim must stay identity-agnostic:\n%s", body)
	}
}

// The fixture's second composition repeats the manifest's own clusterroles
// claim with a WIDER verb set. The manifest entry must survive untouched and
// nothing may be appended for it: a hand-written claim is a deliberate
// statement about kcp's wire contract and a composition never widens it.
func TestMergeCompositionClaimsKeepsTheManifestClaimOnADuplicate(t *testing.T) {
	claims := MergeCompositionClaims(
		[]PermissionClaim{
			{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Verbs: []string{"get", "create"}, TenantScoped: true},
			{Resource: "secrets", Verbs: []string{"get"}},
		},
		[]Composition{
			{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Verbs: []string{"get", "create", "delete"}},
			{Group: "", Resource: "secrets", Verbs: []string{"get", "list"}},
			{Group: "edges.railgrid.ai", Resource: "kubernetesclusters", Verbs: []string{"get"}},
		},
	)
	if len(claims) != 3 {
		t.Fatalf("claims = %d, want 3 (2 manifest + 1 new): %+v", len(claims), claims)
	}
	if got := claims[0]; len(got.Verbs) != 2 || !got.TenantScoped {
		t.Errorf("the manifest claim was rewritten by the composition: %+v", got)
	}
	if got := claims[1]; len(got.Verbs) != 1 {
		t.Errorf("a core-group duplicate was not matched on the empty group: %+v", got)
	}
	if got := claims[2]; got.Group != "edges.railgrid.ai" || got.Resource != "kubernetesclusters" {
		t.Errorf("third claim = %+v, want the composed kind", got)
	}
}

// A composition listed twice (two dependencies naming the same kind) must not
// produce two claims: kcp rejects an export claiming the same group/resource
// more than once.
func TestMergeCompositionClaimsDeduplicatesCompositionsAgainstEachOther(t *testing.T) {
	claims := MergeCompositionClaims(nil, []Composition{
		{Group: "edges.railgrid.ai", Resource: "kubernetesclusters", Verbs: []string{"get"}},
		{Group: "edges.railgrid.ai", Resource: "kubernetesclusters", Verbs: []string{"get", "list"}},
	})
	if len(claims) != 1 {
		t.Fatalf("claims = %+v, want one", claims)
	}
	if len(claims[0].Verbs) != 1 {
		t.Errorf("the first occurrence must win: %+v", claims[0])
	}
}

// Merging must not alias the caller's verb slice, or a later append would
// rewrite the manifest's composition in place.
func TestMergeCompositionClaimsCopiesTheVerbs(t *testing.T) {
	composition := Composition{Group: "edges.railgrid.ai", Resource: "kubernetesclusters", Verbs: []string{"get", "list"}}
	claims := MergeCompositionClaims(nil, []Composition{composition})
	claims[0].Verbs[0] = "delete"
	if composition.Verbs[0] != "get" {
		t.Errorf("the composition's verbs were aliased: %+v", composition.Verbs)
	}
}

func TestGenerateWithCompositionClaimsIsDeterministic(t *testing.T) {
	opts := Options{
		ManifestPath:     filepath.Join("testdata", "manifest.yaml"),
		APIGenExportPath: filepath.Join("testdata", "apigen-export.yaml"),
		SchemasDir:       filepath.Join("testdata", "schemas"),
	}
	first, err := Generate(opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := Generate(opts)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("run %d differs from the first", i)
		}
	}
}
