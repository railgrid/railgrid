/*
Copyright 2026 The Railgrid Authors.

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

package metering

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// PlanExists returns a lookup that answers whether a metering Plan exists,
// for callers that record a plan name on a hub object (Organization.spec.plan)
// and must not accept a typo. It errors while metering is not installed:
// without the store there is nothing to validate against, and an Organization
// cannot be detached onto a plan nobody serves.
func PlanExists(discovery Installed, store dynamic.Interface) func(ctx context.Context, name string) (bool, error) {
	return func(ctx context.Context, name string) (bool, error) {
		if _, ok := discovery.Current(); !ok {
			return false, fmt.Errorf("metering is not installed; plans cannot be assigned")
		}
		if _, err := store.Resource(gvrPlans).Get(ctx, name, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
}
