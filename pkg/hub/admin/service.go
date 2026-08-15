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

// Package admin implements the platform-admin surface mounted at /api/admin/*
// and surfaced in the portal's gated /bonkers area. It lets a platform admin
// see all users / organizations / providers / root identities. It is read-only:
// provider provisioning (workspace + ServiceAccount + kubeconfig) is driven
// declaratively by the Provider CR reconciler
// (pkg/hub/providers/provider_controller.go), not by an admin HTTP action.
package admin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/faroshq/faros/pkg/apiurl"
	"github.com/faroshq/faros/pkg/hub/kcp"
	"github.com/faroshq/faros/pkg/hub/providers"
	"github.com/faroshq/faros/pkg/kcppaths"
)

// exportsWorkspace is where the platform APIExports live (system:controllers).
// ListRootIdentities reads APIExport identity hashes from here.
const exportsWorkspace = kcppaths.SystemControllers

var apiExportGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports",
}

// providerGVR is the declarative Provider provisioning record. Provider objects
// live in root:faros:system:providers; creating one drives the Provider
// reconciler (pkg/hub/providers/provider_controller.go) to provision the
// sub-workspace + ServiceAccount + kubeconfig Secret.
var providerGVR = schema.GroupVersionResource{
	Group: "admin.faros.sh", Version: "v1alpha1", Resource: "providers",
}

// CreateProvider create-or-updates a Provider object in
// root:faros:system:providers. name drives the provisioned sub-workspace
// (root:faros:providers:<name>); displayName is informational. Idempotent.
func (s *Service) CreateProvider(ctx context.Context, name, displayName string) error {
	cfg := rest.CopyConfig(s.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemProviders)
	cl, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("dynamic client for %s: %w", kcppaths.SystemProviders, err)
	}
	spec := map[string]any{}
	if displayName != "" {
		spec["displayName"] = displayName
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admin.faros.sh/v1alpha1",
		"kind":       "Provider",
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
	if _, err := cl.Resource(providerGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating Provider %q in %s: %w", name, kcppaths.SystemProviders, err)
	}
	return nil
}

// KubeconfigServerMode selects which hub address is baked into a downloaded
// provider kubeconfig. The minted Secret carries exactly one server URL (the
// hub's --hub-internal-url when set, else --hub-external-url), but the
// same Secret feeds two different consumers: a provider installed by Helm into
// this cluster, which should stay on the in-cluster Service, and a provider run
// outside the cluster (another cluster, a laptop during development), which can
// only reach the public hostname. Whoever downloads the kubeconfig knows which
// one they are, so the choice is made per download rather than per deployment.
type KubeconfigServerMode string

const (
	// ServerModeAsMinted leaves the server URL exactly as the Provider
	// controller wrote it. The default, so existing callers are unaffected.
	ServerModeAsMinted KubeconfigServerMode = ""
	// ServerModeExternal rewrites the server to the hub's external URL — for
	// providers running outside this cluster.
	ServerModeExternal KubeconfigServerMode = "external"
	// ServerModeInternal rewrites the server to the hub-internal URL (the
	// in-cluster Service) — for providers installed into this cluster. Keeps
	// provider→hub traffic off the public path.
	ServerModeInternal KubeconfigServerMode = "internal"
)

// ErrServerModeUnavailable is returned when a caller asks for a server mode
// this deployment has no URL for — chiefly ServerModeInternal on a hub started
// without --hub-internal-url.
var ErrServerModeUnavailable = errors.New("requested kubeconfig server address is not configured on this hub")

// ParseKubeconfigServerMode maps the `server` query parameter to a mode. An
// empty value means "as minted". Unknown values are rejected rather than
// silently falling back, so a typo can't hand out the wrong address.
func ParseKubeconfigServerMode(v string) (KubeconfigServerMode, error) {
	switch KubeconfigServerMode(v) {
	case ServerModeAsMinted:
		return ServerModeAsMinted, nil
	case ServerModeExternal:
		return ServerModeExternal, nil
	case ServerModeInternal:
		return ServerModeInternal, nil
	default:
		return "", fmt.Errorf("unknown server mode %q (want %q or %q)", v, ServerModeExternal, ServerModeInternal)
	}
}

// AvailableKubeconfigServerModes reports which modes this hub can actually
// serve, so the portal can offer only the ones that will work. External is
// present whenever --hub-external-url is set (always, in practice); internal
// only when --hub-internal-url is.
func (s *Service) AvailableKubeconfigServerModes() []KubeconfigServerMode {
	modes := make([]KubeconfigServerMode, 0, 2)
	if s.hubExternalURL != "" {
		modes = append(modes, ServerModeExternal)
	}
	if s.hubInternalURL != "" {
		modes = append(modes, ServerModeInternal)
	}
	return modes
}

// serverBaseFor resolves a mode to the configured base URL.
func (s *Service) serverBaseFor(mode KubeconfigServerMode) (string, error) {
	switch mode {
	case ServerModeExternal:
		if s.hubExternalURL == "" {
			return "", fmt.Errorf("%w: external (hub --hub-external-url unset)", ErrServerModeUnavailable)
		}
		return s.hubExternalURL, nil
	case ServerModeInternal:
		if s.hubInternalURL == "" {
			return "", fmt.Errorf("%w: internal (hub --hub-internal-url unset)", ErrServerModeUnavailable)
		}
		return s.hubInternalURL, nil
	default:
		return "", fmt.Errorf("mode %q has no configured base URL", mode)
	}
}

// rewriteKubeconfigServer re-points every cluster entry in kc at base, keeping
// each entry's existing path (the /clusters/<logicalCluster> suffix the hub
// routes on) and every other field — notably the SA token and
// insecure-skip-tls-verify, which is why swapping the host needs no cert work.
//
// Parsed and re-serialised through clientcmd rather than string-substituted so
// a kubeconfig shape that drifts from today's mint template still comes out
// valid.
func rewriteKubeconfigServer(kc []byte, base string) ([]byte, error) {
	target, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parsing target server URL %q: %w", base, err)
	}
	if target.Host == "" {
		return nil, fmt.Errorf("target server URL %q has no host", base)
	}
	// A base that already carries a /clusters/ suffix would otherwise stack
	// with the entry's own path.
	basePath := strings.TrimSuffix(target.Path, "/")
	if idx := strings.Index(basePath, "/clusters/"); idx != -1 {
		basePath = basePath[:idx]
	}

	cfg, err := clientcmd.Load(kc)
	if err != nil {
		return nil, fmt.Errorf("parsing minted kubeconfig: %w", err)
	}
	for name, cluster := range cfg.Clusters {
		current, err := url.Parse(cluster.Server)
		if err != nil {
			return nil, fmt.Errorf("parsing server URL of cluster %q: %w", name, err)
		}
		swapped := *target
		swapped.Path = basePath + current.Path
		swapped.RawQuery = current.RawQuery
		cluster.Server = swapped.String()
	}
	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("serialising rewritten kubeconfig: %w", err)
	}
	return out, nil
}

// GetProviderKubeconfig returns the minted kubeconfig the Provider controller
// wrote into a Secret in root:faros:system:providers. It reads the Provider's
// status.secretRef to locate the Secret (falling back to the
// "<name>-kubeconfig" / "default" / "kubeconfig" conventions). Returns a nil
// slice + nil error when the Provider exists but hasn't been provisioned yet
// (no Secret), so callers can surface "not ready".
//
// mode re-points the server URL on the way out; the stored Secret is never
// modified. ServerModeAsMinted returns the bytes untouched.
func (s *Service) GetProviderKubeconfig(ctx context.Context, name string, mode KubeconfigServerMode) ([]byte, error) {
	cfg := rest.CopyConfig(s.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemProviders)
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client for %s: %w", kcppaths.SystemProviders, err)
	}
	prov, err := dyn.Resource(providerGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting Provider %q: %w", name, err)
	}
	secretNS, _, _ := unstructured.NestedString(prov.Object, "status", "secretRef", "namespace")
	secretName, _, _ := unstructured.NestedString(prov.Object, "status", "secretRef", "name")
	secretKey, _, _ := unstructured.NestedString(prov.Object, "status", "secretRef", "key")
	if secretNS == "" {
		secretNS = "default"
	}
	if secretName == "" {
		secretName = name + "-kubeconfig"
	}
	if secretKey == "" {
		secretKey = "kubeconfig"
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("typed client for %s: %w", kcppaths.SystemProviders, err)
	}
	secret, err := cs.CoreV1().Secrets(secretNS).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil // provisioned not complete yet
	}
	if err != nil {
		return nil, fmt.Errorf("getting kubeconfig Secret %s/%s: %w", secretNS, secretName, err)
	}
	kc := secret.Data[secretKey]
	if len(kc) == 0 || mode == ServerModeAsMinted {
		return kc, nil
	}
	base, err := s.serverBaseFor(mode)
	if err != nil {
		return nil, err
	}
	return rewriteKubeconfigServer(kc, base)
}

// DeleteProvider removes a Provider object from root:faros:system:providers.
// The reconciler's finalizer then tears down the sub-workspace. Idempotent.
func (s *Service) DeleteProvider(ctx context.Context, name string) error {
	cfg := rest.CopyConfig(s.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemProviders)
	cl, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("dynamic client for %s: %w", kcppaths.SystemProviders, err)
	}
	if err := cl.Resource(providerGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting Provider %q in %s: %w", name, kcppaths.SystemProviders, err)
	}
	return nil
}

// Service performs read-only admin queries against kcp.
type Service struct {
	prov *providers.Provisioner
	// kcpConfig is the admin kcp rest.Config (used for cross-workspace reads
	// like root-identity discovery).
	kcpConfig *rest.Config
	// bootstrapper walks the tenant workspace hierarchy with kcp-admin
	// credentials so the admin surface can enumerate every org's child
	// workspaces and their enabled provider bindings.
	bootstrapper *kcp.Bootstrapper
	// hubExternalURL / hubInternalURL are the two addresses a downloaded
	// provider kubeconfig can be pointed at. Provisioning itself no longer runs
	// here (the Provider CR reconciler mints the Secret), but the download
	// endpoint re-points the server URL per request — see KubeconfigServerMode.
	hubExternalURL string
	hubInternalURL string
}

// NewService returns an admin Service. hubExternalURL and hubInternalURL
// are the hub's --hub-external-url and --hub-internal-url; they let the
// kubeconfig download offer either address regardless of which one the Provider
// reconciler baked into the Secret. hubInternalURL may be empty, in which
// case only the external mode is offered.
func NewService(kcpConfig *rest.Config, hubExternalURL, hubInternalURL string) *Service {
	return &Service{
		prov:           providers.NewProvisioner(kcpConfig),
		kcpConfig:      kcpConfig,
		bootstrapper:   kcp.NewBootstrapper(kcpConfig),
		hubExternalURL: hubExternalURL,
		hubInternalURL: hubInternalURL,
	}
}

// OrgWorkspace is a child Workspace of an organization together with the
// provider names enabled in it (derived from the workspace's provider
// APIBindings).
type OrgWorkspace struct {
	UUID                string     `json:"uuid"`
	DisplayName         string     `json:"displayName"`
	ClusterName         string     `json:"clusterName"`
	Providers           []string   `json:"providers"`
	DeletionRequestedAt *time.Time `json:"deletionRequestedAt,omitempty"`
}

// ListOrgWorkspaces returns every child Workspace under the org at
// root:faros:tenants:{orgUUID}, enriched with display name, cluster name,
// soft-delete timestamp and the set of enabled provider names. Reads run
// with kcp-admin credentials, so the admin surface sees all workspaces
// regardless of per-user RBAC. Per-workspace lookups are best-effort: a
// workspace that hasn't reached Ready (no cluster) or whose provider
// listing fails still appears with whatever fields resolved.
func (s *Service) ListOrgWorkspaces(ctx context.Context, orgUUID string) ([]OrgWorkspace, error) {
	names, err := s.bootstrapper.ListChildWorkspaces(ctx, orgUUID)
	if err != nil {
		return nil, fmt.Errorf("listing child workspaces for org %s: %w", orgUUID, err)
	}
	out := make([]OrgWorkspace, 0, len(names))
	for _, wsUUID := range names {
		ws := OrgWorkspace{UUID: wsUUID, Providers: []string{}}
		if dn, err := s.bootstrapper.GetWorkspaceDisplayName(ctx, orgUUID, wsUUID); err == nil {
			ws.DisplayName = dn
		}
		if cluster, err := s.bootstrapper.GetChildWorkspaceClusterName(ctx, orgUUID, wsUUID); err == nil {
			ws.ClusterName = cluster
		}
		if t, found, err := s.bootstrapper.GetWorkspaceDeletionRequestedAt(ctx, orgUUID, wsUUID); err == nil && found && t != nil {
			tt := *t
			ws.DeletionRequestedAt = &tt
		}
		if bindings, err := s.bootstrapper.ListProviderAPIBindings(ctx, orgUUID, wsUUID); err == nil {
			for name := range bindings {
				ws.Providers = append(ws.Providers, name)
			}
			sort.Strings(ws.Providers)
		}
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UUID < out[j].UUID })
	return out, nil
}

// OnboardedWorkspace mirrors providers.OnboardedWorkspace for the admin API.
type OnboardedWorkspace struct {
	Name    string
	Cluster string
	Phase   string
}

// ListOnboardedWorkspaces returns the provider sub-workspaces created by
// onboarding, so the admin UI can show providers that have been onboarded but
// whose Helm chart (and CatalogEntry) is not yet installed.
func (s *Service) ListOnboardedWorkspaces(ctx context.Context) ([]OnboardedWorkspace, error) {
	ws, err := s.prov.ListProviderWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]OnboardedWorkspace, 0, len(ws))
	for _, w := range ws {
		out = append(out, OnboardedWorkspace{Name: w.Name, Cluster: w.Cluster, Phase: w.Phase})
	}
	return out, nil
}

// RootIdentity is one (group, resource) served by a first-party APIExport,
// together with the identityHash kcp minted for it. The admin copies the hash a
// provider needs (e.g. edges.faros.sh for kuery) into that provider's
// Helm values so its `init` can stamp it onto the APIExport's permissionClaim.
type RootIdentity struct {
	Group        string `json:"group"`
	Resource     string `json:"resource"`
	IdentityHash string `json:"identityHash"`
	Export       string `json:"export"`
	Path         string `json:"path"`
}

// ListRootIdentities returns the (group, resource → identityHash) tuples served
// by the APIExports in the providers parent workspace. An empty identityHash
// means kcp has not minted the export's identity yet.
func (s *Service) ListRootIdentities(ctx context.Context) ([]RootIdentity, error) {
	cfg := rest.CopyConfig(s.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, exportsWorkspace)
	cl, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client for %s: %w", exportsWorkspace, err)
	}
	list, err := cl.Resource(apiExportGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing APIExports in %s: %w", exportsWorkspace, err)
	}
	out := make([]RootIdentity, 0)
	for i := range list.Items {
		ex := &list.Items[i]
		hash, _, _ := unstructured.NestedString(ex.Object, "status", "identityHash")
		resources, _, _ := unstructured.NestedSlice(ex.Object, "spec", "resources")
		for _, r := range resources {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			group, _ := rm["group"].(string)
			resource, _ := rm["name"].(string)
			if group == "" {
				continue // built-in types need no identityHash
			}
			out = append(out, RootIdentity{
				Group:        group,
				Resource:     resource,
				IdentityHash: hash,
				Export:       ex.GetName(),
				Path:         exportsWorkspace,
			})
		}
	}
	return out, nil
}
