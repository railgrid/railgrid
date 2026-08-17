/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestTerraformContribTemplateBuildsPinnedInfrakubeGraph(t *testing.T) {
	path := filepath.Join("..", "..", "contrib", "terraform", "terraform-stack-template.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Terraform contrib template: %v", err)
	}
	tmpl := decodeTemplate(t, raw)
	if got, want := tmpl.Name, "terraform-stack"; got != want {
		t.Fatalf("Template name = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.Backend, Name; got != want {
		t.Fatalf("backend = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.InstanceCRD.Kind, "TerraformStack"; got != want {
		t.Fatalf("instance kind = %q, want %q", got, want)
	}

	rgd, err := buildRGD(tmpl, testTokens())
	if err != nil {
		t.Fatalf("buildRGD(terraform-stack): %v", err)
	}
	if findResource(t, rgd, "stateNamespace") == nil {
		t.Fatal("terraform-stack template missing stateNamespace resource")
	}
	tf := findResource(t, rgd, "terraform")
	if tf == nil {
		t.Fatal("terraform-stack template missing terraform resource")
	}
	if got, _, _ := unstructured.NestedString(tf, "template", "apiVersion"); got != "infrakube.galleybytes.com/v1" {
		t.Fatalf("Terraform apiVersion = %q, want infrakube.galleybytes.com/v1", got)
	}
	if got, _, _ := unstructured.NestedString(tf, "template", "kind"); got != "Terraform" {
		t.Fatalf("Terraform kind = %q, want Terraform", got)
	}
	if got, _, _ := unstructured.NestedString(tf, "template", "spec", "terraformVersion"); got != "1.5.7" {
		t.Fatalf("Terraform version = %q, want pinned 1.5.7", got)
	}
	module, _, _ := unstructured.NestedString(tf, "template", "spec", "terraformModule", "inline")
	if module == "" || strings.Contains(module, "source =") || !strings.Contains(module, `output "internal_marker"`) {
		t.Fatalf("Terraform POC module must be controlled inline content, got %q", module)
	}
	backend, _, _ := unstructured.NestedString(tf, "template", "spec", "backend")
	if !strings.Contains(backend, "stateNamespace.metadata.namespace") || !strings.Contains(backend, "schema.spec.name") || strings.Contains(backend, `namespace = \"default\"`) {
		t.Fatalf("Terraform backend must resolve the KRO-materialized tenant namespace, got %q", backend)
	}
	if write, found, _ := unstructured.NestedBool(tf, "template", "spec", "writeOutputsToStatus"); !found || !write {
		t.Fatalf("writeOutputsToStatus = %t (found=%t), want explicit true", write, found)
	}
	outputs, _, _ := unstructured.NestedStringSlice(tf, "template", "spec", "outputsToInclude")
	if !slices.Equal(outputs, []string{"message", "resource_id"}) {
		t.Fatalf("outputsToInclude = %v, want only non-sensitive POC outputs", outputs)
	}
	if ignoreDelete, found, _ := unstructured.NestedBool(tf, "template", "spec", "ignoreDelete"); !found || ignoreDelete {
		t.Fatalf("ignoreDelete = %t (found=%t), want explicit false so Infrakube destroys on deletion", ignoreDelete, found)
	}
	status, found, err := unstructured.NestedMap(rgd.Object, "spec", "schema", "status")
	if err != nil || !found {
		t.Fatalf("read terraform-stack status: found=%t err=%v", found, err)
	}
	for _, field := range []string{"phase", "message", "resourceID", "runtimeNamespace"} {
		if _, found := status[field]; !found {
			t.Fatalf("terraform-stack status missing %s", field)
		}
	}
	if statusJSON := mustJSON(t, status); strings.Contains(statusJSON, "internal_marker") {
		t.Fatalf("terraform-stack status projects sensitive internal_marker: %s", statusJSON)
	}
}
