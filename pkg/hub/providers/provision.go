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

	"github.com/kcp-dev/sdk/apis/core"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/faroshq/faros/pkg/apiurl"
	"github.com/faroshq/faros/pkg/kcppaths"
)

// Provisioner owns the kcp-side side-effects of provisioning a provider:
// creating the per-provider sub-workspace, the "provider" ServiceAccount, and
// the minted kubeconfig Secret. It is driven by the Provider CR reconciler
// (provider_controller.go); the provider's own APIExport/schemas come from its
// `init`.
type Provisioner struct {
	kcpConfig *rest.Config
}

// NewProvisioner returns a Provisioner that performs provider-workspace
// side-effects (workspace, ServiceAccount, minted kubeconfig) against kcp using
// the given admin config. Used by the admin onboarding API
// (pkg/hub/admin); the catalog controller no longer provisions.
func NewProvisioner(kcpConfig *rest.Config) *Provisioner {
	return &Provisioner{kcpConfig: kcpConfig}
}

// providersParentWorkspace is the parent of per-provider sub-workspaces
// (root:faros:providers:<name>). NOTE: APIExports and Provider/CatalogEntry
// objects no longer live here — they live in root:faros:system:controllers and
// root:faros:system:providers respectively.
const providersParentWorkspace = kcppaths.ProvidersParent

var (
	workspaceGVR = schema.GroupVersionResource{
		Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces",
	}
	clusterRoleBindingGVR = schema.GroupVersionResource{
		Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings",
	}
	serviceAccountGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "serviceaccounts",
	}
	namespaceGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "namespaces",
	}
	logicalClusterGVR = schema.GroupVersionResource{
		Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
	}
	apiExportGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports",
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

// EnsureProviderSA creates the "provider" ServiceAccount in the platform
// provider's sub-workspace (root:faros:providers/{name}) and grants it
// cluster-admin within that workspace. Idempotent.
func (p *Provisioner) EnsureProviderSA(ctx context.Context, providerName string) error {
	return p.EnsureProviderSAAtPath(ctx, providersParentWorkspace+":"+providerName)
}

// EnsureProviderSAAtPath is EnsureProviderSA against an arbitrary provider
// workspace path. Org-owned providers live at
// root:faros:tenants/{org}/providers/{name} rather than under the platform
// parent, but are otherwise identical — same `provider` WorkspaceType, so the
// same missing `default` namespace and the same workspace-scoped cluster-admin
// grant apply.
func (p *Provisioner) EnsureProviderSAAtPath(ctx context.Context, workspacePath string) error {
	cl, err := p.clientFor(workspacePath)
	if err != nil {
		return err
	}
	// The `provider` WorkspaceType does NOT extend universal, so the `default`
	// namespace is not auto-created. Ensure it before placing the SA there.
	ns := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": ProviderSANamespace},
	}}
	if _, err := cl.Resource(namespaceGVR).Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensuring namespace %s in provider workspace: %w", ProviderSANamespace, err)
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
	crbName := "faros:providers:sa:" + ProviderSAName
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
// no rotation. The server URL is hubExternalURL + /clusters/{logical-cluster-id}
// so the provider's typed Kubernetes clients land in its own workspace by default.
// The ID (not the workspace path) is used so the kubeconfig works once requests
// reach a kcp shard, which only resolves /clusters/<id>.
func (p *Provisioner) MintProviderKubeconfig(ctx context.Context, providerName, hubExternalURL string) ([]byte, error) {
	return p.MintProviderKubeconfigAtPath(ctx, providersParentWorkspace+":"+providerName, hubExternalURL)
}

// MintProviderKubeconfigAtPath is MintProviderKubeconfig against an arbitrary
// provider workspace path, for org-owned providers under
// root:faros:tenants/{org}/providers/{name}.
//
// The resulting kubeconfig is what an Org admin installs their provider's Helm
// chart with. It is scoped to that one workspace: the SA is cluster-admin
// there and nowhere else, so it can create the provider's APIExport, schemas,
// endpoint slice, and CatalogEntry, but cannot read the Org workspace above it
// or any team workspace beside it. Cross-workspace reach only ever comes from
// the APIExport's permission claims, which each consuming workspace accepts
// individually at Enable time.
func (p *Provisioner) MintProviderKubeconfigAtPath(ctx context.Context, workspacePath, hubExternalURL string) ([]byte, error) {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, workspacePath)
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("typed kube client for %s: %w", workspacePath, err)
	}

	token, err := ensureLegacySAToken(ctx, typed, "default", ProviderSAName, ProviderSAName+ProviderTokenSecretSuffix)
	if err != nil {
		return nil, fmt.Errorf("ensuring SA token for %s: %w", workspacePath, err)
	}

	// Resolve the provider workspace's logical cluster ID. The kubeconfig must
	// address kcp by ID (/clusters/<id>), not by workspace path: kcp shards only
	// resolve /clusters/<id>, and workspace-path resolution is front-proxy-only.
	// A provider kubeconfig pointed at the path 404s once the request reaches a
	// shard (the SA token also carries this ID in its clusterName claim).
	clusterID, err := resolveLogicalClusterID(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("resolving logical cluster ID for %s: %w", workspacePath, err)
	}

	server := hubExternalURL
	if server == "" {
		// Fall back to the kcp host we're talking to. Useful in tests
		// when no public hub URL is configured.
		server = cfg.Host
	} else {
		server = apiurl.KCPClusterURL(server, clusterID)
	}

	// Minimal kubeconfig; the provider pod uses controller-runtime which
	// is happy with token auth + insecure-skip-tls-verify in dev.
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: faros
  cluster:
    server: %s
    insecure-skip-tls-verify: true
contexts:
- name: faros
  context:
    cluster: faros
    user: faros
current-context: faros
users:
- name: faros
  user:
    token: %s
`, server, token)
	return []byte(kc), nil
}

// resolveLogicalClusterID returns the kcp logical cluster ID for the workspace
// addressed by cfg (cfg.Host must already point at the target workspace, by path
// or ID). It reads the well-known LogicalCluster object named "cluster" and
// returns its `kcp.io/cluster` annotation. The ID is required for kubeconfigs:
// kcp shards only resolve /clusters/<id>, while workspace-path resolution is
// front-proxy-only, so a path-based server URL 404s once a request reaches a
// shard.
func resolveLogicalClusterID(ctx context.Context, cfg *rest.Config) (string, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("dynamic client: %w", err)
	}
	lc, err := dyn.Resource(logicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting LogicalCluster: %w", err)
	}
	id := lc.GetAnnotations()["kcp.io/cluster"]
	if id == "" {
		return "", fmt.Errorf("LogicalCluster has no kcp.io/cluster annotation")
	}
	return id, nil
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

// EnsureProviderWorkspace creates root:faros:providers/{name} if it does not
// exist and waits for it to reach phase Ready. Idempotent. Returns the
// workspace's logical cluster ID (Workspace.spec.cluster) — the cluster name
// kcp embeds in the provider SA's token claims, which the Enable-time
// edges-proxy grant needs to build the qualified RBAC subject.
func (p *Provisioner) EnsureProviderWorkspace(ctx context.Context, name string) (string, error) {
	parent, err := p.clientFor(providersParentWorkspace)
	if err != nil {
		return "", err
	}
	// Use the restricted `provider` WorkspaceType (config/kcp/workspacetype-provider.yaml,
	// defined under root:faros): no universal → the provider cannot create
	// Workspaces; a defaultAPIBinding pulls in the CatalogEntry export.
	ws := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"type": map[string]any{"name": "provider", "path": kcppaths.Root},
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

// ResolveAPIExportIdentityHash returns the kcp identity hash an APIExport
// publishes in its status, or "" when it cannot be read.
//
// Self-hosted providers need this for permission claims against first-party
// groups: kcp validates a claim's identityHash against the export it targets,
// and a wrong one yields a binding that succeeds while the provider sees none
// of the resources it claimed. Resolving it here is what stops that value from
// being a manual copy out of an admin debug view.
func (p *Provisioner) ResolveAPIExportIdentityHash(ctx context.Context, workspacePath, exportName string) (string, error) {
	if workspacePath == "" || exportName == "" {
		return "", fmt.Errorf("ResolveAPIExportIdentityHash: workspacePath and exportName are required")
	}
	cl, err := p.clientFor(workspacePath)
	if err != nil {
		return "", err
	}
	export, err := cl.Resource(apiExportGVR).Get(ctx, exportName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting APIExport %s in %s: %w", exportName, workspacePath, err)
	}
	hash, _, _ := unstructured.NestedString(export.Object, "status", "identityHash")
	return hash, nil
}

// ResolveClusterPath returns the canonical kcp workspace path of a logical
// cluster (e.g. root:faros:tenants:<org>:providers:<name>), read from the
// kcp.io/path annotation kcp stamps on the cluster's LogicalCluster object when
// the workspace is created.
//
// It deliberately uses the hub's kcp-admin config addressed at /clusters/<id>
// rather than any APIExport virtual-workspace client: a VW only serves the
// resources its APIExport declares, and providers.faros.sh claims none, so
// core.kcp.io/LogicalCluster is simply not present there.
//
// An empty path with a nil error means the read succeeded but the cluster
// carries no path annotation. Callers may treat that as "not a workspace this
// hub created through the Workspace API", because every workspace created that
// way is annotated.
func (p *Provisioner) ResolveClusterPath(ctx context.Context, clusterID string) (string, error) {
	if clusterID == "" {
		return "", fmt.Errorf("ResolveClusterPath: clusterID is required")
	}
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterID)
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("dynamic client for cluster %s: %w", clusterID, err)
	}
	lc, err := dyn.Resource(logicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting LogicalCluster in cluster %s: %w", clusterID, err)
	}
	return lc.GetAnnotations()[core.LogicalClusterPathAnnotationKey], nil
}

// ResolveWorkspaceCluster returns the logical cluster ID of the provider's
// sub-workspace (root:faros:providers/{name}), read-only. Returns "" (no error)
// when the workspace does not exist yet — i.e. the provider has not been
// onboarded. The catalog reconciler feeds this into the registry so the Enable
// endpoint can build the edges-proxy RBAC subject without the hub provisioning
// anything.
func (p *Provisioner) ResolveWorkspaceCluster(ctx context.Context, name string) (string, error) {
	parent, err := p.clientFor(providersParentWorkspace)
	if err != nil {
		return "", err
	}
	got, err := parent.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	cluster, _, _ := unstructured.NestedString(got.Object, "spec", "cluster")
	return cluster, nil
}

// OnboardedWorkspace is a provider sub-workspace under root:faros:providers
// created by onboarding (independent of whether a CatalogEntry has registered
// the provider yet).
type OnboardedWorkspace struct {
	Name    string
	Cluster string
	Phase   string
}

// ListProviderWorkspaces returns the provider sub-workspaces under
// root:faros:providers. Used by the admin UI so onboarded providers appear even
// before their Helm chart (and CatalogEntry) is installed.
func (p *Provisioner) ListProviderWorkspaces(ctx context.Context) ([]OnboardedWorkspace, error) {
	parent, err := p.clientFor(providersParentWorkspace)
	if err != nil {
		return nil, err
	}
	list, err := parent.Resource(workspaceGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing workspaces in %s: %w", providersParentWorkspace, err)
	}
	out := make([]OnboardedWorkspace, 0, len(list.Items))
	for i := range list.Items {
		w := &list.Items[i]
		cluster, _, _ := unstructured.NestedString(w.Object, "spec", "cluster")
		phase, _, _ := unstructured.NestedString(w.Object, "status", "phase")
		out = append(out, OnboardedWorkspace{Name: w.GetName(), Cluster: cluster, Phase: phase})
	}
	return out, nil
}

// The provider's CatalogEntry APIBinding is no longer created imperatively —
// the `provider` WorkspaceType declares a defaultAPIBinding to
// providers.faros.sh (in system:controllers), so kcp's WorkspaceType
// initializer binds it automatically when the sub-workspace is created.

// ProviderKubeconfigSecretKey is the data key the provider kubeconfig is stored
// under in the Secret the Provider controller writes into system:providers.
const ProviderKubeconfigSecretKey = "kubeconfig"

// WriteKubeconfigSecret create-or-updates a Secret in root:faros:system:providers
// (where the Provider CR lives, NOT the provider sub-workspace) holding the
// provider's minted kubeconfig under key. The Secret lives next to the Provider
// CR so a provider pod (or dev tooling) can read its credentials from one
// well-known place. Idempotent. Ensures the target namespace exists first.
func (p *Provisioner) WriteKubeconfigSecret(ctx context.Context, namespace, name, key string, kc []byte, providerName string) error {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemProviders)
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("typed kube client for %s: %w", kcppaths.SystemProviders, err)
	}

	// Defensively ensure the namespace exists.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	if _, err := cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensuring namespace %s in %s: %w", namespace, kcppaths.SystemProviders, err)
	}

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"providers.faros.sh/provider":   providerName,
				"providers.faros.sh/managed-by": "provider-controller",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{key: kc},
	}
	existing, err := cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := cs.CoreV1().Secrets(namespace).Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating kubeconfig Secret %s/%s: %w", namespace, name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting kubeconfig Secret %s/%s: %w", namespace, name, err)
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	if _, err := cs.CoreV1().Secrets(namespace).Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating kubeconfig Secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

// DeleteKubeconfigSecret removes the kubeconfig Secret from
// root:faros:system:providers. Idempotent (NotFound tolerated).
func (p *Provisioner) DeleteKubeconfigSecret(ctx context.Context, namespace, name string) error {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemProviders)
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("typed kube client for %s: %w", kcppaths.SystemProviders, err)
	}
	if err := cs.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting kubeconfig Secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

// DeleteProviderWorkspace deletes the provider sub-workspace
// root:faros:providers/{name}. kcp cascades the ServiceAccount, its token
// Secret, and any APIExport / APIResourceSchemas the provider created there.
// Idempotent (NotFound tolerated).
func (p *Provisioner) DeleteProviderWorkspace(ctx context.Context, name string) error {
	parent, err := p.clientFor(providersParentWorkspace)
	if err != nil {
		return err
	}
	if err := parent.Resource(workspaceGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting sub-workspace %s: %w", name, err)
	}
	return nil
}

// applyUnstructured is a create-or-update helper for cluster-scoped resources.
// Preserves resourceVersion on update.
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

func (p *Provisioner) clientFor(clusterPath string) (dynamic.Interface, error) {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterPath)
	return dynamic.NewForConfig(cfg)
}
