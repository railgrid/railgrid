/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/faroshq/provider-infrastructure/kro"
)

func TestMergePatchValues(t *testing.T) {
	target := map[string]any{
		"name":          "shop",
		"frontendImage": "ghcr.io/acme/web:sha-old",
		"oidc":          map[string]any{"mode": "none", "clientID": ""},
		"replicas":      float64(2),
	}
	patch := map[string]any{
		"frontendImage": "ghcr.io/acme/web:sha-new",
		"oidc":          map[string]any{"mode": "byo", "issuerURL": "https://idp"},
		"replicas":      nil, // null unsets
	}
	got := mergePatchValues(target, patch)

	if got["frontendImage"] != "ghcr.io/acme/web:sha-new" {
		t.Errorf("scalar replace failed: %v", got["frontendImage"])
	}
	oidc := got["oidc"].(map[string]any)
	if oidc["mode"] != "byo" || oidc["issuerURL"] != "https://idp" || oidc["clientID"] != "" {
		t.Errorf("object merge failed: %v", oidc)
	}
	if _, ok := got["replicas"]; ok {
		t.Error("null must unset the key")
	}
	// Inputs must not be mutated.
	if target["frontendImage"] != "ghcr.io/acme/web:sha-old" {
		t.Error("target was mutated")
	}
	if target["oidc"].(map[string]any)["mode"] != "none" {
		t.Error("nested target map was mutated")
	}
}

func TestChangedValuePaths(t *testing.T) {
	old := map[string]any{
		"image": "a:1",
		"oidc":  map[string]any{"mode": "none", "clientID": "x"},
		"port":  float64(8080),
	}
	new := map[string]any{
		"image": "a:2",
		"oidc":  map[string]any{"mode": "byo", "clientID": "x"},
		"env":   map[string]any{"LOG_LEVEL": "debug"},
	}
	got := changedValuePaths(old, new)
	want := []string{"env.LOG_LEVEL", "image", "oidc.mode", "port"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v, want %v", got, want)
	}
}

func TestRejectImmutableChanges(t *testing.T) {
	tmpl := &kro.Template{Name: "application", ImmutableInputs: []string{"database.version"}}

	if err := rejectImmutableChanges([]string{"frontendImage", "oidc.mode"}, tmpl); err != nil {
		t.Errorf("mutable paths must pass, got %v", err)
	}
	for _, path := range []string{"farosMode", "name", "expose.fqdn", "credentialsSecretName", "database.version"} {
		err := rejectImmutableChanges([]string{path}, tmpl)
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("path %q must be rejected naming the path, got %v", path, err)
		}
	}
	// Prefix must not over-match: "database.versionX" is not "database.version".
	if err := rejectImmutableChanges([]string{"database.versionExtra"}, tmpl); err != nil {
		t.Errorf("sibling path must pass, got %v", err)
	}
}

func TestChangedValuePathsAddedSubtreeReportsLeaves(t *testing.T) {
	// A wholesale-added subtree must surface leaf paths so immutability rules
	// on nested paths (database.version) can't be bypassed by adding the
	// parent object in one patch.
	old := map[string]any{"image": "a:1"}
	new := map[string]any{"image": "a:1", "database": map[string]any{"version": "15"}}
	got := changedValuePaths(old, new)
	if !reflect.DeepEqual(got, []string{"database.version"}) {
		t.Errorf("changed = %v, want [database.version]", got)
	}
}

func TestImmutableInputsFromAnnotation(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":        "application",
			"annotations": map[string]any{immutableInputsAnnotation: " database.version , foo "},
		},
	}}
	got := immutableInputsFromAnnotation(u)
	if !reflect.DeepEqual(got, []string{"database.version", "foo"}) {
		t.Errorf("parsed = %v", got)
	}
	if immutableInputsFromAnnotation(&unstructured.Unstructured{Object: map[string]any{}}) != nil {
		t.Error("no annotation must yield nil")
	}
}
