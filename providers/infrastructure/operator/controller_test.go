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

package operator

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

func TestWithRuntimeAccessHint(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterroles"}, "kro-cluster-role", errors.New("denied"))
	helmForbidden := fmt.Errorf("helm upgrade --install kro: exit status 1\nError: clusterroles.rbac.authorization.k8s.io \"kro-cluster-role\" is forbidden: User \"system:serviceaccount:faros:infrastructure-operator\" cannot create resource \"clusterroles\"")
	other := errors.New("dial tcp: connection refused")

	cases := []struct {
		name     string
		condType string
		cause    error
		wantHint bool
	}{
		{"typed forbidden on kro release", v1alpha1.ConditionKroReleased, forbidden, true},
		{"helm output on kro release", v1alpha1.ConditionKroReleased, helmForbidden, true},
		{"typed forbidden on serve rollout", v1alpha1.ConditionProviderDeployed, forbidden, true},
		{"non-permission error on kro release", v1alpha1.ConditionKroReleased, other, false},
		{"forbidden on the kcp bootstrap step", v1alpha1.ConditionBootstrapped, forbidden, false},
		{"forbidden on the CatalogEntry step", v1alpha1.ConditionRegistered, forbidden, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withRuntimeAccessHint(tc.condType, tc.cause)
			if !errors.Is(got, tc.cause) {
				t.Fatalf("hint must wrap the cause, got %v", got)
			}
			if hinted := strings.Contains(got.Error(), "operator.clusterAdmin=true"); hinted != tc.wantHint {
				t.Errorf("hint present = %v, want %v: %v", hinted, tc.wantHint, got)
			}
			if tc.wantHint && !strings.Contains(got.Error(), tc.cause.Error()) {
				t.Errorf("hinted error must keep the original message: %v", got)
			}
		})
	}
	if withRuntimeAccessHint(v1alpha1.ConditionKroReleased, nil) != nil {
		t.Error("nil cause must stay nil")
	}
}
