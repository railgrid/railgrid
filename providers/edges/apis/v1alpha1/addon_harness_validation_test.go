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

package v1alpha1_test

import (
	"strings"
	"testing"
)

// claudeSpec is the Claude Code equivalent of runnerSpec in
// addon_validation_test.go: the same runner enrollment with the harness
// switched over.
func claudeSpec(mutate ...func(map[string]any)) map[string]any {
	spec := runnerSpec()
	runnerBlock, _ := spec["runner"].(map[string]any)
	delete(runnerBlock, "codex")
	runnerBlock["harness"] = "claude"
	runnerBlock["claude"] = map[string]any{
		"binary":        "claude",
		"model":         "sonnet",
		"authSecretRef": map[string]any{"name": "claude-auth", "namespace": "default"},
	}
	for _, m := range mutate {
		m(spec)
	}
	return spec
}

func runnerBlockOf(spec map[string]any) map[string]any {
	block, _ := spec["runner"].(map[string]any)
	return block
}

func TestAddonCELAcceptsBothHarnesses(t *testing.T) {
	explicitCodex := runnerSpec(func(s map[string]any) {
		runnerBlockOf(s)["harness"] = "codex"
	})
	if errs := validate(t, addonObject(explicitCodex)); len(errs) > 0 {
		t.Errorf("an explicit codex harness was rejected: %v", errs.ToAggregate())
	}
	if errs := validate(t, addonObject(claudeSpec())); len(errs) > 0 {
		t.Errorf("a valid claude harness was rejected: %v", errs.ToAggregate())
	}

	// An Addon written before the field existed defaults to codex, and a real
	// API server applies that default before CEL runs — so the rules also have
	// to tolerate the field being absent.
	legacy := runnerSpec()
	delete(runnerBlockOf(legacy), "harness")
	if errs := validate(t, addonObject(legacy)); len(errs) > 0 {
		t.Errorf("an Addon without spec.runner.harness was rejected: %v", errs.ToAggregate())
	}
}

// TestAddonCELRequiresTheSelectedHarnessBlock: spec.runner.harness on its own
// says nothing about how that harness should be configured or authenticated.
func TestAddonCELRequiresTheSelectedHarnessBlock(t *testing.T) {
	noClaude := claudeSpec(func(s map[string]any) {
		delete(runnerBlockOf(s), "claude")
	})
	errs := validate(t, addonObject(noClaude))
	if len(errs) == 0 {
		t.Fatal("harness=claude without spec.runner.claude was accepted")
	}
	if !strings.Contains(errs.ToAggregate().Error(), "spec.runner.claude is required") {
		t.Errorf("unexpected message: %v", errs.ToAggregate())
	}

	noCodex := runnerSpec(func(s map[string]any) {
		block := runnerBlockOf(s)
		block["harness"] = "codex"
		delete(block, "codex")
	})
	if errs := validate(t, addonObject(noCodex)); len(errs) == 0 {
		t.Fatal("harness=codex without spec.runner.codex was accepted")
	}
}

// TestAddonCELRejectsTheUnselectedHarnessBlock: configuration for a harness
// this runner does not drive is ambiguous intent, and the two blocks reference
// different credentials — so it is refused rather than quietly ignored.
func TestAddonCELRejectsTheUnselectedHarnessBlock(t *testing.T) {
	claudeOnCodex := runnerSpec(func(s map[string]any) {
		block := runnerBlockOf(s)
		block["harness"] = "codex"
		block["claude"] = map[string]any{"binary": "claude"}
	})
	errs := validate(t, addonObject(claudeOnCodex))
	if len(errs) == 0 {
		t.Fatal("spec.runner.claude alongside harness=codex was accepted")
	}
	if !strings.Contains(errs.ToAggregate().Error(), "must not be set") {
		t.Errorf("unexpected message: %v", errs.ToAggregate())
	}

	codexOnClaude := claudeSpec(func(s map[string]any) {
		runnerBlockOf(s)["codex"] = map[string]any{
			"binary":        "codex",
			"authSecretRef": map[string]any{"name": "codex-auth", "namespace": "default"},
		}
	})
	if errs := validate(t, addonObject(codexOnClaude)); len(errs) == 0 {
		t.Fatal("spec.runner.codex alongside harness=claude was accepted")
	}
}

// TestAddonCELKeepsTheRunnerRulesForBothHarnesses: switching harness must not
// become a way to slip an unsafe enrollment past admission.
func TestAddonCELKeepsTheRunnerRulesForBothHarnesses(t *testing.T) {
	relative := claudeSpec(func(s map[string]any) {
		runnerBlockOf(s)["repositories"] = map[string]any{
			"app": map[string]any{"source": "repos/app"},
		}
	})
	if errs := validate(t, addonObject(relative)); len(errs) == 0 {
		t.Error("a relative repository source was accepted on the claude harness")
	}

	capacity := claudeSpec(func(s map[string]any) {
		runnerBlockOf(s)["maximumCapacity"] = int64(2)
	})
	if errs := validate(t, addonObject(capacity)); len(errs) == 0 {
		t.Error("maximumCapacity 2 was accepted on the claude harness")
	}
}
