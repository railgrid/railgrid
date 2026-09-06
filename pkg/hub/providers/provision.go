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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

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

	// workspaceClusterAdmin binds the provider ServiceAccount to cluster-admin
	// in its own workspace instead of the generated faros:provider role. See
	// WithWorkspaceClusterAdmin.
	workspaceClusterAdmin bool
	// credentialGracePeriod is how long a rotated-out token Secret stays valid
	// before the sweeper deletes it. Zero means DefaultCredentialGracePeriod.
	credentialGracePeriod time.Duration
	// clock is overridable in tests. Read it through now(), which tolerates a
	// zero-value Provisioner — several tests build one directly.
	clock func() time.Time
}

// now is the Provisioner's clock, defaulting to the real one.
func (p *Provisioner) now() time.Time {
	if p.clock == nil {
		return time.Now()
	}
	return p.clock()
}

// gracePeriod is how long a retired credential stays valid, defaulting to
// DefaultCredentialGracePeriod.
func (p *Provisioner) gracePeriod() time.Duration {
	if p.credentialGracePeriod <= 0 {
		return DefaultCredentialGracePeriod
	}
	return p.credentialGracePeriod
}

// ProvisionerOption configures a Provisioner.
type ProvisionerOption func(*Provisioner)

// WithWorkspaceClusterAdmin selects the role the provider's ServiceAccount is
// bound to inside its own provider workspace: true keeps the historical
// cluster-admin binding, false binds the generated, narrower faros:provider
// ClusterRole (see providerClusterRoleRules).
//
// It is a Provisioner-level option rather than a per-call argument because
// every caller that creates a provider SA — admin onboarding, the Provider
// reconciler, org-owned registration — must agree: a hub that narrowed the
// role in one path and not another would leave the wider binding in place for
// whichever path ran last.
func WithWorkspaceClusterAdmin(clusterAdmin bool) ProvisionerOption {
	return func(p *Provisioner) { p.workspaceClusterAdmin = clusterAdmin }
}

// WithCredentialGracePeriod overrides how long a rotated-out provider token
// Secret stays usable before it is swept. Zero or negative restores the
// default.
func WithCredentialGracePeriod(d time.Duration) ProvisionerOption {
	return func(p *Provisioner) { p.credentialGracePeriod = d }
}

// NewProvisioner returns a Provisioner that performs provider-workspace
// side-effects (workspace, ServiceAccount, minted kubeconfig) against kcp using
// the given admin config. Used by the admin onboarding API
// (pkg/hub/admin); the catalog controller no longer provisions.
//
// The default is the historical behaviour — cluster-admin in the provider's own
// workspace. Pass WithWorkspaceClusterAdmin(false) to bind the narrower
// generated role instead.
func NewProvisioner(kcpConfig *rest.Config, opts ...ProvisionerOption) *Provisioner {
	p := &Provisioner{
		kcpConfig:             kcpConfig,
		workspaceClusterAdmin: true,
		credentialGracePeriod: DefaultCredentialGracePeriod,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
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
	clusterRoleGVR = schema.GroupVersionResource{
		Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles",
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

// ProviderTokenSecretName is the Secret every provider workspace starts with.
// Rotation mints differently-named Secrets beside it and records which one is
// current on the ServiceAccount, so this stays the name of the *first*
// credential rather than always the live one — read the live one with
// activeTokenSecretName.
const ProviderTokenSecretName = ProviderSAName + ProviderTokenSecretSuffix

const (
	// AnnotationActiveTokenSecret names, on the provider ServiceAccount, the
	// token Secret the hub currently mints kubeconfigs from. Its absence means
	// ProviderTokenSecretName, which is what every workspace provisioned
	// before rotation existed carries.
	//
	// The pointer lives on the ServiceAccount rather than being derived from
	// Secret names so that "which credential is current" has exactly one
	// answer, and so a half-finished rotation (new Secret created, pointer not
	// yet moved) keeps handing out the old, still-valid credential rather than
	// an unpopulated one.
	AnnotationActiveTokenSecret = "providers.faros.sh/active-token-secret"

	// AnnotationTokenSecretExpiry is stamped on a rotated-out token Secret with
	// the RFC3339 time after which it may be deleted. Until then both tokens
	// authenticate as the same ServiceAccount, which is what lets a provider be
	// rolled onto the new credential without a gap.
	AnnotationTokenSecretExpiry = "providers.faros.sh/delete-after"
)

// DefaultCredentialGracePeriod is how long a rotated-out provider token stays
// valid. It has to outlast a leisurely rollout of the provider's chart — the
// operator has to take the new kubeconfig, put it in a Secret, and restart the
// workload — so a day rather than an hour, and short enough that a leaked
// credential is not indefinitely live.
const DefaultCredentialGracePeriod = 24 * time.Hour

// ProviderClusterRoleName is the generated, narrower role bound to the provider
// ServiceAccount in its own workspace when the hub runs with
// --provider-workspace-cluster-admin=false.
const ProviderClusterRoleName = "faros:provider"

// providerSABindingName is the ClusterRoleBinding tying the provider SA to
// whichever role the hub selected. The name is historical and stable across the
// role switch: a rename would leave the old (cluster-admin) binding behind.
const providerSABindingName = "faros:providers:sa:" + ProviderSAName

// providerClusterRoleRules is what a provider actually needs inside its own
// workspace, derived from what runs against the minted kubeconfig:
// `provider-sdk/install` (every provider's `init`), the APIExport
// virtual-workspace clients, the SDK's leader election, and the runtime
// bootstrap the infrastructure provider performs.
//
// Everything a provider does OUTSIDE this workspace still comes from its
// APIExport's permission claims, which each consuming workspace accepts at
// Enable time; nothing here widens that.
//
// What it deliberately does NOT grant, and cluster-admin did:
//   - escalate on ClusterRoles. The provider may create RBAC (the bind grant
//     `init` writes), but RBAC's escalation-prevention then holds it to rights
//     it already has, so it cannot promote itself back to cluster-admin.
//   - impersonate, on any subject.
//   - the workspace's own tenancy objects (Workspace, WorkspaceType) — the
//     `provider` WorkspaceType does not extend universal, so a provider could
//     not create sub-workspaces anyway, and this stops it deleting its own.
//
// A provider that defines its own CRDs in this workspace AND writes objects of
// those CRDs (today: infrastructure, which seeds Templates) needs more than
// this: escalation prevention stops it granting itself access to a resource it
// has no rule for. That is the case --provider-workspace-cluster-admin=true
// exists to keep working while providers declare what they need.
func providerClusterRoleRules() []any {
	rule := func(groups, resources, verbs []string) map[string]any {
		return map[string]any{
			"apiGroups": toAnySlice(groups),
			"resources": toAnySlice(resources),
			"verbs":     toAnySlice(verbs),
		}
	}
	readWrite := []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	return []any{
		// `init` applies schemas, the APIExport, and the endpoint slice; the
		// multicluster provider lists APIBindings and watches the slice to
		// discover which logical clusters bound the export.
		rule([]string{"apis.kcp.io"},
			[]string{"apiresourceschemas", "apiexports", "apiexportendpointslices", "apibindings"},
			readWrite),
		// The APIExport virtual workspace gates EVERY call through a SAR for
		// apiexports/content with the verb of the in-flight request — discovery
		// included — so the verb set has to be open. Scope comes from the
		// workspace: only this provider's exports live here.
		rule([]string{"apis.kcp.io"}, []string{"apiexports/content"}, []string{"*"}),
		// `bind` is what lets ApplyBindGrant create the tenant-facing bind
		// ClusterRole without `escalate`: RBAC only permits granting rights the
		// grantor holds.
		rule([]string{"apis.kcp.io"}, []string{"apiexports"}, []string{"bind"}),
		rule([]string{"cache.kcp.io"},
			[]string{"cachedresources", "cachedresourceendpointslices"}, readWrite),
		// Resolving the workspace's own path (ApplyBindGrant's org check, the
		// infrastructure operator's discovery).
		rule([]string{"core.kcp.io"}, []string{"logicalclusters"}, []string{"get", "list", "watch"}),
		// The provider self-registers its CatalogEntry here and the hub reads
		// its status back; the provider updates it on every chart upgrade.
		rule([]string{"providers.faros.sh"},
			[]string{"catalogentries", "catalogentries/status"},
			[]string{"get", "list", "watch", "create", "update", "patch"}),
		// The bind grant (ClusterRole + ClusterRoleBinding), created and — for
		// org-owned workspaces — removed again by ApplyBindGrant.
		rule([]string{"rbac.authorization.k8s.io"},
			[]string{"clusterroles", "clusterrolebindings"},
			[]string{"get", "list", "watch", "create", "update", "delete"}),
		// Per-template CRDs (infrastructure) and any provider that serves
		// workspace-local types.
		rule([]string{"apiextensions.k8s.io"}, []string{"customresourcedefinitions"}, readWrite),
		// Runtime identity minting (infrastructure), controller-runtime event
		// recorders, and the Secrets a provider keeps for itself.
		rule([]string{""},
			[]string{"serviceaccounts", "secrets", "configmaps", "namespaces", "events"},
			readWrite),
		// Leader election (provider-sdk/leaderelection, kuery engagement
		// claims, the edges tunnel registry).
		rule([]string{"coordination.k8s.io"}, []string{"leases"}, readWrite),
		// Providers that authenticate their own data-plane callers delegate the
		// decision back to kcp rather than parsing JWTs.
		rule([]string{"authentication.k8s.io"}, []string{"tokenreviews"}, []string{"create"}),
		rule([]string{"authorization.k8s.io"},
			[]string{"subjectaccessreviews", "selfsubjectaccessreviews", "selfsubjectrulesreviews"},
			[]string{"create"}),
		// Discovery and RESTMapper construction for every client-go and
		// controller-runtime client built from the provider kubeconfig.
		map[string]any{
			"nonResourceURLs": toAnySlice([]string{
				"/api", "/api/*", "/apis", "/apis/*", "/version",
				"/openapi", "/openapi/*", "/healthz", "/livez", "/readyz",
			}),
			"verbs": toAnySlice([]string{"get"}),
		},
	}
}

func toAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

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

	// Bound in the sub-workspace only, to whichever role this hub selected.
	// The provider pod reaches other workspaces via the APIExport's virtual
	// workspace + accepted permission claims — NOT via this SA.
	return p.ensureProviderRoleBinding(ctx, cl)
}

// ensureProviderRoleBinding binds the provider ServiceAccount to cluster-admin
// or to the generated faros:provider role, depending on the hub's
// --provider-workspace-cluster-admin setting, and moves an existing binding
// when the setting changed.
//
// roleRef is immutable in RBAC, so switching roles means delete-then-create
// rather than an update. The window between the two is a few milliseconds in
// which the provider's SA has no rights in its own workspace; its controllers
// retry, and the alternative — a second binding under a different name — would
// leave the wide grant in place forever, which is the thing being removed.
func (p *Provisioner) ensureProviderRoleBinding(ctx context.Context, cl dynamic.Interface) error {
	roleName := "cluster-admin"
	if !p.workspaceClusterAdmin {
		roleName = ProviderClusterRoleName
		cr := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRole",
			"metadata":   map[string]any{"name": ProviderClusterRoleName},
			"rules":      providerClusterRoleRules(),
		}}
		if err := applyUnstructured(ctx, cl, clusterRoleGVR, cr); err != nil {
			return fmt.Errorf("applying ClusterRole %s: %w", ProviderClusterRoleName, err)
		}
	}

	crb := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   map[string]any{"name": providerSABindingName},
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "ClusterRole",
			"name":     roleName,
		},
		"subjects": []any{
			map[string]any{
				"kind":      "ServiceAccount",
				"name":      ProviderSAName,
				"namespace": ProviderSANamespace,
			},
		},
	}}

	existing, err := cl.Resource(clusterRoleBindingGVR).Get(ctx, providerSABindingName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("getting %s: %w", providerSABindingName, err)
	default:
		current, _, _ := unstructured.NestedString(existing.Object, "roleRef", "name")
		if current == roleName {
			crb.SetResourceVersion(existing.GetResourceVersion())
			if _, err := cl.Resource(clusterRoleBindingGVR).Update(ctx, crb, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("updating %s: %w", providerSABindingName, err)
			}
			return nil
		}
		if err := cl.Resource(clusterRoleBindingGVR).Delete(ctx, providerSABindingName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting %s to move it from %s to %s: %w", providerSABindingName, current, roleName, err)
		}
	}
	if _, err := cl.Resource(clusterRoleBindingGVR).Create(ctx, crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating %s: %w", providerSABindingName, err)
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

	// Which Secret is current is recorded on the ServiceAccount by rotation;
	// a workspace that has never rotated has no annotation and keeps the
	// original name, so this returns the same token it always did.
	secretName, err := activeTokenSecretName(ctx, typed)
	if err != nil {
		return nil, fmt.Errorf("resolving active token Secret for %s: %w", workspacePath, err)
	}
	token, err := ensureLegacySAToken(ctx, typed, ProviderSANamespace, ProviderSAName, secretName)
	if err != nil {
		return nil, fmt.Errorf("ensuring SA token for %s: %w", workspacePath, err)
	}

	return p.renderKubeconfig(ctx, cfg, hubExternalURL, token)
}

// renderKubeconfig turns a resolved bearer token into the provider kubeconfig,
// resolving the workspace's logical cluster ID for the server URL. Shared by
// minting and rotation so both hand back byte-identical shapes.
func (p *Provisioner) renderKubeconfig(ctx context.Context, cfg *rest.Config, hubExternalURL, token string) ([]byte, error) {
	// Resolve the provider workspace's logical cluster ID. The kubeconfig must
	// address kcp by ID (/clusters/<id>), not by workspace path: kcp shards only
	// resolve /clusters/<id>, and workspace-path resolution is front-proxy-only.
	// A provider kubeconfig pointed at the path 404s once the request reaches a
	// shard (the SA token also carries this ID in its clusterName claim).
	clusterID, err := resolveLogicalClusterID(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("resolving logical cluster ID for %s: %w", cfg.Host, err)
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

// activeTokenSecretName reads the provider ServiceAccount's pointer to the
// token Secret currently in use. A ServiceAccount without the annotation —
// every workspace provisioned before rotation existed — reports the original
// name, so the answer is stable for a provider that has never rotated.
func activeTokenSecretName(ctx context.Context, cs kubernetes.Interface) (string, error) {
	sa, err := cs.CoreV1().ServiceAccounts(ProviderSANamespace).Get(ctx, ProviderSAName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting ServiceAccount %s/%s: %w", ProviderSANamespace, ProviderSAName, err)
	}
	if name := sa.Annotations[AnnotationActiveTokenSecret]; name != "" {
		return name, nil
	}
	return ProviderTokenSecretName, nil
}

// RotatedCredential describes the outcome of a rotation.
type RotatedCredential struct {
	// Kubeconfig is the new credential, in the same shape registration
	// returns.
	Kubeconfig []byte
	// SecretName is the token Secret the new credential came from.
	SecretName string
	// PreviousSecretName is the Secret that was current before, empty when
	// there was none to retire.
	PreviousSecretName string
	// PreviousValidUntil is when the previous credential stops working. Zero
	// when nothing was retired. Until then BOTH tokens authenticate as the
	// same ServiceAccount, so anything that checks the provider's identity —
	// the heartbeat's TokenReview included — keeps accepting the old one while
	// the provider is rolled onto the new one.
	PreviousValidUntil time.Time
	// RotatedAt is the rotation's wall-clock time, recorded on the
	// CatalogEntry status.
	RotatedAt time.Time
}

// RotateProviderCredential rotates the platform provider at
// root:faros:providers/{name}.
func (p *Provisioner) RotateProviderCredential(ctx context.Context, providerName, hubExternalURL string) (*RotatedCredential, error) {
	return p.RotateProviderCredentialAtPath(ctx, providersParentWorkspace+":"+providerName, providerName, hubExternalURL)
}

// RotateProviderCredentialAtPath issues a SECOND long-lived token for the
// provider's ServiceAccount, makes it the one the hub hands out, and schedules
// the previous one for deletion after the grace period.
//
// Both tokens belong to the same ServiceAccount, so during the grace period the
// provider authenticates identically with either: the identity kcp reports is
// system:serviceaccount:default:provider in both cases, which is what every
// hub-side check keys on. That is what makes rotation a rolling change rather
// than an outage — the operator installs the new kubeconfig whenever it suits
// them, and the old credential stops working on its own.
//
// providerName is only used to record status.credentialsRotatedAt on the
// provider's CatalogEntry; pass "" to skip that.
func (p *Provisioner) RotateProviderCredentialAtPath(ctx context.Context, workspacePath, providerName, hubExternalURL string) (*RotatedCredential, error) {
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, workspacePath)
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("typed kube client for %s: %w", workspacePath, err)
	}

	out, token, err := p.rotateToken(ctx, typed)
	if err != nil {
		return nil, fmt.Errorf("rotating provider credential in %s: %w", workspacePath, err)
	}

	kc, err := p.renderKubeconfig(ctx, cfg, hubExternalURL, token)
	if err != nil {
		return nil, err
	}
	out.Kubeconfig = kc

	if providerName != "" {
		// Best-effort: the CatalogEntry only exists once the provider's chart
		// has run. A rotation before that is still a valid rotation.
		if err := p.recordCredentialsRotated(ctx, cfg, providerName, out.RotatedAt); err != nil {
			klog.FromContext(ctx).V(2).Info("could not record credentialsRotatedAt on CatalogEntry",
				"provider", providerName, "workspace", workspacePath, "err", err.Error())
		}
	}
	return out, nil
}

// rotateToken is the workspace-client half of a rotation: mint a second token
// for the same ServiceAccount, repoint the hub at it, and give the previous one
// a deadline. Split out from RotateProviderCredentialAtPath so it can be
// exercised against an injected clientset — everything it does is Secret and
// ServiceAccount bookkeeping, while its caller additionally needs a live kcp to
// resolve the workspace's logical cluster ID.
func (p *Provisioner) rotateToken(ctx context.Context, cs kubernetes.Interface) (*RotatedCredential, string, error) {
	previous, err := activeTokenSecretName(ctx, cs)
	if err != nil {
		return nil, "", fmt.Errorf("resolving active token Secret: %w", err)
	}

	now := p.now()
	next := fmt.Sprintf("%s-%s", ProviderTokenSecretName, now.UTC().Format("20060102150405"))
	if next == previous {
		// Two rotations inside the same second would otherwise re-point at the
		// Secret being retired and immediately schedule the live credential for
		// deletion.
		return nil, "", fmt.Errorf("a rotation already happened this second; retry")
	}

	token, err := ensureLegacySAToken(ctx, cs, ProviderSANamespace, ProviderSAName, next)
	if err != nil {
		return nil, "", fmt.Errorf("minting rotated SA token: %w", err)
	}

	// Move the pointer BEFORE retiring the old Secret. The reverse order has a
	// window where the old credential is expiring and nothing yet says the new
	// one is current, so a concurrent kubeconfig fetch would hand out the
	// credential that is on its way out.
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, AnnotationActiveTokenSecret, next)
	if _, err := cs.CoreV1().ServiceAccounts(ProviderSANamespace).Patch(
		ctx, ProviderSAName, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	); err != nil {
		return nil, "", fmt.Errorf("recording active token Secret on the ServiceAccount: %w", err)
	}

	out := &RotatedCredential{SecretName: next, RotatedAt: now}
	if previous == "" || previous == next {
		return out, token, nil
	}
	expiry := now.Add(p.gracePeriod())
	retire := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, AnnotationTokenSecretExpiry, expiry.UTC().Format(time.RFC3339))
	switch _, err := cs.CoreV1().Secrets(ProviderSANamespace).Patch(
		ctx, previous, types.MergePatchType, []byte(retire), metav1.PatchOptions{},
	); {
	case apierrors.IsNotFound(err):
		// Nothing to retire: the pointer named a Secret that is already gone.
	case err != nil:
		return nil, "", fmt.Errorf("scheduling previous token Secret %s for deletion: %w", previous, err)
	default:
		out.PreviousSecretName = previous
		out.PreviousValidUntil = expiry
	}
	return out, token, nil
}

// recordCredentialsRotated stamps status.credentialsRotatedAt on the provider's
// CatalogEntry so an operator can see, from the object the platform already
// shows them, when the credential they hold was issued.
func (p *Provisioner) recordCredentialsRotated(ctx context.Context, cfg *rest.Config, providerName string, at time.Time) error {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	patch := fmt.Sprintf(`{"status":{"credentialsRotatedAt":%q}}`, at.UTC().Format(time.RFC3339))
	_, err = dyn.Resource(catalogEntryGVR).Patch(
		ctx, providerName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}, "status",
	)
	return err
}

// SweepExpiredProviderTokens deletes the provider token Secrets in a provider
// workspace whose grace period has passed, and reports when the next one lapses
// (zero when none is pending).
//
// It is what actually ends the old credential's life: rotation only writes the
// expiry. Running it from the catalog reconciler means it runs wherever a
// provider is known, including org-owned workspaces the hub never lists.
//
// cluster may be a workspace path or a logical cluster ID.
func (p *Provisioner) SweepExpiredProviderTokens(ctx context.Context, cluster string) (deleted int, nextExpiry time.Time, err error) {
	if p.kcpConfig == nil {
		// Registry-only mode: no kcp to sweep in, and no credentials to have
		// rotated in the first place.
		return 0, time.Time{}, nil
	}
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, cluster)
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("typed kube client for %s: %w", cluster, err)
	}
	deleted, nextExpiry, err = p.sweepExpiredTokens(ctx, typed)
	if err != nil {
		return deleted, nextExpiry, fmt.Errorf("sweeping expired provider tokens in %s: %w", cluster, err)
	}
	return deleted, nextExpiry, nil
}

// sweepExpiredTokens is SweepExpiredProviderTokens against an already-built
// clientset, so it can be exercised without a live kcp.
func (p *Provisioner) sweepExpiredTokens(ctx context.Context, cs kubernetes.Interface) (deleted int, nextExpiry time.Time, err error) {
	active, err := activeTokenSecretName(ctx, cs)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// No provider ServiceAccount: not a provider workspace, or not
			// provisioned yet. Nothing to sweep either way.
			return 0, time.Time{}, nil
		}
		return 0, time.Time{}, err
	}
	list, err := cs.CoreV1().Secrets(ProviderSANamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("listing Secrets: %w", err)
	}
	now := p.now()
	for i := range list.Items {
		s := &list.Items[i]
		if s.Name == active || s.Type != corev1.SecretTypeServiceAccountToken {
			continue
		}
		if s.Annotations[corev1.ServiceAccountNameKey] != ProviderSAName {
			continue
		}
		raw := s.Annotations[AnnotationTokenSecretExpiry]
		if raw == "" {
			// Never retired — the credential a workspace that has not rotated
			// still holds. Deleting it would be an outage.
			continue
		}
		expiry, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			// An unparseable expiry is not a licence to keep a retired
			// credential alive forever, but deleting on a value we cannot read
			// is worse. Surface it and leave the Secret.
			klog.FromContext(ctx).Info("provider token Secret has an unparseable expiry annotation; leaving it in place",
				"secret", s.Name, "value", raw)
			continue
		}
		if now.Before(expiry) {
			if nextExpiry.IsZero() || expiry.Before(nextExpiry) {
				nextExpiry = expiry
			}
			continue
		}
		if delErr := cs.CoreV1().Secrets(ProviderSANamespace).Delete(ctx, s.Name, metav1.DeleteOptions{}); delErr != nil && !apierrors.IsNotFound(delErr) {
			return deleted, nextExpiry, fmt.Errorf("deleting expired token Secret %s: %w", s.Name, delErr)
		}
		deleted++
	}
	return deleted, nextExpiry, nil
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
