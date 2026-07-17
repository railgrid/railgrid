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

// Package install holds the one-shot bootstrap a kedge provider runs against
// its own kcp workspace, using the workspace-admin kubeconfig the platform
// admin onboarded (which already points at root:kedge:providers:<name>).
//
// It is the provider-side half of the bootstrap split: the admin UI/API creates
// the provider workspace + ServiceAccount + legacy-token kubeconfig, and the
// provider's `init` (driven by this package) applies everything that lives
// INSIDE the workspace — APIResourceSchemas, the APIExport, the
// APIExportEndpointSlice the multicluster manager watches, and the bind RBAC
// grant that lets tenants APIBind. The hub no longer provisions any of this.
//
// Every step is idempotent. The logic is ported from the former hub
// provisioner (pkg/hub/providers/provision.go) so the resulting objects are
// byte-for-byte compatible with what the hub used to create — with one
// deliberate change: identityHash for first-party permission claims is supplied
// by the caller (a Helm value the admin copies from the /bonkers root-identities
// view) instead of being resolved from the parent workspace, which this
// workspace-scoped kubeconfig cannot read.
package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

var (
	apiResourceSchemaGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas",
	}
	// APIExport is v1alpha2 (matches the hub provisioner and Enable flow).
	apiExportGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports",
	}
	apiExportEndpointSliceGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
	}
	clusterRoleGVR = schema.GroupVersionResource{
		Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles",
	}
	clusterRoleBindingGVR = schema.GroupVersionResource{
		Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings",
	}
)

// PermissionClaim is the provider-local mirror of a CatalogEntry permission
// claim. It is duplicated here (rather than imported from the kedge apis
// module) so the SDK has no dependency on the monorepo — keeping every
// provider's standalone build self-contained.
//
// IdentityHash is REQUIRED for first-party (*.faros.sh) claim groups: kcp
// rejects a permissionClaim on a non-built-in API type unless it carries the
// identityHash of the APIExport that serves it. The platform admin reads the
// hash from the /bonkers root-identities view and supplies it via the chart's
// Helm values. Built-in types (empty group / core k8s) need no hash — leave it
// empty.
type PermissionClaim struct {
	Group        string
	Resource     string
	Verbs        []string
	IdentityHash string
}

// Options bundles everything a provider's init needs.
type Options struct {
	// Config is the workspace-admin rest.Config (its Host already targets the
	// provider workspace cluster, e.g. .../clusters/root:kedge:providers:code).
	Config *rest.Config
	// ExportName is the provider's APIExport name (also the slice name by
	// convention), e.g. "code.providers.kedge.faros.sh".
	ExportName string
	// WorkspacePath is the logical-cluster path the APIExport lives in, e.g.
	// "root:kedge:providers:code". REQUIRED so the slice can publish endpoints.
	WorkspacePath string
	// SchemasDir holds APIResourceSchema YAML files (one document per file).
	// Empty or non-existent → no schemas applied (valid for schema-less
	// providers like infrastructure).
	SchemasDir string
	// Claims are the APIExport's permission claims (with admin-supplied
	// IdentityHash for first-party groups).
	Claims []PermissionClaim

	// CatalogEntryFile, when set, is the path to a CatalogEntry YAML the
	// provider self-registers into its OWN workspace (which the platform's
	// Provider controller bound to providers.kedge.faros.sh). Empty → skip
	// (e.g. providers whose CatalogEntry is applied to root:kedge:providers by
	// an admin instead). Applied last, after the APIExport exists.
	CatalogEntryFile string
}

// Bootstrap runs the full provider workspace bootstrap idempotently:
// schemas → APIExport → APIExportEndpointSlice → bind grant. Safe to re-run.
func Bootstrap(ctx context.Context, opts Options) error {
	if opts.Config == nil {
		return fmt.Errorf("install: Config is required")
	}
	if opts.ExportName == "" {
		return fmt.Errorf("install: ExportName is required")
	}
	if opts.WorkspacePath == "" {
		return fmt.Errorf("install: WorkspacePath is required to publish APIExportEndpointSlice endpoints")
	}
	cl, err := dynamic.NewForConfig(opts.Config)
	if err != nil {
		return fmt.Errorf("install: dynamic client: %w", err)
	}

	schemaNames, err := ApplySchemasFromDir(ctx, cl, opts.SchemasDir)
	if err != nil {
		return fmt.Errorf("install: apply schemas: %w", err)
	}
	if err := ApplyAPIExport(ctx, cl, opts.ExportName, schemaNames, opts.Claims); err != nil {
		return fmt.Errorf("install: apply APIExport: %w", err)
	}
	if err := EnsureAPIExportEndpointSlice(ctx, cl, opts.ExportName, opts.ExportName, opts.WorkspacePath); err != nil {
		return fmt.Errorf("install: ensure APIExportEndpointSlice: %w", err)
	}
	if err := ApplyBindGrant(ctx, cl, opts.ExportName); err != nil {
		return fmt.Errorf("install: apply bind grant: %w", err)
	}
	if opts.CatalogEntryFile != "" {
		if err := ApplyCatalogEntry(ctx, cl, opts.CatalogEntryFile); err != nil {
			return fmt.Errorf("install: apply CatalogEntry: %w", err)
		}
	}
	return nil
}

// catalogEntryGVR is the cluster-scoped CatalogEntry served by the
// providers.kedge.faros.sh APIExport, which the platform's Provider controller
// binds into each provider sub-workspace.
var catalogEntryGVR = schema.GroupVersionResource{
	Group: "providers.kedge.faros.sh", Version: "v1alpha1", Resource: "catalogentries",
}

// ApplyCatalogEntry reads a CatalogEntry YAML from path and create-or-updates it
// in the workspace cl targets (the provider's own sub-workspace). This is how a
// provider self-registers its catalog entry from inside its workspace, rather
// than an admin applying it to root:kedge:providers. Idempotent.
func ApplyCatalogEntry(ctx context.Context, cl dynamic.Interface, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading CatalogEntry file %s: %w", path, err)
	}
	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(raw, &u.Object); err != nil {
		return fmt.Errorf("parsing CatalogEntry file %s: %w", path, err)
	}
	if u.GetAPIVersion() != "providers.kedge.faros.sh/v1alpha1" || u.GetKind() != "CatalogEntry" {
		return fmt.Errorf("file %s: expected CatalogEntry providers.kedge.faros.sh/v1alpha1, got %s/%s", path, u.GetAPIVersion(), u.GetKind())
	}
	if u.GetName() == "" {
		return fmt.Errorf("file %s: metadata.name is required", path)
	}
	return applyUnstructured(ctx, cl, catalogEntryGVR, u)
}

// ApplySchemasFromDir reads every *.yaml file in dir as an APIResourceSchema and
// applies it, returning the applied schema names (metadata.name). Files are
// applied in sorted order for determinism. An empty dir (or "") yields no
// schemas. APIResourceSchemas are immutable in kcp, so a re-apply of the same
// name is a no-op (AlreadyExists tolerated); a body change must come with a new
// version-prefixed name.
func ApplySchemasFromDir(ctx context.Context, cl dynamic.Interface, dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading schemas dir %s: %w", dir, err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n := e.Name(); strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") {
			files = append(files, n)
		}
	}
	sort.Strings(files)

	names := make([]string, 0, len(files))
	for _, f := range files {
		path := filepath.Join(dir, f)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading schema file %s: %w", path, err)
		}
		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(raw, &u.Object); err != nil {
			return nil, fmt.Errorf("parsing schema file %s: %w", path, err)
		}
		if u.GetAPIVersion() != "apis.kcp.io/v1alpha1" || u.GetKind() != "APIResourceSchema" {
			return nil, fmt.Errorf("schema file %s: expected APIResourceSchema apis.kcp.io/v1alpha1, got %s/%s", path, u.GetAPIVersion(), u.GetKind())
		}
		if u.GetName() == "" {
			return nil, fmt.Errorf("schema file %s: metadata.name is required", path)
		}
		if _, err := cl.Resource(apiResourceSchemaGVR).Create(ctx, u, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating schema %s: %w", u.GetName(), err)
		}
		names = append(names, u.GetName())
	}
	return names, nil
}

// ApplyAPIExport creates / updates the provider's APIExport referencing the
// applied schemas and declaring the permission claims. identityHash for each
// claim is taken verbatim from the supplied PermissionClaim (admin-provided);
// no parent-workspace lookup is performed.
//
// Merge, don't clobber: spec.resources has multiple writers (this init step,
// plus any runtime controller that mints per-object entries, e.g. the
// infrastructure templates virtual storage). Only entries this step owns
// (keyed by group+name) are upserted; the rest are preserved.
func ApplyAPIExport(ctx context.Context, cl dynamic.Interface, exportName string, schemaNames []string, claims []PermissionClaim) error {
	resources := make([]any, 0, len(schemaNames))
	for _, n := range schemaNames {
		group, resource := splitSchemaName(n)
		if group == "" || resource == "" {
			return fmt.Errorf("cannot derive group/resource from schema name %q", n)
		}
		resources = append(resources, map[string]any{
			"group":   group,
			"name":    resource,
			"schema":  n,
			"storage": map[string]any{"crd": map[string]any{}},
		})
	}

	pcList := make([]any, 0, len(claims))
	for _, c := range claims {
		pc := map[string]any{"resource": c.Resource}
		if c.Group != "" {
			pc["group"] = c.Group
		}
		if c.IdentityHash != "" {
			pc["identityHash"] = c.IdentityHash
		}
		if len(c.Verbs) > 0 {
			verbs := make([]any, 0, len(c.Verbs))
			for _, v := range c.Verbs {
				verbs = append(verbs, v)
			}
			pc["verbs"] = verbs
		}
		pcList = append(pcList, pc)
	}

	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIExport",
		"metadata":   map[string]any{"name": exportName},
		"spec": map[string]any{
			"resources":        resources,
			"permissionClaims": pcList,
		},
	}}

	existing, err := cl.Resource(apiExportGVR).Get(ctx, exportName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err = cl.Resource(apiExportGVR).Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating APIExport %s: %w", exportName, err)
		}
		// Read-after-write: when the provider workspace is still being
		// (re)provisioned, the Create can report success without the object
		// durably landing (the write races the workspace bootstrap). Poll until
		// the export is observable so a lost write fails LOUDLY here — where the
		// init retries — instead of surfacing later as the misleading
		// "no permission to bind to export" from APIExportEndpointSlice admission,
		// which reports a *missing* export as a bind-permission error.
		if err := waitForResourceExists(ctx, cl, apiExportGVR, exportName); err != nil {
			return fmt.Errorf("APIExport %s not observable after create (workspace may still be provisioning): %w", exportName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting existing APIExport %s: %w", exportName, err)
	}

	existingResources, _, err := unstructured.NestedSlice(existing.Object, "spec", "resources")
	if err != nil {
		return fmt.Errorf("reading existing spec.resources on APIExport %s: %w", exportName, err)
	}
	if err := unstructured.SetNestedSlice(desired.Object, mergeAPIExportResources(existingResources, resources), "spec", "resources"); err != nil {
		return fmt.Errorf("merging spec.resources for APIExport %s: %w", exportName, err)
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	if _, err := cl.Resource(apiExportGVR).Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating APIExport %s: %w", exportName, err)
	}
	return nil
}

// waitForResourceExists polls until a cluster-scoped resource is observable via
// Get, or the deadline passes. It guards create-then-vanish races: when a
// provider workspace is being (re)provisioned concurrently, a Create can return
// success without the object durably landing. Returns nil once the object is
// readable, or the last error / a timeout otherwise.
func waitForResourceExists(ctx context.Context, cl dynamic.Interface, gvr schema.GroupVersionResource, name string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 15*time.Second, true, func(ctx context.Context) (bool, error) {
		if _, err := cl.Resource(gvr).Get(ctx, name, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

// EnsureAPIExportEndpointSlice ensures an APIExportEndpointSlice referencing the
// provider's APIExport exists in the provider workspace. spec.export is
// immutable, so a pre-existing slice with a stale path is deleted + recreated.
func EnsureAPIExportEndpointSlice(ctx context.Context, cl dynamic.Interface, sliceName, exportName, workspacePath string) error {
	want := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIExportEndpointSlice",
		"metadata":   map[string]any{"name": sliceName},
		"spec": map[string]any{
			"export": map[string]any{
				"name": exportName,
				"path": workspacePath,
			},
		},
	}}

	existing, err := cl.Resource(apiExportEndpointSliceGVR).Get(ctx, sliceName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err = cl.Resource(apiExportEndpointSliceGVR).Create(ctx, want, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating APIExportEndpointSlice %s: %w", sliceName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting APIExportEndpointSlice %s: %w", sliceName, err)
	}
	existingPath, _, _ := unstructured.NestedString(existing.Object, "spec", "export", "path")
	if existingPath == workspacePath {
		return nil
	}
	if err := cl.Resource(apiExportEndpointSliceGVR).Delete(ctx, sliceName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting stale APIExportEndpointSlice %s: %w", sliceName, err)
	}
	if _, err = cl.Resource(apiExportEndpointSliceGVR).Create(ctx, want, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("recreating APIExportEndpointSlice %s: %w", sliceName, err)
	}
	return nil
}

// ApplyBindGrant creates / updates the ClusterRole + ClusterRoleBinding in the
// provider workspace that lets any authenticated kedge user bind to the
// provider's APIExport from their own workspace. Without it, kcp refuses
// tenant-side APIBinding creates with 403. Subject is "system:authenticated":
// the platform admin is the gatekeeper at onboard/install time.
func ApplyBindGrant(ctx context.Context, cl dynamic.Interface, exportName string) error {
	roleName := "kedge:providers:bind:" + exportName
	cr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata":   map[string]any{"name": roleName},
		"rules": []any{
			map[string]any{
				"apiGroups":     []any{"apis.kcp.io"},
				"resources":     []any{"apiexports"},
				"verbs":         []any{"bind"},
				"resourceNames": []any{exportName},
			},
		},
	}}
	if err := applyUnstructured(ctx, cl, clusterRoleGVR, cr); err != nil {
		return fmt.Errorf("applying ClusterRole %s: %w", roleName, err)
	}
	crb := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   map[string]any{"name": roleName},
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "ClusterRole",
			"name":     roleName,
		},
		"subjects": []any{
			map[string]any{
				"apiGroup": "rbac.authorization.k8s.io",
				"kind":     "Group",
				"name":     "system:authenticated",
			},
		},
	}}
	if err := applyUnstructured(ctx, cl, clusterRoleBindingGVR, crb); err != nil {
		return fmt.Errorf("applying ClusterRoleBinding %s: %w", roleName, err)
	}
	return nil
}

// applyUnstructured is a create-or-update helper for cluster-scoped resources.
func applyUnstructured(ctx context.Context, cl dynamic.Interface, gvr schema.GroupVersionResource, desired *unstructured.Unstructured) error {
	existing, err := cl.Resource(gvr).Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cl.Resource(gvr).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	_, err = cl.Resource(gvr).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

// mergeAPIExportResources upserts the owned entries (keyed by group+name) into
// existing, preserving every existing entry not owned. Owned entries win and
// come first for deterministic output.
func mergeAPIExportResources(existing, owned []any) []any {
	key := func(r any) (string, bool) {
		m, ok := r.(map[string]any)
		if !ok {
			return "", false
		}
		group, _ := m["group"].(string)
		name, _ := m["name"].(string)
		return group + "/" + name, true
	}
	ownedKeys := make(map[string]struct{}, len(owned))
	for _, r := range owned {
		if k, ok := key(r); ok {
			ownedKeys[k] = struct{}{}
		}
	}
	out := make([]any, 0, len(owned)+len(existing))
	out = append(out, owned...)
	for _, r := range existing {
		k, ok := key(r)
		if !ok {
			out = append(out, r)
			continue
		}
		if _, isOwned := ownedKeys[k]; isOwned {
			continue
		}
		out = append(out, r)
	}
	return out
}

// splitSchemaName parses a kcp APIResourceSchema metadata.name of the form
// "v260522-abc.greetings.hello.cost.faros.sh" → resource="greetings",
// group="hello.cost.faros.sh". The version segment is everything up to the
// first dot; the resource is the next segment; the group is the rest.
func splitSchemaName(n string) (group, resource string) {
	first := strings.IndexByte(n, '.')
	if first < 0 || first == len(n)-1 {
		return "", ""
	}
	rest := n[first+1:]
	second := strings.IndexByte(rest, '.')
	if second <= 0 || second == len(rest)-1 {
		return "", ""
	}
	return rest[second+1:], rest[:second]
}

// toUnstructured is retained for callers that build typed objects elsewhere.
func toUnstructured(v any) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &out.Object); err != nil {
		return nil, err
	}
	return out, nil
}

var _ = toUnstructured
