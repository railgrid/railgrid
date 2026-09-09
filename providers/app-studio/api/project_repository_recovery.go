/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Recovery switches only the Project's pending intent. The old resource and
// remote are retained: without a recorded remote ID we cannot safely delete it.
func projectRepositoryCreationRetryable(p *aiv1alpha1.Project, repo *unstructured.Unstructured) bool {
	b := p.Spec.Repository
	if b == nil || b.Adopted || p.GetDeletionTimestamp() != nil || p.UID == "" || repo == nil || repo.GetDeletionTimestamp() != nil || repo.GetName() != b.RepositoryRef {
		return false
	}
	if repo.GetAnnotations()["code.faros.sh/create-only"] != "true" || repo.GetLabels()[projectRepositoryProjectLabel] != p.Name || repo.GetAnnotations()[projectRepositoryUIDAnnotation] != string(p.UID) {
		return false
	}
	id, _, _ := unstructured.NestedString(repo.Object, "status", "repoID")
	connection, _, _ := unstructured.NestedString(repo.Object, "spec", "connectionRef")
	generation, _, _ := unstructured.NestedInt64(repo.Object, "status", "observedGeneration")
	status, reason, _, found := unstructuredCondition(repo, codeConditionReady)
	return id == "" && connection == b.ConnectionRef && generation == repo.GetGeneration() && found && status == "False" && reason == "RepositoryIdentityConflict"
}
