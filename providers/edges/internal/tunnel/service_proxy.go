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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/faroshq/provider-edges/internal/kcpurl"
)

// serviceResource is the URL resource segment for the Service kind. It is
// deliberately NOT registered as a tunnel Kind (Service is not connectable),
// so parseEdgesProxyPath rejects it — edgeservice routes are branched before
// that check in buildEdgesProxyHandler.
const serviceResource = "services"

// svcTargetHeader mirrors the agent-side constant (pkg/agent/tunnel). The agent
// enforces that the target host is loopback.
const svcTargetHeader = "X-Kedge-Svc-Target"

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
		Host          string                  `json:"host,omitempty"`
		Type          string                  `json:"type,omitempty"`
		Scheme        string                  `json:"scheme,omitempty"`
		Port          int32                   `json:"port"`
		AuthSecretRef *corev1.SecretReference `json:"authSecretRef,omitempty"`
		Instructions  string                  `json:"instructions,omitempty"`
	} `json:"spec"`
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
	if v.isKube() {
		return kubernetesClusterResource
	}
	return linuxServerResource
}

// targetHost is the agent-side address of the service: cluster DNS for a
// KubernetesCluster edge, the host loopback for a LinuxServer edge.
func (v *serviceView) targetHost() string {
	if v.isKube() && v.Spec.TargetRef != nil {
		return v.Spec.TargetRef.Name + "." + v.Spec.TargetRef.Namespace + ".svc"
	}
	// LinuxServer edges default to loopback, but spec.host may point the service
	// at another device on the edge's LAN (gated agent-side by
	// KEDGE_AGENT_ALLOW_LAN_SVC_TARGETS).
	if v.Spec.Host != "" {
		return v.Spec.Host
	}
	return "127.0.0.1"
}

// target is the full X-Kedge-Svc-Target value.
func (v *serviceView) target() string {
	return fmt.Sprintf("%s://%s:%d", v.scheme(), v.targetHost(), v.Spec.Port)
}

// parseServicePath extracts {cluster}, {name}, {subresource}, and the
// remaining service-local path from an edgeproxy path targeting a Service.
// It returns ok=false for any non-service path so the caller falls through
// to the connectable-kind handler.
//
// Expected (after /edgeproxy is stripped):
//
//	/clusters/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/services/{name}/{subresource}[/rest...]
func (p *Server) parseServicePath(path string) (cluster, name, subresource, rest string, ok bool) {
	// [0]clusters [1]cluster [2]apis [3]group [4]version [5]resource [6]name [7]subresource [8...]rest
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 9)
	if len(parts) < 8 {
		return "", "", "", "", false
	}
	if parts[0] != "clusters" || parts[2] != "apis" || parts[3] != p.group ||
		parts[4] != p.version || parts[5] != serviceResource {
		return "", "", "", "", false
	}
	rest = ""
	if len(parts) == 9 {
		rest = "/" + parts[8]
	}
	return parts[1], parts[6], parts[7], rest, true
}

// serveService authorizes and dispatches a Service subresource request.
// Supported subresources: "proxy" (HTTP data plane) and "mcp".
func (p *Server) serveService(w http.ResponseWriter, r *http.Request, token, cluster, name, subresource, rest string) {
	ctx := r.Context()
	logger := klog.FromContext(ctx).WithName("edgeservice-proxy")

	// Delegated authorization (static tokens bypass, as in buildEdgesProxyHandler).
	_, isStaticToken := p.staticTokens[token]
	if !isStaticToken && p.kcpConfig != nil {
		tenantCfg, err := p.tenantConfigFor(ctx, cluster)
		if err != nil {
			logger.Error(err, "edgeservice authorization: resolving tenant config failed", "cluster", cluster, "name", name)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if err := p.authorizeFn(ctx, tenantCfg, p.kcpConfig, token, cluster, "proxy", p.group, serviceResource, name); err != nil {
			logger.Error(err, "edgeservice authorization failed", "cluster", cluster, "name", name)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	svc, err := p.fetchService(ctx, cluster, name, token)
	if err != nil {
		logger.Error(err, "fetching service", "cluster", cluster, "name", name)
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	// Resolve the tunnel for the referenced edge (LinuxServer or KubernetesCluster).
	key := edgeConnKey(svc.connResource(), cluster, svc.Spec.EdgeRef.Name)
	dialer, found := p.edgeConnManager.Load(key)
	if !found {
		logger.Info("no active tunnel for edge", "cluster", cluster, "edge", svc.Spec.EdgeRef.Name)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	switch subresource {
	case "proxy":
		p.serviceHTTPProxy(ctx, w, r, cluster, token, svc, dialer, rest)
	case "mcp":
		p.buildServiceMCPHandler(cluster, name, token, svc, dialer).ServeHTTP(w, r)
	default:
		http.Error(w, "unknown subresource", http.StatusNotFound)
	}
}

// Connectable-kind coordinates a Service can reference. The tunnel ConnManager
// keys each edge's dialer under its resource segment.
const (
	linuxServerResource       = "linuxservers"
	kubernetesClusterResource = "kubernetesclusters"
	kubernetesClusterKind     = "KubernetesCluster"
)

// serviceHTTPProxy reverse-proxies an HTTP request to the host-local service
// through the agent's /svc handler, injecting the auth token provider-side.
func (p *Server) serviceHTTPProxy(ctx context.Context, w http.ResponseWriter, r *http.Request, cluster, kcpToken string, svc *serviceView, dialer interface {
	Dial(context.Context) (net.Conn, error)
}, rest string) {
	logger := klog.FromContext(ctx)

	token, err := p.readServiceToken(ctx, cluster, svc, kcpToken)
	if err != nil {
		logger.Error(err, "reading service auth token")
		http.Error(w, "service credentials unavailable", http.StatusBadGateway)
		return
	}

	target := svc.target()
	svcPath := "/svc" + rest

	deviceConn, err := dialer.Dial(ctx)
	if err != nil {
		logger.Error(err, "dialing edge agent for svc proxy")
		http.Error(w, "failed to connect to edge agent", http.StatusBadGateway)
		return
	}

	if isUpgradeRequest(r) {
		p.serviceHandleUpgrade(ctx, w, r, deviceConn, target, svcPath, token)
		return
	}

	transport := &edgeDeviceConnTransport{conn: deviceConn}
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "edge-agent"
			req.URL.Path = svcPath
			req.Header.Set(svcTargetHeader, target)
			// Replace the caller's hub token with the service token.
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			} else {
				req.Header.Del("Authorization")
			}
		},
		Transport: transport,
	}
	proxy.ServeHTTP(w, r)
}

// serviceHandleUpgrade handles WebSocket/upgrade requests to a service by
// hijacking and piping raw bytes through the tunnel (HA uses /api/websocket).
func (p *Server) serviceHandleUpgrade(ctx context.Context, w http.ResponseWriter, r *http.Request, deviceConn net.Conn, target, svcPath, token string) {
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
	r.Header.Set(svcTargetHeader, target)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	} else {
		r.Header.Del("Authorization")
	}

	if err := r.Write(deviceConn); err != nil {
		logger.Error(err, "failed to forward upgrade request to edge agent")
		return
	}

	errc := make(chan error, 2)
	go func() { _, e := io.Copy(deviceConn, clientConn); errc <- e }()
	go func() { _, e := io.Copy(clientConn, deviceConn); errc <- e }()
	<-errc
}

// userClusterConfig returns a rest.Config scoped to a tenant workspace that
// authenticates as the CALLER, not the provider SA.
//
// This is deliberate. The provider SA is not granted direct (non-virtual-
// workspace) RBAC on Service objects in tenant workspaces — only on the
// connectable kinds — so reading a Service with p.kcpConfig 403s. The caller
// owns the workspace and can always read their own Services and the Secret they
// attached, so we read as them. It also avoids a confused-deputy: the provider
// never reads tenant objects on its own authority here. AnonymousClientConfig
// keeps the server URL + CA trust but strips the SA credentials before we set
// the bearer token.
func (p *Server) userClusterConfig(cluster, token string) *rest.Config {
	cfg := rest.AnonymousClientConfig(p.kcpConfig)
	cfg.Host = kcpurl.ClusterURL(p.kcpConfig.Host, cluster)
	cfg.BearerToken = token
	return cfg
}

// fetchService loads a Service CR from the tenant workspace, reading as the
// caller (see userClusterConfig).
func (p *Server) fetchService(ctx context.Context, cluster, name, token string) (*serviceView, error) {
	if p.kcpConfig == nil {
		return nil, fmt.Errorf("no kcp config")
	}
	clusterConfig := p.userClusterConfig(cluster, token)

	dynClient, err := dynamic.NewForConfig(clusterConfig)
	if err != nil {
		return nil, fmt.Errorf("creating cluster-scoped dynamic client: %w", err)
	}
	gvr := schema.GroupVersionResource{Group: p.group, Version: p.version, Resource: serviceResource}
	u, err := dynClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("fetching service %s: %w", name, err)
	}
	view := &serviceView{Name: name}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, view); err != nil {
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

// readServiceToken reads the "token" key from the Service's authSecretRef,
// reading as the caller (see userClusterConfig). token is the caller's kcp
// bearer token; the returned string is the service's own auth token (e.g. a
// Home Assistant long-lived access token). Returns "" (no error) when no secret
// is configured — proxy-only services.
func (p *Server) readServiceToken(ctx context.Context, cluster string, svc *serviceView, token string) (string, error) {
	ref := svc.Spec.AuthSecretRef
	if ref == nil {
		return "", nil
	}
	clusterConfig := p.userClusterConfig(cluster, token)
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
