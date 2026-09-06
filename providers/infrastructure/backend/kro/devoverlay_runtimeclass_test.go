/*
Copyright 2026 The Faros Authors.

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

package kro

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevOverlayRuntimeClassNameFollowsPlatformConfig(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		tokens := devTestTokens()
		tokens[sandboxRuntimeClassNameConfigKey] = "gvisor"
		rgd, err := buildRGD(devTestTemplate(t), tokens)
		if err != nil {
			t.Fatalf("buildRGD: %v", err)
		}
		byID := rgdResources(t, rgd)
		dep := byID["backendDevDeployment"]["template"].(map[string]any)
		podSpec, _, _ := nestedMap(dep, "spec", "template", "spec")
		if got := podSpec["runtimeClassName"]; got != "gvisor" {
			t.Fatalf("dev pod runtimeClassName = %v, want gvisor", got)
		}
		// The internal configuration key is never a template substitution.
		if strings.Contains(mustJSON(t, rgd.Object), sandboxRuntimeClassNameConfigKey) {
			t.Fatal("runtime class config key leaked into the RGD")
		}
		// The production workload keeps its own (cluster default) runtime.
		prod := byID["backendDeployment"]["template"].(map[string]any)
		prodPodSpec, _, _ := nestedMap(prod, "spec", "template", "spec")
		if _, ok := prodPodSpec["runtimeClassName"]; ok {
			t.Fatalf("production pod gained runtimeClassName = %v", prodPodSpec["runtimeClassName"])
		}
	})
	t.Run("empty", func(t *testing.T) {
		for _, value := range []string{"", "   "} {
			tokens := devTestTokens()
			tokens[sandboxRuntimeClassNameConfigKey] = value
			rgd, err := buildRGD(devTestTemplate(t), tokens)
			if err != nil {
				t.Fatalf("buildRGD: %v", err)
			}
			byID := rgdResources(t, rgd)
			dep := byID["backendDevDeployment"]["template"].(map[string]any)
			podSpec, _, _ := nestedMap(dep, "spec", "template", "spec")
			if _, ok := podSpec["runtimeClassName"]; ok {
				t.Fatalf("dev pod runtimeClassName = %v for config %q, want absent (cluster default)", podSpec["runtimeClassName"], value)
			}
		}
	})
	t.Run("universal coding sandbox", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("..", "..", "install", "templates", "universal-coding-sandbox.yaml"))
		if err != nil {
			t.Fatalf("read universal coding sandbox seed: %v", err)
		}
		tokens := devTestTokens()
		tokens[sandboxRuntimeClassNameConfigKey] = "kata"
		rgd, err := buildRGD(decodeTemplate(t, raw), tokens)
		if err != nil {
			t.Fatalf("buildRGD: %v", err)
		}
		byID := rgdResources(t, rgd)
		dep, ok := byID["workspaceDevDeployment"]
		if !ok {
			t.Fatal("universal coding sandbox has no workspaceDevDeployment")
		}
		podSpec, _, _ := nestedMap(dep["template"].(map[string]any), "spec", "template", "spec")
		if got := podSpec["runtimeClassName"]; got != "kata" {
			t.Fatalf("coding sandbox pod runtimeClassName = %v, want kata", got)
		}
	})
}
