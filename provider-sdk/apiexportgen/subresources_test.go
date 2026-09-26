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

	"sigs.k8s.io/yaml"
)

// subresourceManifest declares verbs and actions on two resources, one of which
// the export does not carry, plus an action whose version must stay out of its
// coordinate.
const subresourceManifest = `apiVersion: providers.railgrid.ai/v1alpha1
kind: CatalogEntry
metadata:
  name: fixture
spec:
  export:
    name: fixture.providers.railgrid.ai
    resources:
      - name: widgets
        apiVersion: fixture.railgrid.ai/v1alpha1
        kind: Widget
        verbs:
          - name: proxy
            stream: true
          - name: exec
        actions:
          - name: rebuild
            version: v2
      - name: runtimeonly
        apiVersion: fixture.railgrid.ai/v1alpha1
        kind: RuntimeOnly
        verbs:
          - name: restart
`

// apigenExport is what apigen leaves behind: every kind of the group listed as
// a resource — the stored Widget AND the "<Verb>Request" kinds the API package
// declares for its verbs, which the generator has to set aside.
const apigenExport = `apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: fixture.railgrid.ai
spec:
  resources:
  - group: fixture.railgrid.ai
    name: widgets
    schema: v1.widgets.fixture.railgrid.ai
    storage:
      crd: {}
  - group: fixture.railgrid.ai
    name: exec
    schema: v1.exec.fixture.railgrid.ai
    storage:
      crd: {}
  - group: fixture.railgrid.ai
    name: proxy
    schema: v1.proxy.fixture.railgrid.ai
    storage:
      crd: {}
  - group: fixture.railgrid.ai
    name: rebuild
    schema: v1.rebuild.fixture.railgrid.ai
    storage:
      crd: {}
`

// fixtureKinds are the schemas apigen writes beside that export: plural → kind.
var fixtureKinds = map[string]string{
	"widgets": "Widget",
	"exec":    "ExecRequest",
	"proxy":   "ProxyRequest",
	"rebuild": "RebuildRequest",
}

func schemaYAML(group, plural, kind string) string {
	return "apiVersion: apis.kcp.io/v1alpha1\nkind: APIResourceSchema\nmetadata:\n  name: v1." + plural + "." + group +
		"\nspec:\n  group: " + group + "\n  names:\n    kind: " + kind + "\n    plural: " + plural + "\n  scope: Cluster\n"
}

// writeAPIGenDir lays out an apigen output dir — the group-named export plus
// one schema file per kind — and returns the export's path. kinds defaults to
// fixtureKinds; a caller drops entries to simulate a verb without a type.
func writeAPIGenDir(t *testing.T, export string, kinds map[string]string) string {
	t.Helper()
	if kinds == nil {
		kinds = fixtureKinds
	}
	dir := t.TempDir()
	for plural, kind := range kinds {
		if err := os.WriteFile(filepath.Join(dir, "apiresourceschema-"+plural+".fixture.railgrid.ai.yaml"), []byte(schemaYAML("fixture.railgrid.ai", plural, kind)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "apiexport-fixture.railgrid.ai.yaml")
	if err := os.WriteFile(path, []byte(export), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFixture(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func exportResources(t *testing.T, content []byte) []map[string]any {
	t.Helper()
	var export struct {
		Spec struct {
			Resources []map[string]any `json:"resources"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(content, &export); err != nil {
		t.Fatalf("parsing generated export: %v", err)
	}
	return export.Spec.Resources
}

// Both declarations land on one coordinate and both are sorted together: file
// order would otherwise shuffle unrelated entries into the diff every time an
// action is added beside a verb.
func TestParseSubresourcesCollectsVerbsAndActionsSorted(t *testing.T) {
	subresources, err := ParseSubresources([]byte(subresourceManifest))
	if err != nil {
		t.Fatalf("ParseSubresources: %v", err)
	}
	want := []string{"runtimeonly/restart", "widgets/exec", "widgets/proxy", "widgets/rebuild"}
	got := make([]string, 0, len(subresources))
	for _, sub := range subresources {
		got = append(got, sub.Name())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("names = %v, want %v", got, want)
	}
}

// An action's coordinate is its NAME. The version qualifies the action's schema
// contract, appears in no path, and the APIExport says nothing about it: the
// declaration `{name: rebuild, version: v2}` publishes widgets/rebuild and
// nothing that spells v2.
func TestParseSubresourcesUsesTheActionNameWithoutItsVersion(t *testing.T) {
	subresources, err := ParseSubresources([]byte(subresourceManifest))
	if err != nil {
		t.Fatalf("ParseSubresources: %v", err)
	}
	var found bool
	for _, sub := range subresources {
		if strings.Contains(sub.Name(), "v2") {
			t.Errorf("the action's version reached the coordinate %q", sub.Name())
		}
		if sub.Name() == "widgets/rebuild" {
			found = true
		}
	}
	if !found {
		t.Errorf("no widgets/rebuild among %+v", subresources)
	}
}

// A verb and an action share one coordinate namespace on a resource. kcp's
// spec.resources is a list keyed by name, so declaring one twice is not a
// duplicate statement but a lost one: the second declaration would silently
// never be published.
func TestParseSubresourcesRefusesACoordinateDeclaredTwice(t *testing.T) {
	manifest := `kind: CatalogEntry
spec:
  export:
    name: fixture.providers.railgrid.ai
    resources:
      - name: widgets
        apiVersion: fixture.railgrid.ai/v1alpha1
        kind: Widget
        verbs:
          - name: rebuild
        actions:
          - name: rebuild
            version: v1
`
	_, err := ParseSubresources([]byte(manifest))
	if err == nil || !strings.Contains(err.Error(), "widgets/rebuild") {
		t.Fatalf("err = %v, want a refusal naming the coordinate", err)
	}
}

// status and scale belong to the object's shape and are declared on the
// APIResourceSchema; kcp's APIExport admission refuses them by name. The
// refusal is not conditional on the parent being exported — the coordinate is
// unroutable the day it is written.
func TestValidateSubresourceNamesRefusesStatusAndScale(t *testing.T) {
	for _, verb := range []string{"status", "scale"} {
		err := ValidateSubresourceNames([]Subresource{{Resource: "runtimeonly", Verb: verb, Source: "spec.export.resources[runtimeonly].verbs"}})
		if err == nil || !strings.Contains(err.Error(), "runtimeonly/"+verb) {
			t.Errorf("verb %q: err = %v, want a refusal naming it", verb, err)
		}
	}
}

// One bad name makes the WHOLE export unappliable, so the check runs here
// rather than surfacing at init as a kcp admission error on an unrelated field.
func TestValidateSubresourceNamesRefusesNamesKCPRejects(t *testing.T) {
	err := ValidateSubresourceNames([]Subresource{
		{Resource: "repositories", Verb: "stage_upload", Source: "spec.export.resources[repositories].verbs"},
		{Resource: "repositories", Verb: "mint_token", Source: "spec.export.resources[repositories].actions"},
	})
	if err == nil {
		t.Fatal("an underscore was accepted; kcp's spec.resources[].name pattern rejects it")
	}
	for _, want := range []string{"repositories/stage_upload", "repositories/mint_token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err does not name %q: %v", want, err)
		}
	}
}

func TestValidateSubresourceNamesAcceptsHyphens(t *testing.T) {
	if err := ValidateSubresourceNames([]Subresource{{Resource: "projects", Verb: "set-repository"}}); err != nil {
		t.Errorf("hyphenated verb refused: %v", err)
	}
}

// The group is read off the parent's own entry, never guessed: a provider
// serving several groups would otherwise get the wrong one, and kcp refuses an
// entry whose parent is not exported by the same APIExport.
func TestGenerateEmitsOneEntryPerCoordinateWithTheParentsGroup(t *testing.T) {
	content, err := Generate(Options{
		ManifestPath:     writeFixture(t, "manifest.yaml", subresourceManifest),
		APIGenExportPath: writeAPIGenDir(t, apigenExport, nil),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	resources := exportResources(t, content)
	if len(resources) != 4 {
		t.Fatalf("resources = %d, want the parent plus three subresources: %v", len(resources), resources)
	}
	entry := resources[1]
	if entry["name"] != "widgets/exec" {
		t.Errorf("first subresource = %v, want widgets/exec", entry["name"])
	}
	if entry["group"] != "fixture.railgrid.ai" {
		t.Errorf("group = %v, want the parent's", entry["group"])
	}
	// kcp's admission requires the schema to END IN ".<verb>.<group>", naming
	// the subresource's own kind: the ExecRequest schema apigen minted.
	if entry["schema"] != "v1.exec.fixture.railgrid.ai" {
		t.Errorf("schema = %v", entry["schema"])
	}
	storage, _ := entry["storage"].(map[string]any)
	virtual, _ := storage["virtual"].(map[string]any)
	reference, _ := virtual["reference"].(map[string]any)
	if storage["crd"] != nil {
		t.Error("a custom subresource must use virtual storage, never crd")
	}
	if reference["apiGroup"] != "dataplane.railgrid.ai" || reference["kind"] != "DataPlaneEndpointSlice" {
		t.Errorf("reference = %v", reference)
	}
	if reference["name"] != "fixture.providers.railgrid.ai" {
		t.Errorf("reference name = %v, want the provider's APIExport name", reference["name"])
	}
}

// One provider (infrastructure) mints its resource entries at runtime and
// install merges them in. Refusing would mean it could never generate an export
// at all, so a coordinate with no parent entry is skipped.
func TestGenerateSkipsACoordinateWhoseParentIsNotExported(t *testing.T) {
	content, err := Generate(Options{
		ManifestPath:     writeFixture(t, "manifest.yaml", subresourceManifest),
		APIGenExportPath: writeAPIGenDir(t, apigenExport, nil),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, entry := range exportResources(t, content) {
		if name, _ := entry["name"].(string); strings.HasPrefix(name, "runtimeonly/") {
			t.Errorf("emitted %q, whose parent this export does not carry", name)
		}
	}
}

// The existing resource entries are apigen's, and they come first, untouched.
func TestGenerateLeavesTheApigenResourcesAlone(t *testing.T) {
	content, err := Generate(Options{
		ManifestPath:     writeFixture(t, "manifest.yaml", subresourceManifest),
		APIGenExportPath: writeAPIGenDir(t, apigenExport, nil),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	first := exportResources(t, content)[0]
	if first["name"] != "widgets" || first["schema"] != "v1.widgets.fixture.railgrid.ai" {
		t.Errorf("apigen's entry was disturbed: %v", first)
	}
	storage, _ := first["storage"].(map[string]any)
	if _, ok := storage["crd"]; !ok {
		t.Errorf("the parent lost its CRD storage: %v", storage)
	}
}

// Regeneration feeds the committed export back in as the resource source, so
// the entries written last time must not be read as though apigen produced them
// and written again beside the freshly derived ones.
func TestGenerateIsIdempotentWhenItsOwnOutputIsFedBackIn(t *testing.T) {
	manifest := writeFixture(t, "manifest.yaml", subresourceManifest)
	apigenPath := writeAPIGenDir(t, apigenExport, nil)
	first, err := Generate(Options{ManifestPath: manifest, APIGenExportPath: apigenPath})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// The committed export sits in the apigen dir, beside the schemas.
	committed := filepath.Join(filepath.Dir(apigenPath), "committed.yaml")
	if err := os.WriteFile(committed, first, 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := Generate(Options{ManifestPath: manifest, APIGenExportPath: committed})
	if err != nil {
		t.Fatalf("Generate (round two): %v", err)
	}
	if string(first) != string(again) {
		t.Errorf("feeding the output back in changed it:\n--- first ---\n%s\n--- again ---\n%s", first, again)
	}
}

// A manifest whose export declares no verb and no action at all keeps producing
// exactly what it produced before.
func TestGenerateAddsNothingWithoutAnyCoordinate(t *testing.T) {
	content, err := Generate(Options{
		ManifestPath:     filepath.Join("testdata", "manifest.yaml"),
		APIGenExportPath: writeAPIGenDir(t, apigenExport, nil),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(exportResources(t, content)) != 1 {
		t.Errorf("resources = %v, want apigen's one entry", exportResources(t, content))
	}
}

// apigen lists the "<Verb>Request" kinds as resources of the group. They exist
// only to be the schema a subresource entry names; exported as resources, a
// tenant could list "exec" objects of a kind nothing stores.
func TestGenerateSetsTheVerbKindsAsideFromTheResources(t *testing.T) {
	content, err := Generate(Options{
		ManifestPath:     writeFixture(t, "manifest.yaml", subresourceManifest),
		APIGenExportPath: writeAPIGenDir(t, apigenExport, nil),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, entry := range exportResources(t, content) {
		name, _ := entry["name"].(string)
		if _, verb := fixtureKinds[name]; verb && name != "widgets" {
			t.Errorf("the verb kind %q was exported as a resource", name)
		}
	}
}

// A verb with no "<Verb>Request" type has no schema for its entry to name. The
// generator says which type to add rather than emitting an entry kcp's
// admission would refuse, and says it for every such verb at once.
func TestGenerateFailsWhenAVerbHasNoKind(t *testing.T) {
	// Without the types, apigen knows nothing of proxy and rebuild either.
	kinds := map[string]string{"widgets": "Widget", "exec": "ExecRequest"}
	export := apigenExport[:strings.Index(apigenExport, "  - group: fixture.railgrid.ai\n    name: proxy")]
	_, err := Generate(Options{
		ManifestPath:     writeFixture(t, "manifest.yaml", subresourceManifest),
		APIGenExportPath: writeAPIGenDir(t, export, kinds),
	})
	if err == nil {
		t.Fatal("Generate accepted a verb with no kind")
	}
	for _, want := range []string{"widgets/proxy", "ProxyRequest", "widgets/rebuild", "RebuildRequest"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "ExecRequest") {
		t.Errorf("error blames the verb whose kind exists:\n%v", err)
	}
}

// When the verb is the plural of a stored kind in the same group (App Studio's
// projects/sessions), that kind's schema already has the ".<verb>.<group>"
// name kcp demands: the entry references it and the kind stays a resource.
func TestGenerateReferencesAStoredKindNamedLikeTheVerb(t *testing.T) {
	manifest := `apiVersion: providers.railgrid.ai/v1alpha1
kind: CatalogEntry
metadata:
  name: fixture
spec:
  export:
    name: fixture.providers.railgrid.ai
    resources:
      - name: widgets
        apiVersion: fixture.railgrid.ai/v1alpha1
        kind: Widget
        verbs:
          - name: gadgets
`
	export := `apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: fixture.railgrid.ai
spec:
  resources:
  - group: fixture.railgrid.ai
    name: widgets
    schema: v1.widgets.fixture.railgrid.ai
    storage:
      crd: {}
  - group: fixture.railgrid.ai
    name: gadgets
    schema: v1.gadgets.fixture.railgrid.ai
    storage:
      crd: {}
`
	content, err := Generate(Options{
		ManifestPath:     writeFixture(t, "manifest.yaml", manifest),
		APIGenExportPath: writeAPIGenDir(t, export, map[string]string{"widgets": "Widget", "gadgets": "Gadget"}),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	resources := exportResources(t, content)
	if len(resources) != 3 {
		t.Fatalf("resources = %v, want widgets, gadgets and widgets/gadgets", resources)
	}
	if resources[1]["name"] != "gadgets" {
		t.Errorf("the stored kind Gadget left the resources: %v", resources)
	}
	if resources[2]["name"] != "widgets/gadgets" || resources[2]["schema"] != "v1.gadgets.fixture.railgrid.ai" {
		t.Errorf("entry = %v, want widgets/gadgets naming the Gadget schema", resources[2])
	}
}

// The chart ships the verb schemas beside the stored kinds': GenerateAll names
// them, WriteVerbSchemas copies apigen's files under "<verb>.<group>.yaml" and
// prunes the verb schema of a verb that left the manifest. The stored kinds'
// copies are the Makefile's and are never touched.
func TestWriteVerbSchemasCopiesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("widgets.fixture.railgrid.ai.yaml", schemaYAML("fixture.railgrid.ai", "widgets", "Widget"))
	write("retired.fixture.railgrid.ai.yaml", schemaYAML("fixture.railgrid.ai", "retired", "RetiredRequest"))

	out, err := GenerateAll(Options{
		ManifestPath:     writeFixture(t, "manifest.yaml", subresourceManifest),
		APIGenExportPath: writeAPIGenDir(t, apigenExport, nil),
		SchemasDir:       dir,
	})
	if err != nil {
		t.Fatalf("GenerateAll: %v", err)
	}
	names, err := WriteVerbSchemas(dir, out.VerbSchemas)
	if err != nil {
		t.Fatalf("WriteVerbSchemas: %v", err)
	}
	want := []string{"exec.fixture.railgrid.ai.yaml", "proxy.fixture.railgrid.ai.yaml", "rebuild.fixture.railgrid.ai.yaml"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("wrote %v, want %v", names, want)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, entry := range entries {
		left = append(left, entry.Name())
	}
	if strings.Join(left, ",") != "exec.fixture.railgrid.ai.yaml,proxy.fixture.railgrid.ai.yaml,rebuild.fixture.railgrid.ai.yaml,widgets.fixture.railgrid.ai.yaml" {
		t.Errorf("schemas dir holds %v; the retired verb schema must be gone and the stored kind's copy kept", left)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "exec.fixture.railgrid.ai.yaml"))
	if string(raw) != schemaYAML("fixture.railgrid.ai", "exec", "ExecRequest") {
		t.Errorf("the copied verb schema differs from apigen's:\n%s", raw)
	}

	// A second run over the dir it just filled is clean: the shipped verb
	// schemas are referenced by the entries, not reported as orphans.
	if _, err := GenerateAll(Options{
		ManifestPath:     writeFixture(t, "manifest.yaml", subresourceManifest),
		APIGenExportPath: writeAPIGenDir(t, apigenExport, nil),
		SchemasDir:       dir,
	}); err != nil {
		t.Fatalf("GenerateAll over its own output: %v", err)
	}
}
