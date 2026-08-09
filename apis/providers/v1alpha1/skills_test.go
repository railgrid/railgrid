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

package v1alpha1

import (
	"strings"
	"testing"
)

func TestProviderAssistantSkillDigestCanonicalizesResources(t *testing.T) {
	base := ProviderAssistantSkillSpec{
		PackageName: "databricks-app-integration",
		Version:     "1.0.0",
		Skill:       "---\nname: demo\ndescription: demo\n---\nbody\n",
		Resources: []ProviderAssistantSkillResource{
			{Path: "z-last.md", Content: "z"},
			{Path: "a-first.md", Content: "a"},
		},
	}
	digest, err := ProviderAssistantSkillDigest(base)
	if err != nil {
		t.Fatalf("ProviderAssistantSkillDigest() error = %v", err)
	}
	base.Digest = digest
	reordered := base
	reordered.Resources = []ProviderAssistantSkillResource{base.Resources[1], base.Resources[0]}
	reorderedDigest, err := ProviderAssistantSkillDigest(reordered)
	if err != nil {
		t.Fatalf("ProviderAssistantSkillDigest(reordered) error = %v", err)
	}
	if reorderedDigest != digest {
		t.Fatalf("reordered digest = %q, want %q", reorderedDigest, digest)
	}
	if err := ValidateProviderAssistantSkill(reordered); err != nil {
		t.Fatalf("ValidateProviderAssistantSkill(valid) error = %v", err)
	}

	for name, mutate := range map[string]func(*ProviderAssistantSkillSpec){
		"digest drift":       func(skill *ProviderAssistantSkillSpec) { skill.Skill += "changed" },
		"resource traversal": func(skill *ProviderAssistantSkillSpec) { skill.Resources[0].Path = "../secret" },
		"resource duplicate": func(skill *ProviderAssistantSkillSpec) { skill.Resources[1].Path = skill.Resources[0].Path },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := base
			invalid.Resources = append([]ProviderAssistantSkillResource(nil), base.Resources...)
			mutate(&invalid)
			if err := ValidateProviderAssistantSkill(invalid); err == nil {
				t.Fatal("invalid package was accepted")
			}
		})
	}
	if err := ValidateProviderAssistantSkills([]ProviderAssistantSkillSpec{base, base}); err == nil || !strings.Contains(err.Error(), "duplicate packageName") {
		t.Fatalf("duplicate package validation error = %v", err)
	}
}

func TestProviderAssistantSkillDocumentAndListAggregateBounds(t *testing.T) {
	makeSkill := func(name string, documentBytes int, resourceBytes int) ProviderAssistantSkillSpec {
		skill := ProviderAssistantSkillSpec{
			PackageName: name,
			Version:     "1.0.0",
			Skill:       strings.Repeat("x", documentBytes),
		}
		if resourceBytes > 0 {
			skill.Resources = []ProviderAssistantSkillResource{{Path: "reference.txt", Content: strings.Repeat("r", resourceBytes)}}
		}
		digest, err := ProviderAssistantSkillDigest(skill)
		if err != nil {
			t.Fatalf("digest %s: %v", name, err)
		}
		skill.Digest = digest
		return skill
	}

	boundary := makeSkill("boundary", ProviderAssistantSkillMaxDocumentBytes, 0)
	if err := ValidateProviderAssistantSkill(boundary); err != nil {
		t.Fatalf("document boundary rejected: %v", err)
	}
	tooLarge := makeSkill("too-large", ProviderAssistantSkillMaxDocumentBytes+1, 0)
	if err := ValidateProviderAssistantSkill(tooLarge); err == nil {
		t.Fatal("document above 32 KiB was accepted")
	}

	packages := make([]ProviderAssistantSkillSpec, 0, 16)
	for index := 0; index < 16; index++ {
		packages = append(packages, makeSkill("pkg-"+string(rune('a'+index)), ProviderAssistantSkillMaxDocumentBytes, 0))
	}
	if err := ValidateProviderAssistantSkills(packages); err != nil {
		t.Fatalf("exact list aggregate boundary rejected: %v", err)
	}
	packages[0] = makeSkill("pkg-a", ProviderAssistantSkillMaxDocumentBytes, 1)
	if err := ValidateProviderAssistantSkills(packages); err == nil || !strings.Contains(err.Error(), "aggregate exceeds") {
		t.Fatalf("list aggregate overflow error = %v, want bounded rejection", err)
	}

	withResources := make([]ProviderAssistantSkillSpec, 0, 8)
	for index := 0; index < 8; index++ {
		withResources = append(withResources, makeSkill("res-"+string(rune('a'+index)), 32<<10, 32<<10))
	}
	if err := ValidateProviderAssistantSkills(withResources); err != nil {
		t.Fatalf("resource aggregate boundary rejected: %v", err)
	}
	withResources[1] = makeSkill("res-b", 32<<10, (32<<10)+1)
	if err := ValidateProviderAssistantSkills(withResources); err == nil {
		t.Fatal("resource aggregate overflow was accepted")
	}
}
