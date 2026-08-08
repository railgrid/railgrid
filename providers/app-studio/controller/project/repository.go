/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"context"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

// repositoryGVK is the code provider's Repository resource.
var repositoryGVK = schema.GroupVersionKind{Group: "code.kedge.faros.sh", Version: "v1alpha1", Kind: "Repository"}

// projectRepositoryLabel matches the api layer's claim label/annotation so
// handler-side cleanup and adoption keep recognizing reconciler-created
// repositories.
const projectRepositoryLabel = "app-studio.ai.kedge.faros.sh/project"

// ensureRepository creates the Repository CR the spec binding names, if any
// (autoInit creates the repo on the git host), and returns the observed
// object (nil right after creation). Adopted repositories are NEVER
// (re)created — the user imported an existing repo and its CR lifecycle is
// theirs. Repositories are also deliberately NOT deleted on Project delete —
// they hold user code; deletion only releases the claim (handler-side).
func (r *Reconciler) ensureRepository(ctx context.Context, c client.Client, p *aiv1alpha1.Project) (*unstructured.Unstructured, error) {
	b := p.Spec.Repository
	if b == nil || strings.TrimSpace(b.RepositoryRef) == "" {
		return nil, nil
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(repositoryGVK)
	err := c.Get(ctx, types.NamespacedName{Name: b.RepositoryRef}, got)
	if err == nil {
		return got, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	if b.Adopted || strings.TrimSpace(b.ConnectionRef) == "" {
		// Adopted (or under-specified legacy) binding: nothing to create.
		return nil, nil
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		name = b.RepositoryRef
	}
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": repositoryGVK.GroupVersion().String(),
		"kind":       repositoryGVK.Kind,
		"metadata": map[string]any{
			"name":        b.RepositoryRef,
			"labels":      map[string]any{projectRepositoryLabel: p.Name},
			"annotations": map[string]any{projectRepositoryLabel: p.Name},
		},
		"spec": map[string]any{
			"connectionRef": b.ConnectionRef,
			"name":          name,
			"visibility":    "private",
			"description":   "Created by App Studio for " + p.Spec.DisplayName,
			"autoInit":      true,
		},
	}}
	if err := c.Create(ctx, repo); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return nil, nil
}

// repositoryReady reports whether the observed Repository CR has its Ready
// condition True — the gate before commits are attempted.
func repositoryReady(repo *unstructured.Unstructured) bool {
	if repo == nil {
		return false
	}
	conds, _, _ := unstructured.NestedSlice(repo.Object, "status", "conditions")
	for _, cond := range conds {
		cm, ok := cond.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t == "Ready" {
			s, _ := cm["status"].(string)
			return s == "True"
		}
	}
	return false
}
