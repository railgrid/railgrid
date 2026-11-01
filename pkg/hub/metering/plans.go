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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

// EntitlementPlan returns a lookup for the plan an organization is on
// according to its metering Entitlement, the one thing that decides what the
// organization may use. It is preferred over Organization.spec.plan, which
// is only an admin's assignment: a plan set on the Entitlement directly, or
// by a subscription, never reaches that field. ok is false when metering has
// nothing to say (not installed, no Entitlement yet, or the read failed), and
// the caller falls back to spec.plan.
func EntitlementPlan(discovery Installed, store dynamic.Interface, logger klog.Logger) func(ctx context.Context, org string) (plan string, ok bool, err error) {
	return func(ctx context.Context, org string) (string, bool, error) {
		if _, installed := discovery.Current(); !installed {
			return "", false, nil
		}
		ent, err := store.Resource(gvrEntitlements).Get(ctx, org, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "reading an organization's entitlement for its plan; using the organization's own field", "organization", org)
			}
			return "", false, nil
		}
		plan, _, _ := unstructured.NestedString(ent.Object, "spec", "plan")
		return plan, true, nil
	}
}

// ResetPlan returns the action behind an explicit "back to the default plan"
// on the admin plan route. Clearing Organization.spec.plan alone only moves
// an Entitlement whose plan the hub wrote (LabelPlanFromSpec): one an admin
// edited directly keeps its plan, by design, so a clear of an already-empty
// field would change nothing. The reset puts the Entitlement on defaultPlan
// and drops the hub's labels, whoever set the plan; a missing Entitlement
// is fine (the reconciler creates it on the default plan). Overrides are
// untouched.
func ResetPlan(discovery Installed, store dynamic.Interface, defaultPlan string, logger klog.Logger) func(ctx context.Context, org string) error {
	return func(ctx context.Context, org string) error {
		if _, installed := discovery.Current(); !installed {
			return fmt.Errorf("metering is not installed; plans cannot be assigned")
		}
		res := store.Resource(gvrEntitlements)
		ent, err := res.Get(ctx, org, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		current, _, _ := unstructured.NestedString(ent.Object, "spec", "plan")
		labels := ent.GetLabels()
		_, fromSpec := labels[LabelPlanFromSpec]
		_, sourced := labels[PlanSourceLabel]
		if current == defaultPlan && !fromSpec && !sourced {
			return nil
		}
		if err := unstructured.SetNestedField(ent.Object, defaultPlan, "spec", "plan"); err != nil {
			return err
		}
		delete(labels, LabelPlanFromSpec)
		delete(labels, PlanSourceLabel)
		ent.SetLabels(labels)
		if _, err := res.Update(ctx, ent, metav1.UpdateOptions{FieldManager: FieldManager}); err != nil {
			return err
		}
		logger.Info("organization entitlement reset to the default plan", "organization", org, "from", current, "plan", defaultPlan)
		return nil
	}
}

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
