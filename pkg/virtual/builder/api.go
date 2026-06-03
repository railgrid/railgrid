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

package builder

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	gossh "golang.org/x/crypto/ssh"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

// Compile-time hint that these imports stay live even when the build tag
// shape changes; the symbols are surfaced via the Deps struct fields.
var (
	_ kubernetes.Interface
	_ *kedgeclient.Client
)

// Deps is the public dependency bundle providers receive when constructing
// their virtual-workspace handlers. The hub builds one Deps instance from
// VirtualWorkspaceHandlers.Deps() and threads it into each provider's
// Build function. Direct field access lets providers grab only what they
// need; helper methods cover common composite operations (HubBaseURL,
// OpenSSHSession) so providers don't reimplement the SSH dance.
//
// The unexported `vws` back-reference lets the SSH helpers reuse the
// real fetchSSHCredentials path (which itself constructs cluster-scoped
// clients from kcpConfig) without us extracting that whole apparatus
// into the public API surface.
type Deps struct {
	KCPConfig       *rest.Config
	KCPK8sClient    kubernetes.Interface
	KedgeClient     *kedgeclient.Client
	EdgeConnManager *ConnManager
	HubExternalURL  string
	HubInternalURL  string
	Logger          klog.Logger

	// ProviderEnumerator, when set, returns the live set of Ready
	// external providers that expose an MCP endpoint. The aggregate
	// MCPServer handler (providers/mcp/virtual/builder.go) passes this
	// to the aggregator, which fetches each provider's tools/list and
	// re-exposes them as `<provider-slug>__<tool>` so MCP clients can
	// drive providers (e.g. kro-multicluster) through the same
	// connection they use for edges. nil = no provider proxying — the
	// aggregator falls back to edge-only behavior. Wired in pkg/hub/
	// server.go from the providers.Registry; kept as an opaque function
	// here to avoid pulling pkg/hub/providers into the virtual layer.
	ProviderEnumerator func(context.Context) []ProviderTarget

	vws *virtualWorkspaces
}

// ProviderTarget is one entry returned by ProviderEnumerator. Mirrors
// the subset of pkg/hub/providers.Provider the aggregator needs;
// kept here rather than imported to avoid an import cycle between
// pkg/virtual/builder (used by providers/mcp/aggregate) and
// pkg/hub/providers (which depends on the aggregator's bootstrap).
type ProviderTarget struct {
	Name        string
	DisplayName string
	Description string
	Vendor      string
	Version     string
	MCPURL      string // full URL including scheme + host + /mcp suffix
	Ready       bool
}

// HubBaseURL returns the URL providers should use as the base for
// internal callbacks (e.g. an aggregate MCP handler calling back into
// edges-proxy on the same hub). Falls back to the external URL when no
// internal one is configured. Trailing slash is trimmed.
func (d *Deps) HubBaseURL() string {
	if d.HubInternalURL != "" {
		return strings.TrimRight(d.HubInternalURL, "/")
	}
	return strings.TrimRight(d.HubExternalURL, "/")
}

// OpenSSHSession performs the full open-an-SSH-session-to-an-edge dance:
// look up the tunnel dialer, fetch the caller's SSH credentials from kcp,
// open the agent SSH tunnel, and return an authenticated *ssh.Client.
// Provider builders wrap this in their own typed OpenSession adapters so
// they don't have to reimplement the SSH plumbing.
func (d *Deps) OpenSSHSession(ctx context.Context, cluster, edgeName, callerIdentity string, logger klog.Logger) (*gossh.Client, error) {
	key := edgeConnKey(cluster, edgeName)
	dialer, ok := d.EdgeConnManager.Load(key)
	if !ok {
		return nil, fmt.Errorf("no active tunnel for edge %q", edgeName)
	}
	// Best-effort credential fetch: nil creds fall through and let
	// newSSHClient raise the specific failure.
	var creds *SSHClientCredentials
	if d.vws != nil {
		var err error
		creds, err = d.vws.fetchSSHCredentials(ctx, cluster, edgeName, callerIdentity, logger)
		if err != nil {
			logger.Error(err, "fetchSSHCredentials")
		}
	}
	deviceConn, err := dialer.Dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("dial edge agent: %w", err)
	}
	sshConn, err := openAgentSSHTunnel(ctx, deviceConn)
	if err != nil {
		_ = deviceConn.Close()
		return nil, fmt.Errorf("open ssh tunnel: %w", err)
	}
	var hostKey string
	if creds != nil {
		hostKey = creds.SSHHostKey
	}
	client, err := newSSHClient(ctx, sshConn, creds, hostKey, logger)
	if err != nil {
		_ = sshConn.Close()
		return nil, fmt.Errorf("new ssh client: %w", err)
	}
	return client, nil
}

// Deps returns the dependency bundle providers use to construct their
// virtual-workspace handlers. Returns nil-able fields untouched —
// providers validate what they actually need.
//
// ProviderEnumerator is always set to a lazy wrapper rather than a
// snapshot of h.vws.providerEnumerator. The provider-MCP virtual
// builder captures *Deps once at route-mount time; if we copied the
// underlying field, a later SetProviderEnumerator (called AFTER the
// providers.Registry is built in pkg/hub/server.go) would not be
// visible. The wrapper indirects through h.vws on every call so the
// real enumerator becomes visible as soon as it's installed.
func (h *VirtualWorkspaceHandlers) Deps() *Deps {
	return &Deps{
		KCPConfig:       h.vws.kcpConfig,
		KCPK8sClient:    h.vws.kcpK8sClient,
		KedgeClient:     h.vws.kedgeClient,
		EdgeConnManager: h.vws.edgeConnManager,
		HubExternalURL:  h.vws.hubExternalURL,
		HubInternalURL:  h.vws.hubInternalURL,
		Logger:          h.vws.logger,
		ProviderEnumerator: func(ctx context.Context) []ProviderTarget {
			if h.vws.providerEnumerator == nil {
				return nil
			}
			return h.vws.providerEnumerator(ctx)
		},
		vws: h.vws,
	}
}

// SetProviderEnumerator installs the provider-discovery callback the
// aggregate MCP handler uses to federate external providers' tools.
// Called once at hub startup AFTER providers.Registry is constructed
// (which happens after NewVirtualWorkspaces — hence the two-step
// wiring). Calling with nil disables provider federation; subsequent
// MCP requests then return only edge-family tools. Goroutine-safe in
// the trivial sense that hub init is single-threaded and no MCP
// requests land before this call returns.
func (h *VirtualWorkspaceHandlers) SetProviderEnumerator(fn func(context.Context) []ProviderTarget) {
	h.vws.providerEnumerator = fn
}

// EdgeGVRForMCPSelector is the GVR providers use when listing Edge
// resources to evaluate an edgeSelector. Exported because every
// per-provider virtual-workspace builder needs it.
var EdgeGVRForMCPSelector = schema.GroupVersionResource{
	Group:    "kedge.faros.sh",
	Version:  "v1alpha1",
	Resource: "edges",
}

// ExtractBearerToken returns the bearer token from the Authorization header
// of r, or "" if the header is missing or malformed. Public alias of the
// internal helper for provider builders.
func ExtractBearerToken(r *http.Request) string { return extractBearerToken(r) }

// ClusterScopedDynamicClient builds a dynamic client rooted at a specific
// kcp logical cluster. Provider builders use it to read tenant-scoped CRs
// (MCPServer, KubernetesMCP, LinuxMCP, …) on a per-request basis.
func ClusterScopedDynamicClient(kcpConfig *rest.Config, cluster string) (dynamic.Interface, error) {
	return clusterScopedDynamicClient(kcpConfig, cluster)
}

// ResolveCallerIdentity returns the canonical RBAC identity for a bearer
// token by asking kcp who that token belongs to. Returns "" when the token
// is unparseable; callers should already have authorized the request.
func ResolveCallerIdentity(ctx context.Context, kcpConfig *rest.Config, token string, logger klog.Logger) string {
	return resolveCallerIdentity(ctx, kcpConfig, token, logger)
}

// UnstructuredNestedMap fetches a nested map[string]any from an
// unstructured object using key. Wrapper for the internal helper so
// provider builders don't depend on a private symbol.
func UnstructuredNestedMap(obj map[string]any, key string) (map[string]any, bool, error) {
	return unstructuredNestedMap(obj, key)
}

// EdgeConnKey is the lookup key providers use against EdgeConnManager to
// check whether an edge has a live tunnel. Mirror of the private helper.
func EdgeConnKey(cluster, edgeName string) string { return edgeConnKey(cluster, edgeName) }
