// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
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
	for _, key := range []string{"displayName", "description", "category", "iconURL", "apiExport"} {
		if !reflect.DeepEqual(a[key], b[key]) {
			t.Fatalf("manifest/chart drift %s", key)
		}
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
