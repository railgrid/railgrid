/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package crossprovider

import (
	"errors"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The distinction the teardown paths turn on. "This workspace does not serve
// the kind" and "this object is not there" arrive as different errors and must
// stay different: reading the first as the second releases a finalizer over
// live objects, which is the regression the claimed-kind move had to avoid.
func TestClaimUnacceptedSeparatesAnUnservedKindFromAMissingObject(t *testing.T) {
	instances := schema.GroupResource{Group: InfrastructureAPIGroup, Resource: InstancesResource}

	unserved := []struct {
		name string
		err  error
	}{
		{"no kind match", &meta.NoKindMatchError{
			GroupKind:        schema.GroupKind{Group: InfrastructureAPIGroup, Kind: "Instance"},
			SearchedVersions: []string{"v1alpha1"},
		}},
		{"no resource match", &meta.NoResourceMatchError{
			PartialResource: schema.GroupVersionResource{Group: CodeAPIGroup, Version: "v1alpha1", Resource: RepositoriesResource},
		}},
		{"forbidden", apierrors.NewForbidden(instances, "demo-dev", errors.New("no"))},
		// Callers wrap before they branch — settleRepository says "read Code
		// repository %q: %w" — so this has to survive %w or the teardown guard
		// is decorative.
		{"wrapped", fmt.Errorf("read Code repository %q: %w", "demo-repo", &meta.NoKindMatchError{
			GroupKind:        schema.GroupKind{Group: CodeAPIGroup, Kind: "Repository"},
			SearchedVersions: []string{"v1alpha1"},
		})},
	}
	for _, tc := range unserved {
		if !ClaimUnaccepted(tc.err) {
			t.Errorf("%s: ClaimUnaccepted = false, want true — the caller would treat it as a settled object", tc.name)
		}
	}

	served := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"object not found", apierrors.NewNotFound(instances, "demo-dev")},
		{"conflict", apierrors.NewConflict(instances, "demo-dev", errors.New("modified"))},
		{"plain error", errors.New("connection refused")},
	}
	for _, tc := range served {
		if ClaimUnaccepted(tc.err) {
			t.Errorf("%s: ClaimUnaccepted = true, want false — this says nothing about the claim", tc.name)
		}
	}
}
