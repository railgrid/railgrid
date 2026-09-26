/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// writeKCPDir lays out a KCPDir the way the chart's files/ directory is laid
// out: schemas in a schemas/ subdirectory, the generated export beside it.
func writeKCPDir(t *testing.T, export string, schemas map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if export != "" {
		if err := os.WriteFile(filepath.Join(dir, APIExportFileName), []byte(export), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(schemas) == 0 {
		return dir
	}
	if err := os.MkdirAll(filepath.Join(dir, "schemas"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range schemas {
		if err := os.WriteFile(filepath.Join(dir, "schemas", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func schemaYAML(name string) string {
	return "apiVersion: apis.kcp.io/v1alpha1\nkind: APIResourceSchema\nmetadata:\n  name: " + name + "\n"
}

const testExportYAML = `apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: code.providers.railgrid.ai
spec:
  permissionClaims:
  - resource: secrets
    verbs:
    - get
  resources:
  - group: code.railgrid.ai
    name: connections
    schema: v1.connections.code.railgrid.ai
    storage:
      crd: {}
`

// A KCPDir holds the schemas AND the generated export. The export is not a
// schema, whichever of the two directory layouts it arrives in.
func TestSchemaFilesSkipTheGeneratedExport(t *testing.T) {
	dir := writeKCPDir(t, testExportYAML, map[string]string{
		"connections.code.railgrid.ai.yaml": schemaYAML("v1.connections.code.railgrid.ai"),
	})
	// config/kcp's layout: schemas flat, beside an apiexport-<name>.yaml.
	flat := t.TempDir()
	for name, body := range map[string]string{
		"apiresourceschema-connections.code.railgrid.ai.yaml": schemaYAML("v1.connections.code.railgrid.ai"),
		"apiexport-code.providers.railgrid.ai.yaml":           testExportYAML,
	} {
		if err := os.WriteFile(filepath.Join(flat, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{dir, flat} {
		files, err := schemaFiles(d)
		if err != nil {
			t.Fatalf("schemaFiles(%s): %v", d, err)
		}
		if len(files) != 1 {
			t.Fatalf("schemaFiles(%s) = %v, want exactly the one schema", d, files)
		}
		if strings.Contains(filepath.Base(files[0]), "apiexport") {
			t.Errorf("the APIExport was read as a schema: %s", files[0])
		}
	}
}

func TestLoadAPIExportRejectsTheWrongObject(t *testing.T) {
	dir := writeKCPDir(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: nope\n", nil)
	if _, err := LoadAPIExport(filepath.Join(dir, APIExportFileName)); err == nil || !strings.Contains(err.Error(), "expected APIExport") {
		t.Fatalf("err = %v, want a kind error", err)
	}
	if _, err := LoadAPIExport(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("a missing file must be an error, not an empty export")
	}
}

// The export is applied exactly as generated — name, claims and resources —
// and the schemas it references have to be in the workspace first.
func TestApplyAPIExportAppliesTheFileVerbatim(t *testing.T) {
	ctx := context.Background()
	cl := exportClient(t, seedSchema("v1.connections.code.railgrid.ai"))

	export, err := LoadAPIExport(filepath.Join(writeKCPDir(t, testExportYAML, nil), APIExportFileName))
	if err != nil {
		t.Fatalf("LoadAPIExport: %v", err)
	}
	if err := ApplyAPIExport(ctx, cl, export); err != nil {
		t.Fatalf("ApplyAPIExport: %v", err)
	}
	got, err := cl.Resource(apiExportGVR).Get(ctx, "code.providers.railgrid.ai", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get export: %v", err)
	}
	claims, _, _ := unstructured.NestedSlice(got.Object, "spec", "permissionClaims")
	if len(claims) != 1 || claims[0].(map[string]any)["resource"] != "secrets" {
		t.Errorf("claims = %v", claims)
	}
	resources, _, _ := unstructured.NestedSlice(got.Object, "spec", "resources")
	if len(resources) != 1 || resources[0].(map[string]any)["name"] != "connections" {
		t.Errorf("resources = %v", resources)
	}
}

func TestApplyAPIExportRefusesAnUnappliedSchema(t *testing.T) {
	export, err := LoadAPIExport(filepath.Join(writeKCPDir(t, testExportYAML, nil), APIExportFileName))
	if err != nil {
		t.Fatalf("LoadAPIExport: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = ApplyAPIExport(ctx, exportClient(t), export)
	if err == nil || !strings.Contains(err.Error(), "v1.connections.code.railgrid.ai") {
		t.Fatalf("err = %v, want a missing-schema error", err)
	}
}

func seedSchema(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIResourceSchema",
		"metadata":   map[string]any{"name": name},
	}}
}

func exportClient(t *testing.T, seed ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	t.Helper()
	objs := make([]runtime.Object, 0, len(seed))
	for _, o := range seed {
		objs = append(objs, o)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			apiExportGVR:         "APIExportList",
			apiResourceSchemaGVR: "APIResourceSchemaList",
		}, objs...)
}

func TestMergeAPIExportResources(t *testing.T) {
	res := func(group, name string) map[string]any {
		return map[string]any{"group": group, "name": name}
	}
	existing := []any{
		res("code.railgrid.ai", "connections"),         // owned → replaced
		res("code.railgrid.ai", "coderepos"),           // stale in owned group → pruned
		res("infrastructure.railgrid.ai", "templates"), // foreign → preserved
		"unparseable", // kept verbatim
	}
	owned := []any{
		res("code.railgrid.ai", "connections"),
		res("code.railgrid.ai", "repositories"),
	}
	out := mergeAPIExportResources(existing, owned)

	// owned entries come first, in order
	if len(out) != 4 {
		t.Fatalf("expected 4 entries, got %d: %v", len(out), out)
	}
	if m, ok := out[0].(map[string]any); !ok || m["name"] != "connections" {
		t.Errorf("out[0] = %v, want owned connections first", out[0])
	}
	if m, ok := out[1].(map[string]any); !ok || m["name"] != "repositories" {
		t.Errorf("out[1] = %v, want owned repositories second", out[1])
	}
	// foreign templates preserved (not dropped by the connections overlap);
	// stale coderepos in the owned group pruned
	foundTemplates, foundUnparseable, foundStale := false, false, false
	for _, r := range out {
		if m, ok := r.(map[string]any); ok {
			switch m["name"] {
			case "templates":
				foundTemplates = true
			case "coderepos":
				foundStale = true
			}
		}
		if s, ok := r.(string); ok && s == "unparseable" {
			foundUnparseable = true
		}
	}
	if !foundTemplates {
		t.Error("foreign 'templates' resource was dropped")
	}
	if !foundUnparseable {
		t.Error("unparseable entry was dropped")
	}
	if foundStale {
		t.Error("stale 'coderepos' entry in an owned group was not pruned")
	}
}

// TestValidateClaimScopes covers the one thing an unscoped core-group Secrets
// claim does NOT do: fail. kcp accepts it and grants the provider every Secret
// in every workspace that binds the export, so init is the last place that can
// refuse it (docs/cross-provider-simplification.md X-4).
func TestValidateClaimScopes(t *testing.T) {
	export := func(claims ...any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apis.kcp.io/v1alpha2",
			"kind":       "APIExport",
			"metadata":   map[string]any{"name": "x.providers.railgrid.ai"},
			"spec":       map[string]any{"permissionClaims": claims},
		}}
	}
	scoped := map[string]any{
		"resource": "secrets",
		"verbs":    []any{"get"},
		"defaultSelector": map[string]any{
			"matchLabels": map[string]any{"railgrid.ai/owner": "x"},
		},
	}

	if err := ValidateClaimScopes(export(scoped)); err != nil {
		t.Errorf("a scoped secrets claim was refused: %v", err)
	}

	err := ValidateClaimScopes(export(map[string]any{"resource": "secrets", "verbs": []any{"get"}}))
	if err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Errorf("err = %v, want a refusal naming the resource", err)
	}

	// An empty selector is not a selector: it would be written into kcp as a
	// label selector matching nothing, which is a different bug, not a scope.
	if err := ValidateClaimScopes(export(map[string]any{
		"resource":        "secrets",
		"verbs":           []any{"get"},
		"defaultSelector": map[string]any{"matchLabels": map[string]any{}},
	})); err == nil {
		t.Error("an empty matchLabels was accepted as a scope")
	}

	// Only the core group's credential-bearing resources are covered: a
	// first-party claim is already pinned by identityHash to one export's
	// types, and configmaps are not in the list.
	for _, claim := range []any{
		map[string]any{"group": "edges.railgrid.ai", "resource": "kubernetesclusters", "verbs": []any{"get"}},
		map[string]any{"resource": "namespaces", "verbs": []any{"get"}},
		map[string]any{"resource": "configmaps", "verbs": []any{"get"}},
	} {
		if err := ValidateClaimScopes(export(claim)); err != nil {
			t.Errorf("claim %+v was refused: %v", claim, err)
		}
	}

	// A claimless export (quickstart) is fine.
	if err := ValidateClaimScopes(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIExport",
		"metadata": map[string]any{"name": "bare"}, "spec": map[string]any{},
	}}); err != nil {
		t.Errorf("ValidateClaimScopes on a claimless export: %v", err)
	}
}
