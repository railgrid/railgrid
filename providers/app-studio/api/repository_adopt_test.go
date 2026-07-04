/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRepositoryAdopted(t *testing.T) {
	adopted := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{projectRepositoryAdoptedAnnotation: "true"},
		},
	}}
	if !repositoryAdopted(adopted) {
		t.Error("adopted annotation not recognized")
	}

	created := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{projectRepositoryProjectAnnotation: "shop"},
		},
	}}
	if repositoryAdopted(created) {
		t.Error("app-studio-created repository misreported as adopted")
	}
	if repositoryAdopted(nil) {
		t.Error("nil repository misreported as adopted")
	}
}
