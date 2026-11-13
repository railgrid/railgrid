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
	"encoding/json"
	"errors"
	"fmt"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
)

// WorkspaceSeedResult says how far one seed got in one workspace.
type WorkspaceSeedResult struct {
	// BindingsCreated counts the APIBindings this call created.
	BindingsCreated int
	// BindingsChanged counts the existing bindings whose claims or
	// annotations this call rewrote.
	BindingsChanged int
	// ObjectsApplied counts the objects applied.
	ObjectsApplied int
	// Unbound counts the seed's own bindings that are not Bound yet,
	// including ones this call just created.
	Unbound int
	// Waiting names what is not there yet: a binding of the seed that is not
	// Bound, and the objects held back by it or by their whenBound. Empty
	// when the workspace holds everything the seed declares.
	Waiting []string
}

// EnsureWorkspaceSeed brings the logical cluster clusterName to what the seed
// declares: its APIBindings, with the listed permission claims accepted and
// the tenant-access annotation set, and, once those are Bound, its objects.
// It writes with the hub's own credential and is idempotent.
//
// It never waits. A binding kcp has not bound yet holds the objects back and
// is reported in Waiting; the caller comes back when the binding changes.
//
// A binding of the seed's name that the seed does not own is not touched and
// fails the call: one that references another export, or one without the seed
// annotation, which is somebody's deliberate binding (a provider enabled
// under that name, say). The remaining bindings and objects are still
// attempted, and every failure is in the returned error.
func (b *Bootstrapper) EnsureWorkspaceSeed(ctx context.Context, clusterName, seedName string, spec adminv1alpha1.WorkspaceSeedSpec) (WorkspaceSeedResult, error) {
	if clusterName == "" || seedName == "" {
		return WorkspaceSeedResult{}, fmt.Errorf("EnsureWorkspaceSeed: clusterName and seedName are required")
	}
	wsClient, err := dynamic.NewForConfig(configForPath(b.config, clusterName))
	if err != nil {
		return WorkspaceSeedResult{}, fmt.Errorf("creating cluster client for %s: %w", clusterName, err)
	}
	return ensureWorkspaceSeed(ctx, wsClient, seedName, spec)
}

func ensureWorkspaceSeed(ctx context.Context, wsClient dynamic.Interface, seedName string, spec adminv1alpha1.WorkspaceSeedSpec) (WorkspaceSeedResult, error) {
	var (
		result WorkspaceSeedResult
		errs   []error
	)
	list, err := wsClient.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return result, fmt.Errorf("listing APIBindings: %w", err)
	}
	byName := make(map[string]*unstructured.Unstructured, len(list.Items))
	for i := range list.Items {
		byName[list.Items[i].GetName()] = &list.Items[i]
	}

	ownBound := true
	for i := range spec.Bindings {
		sb := &spec.Bindings[i]
		bound, created, changed, err := ensureSeedBinding(ctx, wsClient, seedName, sb, byName[sb.Name])
		if err != nil {
			errs = append(errs, fmt.Errorf("binding %q: %w", sb.Name, err))
			ownBound = false
			continue
		}
		if created {
			result.BindingsCreated++
		}
		if changed {
			result.BindingsChanged++
		}
		if !bound {
			ownBound = false
			result.Unbound++
			result.Waiting = append(result.Waiting, fmt.Sprintf("APIBinding %s is not Bound", sb.Name))
		}
	}

	boundExports := map[adminv1alpha1.SeedExportReference]bool{}
	for _, item := range byName {
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		if item.GetDeletionTimestamp() != nil || phase != "Bound" {
			continue
		}
		path, _, _ := unstructured.NestedString(item.Object, "spec", "reference", "export", "path")
		name, _, _ := unstructured.NestedString(item.Object, "spec", "reference", "export", "name")
		boundExports[adminv1alpha1.SeedExportReference{Path: path, Name: name}] = true
	}

	for i := range spec.Objects {
		so := &spec.Objects[i]
		obj, gvr, err := seedObject(so)
		if err != nil {
			errs = append(errs, fmt.Errorf("object %d: %w", i, err))
			continue
		}
		label := fmt.Sprintf("%s %s", gvr.Resource, obj.GetName())
		if !ownBound {
			result.Waiting = append(result.Waiting, label+" waits for the seed's bindings")
			continue
		}
		if missing := firstUnbound(so.WhenBound, boundExports); missing != nil {
			result.Waiting = append(result.Waiting, fmt.Sprintf("%s waits for a binding of %s:%s", label, missing.Path, missing.Name))
			continue
		}
		if err := applySeedObject(ctx, wsClient, gvr, obj); err != nil {
			errs = append(errs, fmt.Errorf("applying %s: %w", label, err))
			continue
		}
		result.ObjectsApplied++
	}
	return result, errors.Join(errs...)
}

// ensureSeedBinding creates the binding or brings an existing one the seed
// owns to the seed's claims and annotations. existing is the binding of that
// name as listed, or nil.
func ensureSeedBinding(ctx context.Context, wsClient dynamic.Interface, seedName string, sb *adminv1alpha1.SeedBinding, existing *unstructured.Unstructured) (bound, created, changed bool, err error) {
	if sb.Name == CoreBindingName {
		return false, false, false, fmt.Errorf("the name %q is the hub's own binding", CoreBindingName)
	}
	claims := seedBindingClaims(sb)
	annotations := map[string]string{
		adminv1alpha1.WorkspaceSeedAnnotation: seedName,
		adminv1alpha1.TenantAccessAnnotation:  string(seedTenantAccess(sb)),
	}
	if existing == nil {
		u, err := newProviderAPIBinding(sb.Name, sb.Export.Path, sb.Export.Name, claims, annotations)
		if err != nil {
			return false, false, false, err
		}
		if _, err := wsClient.Resource(apiBindingGVR).Create(ctx, u, metav1.CreateOptions{}); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return false, false, false, fmt.Errorf("creating APIBinding: %w", err)
			}
			// Another reconcile created it between the list and here; the
			// next pass sees it.
			return false, false, false, nil
		}
		return false, true, false, nil
	}

	path, _, _ := unstructured.NestedString(existing.Object, "spec", "reference", "export", "path")
	name, _, _ := unstructured.NestedString(existing.Object, "spec", "reference", "export", "name")
	if path != sb.Export.Path || name != sb.Export.Name {
		return false, false, false, fmt.Errorf("an APIBinding of that name references %s:%s, not %s:%s; it is left alone", path, name, sb.Export.Path, sb.Export.Name)
	}
	if owner := existing.GetAnnotations()[adminv1alpha1.WorkspaceSeedAnnotation]; owner != seedName {
		if owner == "" {
			return false, false, false, fmt.Errorf("an APIBinding of that name exists and was not created by a WorkspaceSeed; it is left alone")
		}
		return false, false, false, fmt.Errorf("an APIBinding of that name belongs to WorkspaceSeed %q; it is left alone", owner)
	}
	if existing.GetDeletionTimestamp() != nil {
		return false, false, false, nil
	}

	if existing.GetAnnotations()[adminv1alpha1.TenantAccessAnnotation] != annotations[adminv1alpha1.TenantAccessAnnotation] {
		patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{
			adminv1alpha1.TenantAccessAnnotation: annotations[adminv1alpha1.TenantAccessAnnotation],
		}}})
		if err != nil {
			return false, false, false, err
		}
		if _, err := wsClient.Resource(apiBindingGVR).Patch(ctx, sb.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return false, false, false, fmt.Errorf("setting tenant access on APIBinding: %w", err)
		}
		changed = true
	}
	claimsChanged, err := updateAPIBindingClaims(ctx, wsClient, sb.Name, claims)
	if err != nil {
		return false, changed, false, err
	}
	phase, _, _ := unstructured.NestedString(existing.Object, "status", "phase")
	// Claims just rewritten are not what the listed status was computed
	// from; the binding's next event brings the caller back.
	return phase == "Bound" && !claimsChanged, false, changed || claimsChanged, nil
}

func seedTenantAccess(sb *adminv1alpha1.SeedBinding) adminv1alpha1.SeedTenantAccess {
	if sb.TenantAccess == "" {
		return adminv1alpha1.SeedTenantAccessReadWrite
	}
	return sb.TenantAccess
}

// seedBindingClaims turns the seed's claims into the binding's: each
// Accepted, scoped by its selector, or matchAll without one.
func seedBindingClaims(sb *adminv1alpha1.SeedBinding) []apisv1alpha2.AcceptablePermissionClaim {
	if len(sb.PermissionClaims) == 0 {
		return nil
	}
	out := make([]apisv1alpha2.AcceptablePermissionClaim, 0, len(sb.PermissionClaims))
	for i := range sb.PermissionClaims {
		pc := &sb.PermissionClaims[i]
		selector := apisv1alpha2.PermissionClaimSelector{MatchAll: true}
		if pc.Selector != nil && (len(pc.Selector.MatchLabels) > 0 || len(pc.Selector.MatchExpressions) > 0) {
			selector = apisv1alpha2.PermissionClaimSelector{LabelSelector: *pc.Selector.DeepCopy()}
		}
		out = append(out, apisv1alpha2.AcceptablePermissionClaim{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: pc.Group, Resource: pc.Resource},
					Verbs:         append([]string(nil), pc.Verbs...),
					IdentityHash:  pc.IdentityHash,
				},
				Selector: selector,
			},
			State: apisv1alpha2.ClaimAccepted,
		})
	}
	return out
}

// seedObject decodes a seed object's manifest and derives where it goes.
func seedObject(so *adminv1alpha1.SeedObject) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal(so.Manifest.Raw, &obj.Object); err != nil {
		return nil, schema.GroupVersionResource{}, fmt.Errorf("decoding manifest: %w", err)
	}
	if obj.GetAPIVersion() == "" || obj.GetKind() == "" || obj.GetName() == "" {
		return nil, schema.GroupVersionResource{}, fmt.Errorf("manifest needs apiVersion, kind and metadata.name")
	}
	gv, err := schema.ParseGroupVersion(obj.GetAPIVersion())
	if err != nil {
		return nil, schema.GroupVersionResource{}, fmt.Errorf("manifest apiVersion: %w", err)
	}
	return obj, gv.WithResource(so.Resource), nil
}

func firstUnbound(refs []adminv1alpha1.SeedExportReference, bound map[adminv1alpha1.SeedExportReference]bool) *adminv1alpha1.SeedExportReference {
	for i := range refs {
		if !bound[refs[i]] {
			return &refs[i]
		}
	}
	return nil
}

// applySeedObject server-side applies the object with force under the seed
// field manager.
func applySeedObject(ctx context.Context, wsClient dynamic.Interface, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	data, err := json.Marshal(obj.Object)
	if err != nil {
		return err
	}
	force := true
	var res dynamic.ResourceInterface = wsClient.Resource(gvr)
	if ns := obj.GetNamespace(); ns != "" {
		res = wsClient.Resource(gvr).Namespace(ns)
	}
	_, err = res.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: adminv1alpha1.WorkspaceSeedFieldManager,
		Force:        &force,
	})
	return err
}
