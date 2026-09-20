/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package commitexec

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/commitbundle"
)

func TestCommitObjectKeepsAuthoritativeRepositoryLabel(t *testing.T) {
	repo := &codev1alpha1.Repository{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-app",
			Labels: map[string]string{
				codev1alpha1.LabelRepository:        "stale-repo",
				"app-studio.ai.railgrid.ai/project": "demo-project",
			},
		},
	}
	obj := commitObject(repo, commitbundle.BundleRef{
		Name:   "bundle-123",
		Digest: "sha256:123",
	}, Request{
		RepositoryRef: "demo-app",
		Message:       "Initial app",
	})
	if got := obj.GetLabels()[codev1alpha1.LabelRepository]; got != "demo-app" {
		t.Fatalf("repository label = %q, want demo-app", got)
	}
	if got := obj.GetLabels()["app-studio.ai.railgrid.ai/project"]; got != "demo-project" {
		t.Fatalf("project label = %q, want demo-project", got)
	}
	scope, _, _ := unstructured.NestedString(obj.Object, "spec", "source", "bundleRef", "scope")
	if scope != "" {
		t.Fatalf("RepositoryCommit spec exposed bundle scope %q", scope)
	}
}

func TestBundleStorageScope(t *testing.T) {
	obj := &unstructured.Unstructured{}
	obj.SetAnnotations(map[string]string{"kcp.io/cluster": " logical-cluster "})
	if got := bundleStorageScope(obj); got != "logical-cluster" {
		t.Fatalf("scope = %q, want logical-cluster", got)
	}
	if got := bundleStorageScope(&unstructured.Unstructured{}); got != "" {
		t.Fatalf("fallback scope = %q, want empty", got)
	}
	if got := bundleStorageScope(nil); got != "" {
		t.Fatalf("nil fallback scope = %q, want empty", got)
	}
}

func TestObjectName(t *testing.T) {
	name := ObjectName(strings.Repeat("a", 260), "sha256:1234567890abcdef", time.Unix(1, 2))
	if len(name) > 253 {
		t.Fatalf("name length = %d, want <= 253", len(name))
	}
	if !strings.Contains(name, "-commit-1234567890ab-") {
		t.Fatalf("name = %q, want digest suffix", name)
	}
}

// A request names one source. Both halves of the rule matter: an empty
// request is a caller bug, and a request naming files AND a staged bundle is
// ambiguous about which bytes the commit carries.
func TestValidateRejectsAmbiguousAndEmptySources(t *testing.T) {
	for name, req := range map[string]Request{
		"no source": {RepositoryRef: "demo"},
		"both sources": {
			RepositoryRef: "demo",
			Files:         []commitbundle.File{{Path: "a.txt", Content: "a"}},
			Bundle:        &commitbundle.BundleRef{Name: "bundle-1"},
		},
		"bundle without a name": {RepositoryRef: "demo", Bundle: &commitbundle.BundleRef{}},
		"no repository":         {Files: []commitbundle.File{{Path: "a.txt", Content: "a"}}},
		"unknown encoding": {
			RepositoryRef: "demo",
			Files:         []commitbundle.File{{Path: "a.txt", Content: "a", Encoding: "rot13"}},
		},
		"over-long message": {
			RepositoryRef: "demo",
			Message:       strings.Repeat("m", MaxCommitMessageLength+1),
			Files:         []commitbundle.File{{Path: "a.txt", Content: "a"}},
		},
	} {
		req := req
		if err := req.Validate(); err == nil {
			t.Fatalf("%s: Validate accepted the request", name)
		}
	}
	req := Request{RepositoryRef: " demo ", Message: " hello ", Files: []commitbundle.File{{Path: "a.txt", Content: "a"}}}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate rejected a well-formed request: %v", err)
	}
	if req.RepositoryRef != "demo" || req.Message != "hello" {
		t.Fatalf("Validate did not normalize the request: %#v", req)
	}
}
