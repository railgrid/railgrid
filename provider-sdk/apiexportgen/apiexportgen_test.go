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

func TestParseExportReadsTheCatalogEntry(t *testing.T) {
	decl, err := LoadExport(filepath.Join("testdata", "manifest.yaml"))
	if err != nil {
		t.Fatalf("LoadExport: %v", err)
	}
	if decl.Name != "fixture.providers.railgrid.ai" {
		t.Errorf("name = %q, want fixture.providers.railgrid.ai", decl.Name)
	}
}

func TestParseExportRejectsAManifestWithoutAnExport(t *testing.T) {
	if _, err := ParseExport([]byte("kind: Secret\n")); err == nil || !strings.Contains(err.Error(), "no kind: CatalogEntry") {
		t.Fatalf("err = %v, want a missing-CatalogEntry error", err)
	}
	if _, err := ParseExport([]byte("kind: CatalogEntry\nspec:\n  displayName: x\n")); err == nil || !strings.Contains(err.Error(), "spec.export.name") {
		t.Fatalf("err = %v, want a missing-export error", err)
	}
}

// The export shape is the contract with kcp: an empty group is omitted rather
// than written as "", no tenant-scope field reaches the APIExport (everything
// under spec.requires is tenant-scoped by definition and kcp has no
// counterpart), and no identityHash is written.
func TestExportClaimsUseTheKCPShape(t *testing.T) {
	claims, err := ExportClaims([]Requirement{
		{Resources: []RequiredResource{
			{Name: "secrets", Verbs: []string{"get", "list"}},
			{Name: "serviceaccounts", Verbs: []string{"get"}},
		}},
		{Group: "rbac.authorization.k8s.io", Resources: []RequiredResource{
			{Name: "clusterroles", Verbs: []string{"get"}},
		}},
	})
	if err != nil {
		t.Fatalf("ExportClaims: %v", err)
	}
	if len(claims) != 3 {
		t.Fatalf("claims = %d, want 3", len(claims))
	}
	first := claims[0].(map[string]any)
	if _, ok := first["group"]; ok {
		t.Errorf("the core group was written: %+v", first)
	}
	if _, ok := first["tenantScoped"]; ok {
		t.Errorf("a tenant-scope field leaked into the APIExport: %+v", first)
	}
	if _, ok := first["identityHash"]; ok {
		t.Errorf("identityHash must be left to install: %+v", first)
	}
	if got := first["verbs"].([]any); len(got) != 2 || got[0] != "get" {
		t.Errorf("verbs = %+v", got)
	}
	if got := claims[2].(map[string]any)["group"]; got != "rbac.authorization.k8s.io" {
		t.Errorf("group = %v", got)
	}
}

// A verb coordinate claims the verb WHOLE: kcp authorizes the HTTP method as
// the RBAC verb on a custom subresource, so the claim spells every verb rather
// than guessing which method the serving provider chose.
func TestExportClaimsSpellEveryVerbOnAVerbCoordinate(t *testing.T) {
	claims, err := ExportClaims([]Requirement{{
		Provider: "infrastructure",
		Group:    "infrastructure.railgrid.ai",
		Resources: []RequiredResource{
			{Name: "instances", Verbs: []string{"get", "create"}},
			{Name: "instances/exec"},
		},
	}})
	if err != nil {
		t.Fatalf("ExportClaims: %v", err)
	}
	coordinate := claims[1].(map[string]any)
	if coordinate["resource"] != "instances/exec" || coordinate["group"] != "infrastructure.railgrid.ai" {
		t.Fatalf("coordinate claim = %+v", coordinate)
	}
	verbs, ok := coordinate["verbs"].([]any)
	if !ok || len(verbs) != 1 || verbs[0] != "*" {
		t.Errorf("verbs = %+v, want the wildcard kcp's claim authorizer accepts", coordinate["verbs"])
	}
	// The provider name drives the hub's Enable ordering and has no place on
	// an APIExport.
	if _, ok := coordinate["provider"]; ok {
		t.Errorf("the provider name leaked into the APIExport: %+v", coordinate)
	}
}

// A group belongs to one provider, so spec.requires holds ONE entry per group.
// A second entry is a lost edit, not something to merge: whichever list is read
// second would silently disappear, and the export would claim less than its
// author wrote.
func TestExportClaimsRefusesARepeatedGroup(t *testing.T) {
	_, err := ExportClaims([]Requirement{
		{Provider: "code", Group: "code.railgrid.ai", Resources: []RequiredResource{{Name: "repositories", Verbs: []string{"get"}}}},
		{Group: "rbac.authorization.k8s.io", Resources: []RequiredResource{{Name: "clusterroles", Verbs: []string{"get"}}}},
		{Provider: "code", Group: "code.railgrid.ai", Resources: []RequiredResource{{Name: "connections", Verbs: []string{"get"}}}},
	})
	if err == nil {
		t.Fatal("a repeated group was accepted")
	}
	if !strings.Contains(err.Error(), "code.railgrid.ai") || !strings.Contains(err.Error(), "twice") {
		t.Errorf("err does not name the repeated group: %v", err)
	}
	// The core group is a group too, even though it is written as an absent
	// `group` field on both entries.
	_, err = ExportClaims([]Requirement{
		{Resources: []RequiredResource{{Name: "secrets", Verbs: []string{"get"}}}},
		{Resources: []RequiredResource{{Name: "configmaps", Verbs: []string{"get"}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "core") {
		t.Errorf("err = %v, want the core group refused as a repeat", err)
	}
}

// kcp refuses an APIExport claiming one group/resource more than once, so the
// generator refuses it first, naming the coordinate.
func TestExportClaimsRefusesADuplicateResourceInOneGroup(t *testing.T) {
	_, err := ExportClaims([]Requirement{{
		Group: "code.railgrid.ai",
		Resources: []RequiredResource{
			{Name: "repositories", Verbs: []string{"get"}},
			{Name: "repositories", Verbs: []string{"get", "list"}},
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "repositories") {
		t.Fatalf("err = %v, want a duplicate-resource refusal", err)
	}
}

// Everything the CatalogEntry contract forbids on a requirement is refused
// here as well, rather than generating a claim that says more (or less) than
// its author did.
func TestExportClaimsRefusesWhatTheContractForbids(t *testing.T) {
	for name, requirement := range map[string]Requirement{
		"verbs on a verb coordinate": {
			Group:     "infrastructure.railgrid.ai",
			Resources: []RequiredResource{{Name: "instances/exec", Verbs: []string{"create"}}},
		},
		"selector on a verb coordinate": {
			Group: "infrastructure.railgrid.ai",
			Resources: []RequiredResource{{
				Name:     "instances/exec",
				Selector: &LabelSelector{MatchLabels: map[string]string{"a": "b"}},
			}},
		},
		"a plain resource with no verbs": {
			Group:     "infrastructure.railgrid.ai",
			Resources: []RequiredResource{{Name: "instances"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ExportClaims([]Requirement{requirement})
			if err == nil {
				t.Fatal("the declaration was accepted")
			}
			if !strings.Contains(err.Error(), requirement.Resources[0].Name) {
				t.Errorf("err does not name the resource: %v", err)
			}
		})
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
  - defaultSelector:
      matchLabels:
        railgrid.ai/owner: fixture
    resource: secrets
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
  - group: edges.railgrid.ai
    resource: kubernetesclusters/kubeconfig
    verbs:
    - '*'
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
	claims, err := ExportClaims([]Requirement{{Resources: []RequiredResource{
		{
			Name:     "secrets",
			Verbs:    []string{"get"},
			Selector: &LabelSelector{MatchLabels: map[string]string{"railgrid.ai/owner": "fixture"}},
		},
		{Name: "configmaps", Verbs: []string{"get"}},
		{Name: "namespaces", Verbs: []string{"get"}, Selector: &LabelSelector{}},
	}}})
	if err != nil {
		t.Fatalf("ExportClaims: %v", err)
	}

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
  export:
    name: fixture.providers.railgrid.ai
  requires:
    - resources:
        - name: secrets
          verbs: [get]
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

// --- spec.requires, the one source of the claims -----------------------------

func TestParseRequirementsReadsEveryGroupInManifestOrder(t *testing.T) {
	requirements, err := LoadRequirements(filepath.Join("testdata", "manifest.yaml"))
	if err != nil {
		t.Fatalf("LoadRequirements: %v", err)
	}
	if len(requirements) != 3 {
		t.Fatalf("requirements = %d, want 3: %+v", len(requirements), requirements)
	}
	if got := requirements[0]; got.Group != "" || got.Provider != "" || len(got.Resources) != 2 {
		t.Errorf("first requirement = %+v, want the core group's two claims", got)
	}
	if got := requirements[0].Resources[0]; got.Selector == nil || got.Selector.MatchLabels["railgrid.ai/owner"] != "fixture" {
		t.Errorf("the core secrets selector was dropped while parsing: %+v", got)
	}
	if got := requirements[2]; got.Provider != "edges" || got.Group != "edges.railgrid.ai" {
		t.Errorf("third requirement = %+v", got)
	}
	// A verb coordinate carries no verbs: the generated claim spells them all.
	if got := requirements[2].Resources[1]; got.Name != "kubernetesclusters/kubeconfig" || len(got.Verbs) != 0 {
		t.Errorf("verb coordinate = %+v", got)
	}
}

// A manifest that needs nothing from anybody must yield no requirements rather
// than an error: a self-contained provider is a normal provider.
func TestParseRequirementsToleratesAManifestWithoutRequirements(t *testing.T) {
	requirements, err := ParseRequirements([]byte("kind: CatalogEntry\nspec:\n  export:\n    name: x\n"))
	if err != nil {
		t.Fatalf("ParseRequirements: %v", err)
	}
	if len(requirements) != 0 {
		t.Errorf("requirements = %+v, want none", requirements)
	}
}

// The refusal reaches the generator, not just the helper: a manifest that
// repeats a group fails codegen with the group named.
func TestGenerateFailsOnARepeatedGroupInRequires(t *testing.T) {
	manifest := writeFixture(t, "manifest.yaml", `kind: CatalogEntry
spec:
  export:
    name: fixture.providers.railgrid.ai
  requires:
    - provider: code
      group: code.railgrid.ai
      resources:
        - name: repositories
          verbs: [get]
    - provider: code
      group: code.railgrid.ai
      resources:
        - name: repositories/commit
`)
	_, err := Generate(Options{ManifestPath: manifest})
	if err == nil || !strings.Contains(err.Error(), "code.railgrid.ai") {
		t.Fatalf("err = %v, want a repeated-group refusal naming the group", err)
	}
}
