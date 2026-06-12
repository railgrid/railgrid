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

package providers

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	providersv1alpha1 "github.com/faroshq/faros-kedge/apis/providers/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/apiurl"
)

// provisioner owns the side-effects the catalog controller performs against
// kcp when a CatalogEntry is reconciled: creating the per-provider
// sub-workspace, applying inline APIResourceSchemas, and applying the
// APIExport that lets tenants bind.
//
// Phase 1B scope. Phase 1C will add the RBAC grant + MaximalPermissionPolicy
// that gate tenant binds.
type provisioner struct {
	kcpConfig *rest.Config
}

const providersParentWorkspace = "root:kedge:providers"

var (
	workspaceGVR = schema.GroupVersionResource{
		Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces",
	}
	apiResourceSchemaGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas",
	}
	apiExportGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports",
	}
	clusterRoleGVR = schema.GroupVersionResource{
		Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles",
	}
	clusterRoleBindingGVR = schema.GroupVersionResource{
		Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings",
	}
	serviceAccountGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "serviceaccounts",
	}
)

// ProviderSAName is the standard ServiceAccount name created in every
// provider's sub-workspace. The provider pod is expected to mount a
// kubeconfig minted from this SA's token.
const ProviderSAName = "provider"

// ProviderSANamespace is the namespace ProviderSAName lives in. The
// Enable-time edge-proxy grant derives the SA's qualified identity from
// this tuple, so it must stay in lockstep with EnsureProviderSA.
const ProviderSANamespace = "default"

// ProviderTokenSecretSuffix is appended to the SA name to form the
// kubernetes.io/service-account-token Secret that holds the provider's
// long-lived bearer. kcp's token controller populates it; the token does
// not expire (valid until the Secret or its ServiceAccount is deleted), so
// the provider pod — and any downstream consumer such as the kro cluster —
// never needs a rotation loop.
const ProviderTokenSecretSuffix = "-token"

// EnsureProviderSA creates the "provider" ServiceAccount in the sub-workspace
// and grants it cluster-admin within that workspace. Idempotent. Returns the
// fully-qualified SA cluster-role-bound name "system:serviceaccount:default:provider".
func (p *provisioner) EnsureProviderSA(ctx context.Context, providerName string) error {
	cl, err := p.clientFor(providersParentWorkspace + ":" + providerName)
	if err != nil {
		return err
	}
	sa := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ServiceAccount",
		"metadata":   map[string]any{"name": ProviderSAName, "namespace": ProviderSANamespace},
	}}
	if _, err := cl.Resource(serviceAccountGVR).Namespace(ProviderSANamespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ServiceAccount %s/%s: %w", ProviderSANamespace, ProviderSAName, err)
	}

	// cluster-admin in the sub-workspace only. The provider pod reaches
	// other workspaces via the APIExport's virtual workspace + accepted
	// permission claims — NOT via this SA.
	crbName := "kedge:providers:sa:" + ProviderSAName
	crb := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   map[string]any{"name": crbName},
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "ClusterRole",
			"name":     "cluster-admin",
		},
		"subjects": []any{
			map[string]any{
				"kind":      "ServiceAccount",
				"name":      ProviderSAName,
				"namespace": "default",
			},
		},
	}}
	if err := applyUnstructured(ctx, cl, clusterRoleBindingGVR, crb); err != nil {
		return fmt.Errorf("applying %s: %w", crbName, err)
	}
	return nil
}

// MintProviderKubeconfig ensures a long-lived (legacy) token for the provider
// SA and returns a base64-encoded exec-credential-less kubeconfig the provider
// pod can mount. The token is read from a kubernetes.io/service-account-token
// Secret populated by kcp's token controller, so it does not expire and needs
// no rotation. The server URL is hubExternalURL + /clusters/{sub-workspace-path}
// so the provider's typed Kubernetes clients land in its own workspace by default.
func (p *provisioner) MintProviderKubeconfig(ctx context.Context, providerName, hubExternalURL string) ([]byte, error) {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, providersParentWorkspace+":"+providerName)
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("typed kube client for %s: %w", providerName, err)
	}

	token, err := ensureLegacySAToken(ctx, typed, "default", ProviderSAName, ProviderSAName+ProviderTokenSecretSuffix)
	if err != nil {
		return nil, fmt.Errorf("ensuring SA token for %s: %w", providerName, err)
	}

	server := hubExternalURL
	if server == "" {
		// Fall back to the kcp host we're talking to. Useful in tests
		// when no public hub URL is configured.
		server = cfg.Host
	} else {
		server = apiurl.KCPClusterURL(server, providersParentWorkspace+":"+providerName)
	}

	// Minimal kubeconfig; the provider pod uses controller-runtime which
	// is happy with token auth + insecure-skip-tls-verify in dev.
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: kedge
  cluster:
    server: %s
    insecure-skip-tls-verify: true
contexts:
- name: kedge
  context:
    cluster: kedge
    user: kedge
current-context: kedge
users:
- name: kedge
  user:
    token: %s
`, server, token)
	return []byte(kc), nil
}

// ensureLegacySAToken creates (idempotently) a kubernetes.io/service-account-token
// Secret bound to saName and waits for kcp's token controller to populate its
// `token` field, then returns that token. Unlike a TokenRequest bearer this
// token does not expire — it stays valid until the Secret or its ServiceAccount
// is deleted — so callers need no rotation loop. Re-invoking reuses the existing
// Secret and returns the same token, keeping the value stable across reconciles.
func ensureLegacySAToken(ctx context.Context, cs kubernetes.Interface, namespace, saName, secretName string) (string, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: saName,
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}
	if _, err := cs.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating service-account-token Secret %s/%s: %w", namespace, secretName, err)
	}

	var token string
	if err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		got, err := cs.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if t := got.Data[corev1.ServiceAccountTokenKey]; len(t) > 0 {
			token = string(t)
			return true, nil
		}
		return false, nil
	}); err != nil {
		return "", fmt.Errorf("waiting for token controller to populate Secret %s/%s: %w", namespace, secretName, err)
	}
	return token, nil
}

// EncodeKubeconfig is a tiny helper for status reporting — surface the
// minted kubeconfig as a base64-encoded blob so cluster operators can fish
// it out of the CatalogEntry status without needing to read a Secret
// (relevant when --provider-secret-write is disabled).
func EncodeKubeconfig(kc []byte) string {
	return base64.StdEncoding.EncodeToString(kc)
}

// EnsureProviderWorkspace creates root:kedge:providers/{name} if it does not
// exist and waits for it to reach phase Ready. Idempotent. Returns the
// workspace's logical cluster ID (Workspace.spec.cluster) — the cluster name
// kcp embeds in the provider SA's token claims, which the Enable-time
// edges-proxy grant needs to build the qualified RBAC subject.
func (p *provisioner) EnsureProviderWorkspace(ctx context.Context, name string) (string, error) {
	parent, err := p.clientFor(providersParentWorkspace)
	if err != nil {
		return "", err
	}
	ws := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"type": map[string]any{"name": "universal", "path": "root"},
		},
	}}
	if _, err := parent.Resource(workspaceGVR).Create(ctx, ws, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating sub-workspace %s: %w", name, err)
	}

	// Wait for Ready so subsequent schema/export writes target a live
	// workspace; spec.cluster is populated by then.
	var cluster string
	if err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		got, err := parent.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		cluster, _, _ = unstructured.NestedString(got.Object, "spec", "cluster")
		return phase == "Ready", nil
	}); err != nil {
		return "", err
	}
	return cluster, nil
}

// ApplySchemas parses each inline APIResourceSchema body and applies it to
// the provider's sub-workspace. APIResourceSchemas are immutable in kcp, so
// the body author MUST embed a content-version in metadata.name (e.g.
// "v260522-abc.greetings.hello.cost.faros.sh"). Re-applying the same name
// is a no-op; a schema-body change must come with a new name.
func (p *provisioner) ApplySchemas(ctx context.Context, providerName string, schemas []providersv1alpha1.ProviderAPIResourceSchema) ([]string, error) {
	cl, err := p.clientFor(providersParentWorkspace + ":" + providerName)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(schemas))
	for i, s := range schemas {
		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal([]byte(s.Body), &u.Object); err != nil {
			return nil, fmt.Errorf("parsing schema[%d] (%s): %w", i, s.GroupResource, err)
		}
		// Defensive: require apiVersion + kind + name on the parsed object.
		if u.GetAPIVersion() == "" || u.GetKind() == "" || u.GetName() == "" {
			return nil, fmt.Errorf("schema[%d] (%s): body must include apiVersion, kind, and metadata.name", i, s.GroupResource)
		}
		if u.GetAPIVersion() != "apis.kcp.io/v1alpha1" || u.GetKind() != "APIResourceSchema" {
			return nil, fmt.Errorf("schema[%d] (%s): expected APIResourceSchema apis.kcp.io/v1alpha1, got %s/%s", i, s.GroupResource, u.GetAPIVersion(), u.GetKind())
		}
		if _, err := cl.Resource(apiResourceSchemaGVR).Create(ctx, u, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating schema %s: %w", u.GetName(), err)
		}
		names = append(names, u.GetName())
	}
	return names, nil
}

// ApplyAPIExport creates / updates the APIExport in the provider's sub-
// workspace that references the just-applied schemas. The
// permissionClaims on the export mirror the catalog entry's declared
// claims; Phase 3 will additionally set MaximalPermissionPolicy.
func (p *provisioner) ApplyAPIExport(ctx context.Context, providerName, exportName string, schemaNames []string, claims []providersv1alpha1.ProviderPermissionClaim) error {
	cl, err := p.clientFor(providersParentWorkspace + ":" + providerName)
	if err != nil {
		return err
	}
	resources := make([]any, 0, len(schemaNames))
	for _, n := range schemaNames {
		// Schema name format is "vYYMMDD-hash.<resource>.<group>" — derive
		// resource + group for the APIExport.spec.resources entry.
		group, resource := splitSchemaName(n)
		if group == "" || resource == "" {
			return fmt.Errorf("cannot derive group/resource from schema name %q", n)
		}
		resources = append(resources, map[string]any{
			"group":  group,
			"name":   resource,
			"schema": n,
			"storage": map[string]any{
				"crd": map[string]any{},
			},
		})
	}

	pcList := make([]any, 0, len(claims))
	for _, c := range claims {
		pc := map[string]any{
			"resource": c.Resource,
		}
		if c.Group != "" {
			pc["group"] = c.Group
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
			// MaximalPermissionPolicy is intentionally NOT set. With
			// Local{}, kcp gates tenant access to bound resources on RBAC
			// in *this* workspace for apis.kcp.io:binding:<user> subjects
			// — i.e. it caps the tenant too, not just the provider's
			// controllers. Without prefixed-subject ClusterRoles minted
			// here, that policy effectively denies tenants access to
			// their own bound CRs. The right way to scope provider
			// controller reach is via the permissionClaims list above
			// (which kcp enforces) plus the bind grant (ClusterRole +
			// CRB) ApplyBindGrant creates. If a future provider model
			// genuinely needs a maximal-permission policy, plumb in the
			// required apis.kcp.io:binding:* RBAC at the same time.
		},
	}}

	existing, err := cl.Resource(apiExportGVR).Get(ctx, exportName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cl.Resource(apiExportGVR).Create(ctx, desired, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating APIExport %s: %w", exportName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting existing APIExport %s: %w", exportName, err)
	}

	// Merge, don't clobber. spec.resources has multiple writers: this hub
	// controller (the catalog-declared schemas), the provider's one-shot
	// install step (e.g. templates.infrastructure.kedge.faros.sh with
	// storage.virtual), and the provider's Template controller (per-template
	// entries minted at runtime). A full replace here drops every entry not
	// derived from the CatalogEntry's schemas — which is exactly what silently
	// deletes the templates virtual-storage resource on a reconcile. So we
	// upsert only the entries we own (keyed by group+name) and preserve the rest.
	existingResources, _, err := unstructured.NestedSlice(existing.Object, "spec", "resources")
	if err != nil {
		return fmt.Errorf("reading existing spec.resources on APIExport %s: %w", exportName, err)
	}
	if err := unstructured.SetNestedSlice(desired.Object, mergeAPIExportResources(existingResources, resources), "spec", "resources"); err != nil {
		return fmt.Errorf("merging spec.resources for APIExport %s: %w", exportName, err)
	}

	// Patch spec to converge, preserving resourceVersion.
	desired.SetResourceVersion(existing.GetResourceVersion())
	if _, err := cl.Resource(apiExportGVR).Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating APIExport %s: %w", exportName, err)
	}
	return nil
}

// mergeAPIExportResources upserts the hub-owned entries (owned, derived from
// the CatalogEntry's schemas) into the existing spec.resources list, keyed by
// (group, name), while preserving every existing entry the hub does not own.
// Owned entries win (schema/storage refreshed from the catalog) and come first
// in the given order so the result stays deterministic; foreign entries — e.g.
// the provider's templates virtual-storage resource or runtime per-template
// entries — are kept verbatim.
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
			out = append(out, r) // unparseable — keep rather than risk dropping data
			continue
		}
		if _, isOwned := ownedKeys[k]; isOwned {
			continue // replaced by the owned version above
		}
		out = append(out, r)
	}
	return out
}

// ApplyBindGrant creates / updates the ClusterRole + ClusterRoleBinding in
// the provider's sub-workspace that lets any authenticated kedge user bind
// to the provider's APIExport from their own workspace. Without this grant,
// kcp refuses tenant-side APIBinding creates with 403.
//
// Subject is "system:authenticated" — the platform-installation contract is
// "every authenticated tenant may opt in to any installed provider". The
// platform admin is the gatekeeper at chart-install time. If finer scoping
// is needed later (e.g. allow-list per provider), this is the hook.
//
// Idempotent: re-apply on every reconcile.
func (p *provisioner) ApplyBindGrant(ctx context.Context, providerName, exportName string) error {
	cl, err := p.clientFor(providersParentWorkspace + ":" + providerName)
	if err != nil {
		return err
	}
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

// applyUnstructured is a tiny create-or-update helper for cluster-scoped
// resources. Preserves resourceVersion on update.
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

func (p *provisioner) clientFor(clusterPath string) (dynamic.Interface, error) {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterPath)
	return dynamic.NewForConfig(cfg)
}

// splitSchemaName parses a kcp APIResourceSchema metadata.name of the form
// "v260522-abc.greetings.hello.cost.faros.sh" → resource="greetings",
// group="hello.cost.faros.sh". The leading version segment is everything up
// to the FIRST dot; the resource is the next segment; the group is the rest.
func splitSchemaName(n string) (group, resource string) {
	// find first dot (end of version segment)
	first := -1
	for i := 0; i < len(n); i++ {
		if n[i] == '.' {
			first = i
			break
		}
	}
	if first < 0 || first == len(n)-1 {
		return "", ""
	}
	rest := n[first+1:]
	// find next dot (end of resource segment)
	second := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' {
			second = i
			break
		}
	}
	if second <= 0 || second == len(rest)-1 {
		return "", ""
	}
	return rest[second+1:], rest[:second]
}
