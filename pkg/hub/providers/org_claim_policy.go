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

package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	sdkinstall "github.com/railgrid/provider-sdk/install"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/hub/bootstrap"
)

var orgClaimPolicyGVR = schema.GroupVersionResource{Group: "admin.kcp.io", Version: "v1alpha1", Resource: "permissionclaimpolicies"}

const orgClaimPolicyOwnerLabel = "providers.railgrid.ai/issuer-cluster"

func orgClaimPolicyName(cluster string) string {
	hash := sha256.Sum256([]byte(cluster))
	return "railgrid-byo-" + hex.EncodeToString(hash[:16])
}

// The declaration has already passed bootstrap admission. This policy admits
// identity-agnostic claims for customer-authored groups without changing the
// generated export or maintaining a platform catalog entry for every customer.
// Native organization fences and accepted consumer claims still govern access.
func orgProviderClaimPolicy(identity OrgBootstrapIdentity, admitted sdkinstall.OrganizationBootstrap) (*unstructured.Unstructured, error) {
	if identity.Cluster == "" || admitted.APIExport == nil || admitted.CatalogEntry == nil {
		return nil, fmt.Errorf("claim policy requires an admitted provider identity")
	}
	exported := map[string]bool{}
	resources, _, _ := unstructured.NestedSlice(admitted.APIExport.Object, "spec", "resources")
	for _, raw := range resources {
		if resource, ok := raw.(map[string]any); ok {
			if group, _ := resource["group"].(string); group != "" {
				exported[group] = true
			}
		}
	}
	foreign := map[string]bool{}
	requirements, _, _ := unstructured.NestedSlice(admitted.CatalogEntry.Object, "spec", "requires")
	for _, raw := range requirements {
		if requirement, ok := raw.(map[string]any); ok {
			provider, _ := requirement["provider"].(string)
			group, _ := requirement["group"].(string)
			if provider != "" && group != "" {
				foreign[group] = true
			}
		}
	}
	if len(foreign) == 0 {
		return nil, nil
	}
	groups, claimers := sortedGroupNames(foreign), sortedGroupNames(exported)
	claims := make([]any, 0, len(claimers))
	for _, claimer := range claimers {
		claims = append(claims, map[string]any{"claimer": claimer, "groups": toAnySlice(groups)})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admin.kcp.io/v1alpha1", "kind": "PermissionClaimPolicy",
		"metadata": map[string]any{"name": orgClaimPolicyName(identity.Cluster), "labels": map[string]any{orgClaimPolicyOwnerLabel: identity.Cluster}},
		"spec": map[string]any{
			"claims": claims,
			// A policy reserves both its claimers and foreign groups. Preserve
			// the platform's publishers when those groups overlap its policy.
			"providers": []any{
				map[string]any{"kind": "User", "name": "kcp-admin"},
				map[string]any{"kind": "User", "name": ProviderSAUsername},
			},
		},
	}}, nil
}

func sortedGroupNames(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func ensureOrgProviderClaimPolicy(ctx context.Context, config *rest.Config, identity OrgBootstrapIdentity, admitted sdkinstall.OrganizationBootstrap) error {
	policy, err := orgProviderClaimPolicy(identity, admitted)
	if err != nil {
		return err
	}
	client, err := dynamic.NewForConfig(bootstrap.PermissionClaimPolicyTargetConfig(config))
	if err != nil {
		return err
	}
	if policy == nil {
		err := client.Resource(orgClaimPolicyGVR).Delete(ctx, orgClaimPolicyName(identity.Cluster), metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return applyOrgObject(ctx, client, orgClaimPolicyGVR, policy)
}

// DeleteOrgProviderClaimPolicy removes the Hub-owned admission declaration for
// an exporter after its tenant bindings and export have been drained.
func DeleteOrgProviderClaimPolicy(ctx context.Context, config *rest.Config, cluster string) error {
	if cluster == "" {
		return nil
	}
	client, err := dynamic.NewForConfig(bootstrap.PermissionClaimPolicyTargetConfig(config))
	if err != nil {
		return err
	}
	err = client.Resource(orgClaimPolicyGVR).Delete(ctx, orgClaimPolicyName(cluster), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
