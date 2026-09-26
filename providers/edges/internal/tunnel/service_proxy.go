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

package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/railgrid/provider-sdk/dataplane"
)

// serviceResource is the URL resource segment for the Service kind. It is
// deliberately NOT registered as a tunnel Kind (Service is not connectable):
// it names a published endpoint on some other edge's tunnel, and
// buildEdgesProxyHandler branches to serveService on it after the gates.
const serviceResource = "services"

// svcTargetHeader mirrors the agent-side constant (pkg/agent/tunnel). The agent
// decides whether it dials the host: loopback always, cluster DNS in kubernetes
// mode, any other address only inside the agent's --svc-allow-cidr ranges
// (link-local never). A refused target comes back as a 403 with
// X-Railgrid-Svc-Policy: enforce, which this proxy passes through unchanged.
const svcTargetHeader = "X-Railgrid-Svc-Target"

// svcTLSInsecureHeader mirrors the agent-side constant: set to "true" from
// spec.tlsInsecureSkipVerify so the agent skips certificate verification for
// a non-loopback https host (e.g. a self-signed UniFi console).
const svcTLSInsecureHeader = "X-Railgrid-Svc-TLS-Insecure"

// serviceView is the projection of a Service CR the proxy needs. As
// with sshEdgeView, every field must be exported and non-object fields tagged
// json:"-" or runtime.DefaultUnstructuredConverter panics.
type serviceView struct {
	Name string `json:"-"`
	Spec struct {
		EdgeRef struct {
			Kind string `json:"kind,omitempty"`
			Name string `json:"name"`
		} `json:"edgeRef"`
		TargetRef *struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"targetRef,omitempty"`
		Host                  string                  `json:"host,omitempty"`
		TLSInsecureSkipVerify bool                    `json:"tlsInsecureSkipVerify,omitempty"`
		Type                  string                  `json:"type,omitempty"`
		Scheme                string                  `json:"scheme,omitempty"`
		Port                  int32                   `json:"port"`
		AuthSecretRef         *corev1.SecretReference `json:"authSecretRef,omitempty"`
		Auth                  string                  `json:"auth,omitempty"`
		Instructions          string                  `json:"instructions,omitempty"`
	} `json:"spec"`
}

// Authorization modes, mirroring edges.railgrid.ai/v1alpha1 ServiceAuthMode. The
// SDK keeps its own copy for the same reason serviceView exists at all: this
// package decodes tenant objects without importing any provider's types.
const (
	serviceAuthSecret      = "secret"
	serviceAuthPassthrough = "passthrough"
	serviceAuthNone        = "none"
)

// authMode is spec.auth with the default applied. Empty means "secret" — the
// behaviour every Service had before the field existed.
func (v *serviceView) authMode() string {
	switch v.Spec.Auth {
	case serviceAuthPassthrough:
		return serviceAuthPassthrough
	case serviceAuthNone:
		return serviceAuthNone
	default:
		return serviceAuthSecret
	}
}

// scheme returns the URL scheme, defaulting to http.
func (v *serviceView) scheme() string {
	if v.Spec.Scheme == "https" {
		return "https"
	}
	return "http"
}

// isKube reports whether this Service lives on a KubernetesCluster edge.
func (v *serviceView) isKube() bool {
	return v.Spec.EdgeRef.Kind == kubernetesClusterKind
}

// connResource is the tunnel ConnManager resource segment for the referenced
// edge kind.
func (v *serviceView) connResource() string {
	switch v.Spec.EdgeRef.Kind {
	case "", linuxServerKind:
		return linuxServerResource
	case kubernetesClusterKind:
		return kubernetesClusterResource
	case macOSServerKind:
		return macOSServerResource
	default:
		// The CRD enum rejects this, but the proxy also receives unstructured
		// objects from clients and must fail closed if admission was bypassed.
		return ""
	}
}

// targetHost is the agent-side address of the service: cluster DNS for a
// KubernetesCluster edge, the host loopback for a LinuxServer or MacOSServer edge.
func (v *serviceView) targetHost() string {
	// spec.host wins on either edge kind: dial the address directly (loopback, or
	// a device on the edge's LAN like a UniFi console).
	if v.Spec.Host != "" {
		return v.Spec.Host
	}
	if v.isKube() && v.Spec.TargetRef != nil {
		return v.Spec.TargetRef.Name + "." + v.Spec.TargetRef.Namespace + ".svc"
	}
	return "127.0.0.1"
}

// target is the full X-Railgrid-Svc-Target value.
func (v *serviceView) target() string {
	return fmt.Sprintf("%s://%s:%d", v.scheme(), v.targetHost(), v.Spec.Port)
}

// setSvcHeaders stamps the agent control headers for this Service on h: the
// target, and the TLS opt-out when spec.tlsInsecureSkipVerify is set.
func (v *serviceView) setSvcHeaders(h http.Header) {
	h.Set(svcTargetHeader, v.target())
	if v.Spec.TLSInsecureSkipVerify {
		h.Set(svcTLSInsecureHeader, "true")
	} else {
		h.Del(svcTLSInsecureHeader)
	}
}

// serveService dispatches an already-gated Service verb. svcObj is the object
// the gate returned: read as the provider only after a SubjectAccessReview on
// the caller's behalf proved the caller may see it, so the spec this proxy
// acts on is one the caller could have read — the provider never acts on an
// object the gate did not vouch for.
//
// There is no caller bearer on this path. Everything downstream acts on
// svcObj, and the Service's auth Secret — the one thing still fetched from kcp
// here — is read with the provider's own tenant credential, because it is a
// provider-owned object (see readServiceToken).
//
// Verbs: "proxy" (HTTP data plane) and "mcp".
func (p *Server) serveService(w http.ResponseWriter, r *http.Request, req dataplane.Request, svcObj *unstructured.Unstructured) {
	ctx := r.Context()
	logger := klog.FromContext(ctx).WithName("edgeservice-proxy")

	svc, err := decodeServiceView(req.Name, svcObj)
	if err != nil {
		logger.Error(err, "decoding service", "cluster", req.ClusterID, "name", req.Name)
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	// Resolve the tunnel for the referenced edge (LinuxServer, MacOSServer, or
	// KubernetesCluster).
	if svc.connResource() == "" {
		logger.Info("service references unsupported edge kind", "cluster", req.ClusterID, "name", req.Name, "kind", svc.Spec.EdgeRef.Kind)
		http.Error(w, "unsupported service edge kind", http.StatusBadRequest)
		return
	}
	key := edgeConnKey(svc.connResource(), req.ClusterID, svc.Spec.EdgeRef.Name)
	dialer, found := p.edgeConnManager.Load(key)
	if !found {
		logger.Info("no active tunnel for edge", "cluster", req.ClusterID, "edge", svc.Spec.EdgeRef.Name)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	rest := ""
	if req.Tail != "" {
		rest = "/" + req.Tail
	}

	switch req.Verb {
	case VerbProxy:
		if rest == "" {
			// ".../proxy" with no trailing slash: the agent routes only /svc/…,
			// so this answered a bare 404. Like the Kubernetes service proxy,
			// send a browser to ".../proxy/" so the page's relative links
			// resolve under the proxy; other methods go to the service root.
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				// Relative on purpose, and set by hand: http.Redirect would
				// absolutize it against r.URL.Path, which the hub has already
				// stripped its prefix from.
				target := "proxy/"
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				w.Header().Set("Location", target)
				w.WriteHeader(http.StatusMovedPermanently)
				return
			}
			rest = "/"
		}
		p.serviceHTTPProxy(ctx, w, r, req.ClusterID, svc, dialer, rest)
	case VerbMCP:
		p.buildServiceMCPHandler(req.ClusterID, req.Name, svc, dialer).ServeHTTP(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// Connectable-kind coordinates a Service can reference. The tunnel ConnManager
// keys each edge's dialer under its resource segment.
const (
	linuxServerResource       = "linuxservers"
	kubernetesClusterResource = "kubernetesclusters"
	macOSServerResource       = "macosservers"
	linuxServerKind           = "LinuxServer"
	macOSServerKind           = "MacOSServer"
	kubernetesClusterKind     = "KubernetesCluster"
)

// serviceHTTPProxy reverse-proxies an HTTP request to the host-local service
// through the agent's /svc handler, resolving Authorization per spec.auth
// (see applyServiceAuth).
func (p *Server) serviceHTTPProxy(ctx context.Context, w http.ResponseWriter, r *http.Request, cluster string, svc *serviceView, dialer interface {
	Dial(context.Context) (net.Conn, error)
}, rest string) {
	logger := klog.FromContext(ctx)

	mode := svc.authMode()
	var token string
	if mode == serviceAuthSecret {
		var err error
		token, err = p.readServiceToken(ctx, cluster, svc)
		if err != nil {
			logger.Error(err, "reading service auth token")
			http.Error(w, "service credentials unavailable", http.StatusBadGateway)
			return
		}
	}

	svcPath := "/svc" + rest

	deviceConn, err := dialer.Dial(ctx)
	if err != nil {
		logger.Error(err, "dialing edge agent for svc proxy")
		http.Error(w, "failed to connect to edge agent", http.StatusBadGateway)
		return
	}

	if isUpgradeRequest(r) {
		p.serviceHandleUpgrade(ctx, w, r, deviceConn, svc, svcPath, mode, token)
		return
	}

	// edgeDeviceConnTransport does one write and one ReadResponse over this
	// conn; closing the response body does not close it, and there is no
	// pooling to hand it back to. ServeHTTP has finished copying the body by
	// the time it returns, so closing here is safe — and skipping it leaks one
	// tunnel stream per request, which only stays invisible while the callers
	// are low-rate (SSH, Home Assistant).
	defer deviceConn.Close() //nolint:errcheck

	transport := &edgeDeviceConnTransport{conn: deviceConn}
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "edge-agent"
			req.URL.Path = svcPath
			svc.setSvcHeaders(req.Header)
			stampCallerForUpstream(req.Header, ctx, cluster)
			applyServiceAuth(req.Header, mode, token)
		},
		// The agent's answer is relayed as-is, including a 403 with
		// X-Railgrid-Svc-Policy: enforce when it refuses to dial spec.host.
		Transport: transport,
		// Unbuffered, as on the hub's own backend proxy: this path carries
		// log tails and other streams (a provider backend reached over an
		// edge serves the same verbs it would in-cluster), and the default
		// buffering turns those into an apparent hang.
		FlushInterval: -1,
	}
	proxy.ServeHTTP(w, r)
}

// stampCallerForUpstream replaces what kcp stamped for THIS provider with what
// a service behind the tunnel may reasonably want to know about the caller.
// The requestheader identity (X-Remote-*) and the hop counter are meaningful
// only to a server that trusts the shard, so they never cross the tunnel; the
// caller's user name and tenant travel as the railgrid headers a self-hosted
// provider backend reads to attribute and scope the call (X-Railgrid-User,
// X-Railgrid-Tenant, X-Railgrid-Cluster), overwriting anything the caller
// spelled there itself.
func stampCallerForUpstream(h http.Header, ctx context.Context, cluster string) {
	stripCallerHeaders(h)
	h.Del(dataplane.HeaderUser)
	if identity, ok := dataplane.ProxiedIdentityFrom(ctx); ok && identity.User != "" {
		h.Set(dataplane.HeaderUser, identity.User)
	}
	h.Set(dataplane.HeaderTenant, cluster)
	h.Set(dataplane.HeaderCluster, cluster)
}

// applyServiceAuth sets the Authorization the agent-side upstream will see,
// per spec.auth.
//
// The request's own Authorization never reaches this proxy: on the kube path
// it was the caller's kcp credential, and serve's adapter dropped it. A
// credential meant for the far-end service travels instead in
// dataplane.HeaderUpstreamAuthorization (X-Railgrid-Upstream-Authorization),
// which is what passthrough mode forwards as the upstream Authorization — the
// hub's edge hop uses it to hand an org-owned provider the delegated token
// (pkg/hub/providers/proxy_edge.go). The header itself is consumed here in
// every mode, so no upstream ever sees it.
func applyServiceAuth(h http.Header, mode, token string) {
	upstream := strings.TrimSpace(h.Get(dataplane.HeaderUpstreamAuthorization))
	h.Del(dataplane.HeaderUpstreamAuthorization)
	switch mode {
	case serviceAuthPassthrough:
		if upstream != "" {
			h.Set("Authorization", upstream)
		} else {
			h.Del("Authorization")
		}
	case serviceAuthNone:
		h.Del("Authorization")
	default: // serviceAuthSecret
		if token != "" {
			h.Set("Authorization", "Bearer "+token)
		} else {
			h.Del("Authorization")
		}
	}
}

// serviceHandleUpgrade handles WebSocket/upgrade requests to a service by
// hijacking and piping raw bytes through the tunnel (HA uses /api/websocket).
func (p *Server) serviceHandleUpgrade(ctx context.Context, w http.ResponseWriter, r *http.Request, deviceConn net.Conn, svc *serviceView, svcPath, mode, token string) {
	logger := klog.FromContext(ctx)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		logger.Error(err, "failed to hijack client connection for edgeservice upgrade")
		return
	}
	defer clientConn.Close() //nolint:errcheck
	defer deviceConn.Close() //nolint:errcheck

	r.URL.Path = svcPath
	r.RequestURI = r.URL.RequestURI()
	svc.setSvcHeaders(r.Header)
	stampCallerForUpstream(r.Header, ctx, r.Header.Get(dataplane.HeaderCluster))
	applyServiceAuth(r.Header, mode, token)

	if err := r.Write(deviceConn); err != nil {
		logger.Error(err, "failed to forward upgrade request to edge agent")
		return
	}

	errc := make(chan error, 2)
	go func() { _, e := io.Copy(deviceConn, clientConn); errc <- e }()
	go func() { _, e := io.Copy(clientConn, deviceConn); errc <- e }()
	<-errc
}

// decodeServiceView projects the Service object the gate already read into the
// fields the proxy needs, and refuses one whose spec cannot be acted on.
//
// It takes the object rather than re-reading it on purpose: acting on a spec
// read separately from the one the caller was authorized against is a
// confused deputy. The gate's read — as the provider, after the access review
// on the caller's behalf — is the read.
func decodeServiceView(name string, obj *unstructured.Unstructured) (*serviceView, error) {
	if obj == nil {
		return nil, fmt.Errorf("service %s: no object", name)
	}
	view := &serviceView{Name: name}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, view); err != nil {
		return nil, fmt.Errorf("decoding service %s: %w", name, err)
	}
	if view.Spec.EdgeRef.Name == "" {
		return nil, fmt.Errorf("service %s has no spec.edgeRef.name", name)
	}
	if view.Spec.Port == 0 {
		return nil, fmt.Errorf("service %s has no spec.port", name)
	}
	// Defence in depth behind the CRD's CEL rule: without a targetRef there is
	// no cluster-DNS name to dial, and defaulting to loopback would silently
	// proxy to the agent pod itself.
	if view.isKube() && (view.Spec.TargetRef == nil || view.Spec.TargetRef.Name == "" || view.Spec.TargetRef.Namespace == "") {
		return nil, fmt.Errorf("service %s targets a KubernetesCluster edge but has no spec.targetRef", name)
	}
	return view, nil
}

// readServiceToken reads the "token" key from the Service's authSecretRef as
// the PROVIDER, through its APIExport virtual workspace (tenantConfigFor) —
// never with the caller's credential. The returned string is the service's own
// auth token (e.g. a Home Assistant long-lived access token). Returns ""
// (no error) when no secret is configured — proxy-only services.
//
// The Secret is an edges-owned object, not a caller-owned one: the portal
// writes it labelled railgrid.ai/owner: edges (portal/src/api.ts,
// connectEdgeService) precisely so it falls inside this provider's
// label-scoped `secrets` permission claim, and the Service validation
// reconciler already reads it the same way (internal/servicectrl). Reading it
// as the caller instead needs core-group `get` on secrets in the tenant
// workspace, which the hub's identity policy will never mint for a workload
// identity — so every non-human caller (a factory runner dispatch, kuery) 403'd
// on any Service with a credential attached.
//
// This is not a confused deputy. The caller has already passed both gates —
// kcp authorized the method on services/{verb} before forwarding, and the
// gate proved with a SubjectAccessReview that the caller may see the Service —
// and `ref` comes from the spec the gate's read returned, never from the
// request. The provider only ever unwraps the credential attached to a Service
// the caller was just authorized to use.
func (p *Server) readServiceToken(ctx context.Context, cluster string, svc *serviceView) (string, error) {
	ref := svc.Spec.AuthSecretRef
	if ref == nil {
		return "", nil
	}
	clusterConfig, err := p.tenantConfigFor(ctx, cluster)
	if err != nil {
		return "", fmt.Errorf("resolving tenant config: %w", err)
	}
	k8sClient, err := kubernetes.NewForConfig(clusterConfig)
	if err != nil {
		return "", fmt.Errorf("creating cluster-scoped k8s client: %w", err)
	}
	secret, err := k8sClient.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("fetching auth secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	if tok, ok := secret.Data["token"]; ok {
		return string(tok), nil
	}
	return "", fmt.Errorf("auth secret %s/%s has no \"token\" key", ref.Namespace, ref.Name)
}
