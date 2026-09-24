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

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

var organizationMigrationGVR = schema.GroupVersionResource{Group: "tenants.railgrid.ai", Version: "v1alpha1", Resource: "organizations"}

const initialWorkspaceMigrationAnnotation = "tenants.railgrid.ai/initial-workspace"

// PreserveInitialWorkspaceRequests must run before replacing the Organization
// schema. The old schema cannot store the new status.defaultWorkspace field;
// metadata preserves the old UUID across pruning until the org controller can
// adopt it. Keep all conditions unchanged, including completed/access handoffs.
func PreserveInitialWorkspaceRequests(ctx context.Context, dyn dynamic.Interface) error {
	resource := dyn.Resource(organizationMigrationGVR)
	list, err := resource.List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		// Fresh installations have not bound/installed the Organization API yet.
		return nil
	}
	if err != nil {
		return fmt.Errorf("listing organizations before schema migration: %w", err)
	}
	for i := range list.Items {
		name := list.Items[i].GetName()
		if _, found, err := unstructured.NestedMap(list.Items[i].Object, "spec", "initialWorkspace"); err != nil {
			return fmt.Errorf("reading legacy organization %s: %w", name, err)
		} else if !found {
			continue
		}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			org, err := resource.Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			workspace, found, err := unstructured.NestedString(org.Object, "spec", "initialWorkspace", "name")
			if err != nil {
				return err
			}
			if !found {
				return nil
			}
			creator, _, err := unstructured.NestedString(org.Object, "spec", "initialWorkspace", "user")
			if err != nil {
				return err
			}
			if workspace == "" || creator == "" {
				return fmt.Errorf("legacy initial workspace is missing its UUID or creator")
			}
			labels := org.GetLabels()
			annotations := org.GetAnnotations()
			// A conflicting durable identity must stop the schema upgrade rather
			// than silently choose a different creator or create a second child.
			for key, pair := range map[string][2]string{
				tenancyv1alpha1.OrganizationCreatorLabel:        {labels[tenancyv1alpha1.OrganizationCreatorLabel], creator},
				tenancyv1alpha1.OrganizationBootstrapAnnotation: {annotations[tenancyv1alpha1.OrganizationBootstrapAnnotation], tenancyv1alpha1.OrganizationBootstrapVersion},
				initialWorkspaceMigrationAnnotation:             {annotations[initialWorkspaceMigrationAnnotation], workspace},
			} {
				if pair[0] != "" && pair[0] != pair[1] {
					return fmt.Errorf("legacy initial workspace conflicts with %s", key)
				}
			}
			if labels[tenancyv1alpha1.OrganizationCreatorLabel] == creator &&
				annotations[tenancyv1alpha1.OrganizationBootstrapAnnotation] == tenancyv1alpha1.OrganizationBootstrapVersion &&
				annotations[initialWorkspaceMigrationAnnotation] == workspace {
				return nil
			}
			patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
				"resourceVersion": org.GetResourceVersion(),
				"labels":          map[string]string{tenancyv1alpha1.OrganizationCreatorLabel: creator},
				"annotations": map[string]string{
					tenancyv1alpha1.OrganizationBootstrapAnnotation: tenancyv1alpha1.OrganizationBootstrapVersion,
					initialWorkspaceMigrationAnnotation:             workspace,
				},
			}})
			if err != nil {
				return err
			}
			_, err = resource.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
			return err
		}); err != nil {
			return fmt.Errorf("preserving organization %s initial workspace before schema migration: %w", name, err)
		}
	}
	return nil
}
