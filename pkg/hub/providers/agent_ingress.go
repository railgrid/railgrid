// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/railgrid/provider-sdk/dataplane"
)

// AgentIngressTarget is the authenticated consumer organization and native
// export identity selected by its actual Edges binding.
type AgentIngressTarget struct {
	OrgUUID       string
	ExportPath    string
	ExportName    string
	ExportCluster string
}

// AgentIngressResolver verifies an edge's bootstrap/scoped credential before
// returning its target. No human bearer is forwarded by this path.
type AgentIngressResolver func(context.Context, string, string, string, string) (AgentIngressTarget, error)

func (p *ProviderProxy) SetAgentIngressResolver(resolve AgentIngressResolver) {
	p.agentIngress = resolve
	p.agentIngressAdmission = &agentIngressAdmission{
		limiter: rate.NewLimiter(rate.Every(10*time.Millisecond), 200),
		active:  make(chan struct{}, 32),
	}
}

// Admission bounds pre-authentication kcp work, not established tunnel count.
// One fixed token bucket bounds total work without storing attacker-controlled keys.
type agentIngressAdmission struct {
	limiter interface{ Allow() bool }
	active  chan struct{}
}

func (a *agentIngressAdmission) begin() (func(), bool) {
	if !a.limiter.Allow() {
		return nil, false
	}
	select {
	case a.active <- struct{}{}:
		return func() { <-a.active }, true
	default:
		return nil, false
	}
}

// agentIngressScope recognizes only the closed agent-ingress grammar. Pickup
// addressing is supplied by the authenticated control tunnel, and is verified
// again against its credential; it is never accepted as authority by itself.
func agentIngressScope(r *http.Request, rest string) (cluster, resource, name string, scoped bool, err error) {
	if !strings.HasPrefix(rest, "/agent/") {
		return "", "", "", false, nil
	}
	if r.URL.RawPath != "" {
		return "", "", "", true, fmt.Errorf("encoded agent path")
	}
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if len(parts) == 6 && parts[0] == "agent" && parts[1] == "clusters" && parts[5] == "proxy" {
		cluster, resource, name = parts[2], parts[3], parts[4]
	} else if (len(parts) == 2 || len(parts) == 3 && cleanAgentSegment(parts[2])) && parts[0] == "agent" && parts[1] == "proxy" {
		q := r.URL.Query()
		if !q.Has("railgrid.agent.cluster") && !q.Has("railgrid.agent.resource") && !q.Has("railgrid.agent.name") {
			return "", "", "", false, nil // Existing platform pickup URLs.
		}
		for _, key := range []string{"railgrid.agent.cluster", "railgrid.agent.resource", "railgrid.agent.name"} {
			if len(q[key]) != 1 {
				return "", "", "", true, fmt.Errorf("invalid pickup scope")
			}
		}
		cluster, resource, name = q.Get("railgrid.agent.cluster"), q.Get("railgrid.agent.resource"), q.Get("railgrid.agent.name")
	} else {
		return "", "", "", true, fmt.Errorf("invalid agent path")
	}
	if !cleanAgentSegment(cluster) || !cleanAgentSegment(name) || (resource != "kubernetesclusters" && resource != "linuxservers" && resource != "macosservers") {
		return "", "", "", true, fmt.Errorf("invalid agent scope")
	}
	return cluster, resource, name, true, nil
}

func cleanAgentSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// serveAgentIngress returns true when it handled the request. The resolver
// authenticates before this path can send an original credential into an org's
// cluster. All other provider requests retain ordinary human delegation.
func (p *ProviderProxy) serveAgentIngress(w http.ResponseWriter, r *http.Request, name, rest string) bool {
	if name != EdgesProviderName || p.agentIngress == nil {
		return false
	}
	cluster, resource, edge, scoped, err := agentIngressScope(r, rest)
	if !scoped {
		return false
	}
	if err != nil || r.Method != http.MethodGet {
		http.Error(w, "invalid agent ingress request", http.StatusBadRequest)
		return true
	}
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" || len(token) > 64*1024 || strings.ContainsAny(token, " \t\r\n") {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return true
	}
	if r.Context().Err() != nil {
		http.Error(w, "agent authentication canceled", http.StatusRequestTimeout)
		return true
	}
	release, admitted := p.agentIngressAdmission.begin()
	if !admitted {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "agent authentication temporarily busy", http.StatusTooManyRequests)
		return true
	}
	// Release before forwarding: a long-lived control tunnel must not occupy an
	// authentication slot. Deferring inside this scope also releases on panic.
	target, err := func() (AgentIngressTarget, error) {
		defer release()
		return p.agentIngress(r.Context(), cluster, resource, edge, token)
	}()
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return true
	}
	// Bind the credential to the resolved native export identity, not just its
	// reusable path/name. A replacement registration must not receive an old
	// Edge's bearer or SSH enrollment credentials.
	prov, found := p.reg.BoundCopy(target.OrgUUID, name, target.ExportPath, target.ExportName, target.ExportCluster)
	if !found || !prov.Ready() {
		http.Error(w, "agent provider unavailable", http.StatusServiceUnavailable)
		return true
	}
	if prov.OrgUUID == "" {
		// The ordinary platform path already preserves agent credentials. Pin its
		// selection despite any spoofed tenant headers or unrelated human scope.
		p.serveAgentDirect(w, r, prov, rest)
		return true
	}
	hop, err := p.resolveEdgeHop(prov)
	if err != nil {
		http.Error(w, "agent provider transport unavailable", http.StatusServiceUnavailable)
		return true
	}
	dst := hop.url(rest)
	p.serveAgentHop(w, r, prov, dst, hop.transport, token)
	return true
}

func (p *ProviderProxy) serveAgentDirect(w http.ResponseWriter, r *http.Request, prov Provider, rest string) {
	if prov.BackendURL == nil {
		http.Error(w, "agent provider unavailable", http.StatusServiceUnavailable)
		return
	}
	dst := *prov.BackendURL
	dst.Path = singleJoiningSlash(dst.Path, rest)
	p.serveAgentHop(w, r, prov, &dst, nil, "")
}

func (p *ProviderProxy) serveAgentHop(w http.ResponseWriter, r *http.Request, prov Provider, dst *url.URL, transport http.RoundTripper, upstreamToken string) {
	rp := &httputil.ReverseProxy{FlushInterval: -1, Transport: transport, Director: func(req *http.Request) {
		req.URL.Scheme, req.URL.Host, req.URL.Path, req.URL.RawPath = dst.Scheme, dst.Host, dst.Path, ""
		req.Host = dst.Host
		original := req.Header
		req.Header = make(http.Header)
		for _, key := range []string{
			"Content-Type", "Accept", "Connection", "Upgrade", "Sec-Websocket-Key", "Sec-Websocket-Version",
			// These are agent protocol data, accepted only after the credential
			// is verified against this Edge. Preserve host enrollment without
			// forwarding arbitrary X-Railgrid identity or browser credentials.
			"X-Railgrid-Agent-Hostname", "X-Railgrid-SSH-User", "X-Railgrid-SSH-Password", "X-Railgrid-SSH-PrivateKey", "X-Railgrid-SSH-HostKey",
		} {
			if values := original.Values(key); len(values) > 0 {
				req.Header[key] = values
			}
		}
		if prov.OrgUUID == "" {
			req.Header.Set("Authorization", original.Get("Authorization"))
		} else {
			req.Header.Set(dataplane.HeaderUpstreamAuthorization, "Bearer "+upstreamToken)
		}
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
		p.log.Error(err, "agent upstream error", "provider", prov.Name)
		http.Error(w, "agent provider upstream error", http.StatusBadGateway)
	}}
	if prov.OrgUUID != "" {
		// An org-owned edges provider crosses the Hub origin, including for
		// plain HTTP agent requests that do not upgrade. Filter its response
		// headers and trailer channel while retaining the agent upgrade path.
		rp.ModifyResponse = func(resp *http.Response) error {
			return orgProviderModifyResponse(resp, r.Header)
		}
		w = newOrgProviderResponseWriter(w)
	}
	rp.ServeHTTP(w, r)
}
