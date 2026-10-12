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

package kcp

import (
	"context"
	"fmt"
	"strings"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/railgrid/railgrid/config/kcp"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// CoreExportName is the merged platform APIExport every tenant workspace binds
// (config/kcp/apiexport-core.railgrid.ai.yaml), and CoreBindingName the name
// of that binding in each workspace.
const (
	CoreExportName  = "core.railgrid.ai"
	CoreBindingName = "railgrid"

	coreExportFile       = "apiexport-core.railgrid.ai.yaml"
	tenancyIdentityToken = "__TENANCY_IDENTITY_HASH__"
)

var logicalClusterGVR = schema.GroupVersionResource{
	Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
}

// coreBindingClaims is the claim set the hub keeps accepted on every tenant's
// `railgrid` APIBinding: exactly the permissionClaims the embedded
// core.railgrid.ai APIExport declares, each Accepted, scoped by the export's
// defaultSelector where it has one and matchAll otherwise.
//
// Reading the generated export rather than keeping a second list here is what
// keeps the two from drifting: the export is what kcp enforces, and a binding
// claim with different verbs reports PermissionClaimsMismatch and serves
// nothing. The tenancy.kcp.io placeholder is filled from the identity hash
// Bootstrap resolved; before Bootstrap ran that claim cannot be built, and the
// caller gets an error rather than a binding with an empty identity.
func (b *Bootstrapper) coreBindingClaims() ([]apisv1alpha2.AcceptablePermissionClaim, error) {
	raw, err := kcp.ProvidersFS.ReadFile(coreExportFile)
	if err != nil {
		return nil, fmt.Errorf("reading embedded %s: %w", coreExportFile, err)
	}
	var export apisv1alpha2.APIExport
	if err := yaml.Unmarshal(raw, &export); err != nil {
		return nil, fmt.Errorf("decoding embedded %s: %w", coreExportFile, err)
	}
	out := make([]apisv1alpha2.AcceptablePermissionClaim, 0, len(export.Spec.PermissionClaims))
	for _, pc := range export.Spec.PermissionClaims {
		if pc.IdentityHash == tenancyIdentityToken {
			if b.workspaceIdentityHash == "" {
				return nil, fmt.Errorf("claim on %s/%s needs the tenancy.kcp.io identity hash, which Bootstrap has not resolved", pc.Group, pc.Resource)
			}
			pc.IdentityHash = b.workspaceIdentityHash
		}
		selector := apisv1alpha2.PermissionClaimSelector{MatchAll: true}
		if pc.DefaultSelector != nil {
			selector = *pc.DefaultSelector
		}
		out = append(out, apisv1alpha2.AcceptablePermissionClaim{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: pc.GroupResource,
					Verbs:         append([]string(nil), pc.Verbs...),
					IdentityHash:  pc.IdentityHash,
				},
				Selector: selector,
			},
			State: apisv1alpha2.ClaimAccepted,
		})
	}
	return out, nil
}

// EnsureCoreBindingClaims brings the `railgrid` binding in the logical cluster
// clusterName to the claim set the embedded core.railgrid.ai export declares,
// and reports whether it changed anything. It is the per-cluster half of
// EnsureChildWorkspaceRailgridBinding, used by the binding-claims reconciler
// for workspaces it reaches by cluster ID rather than by path.
//
// The binding is read first and left alone unless it really is the hub's
// binding to the core export: the reconciler sees every APIBinding a tenant
// holds, and a tenant could name one of theirs `railgrid`.
func (b *Bootstrapper) EnsureCoreBindingClaims(ctx context.Context, clusterName, bindingName string) (bool, error) {
	if clusterName == "" || bindingName == "" {
		return false, fmt.Errorf("EnsureCoreBindingClaims: clusterName and bindingName are required")
	}
	wsClient, err := dynamic.NewForConfig(configForPath(b.config, clusterName))
	if err != nil {
		return false, fmt.Errorf("creating cluster client for %s: %w", clusterName, err)
	}
	u, err := wsClient.Resource(apiBindingGVR).Get(ctx, bindingName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("getting APIBinding %q in %s: %w", bindingName, clusterName, err)
	}
	path, _, _ := unstructured.NestedString(u.Object, "spec", "reference", "export", "path")
	name, _, _ := unstructured.NestedString(u.Object, "spec", "reference", "export", "name")
	if path != kcppaths.SystemControllers || name != CoreExportName {
		return false, nil
	}
	desired, err := b.coreBindingClaims()
	if err != nil {
		return false, err
	}
	return updateAPIBindingClaims(ctx, wsClient, bindingName, desired)
}

// ReconcileProviderBindingClaims is ReacceptProviderAPIBindingClaims addressed
// by logical cluster instead of by (org, workspace) path: the binding-claims
// reconciler learns of a tenant binding through the core.railgrid.ai virtual
// workspace, where the cluster ID is all it has. Same policy, same writer.
//
// Unlike the admin entry point it accepts an empty claim set: the reconciler
// only calls it for a provider whose CatalogEntry the hub has observed, so an
// empty list is the provider declaring nothing, and the binding follows.
func (b *Bootstrapper) ReconcileProviderBindingClaims(ctx context.Context, clusterName, bindingName string, claims []ProviderClaim) (bool, error) {
	if clusterName == "" || bindingName == "" {
		return false, fmt.Errorf("ReconcileProviderBindingClaims: clusterName and bindingName are required")
	}
	wsClient, err := dynamic.NewForConfig(configForPath(b.config, clusterName))
	if err != nil {
		return false, fmt.Errorf("creating cluster client for %s: %w", clusterName, err)
	}
	changed, err := reacceptClaimsOnBinding(ctx, wsClient, bindingName, claims)
	if err != nil {
		return false, fmt.Errorf("APIBinding %q in %s: %w", bindingName, clusterName, err)
	}
	return changed, nil
}

// ResolveClusterPath returns the canonical workspace path of a logical cluster
// (e.g. root:railgrid:tenants:<org>:<ws>), read from the kcp.io/path annotation
// kcp stamps on its LogicalCluster.
func (b *Bootstrapper) ResolveClusterPath(ctx context.Context, clusterName string) (string, error) {
	if clusterName == "" {
		return "", fmt.Errorf("ResolveClusterPath: clusterName is required")
	}
	cl, err := dynamic.NewForConfig(configForPath(b.config, clusterName))
	if err != nil {
		return "", fmt.Errorf("creating cluster client for %s: %w", clusterName, err)
	}
	lc, err := cl.Resource(logicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting LogicalCluster for %s: %w", clusterName, err)
	}
	path := strings.TrimSpace(lc.GetAnnotations()["kcp.io/path"])
	if path == "" {
		return "", fmt.Errorf("LogicalCluster %s carries no kcp.io/path annotation", clusterName)
	}
	return path, nil
}

// decodeClaims converts the typed claim list into the unstructured items an
// APIBinding update carries, so only spec.permissionClaims is rewritten and
// nothing this build's kcp SDK does not know about is dropped.
func decodeClaims(desired []apisv1alpha2.AcceptablePermissionClaim) ([]any, error) {
	items := make([]any, 0, len(desired))
	for i := range desired {
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&desired[i])
		if err != nil {
			return nil, fmt.Errorf("encoding permission claim: %w", err)
		}
		items = append(items, raw)
	}
	return items, nil
}
