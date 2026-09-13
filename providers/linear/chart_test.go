// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestChartClaimsAndIdentitySeparation(t *testing.T) {
	raw, err := exec.Command("helm", "template", "linear", "deploy/chart", "--namespace", "linear-hosting").CombinedOutput()
	if err != nil {
		t.Fatalf("helm: %s %v", raw, err)
	}
	var catalog map[string]any
	var deployment map[string]any
	for _, doc := range bytes.Split(raw, []byte("\n---")) {
		var obj map[string]any
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			t.Fatal(err)
		}
		switch obj["kind"] {
		case "ConfigMap":
			data, ok := obj["data"].(map[string]any)
			if ok && data["catalogentry.yaml"] != nil {
				if err := yaml.Unmarshal([]byte(data["catalogentry.yaml"].(string)), &catalog); err != nil {
					t.Fatal(err)
				}
			}
		case "Deployment":
			deployment = obj
		}
	}
	source, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = yaml.Unmarshal(source, &manifest); err != nil {
		t.Fatal(err)
	}
	a := manifest["spec"].(map[string]any)
	b := catalog["spec"].(map[string]any)
	for _, key := range []string{"displayName", "description", "category", "iconURL", "apiExport", "actions"} {
		if !reflect.DeepEqual(a[key], b[key]) {
			t.Fatalf("manifest/chart drift %s", key)
		}
	}
	actions, ok := a["actions"].([]any)
	if !ok || len(actions) != 8 {
		t.Fatalf("expected eight declared actions, got %v", a["actions"])
	}
	for _, item := range actions {
		action := item.(map[string]any)
		data, err := json.Marshal(map[string]any{"input": action["inputSchema"], "output": action["outputSchema"]})
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if action["schemaDigest"] != "sha256:"+hex.EncodeToString(hash[:]) {
			t.Fatalf("schema digest drift: %v", action["id"])
		}
	}
	if !reflect.DeepEqual(a["ui"].(map[string]any)["children"], b["ui"].(map[string]any)["children"]) {
		t.Fatal("navigation manifest drift")
	}
	strategy := deployment["spec"].(map[string]any)["strategy"].(map[string]any)
	if strategy["type"] != "Recreate" {
		t.Fatal("rollout permits overlapping provider processes")
	}
	pod := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	containers := pod["containers"].([]any)
	runtime := containers[0].(map[string]any)
	mounts := runtime["volumeMounts"].([]any)
	if len(mounts) != 1 || mounts[0].(map[string]any)["name"] != "runtime-kubeconfig" {
		t.Fatalf("runtime mounted bootstrap credential: %v", mounts)
	}
	if pod["automountServiceAccountToken"] != false {
		t.Fatal("hosting token is mounted")
	}
	if len(permissionClaims) != 1 || permissionClaims[0].Resource != "secrets" || !reflect.DeepEqual(permissionClaims[0].Verbs, []string{"get"}) {
		t.Fatal("unexpected init claims")
	}
	if _, err = exec.Command("helm", "template", "linear", "deploy/chart", "--set", "replicaCount=2").CombinedOutput(); err == nil {
		t.Fatal("multi-replica configuration accepted")
	}
}

func TestGeneratedResourcesAreWorkspaceScoped(t *testing.T) {
	for _, retired := range []string{"operations", "events", "actionreceipts"} {
		if _, err := os.Stat("deploy/chart/files/schemas/apiresourceschema-" + retired + ".linear.providers.faros.sh.yaml"); !os.IsNotExist(err) {
			t.Fatalf("private or retired resource %s must not be exported", retired)
		}
	}
	for _, resource := range []string{"connections", "teams"} {
		var previousSpec any
		for _, path := range []string{"config/crds/linear.providers.faros.sh_" + resource + ".yaml", "config/kcp/apiresourceschema-" + resource + ".linear.providers.faros.sh.yaml", "deploy/chart/files/schemas/apiresourceschema-" + resource + ".linear.providers.faros.sh.yaml"} {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var obj map[string]any
			if err = yaml.Unmarshal(raw, &obj); err != nil {
				t.Fatal(err)
			}
			spec := obj["spec"].(map[string]any)
			if spec["scope"] != "Cluster" {
				t.Fatalf("%s scope=%v", path, spec["scope"])
			}
			if strings.Contains(path, "config/kcp/") {
				previousSpec = spec
			}
			if strings.Contains(path, "deploy/chart/") && !reflect.DeepEqual(previousSpec, spec) {
				t.Fatalf("chart schema drift for %s", resource)
			}
			if resource == "connections" {
				version := spec["versions"].([]any)[0].(map[string]any)
				schema := version["schema"].(map[string]any)
				if obj["kind"] == "CustomResourceDefinition" {
					schema = schema["openAPIV3Schema"].(map[string]any)
				}
				properties := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)
				apiKey := properties["apiKeySecretRef"].(map[string]any)
				if _, exists := properties["subscription"]; exists {
					t.Fatal("retired webhook configuration remains in Connection")
				}
				for _, ref := range []map[string]any{apiKey} {
					namespace := ref["properties"].(map[string]any)["namespace"].(map[string]any)
					if namespace["default"] != "default" {
						t.Fatalf("%s secret namespace default missing", path)
					}
				}
			}
		}
	}
}
