/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/commitbundle"
)

func TestCommitFilesCreatesRepositoryCommitRequest(t *testing.T) {
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": codev1alpha1.SchemeGroupVersion.String(),
		"kind":       "Repository",
		"metadata": map[string]any{
			"name": "demo-app",
			"labels": map[string]any{
				"app-studio.ai.kedge.faros.sh/project": "demo-project",
			},
		},
		"spec": map[string]any{
			"connectionRef": "github",
			"name":          "demo-app",
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), repo)
	store, err := commitbundle.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, out, err := commitFiles(ctx, dyn, store, commitFilesInput{
		RepositoryRef: "demo-app",
		Message:       "Initial app",
		Files: []commitFileInput{
			{Path: "package.json", Content: `{"private":true}`},
			{Path: "src/App.tsx", Content: "export default function App() { return null }"},
		},
	})
	if err != nil {
		t.Fatalf("commitFiles returned error: %v", err)
	}
	if out.Name == "" || out.BundleRef == "" || out.BundleDigest == "" {
		t.Fatalf("unexpected output: %#v", out)
	}
	if out.CommitSHA != "" {
		t.Fatalf("CommitSHA = %q, want empty before controller status", out.CommitSHA)
	}

	created, err := dyn.Resource(repositoryCommitsGVR).Get(context.Background(), out.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get RepositoryCommit returned error: %v", err)
	}
	if created.GetLabels()[codev1alpha1.LabelRepository] != "demo-app" {
		t.Fatalf("repository label = %q, want demo-app", created.GetLabels()[codev1alpha1.LabelRepository])
	}
	if created.GetLabels()["app-studio.ai.kedge.faros.sh/project"] != "demo-project" {
		t.Fatalf("project label was not copied: %#v", created.GetLabels())
	}
	if _, found, _ := unstructured.NestedSlice(created.Object, "spec", "files"); found {
		t.Fatal("RepositoryCommit spec unexpectedly stores file contents")
	}
	name, _, _ := unstructured.NestedString(created.Object, "spec", "source", "bundleRef", "name")
	digest, _, _ := unstructured.NestedString(created.Object, "spec", "source", "bundleRef", "digest")
	if name != out.BundleRef || digest != out.BundleDigest {
		t.Fatalf("bundle ref = %s/%s, want %s/%s", name, digest, out.BundleRef, out.BundleDigest)
	}
}

func TestCommitObjectName(t *testing.T) {
	name := commitObjectName(strings.Repeat("a", 260), "sha256:1234567890abcdef", time.Unix(1, 2))
	if len(name) > 253 {
		t.Fatalf("name length = %d, want <= 253", len(name))
	}
	if !strings.Contains(name, "-commit-1234567890ab-") {
		t.Fatalf("name = %q, want digest suffix", name)
	}
}
