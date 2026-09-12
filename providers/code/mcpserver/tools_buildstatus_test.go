/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
)

func TestNormalizeWorkflowFileName(t *testing.T) {
	cases := map[string]string{
		"":                                    "build.yaml",
		"   ":                                 "build.yaml",
		"build.yaml":                          "build.yaml",
		" faros-app-studio-build.yml ":        "faros-app-studio-build.yml",
		".github/workflows/build.yaml":        "build.yaml",
		".github/workflows/ci.yml":            "ci.yml",
		"/.github/workflows/ci.yml":           "ci.yml",
		".github\\workflows\\ci.yml":          "ci.yml",
		".github/workflows/":                  "build.yaml",
		"nested/dir/.github/workflows/x.yaml": "x.yaml",
	}
	for in, want := range cases {
		if got := normalizeWorkflowFileName(in); got != want {
			t.Errorf("normalizeWorkflowFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// buildStatusFixture returns a tenant client whose RepositoryBuildStatus
// creates complete immediately, standing in for the controller, and records
// the spec each create carried.
func buildStatusFixture(t *testing.T) (*dynamicfake.FakeDynamicClient, *[]map[string]any) {
	t.Helper()
	specs := &[]map[string]any{}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("create", "repositorybuildstatuses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
		*specs = append(*specs, spec)
		obj.Object["status"] = map[string]any{
			"phase":      string(codev1alpha1.RepositoryBuildStatusPhaseSucceeded),
			"dispatched": true,
			"run": map[string]any{
				"found":      true,
				"runID":      int64(42),
				"status":     "completed",
				"conclusion": "failure",
				"jobs":       []any{map[string]any{"name": "build", "conclusion": "failure", "failureLog": "boom"}},
			},
		}
		if err := dyn.Tracker().Create(repositoryBuildStatusesGVR, obj, "", metav1.CreateOptions{}); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	return dyn, specs
}

// workflowFileName is optional: an omitted value reaches the controller as
// build.yaml, and a full workflow path is reduced to its basename.
func TestBuildStatusDefaultsAndNormalizesWorkflowFileName(t *testing.T) {
	dyn, specs := buildStatusFixture(t)

	_, out, err := buildStatus(t.Context(), dyn, buildStatusInput{RepositoryRef: "demo-app"})
	if err != nil {
		t.Fatalf("build_status without workflowFileName: %v", err)
	}
	if !out.Found || out.RunID != 42 || out.Conclusion != "failure" || len(out.Jobs) != 1 || out.Jobs[0].FailureLog != "boom" {
		t.Errorf("unexpected output: %+v", out)
	}

	if _, _, err := buildStatus(t.Context(), dyn, buildStatusInput{RepositoryRef: "demo-app", WorkflowFileName: ".github/workflows/ci.yml"}); err != nil {
		t.Fatalf("build_status with a workflow path: %v", err)
	}

	if len(*specs) != 2 {
		t.Fatalf("expected 2 RepositoryBuildStatus creates, got %d", len(*specs))
	}
	if got := (*specs)[0]["workflowFileName"]; got != "build.yaml" {
		t.Errorf("omitted workflowFileName sent as %v, want build.yaml", got)
	}
	if got := (*specs)[1]["workflowFileName"]; got != "ci.yml" {
		t.Errorf("workflow path sent as %v, want ci.yml", got)
	}
	if got := (*specs)[0]["action"]; got != string(codev1alpha1.RepositoryBuildStatusActionStatus) {
		t.Errorf("action = %v", got)
	}
}

func TestRebuildDefaultsWorkflowFileName(t *testing.T) {
	dyn, specs := buildStatusFixture(t)

	_, out, err := rebuildWorkflow(t.Context(), dyn, rebuildInput{RepositoryRef: "demo-app", Ref: "main"})
	if err != nil {
		t.Fatalf("rebuild without workflowFileName: %v", err)
	}
	if !out.Dispatched {
		t.Errorf("dispatched = false")
	}
	if len(*specs) != 1 {
		t.Fatalf("expected 1 create, got %d", len(*specs))
	}
	spec := (*specs)[0]
	if spec["workflowFileName"] != "build.yaml" || spec["ref"] != "main" || spec["action"] != string(codev1alpha1.RepositoryBuildStatusActionRerun) {
		t.Errorf("spec = %v", spec)
	}
}

func TestBuildStatusStillRequiresRepositoryRef(t *testing.T) {
	dyn, _ := buildStatusFixture(t)
	if _, _, err := buildStatus(t.Context(), dyn, buildStatusInput{}); err == nil || !strings.Contains(err.Error(), "repositoryRef") {
		t.Errorf("build_status without repositoryRef: %v", err)
	}
	if _, _, err := rebuildWorkflow(t.Context(), dyn, rebuildInput{}); err == nil || !strings.Contains(err.Error(), "repositoryRef") {
		t.Errorf("rebuild without repositoryRef: %v", err)
	}
}
