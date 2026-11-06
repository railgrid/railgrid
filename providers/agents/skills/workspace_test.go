// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package skills

import (
	"context"
	"strings"
	"testing"

	agents "github.com/railgrid/provider-agents/apis/v1alpha1"
	sdk "github.com/railgrid/provider-sdk/skills"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func packageFixture() agents.Skill {
	return agents.Skill{ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: agents.SkillSpec{Document: "---\nname: demo\ndescription: shared guidance\n---\nCheck the evidence.", Resources: []agents.SkillResource{{Name: "references/checklist.md", Content: "Verify the result."}}}}
}

func TestWorkspaceLibraryRequiresIndependentAgentOptIn(t *testing.T) {
	ctx := context.Background()
	skill := packageFixture()
	source := NewWorkspaceSource([]agents.Skill{skill})
	first, err := Build(ctx, []sdk.Source{source}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := first.Find("workspace:demo")
	if !ok || entry.Enabled || !entry.Editable {
		t.Fatalf("default catalog entry: %#v", entry)
	}
	second, err := Build(ctx, []sdk.Source{source}, []agents.AgentSkillReference{{Name: "workspace:demo"}})
	if err != nil {
		t.Fatal(err)
	}
	enabled, _ := second.Find("workspace:demo")
	if !enabled.Enabled {
		t.Fatal("explicit opt-in was ignored")
	}
	again, err := Build(ctx, []sdk.Source{source}, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := again.Find("workspace:demo")
	if other.Enabled {
		t.Fatal("one agent's activation leaked into another")
	}
	resource, err := second.ReadResource(ctx, "workspace:demo", "references/checklist.md", sdk.ResourceReadOptions{Limit: 6})
	if err != nil || string(resource.Content) != "Verify" || !resource.Truncated {
		t.Fatalf("resource = %#v, %v", resource, err)
	}
	if _, err := second.ReadResource(ctx, "workspace:demo", "../checklist.md", sdk.ResourceReadOptions{}); err == nil {
		t.Fatal("traversal accepted")
	}
	skill.Spec.Resources[0].Content = "Changed guidance."
	changed, err := Build(ctx, []sdk.Source{NewWorkspaceSource([]agents.Skill{skill})}, []agents.AgentSkillReference{{Name: "workspace:demo"}})
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := changed.Find("workspace:demo")
	if updated.Digest == enabled.Digest {
		t.Fatal("supporting resource edit did not change selection digest")
	}
}

func TestInvalidPackagesCannotPoisonValidSiblings(t *testing.T) {
	good := packageFixture()
	bad := packageFixture()
	bad.Name = "unsafe"
	bad.Spec.Resources[0].Name = "../outside.md"
	snapshot, err := Build(context.Background(), []sdk.Source{NewWorkspaceSource([]agents.Skill{bad, good})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || len(snapshot.Warnings) == 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	for _, mutate := range []func(*agents.Skill){
		func(s *agents.Skill) { s.Spec.Document = "---\nname: demo\nagent: root\n---\nbody" },
		func(s *agents.Skill) { s.Spec.Resources = append(s.Spec.Resources, s.Spec.Resources[0]) },
		func(s *agents.Skill) { s.Spec.Resources[0].Content = string([]byte{0xff}) },
		func(s *agents.Skill) {
			s.Spec.Resources[0].Content = strings.Repeat("x", sdk.DefaultMaxResourceBytes+1)
		},
		func(s *agents.Skill) {
			s.Spec.Resources = nil
			for i := 0; i < 64; i++ {
				s.Spec.Resources = append(s.Spec.Resources, agents.SkillResource{Name: strings.Repeat("a", i+1) + ".txt", Content: strings.Repeat("x", sdk.DefaultMaxResourceBytes)})
			}
		},
	} {
		candidate := packageFixture()
		mutate(&candidate)
		if Validate(candidate) == nil {
			t.Fatal("unsafe or oversized package accepted")
		}
	}
}
