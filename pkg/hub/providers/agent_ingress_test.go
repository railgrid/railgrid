// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/go-logr/logr"
	"github.com/railgrid/provider-sdk/dataplane"
)

func TestAgentIngressRoutesVerifiedCredentialToActualBinding(t *testing.T) {
	for _, orgCopy := range []bool{false, true} {
		t.Run(map[bool]string{false: "platform binding despite org shadow", true: "org binding with opaque token"}[orgCopy], func(t *testing.T) {
			var gotPath string
			var gotHeaders http.Header
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotHeaders = r.Header.Clone()
				w.Header().Set("Set-Cookie", "provider-session=secret; Path=/")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Content-Security-Policy", "default-src https:")
				w.Header().Set("Cache-Control", "public")
				w.Header().Set("X-Content-Type-Options", "unsafe")
				w.Header().Set("Trailer", "X-Provider-Trailer")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("agent response"))
				w.Header().Set("X-Provider-Trailer", "announced")
				w.Header()[http.TrailerPrefix+"X-Provider-Late"] = []string{"late"}
			}))
			defer up.Close()
			u, _ := url.Parse(up.URL)
			reg := NewRegistry()
			const platformPath = "root:railgrid:providers:edges"
			const orgPath = "root:railgrid:tenants:org:providers:edges"
			reg.Upsert(Provider{Name: "edges", WorkspaceCluster: "platform-provider", APIExportPath: platformPath, APIExportName: "edges-export", BackendURL: u, EndpointsValid: true})
			reg.Upsert(Provider{Name: "edges", OrgUUID: "org", WorkspaceCluster: "org-provider", APIExportPath: orgPath, APIExportName: "edges-export", EndpointsValid: true, EdgeRoute: &EdgeRoute{Cluster: "host-cluster", EdgeName: "host", ServiceName: "provider-edges", WorkspaceUUID: "host-ws"}})
			p := NewBackendProxy(reg, logr.Discard())
			p.SetKCPFrontDoor(u, http.DefaultTransport)
			p.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
				return "", "", errors.New("not a human token")
			}))
			p.SetAgentIngressResolver(func(_ context.Context, cluster, resource, name, token string) (AgentIngressTarget, error) {
				if cluster != "consumer" || resource != "kubernetesclusters" || name != "edge" || token != "edge-join" {
					t.Fatal("wrong scope or credential")
				}
				if orgCopy {
					return AgentIngressTarget{OrgUUID: "org", ExportPath: orgPath, ExportName: "edges-export", ExportCluster: "org-provider"}, nil
				}
				return AgentIngressTarget{OrgUUID: "org", ExportPath: platformPath, ExportName: "edges-export", ExportCluster: "platform-provider"}, nil
			})
			r := httptest.NewRequest(http.MethodGet, "/services/providers/edges/agent/clusters/consumer/kubernetesclusters/edge/proxy", nil)
			r.Header.Set("Authorization", "Bearer edge-join")
			r.Header.Set("Cookie", "human-session")
			r.Header.Set("X-Railgrid-Org", "other")
			r.Header.Set("X-Remote-User", "admin")
			r.Header.Set(dataplane.HeaderUpstreamIdentity, "forged")
			protocol := map[string]string{"X-Railgrid-Agent-Hostname": "agent-host", "X-Railgrid-SSH-User": "agent", "X-Railgrid-SSH-Password": "fixture-password", "X-Railgrid-SSH-PrivateKey": "fixture-key", "X-Railgrid-SSH-HostKey": "fixture-host-key"}
			for header, value := range protocol {
				r.Header.Set(header, value)
			}
			w := httptest.NewRecorder()
			w.Header().Add("Set-Cookie", "hub-session=trusted; HttpOnly; Path=/")
			p.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if got := w.Body.String(); got != "agent response" {
				t.Errorf("body = %q, want the agent response", got)
			}
			if orgCopy {
				if cookies := w.Header().Values("Set-Cookie"); len(cookies) != 1 || cookies[0] != "hub-session=trusted; HttpOnly; Path=/" {
					t.Errorf("org agent cookies = %#v, want trusted Hub cookie only", cookies)
				}
				for _, name := range []string{"Access-Control-Allow-Origin", "Trailer", "X-Provider-Trailer", "X-Provider-Late"} {
					if w.Header().Get(name) != "" || w.Result().Trailer.Get(name) != "" {
						t.Errorf("org agent response leaked %s", name)
					}
				}
				for name, want := range map[string]string{
					"Content-Security-Policy": orgProviderContentSecurityPolicy,
					"Cache-Control":           "private, no-store",
					"X-Content-Type-Options":  "nosniff",
				} {
					if got := w.Header().Get(name); got != want {
						t.Errorf("org agent %s = %q, want %q", name, got, want)
					}
				}
			} else {
				cookies := w.Header().Values("Set-Cookie")
				if len(cookies) != 2 || cookies[0] != "hub-session=trusted; HttpOnly; Path=/" || cookies[1] != "provider-session=secret; Path=/" {
					t.Errorf("platform agent Set-Cookie = %#v, want outer and provider cookies preserved", cookies)
				}
				if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
					t.Errorf("platform agent CORS = %q, want existing behavior", got)
				}
				if got := w.Header().Get("Content-Security-Policy"); got != "default-src https:" {
					t.Errorf("platform agent CSP = %q, want existing behavior", got)
				}
			}
			if orgCopy {
				if gotPath != "/clusters/host-cluster/apis/edges.railgrid.ai/v1alpha1/services/provider-edges/proxy/agent/clusters/consumer/kubernetesclusters/edge/proxy" {
					t.Fatal(gotPath)
				}
				if gotHeaders.Get("Authorization") != "" || gotHeaders.Get(dataplane.HeaderUpstreamAuthorization) != "Bearer edge-join" {
					t.Fatal("agent credential not bounded to upstream hop")
				}
			} else if gotPath != "/agent/clusters/consumer/kubernetesclusters/edge/proxy" || gotHeaders.Get("Authorization") != "Bearer edge-join" {
				t.Fatal("platform binding was shadowed")
			}
			for _, h := range []string{"Cookie", "X-Railgrid-Org", "X-Remote-User", dataplane.HeaderUpstreamIdentity} {
				if gotHeaders.Get(h) != "" {
					t.Fatalf("leaked %s", h)
				}
			}
			for header, value := range protocol {
				if gotHeaders.Get(header) != value {
					t.Errorf("agent protocol header %s was dropped", header)
				}
			}
		})
	}
}

func TestAgentIngressOrgUpgradeFiltersHeadersAndKeepsDuplexStream(t *testing.T) {
	const exportPath = "root:railgrid:tenants:" + testOrg + ":providers:edges"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "relay-test") {
			t.Errorf("upstream Upgrade = %q, want relay-test", r.Header.Get("Upgrade"))
		}
		serveProviderTestUpgrade(w)
	}))
	t.Cleanup(upstream.Close)
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	registry.Upsert(Provider{Name: EdgesProviderName, BackendURL: upstreamURL, EndpointsValid: true})
	registry.Upsert(Provider{
		Name: EdgesProviderName, OrgUUID: testOrg, WorkspaceCluster: "org-provider",
		APIExportPath: exportPath, APIExportName: "edges-export", EndpointsValid: true,
		EdgeRoute: &EdgeRoute{Cluster: testCluster, EdgeName: "host", ServiceName: "provider-edges", WorkspaceUUID: testWS},
	})
	proxy := NewBackendProxy(registry, logr.Discard())
	proxy.SetKCPFrontDoor(upstreamURL, http.DefaultTransport)
	proxy.SetAgentIngressResolver(func(context.Context, string, string, string, string) (AgentIngressTarget, error) {
		return AgentIngressTarget{OrgUUID: testOrg, ExportPath: exportPath, ExportName: "edges-export", ExportCluster: "org-provider"}, nil
	})
	frontend := httptest.NewServer(proxy)
	t.Cleanup(frontend.Close)
	request, err := http.NewRequest(http.MethodGet, frontend.URL+"/services/providers/edges/agent/clusters/consumer/kubernetesclusters/edge/proxy", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer edge-join")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "relay-test")
	transport := &http.Transport{ResponseHeaderTimeout: 3 * time.Second}
	t.Cleanup(transport.CloseIdleConnections)
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", response.StatusCode)
	}
	if response.Header.Get("Set-Cookie") != "" || response.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unsafe agent upgrade headers escaped: %#v", response.Header)
	}
	if response.Header.Get("Content-Security-Policy") != orgProviderContentSecurityPolicy {
		t.Errorf("CSP = %q, want forced policy", response.Header.Get("Content-Security-Policy"))
	}
	stream, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatal("agent upgrade lost duplex stream")
	}
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(stream, reply); err != nil || string(reply) != "ping" {
		t.Fatalf("agent upgrade echo %q: %v", reply, err)
	}
}

func TestAgentIngressRefusesUnverifiedCredentials(t *testing.T) {
	p := NewBackendProxy(NewRegistry(), logr.Discard())
	p.SetAgentIngressResolver(func(context.Context, string, string, string, string) (AgentIngressTarget, error) {
		return AgentIngressTarget{}, errors.New("not edge-scoped")
	})
	r := httptest.NewRequest(http.MethodGet, "/services/providers/edges/agent/clusters/c/linuxservers/e/proxy", nil)
	r.Header.Set("Authorization", "Bearer broad-human-token")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestAgentIngressPickupUsesCredentialBoundScope(t *testing.T) {
	p, upstream := newEdgeBackedProxy(t, "")
	const exportPath = "root:railgrid:tenants:" + testOrg + ":providers:edges"
	p.reg.Upsert(Provider{Name: "edges", OrgUUID: testOrg, WorkspaceCluster: "org-provider", APIExportPath: exportPath, APIExportName: "edges-export", EndpointsValid: true,
		EdgeRoute: &EdgeRoute{Cluster: testCluster, EdgeName: "host", ServiceName: "provider-edges", WorkspaceUUID: testWS}})
	p.SetAgentIngressResolver(func(_ context.Context, cluster, resource, name, token string) (AgentIngressTarget, error) {
		if cluster != "consumer" || resource != "linuxservers" || name != "edge" || token != "agent-token" {
			t.Fatal("pickup scope was not authenticated")
		}
		return AgentIngressTarget{OrgUUID: testOrg, ExportPath: exportPath, ExportName: "edges-export", ExportCluster: "org-provider"}, nil
	})
	r := httptest.NewRequest(http.MethodGet, "/services/providers/edges/agent/proxy/replica-a?railgrid.agent.cluster=consumer&railgrid.agent.resource=linuxservers&railgrid.agent.name=edge&revdial.dialer=opaque", nil)
	r.Header.Set("Authorization", "Bearer agent-token")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !upstream.hit || upstream.upstreamAuth != "Bearer agent-token" {
		t.Fatalf("pickup not routed over org edge: status=%d upstream=%+v", w.Code, upstream)
	}
	if upstream.path != "/clusters/"+testCluster+"/apis/edges.railgrid.ai/v1alpha1/services/provider-edges/proxy/agent/proxy/replica-a" {
		t.Fatal(upstream.path)
	}
}

func TestAgentIngressScope(t *testing.T) {
	for _, tc := range []struct {
		path          string
		scoped, valid bool
	}{
		{"/agent/clusters/c/linuxservers/e/proxy", true, true},
		{"/agent/proxy/replica-a?railgrid.agent.cluster=c&railgrid.agent.resource=macosservers&railgrid.agent.name=e&revdial.dialer=id", true, true},
		{"/agent/proxy?revdial.dialer=legacy", false, true},
		{"/mcp", false, true},
		{"/agent/clusters/root:tenant/linuxservers/e/proxy", true, false},
		{"/agent/clusters/c/services/e/proxy", true, false},
		{"/agent/clusters/c/linuxservers/../proxy", true, false},
		{"/agent/proxy?railgrid.agent.cluster=c", true, false},
		{"/agent/proxy?railgrid.agent.cluster=c&railgrid.agent.cluster=d&railgrid.agent.resource=linuxservers&railgrid.agent.name=e", true, false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			_, _, _, scoped, err := agentIngressScope(r, r.URL.Path)
			if scoped != tc.scoped || (err == nil) != tc.valid {
				t.Fatalf("scoped=%v err=%v", scoped, err)
			}
		})
	}
}

// Even an authenticated old Edge must not hand its bearer or enrollment secrets
// to a replacement provider registered at the same reusable export path/name.
func TestAgentIngressRejectsReplacedOrForeignExportBeforeForwarding(t *testing.T) {
	const exportPath = "root:railgrid:tenants:org:providers:edges"
	for _, tc := range []struct {
		name, boundCluster, consumerOrg string
		wantForward                     bool
	}{
		{"matching current export", "current-provider", "org", true},
		{"replaced provider workspace", "old-provider", "org", false},
		{"missing resolved export cluster", "", "org", false},
		{"foreign provider organization", "current-provider", "foreign", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forwarded := false
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true; w.WriteHeader(200) }))
			defer upstream.Close()
			targetURL, _ := url.Parse(upstream.URL)
			registry := NewRegistry()
			registry.Upsert(Provider{Name: "edges", WorkspaceCluster: "platform-provider", APIExportPath: "root:railgrid:providers:edges", APIExportName: "edges-export", BackendURL: targetURL, EndpointsValid: true})
			registry.Upsert(Provider{Name: "edges", OrgUUID: "org", WorkspaceCluster: "current-provider", APIExportPath: exportPath, APIExportName: "edges-export", BackendURL: targetURL, EndpointsValid: true, EdgeRoute: &EdgeRoute{Cluster: "host", EdgeName: "host", ServiceName: "provider-edges", WorkspaceUUID: "hosting"}})
			proxy := NewBackendProxy(registry, logr.Discard())
			proxy.SetKCPFrontDoor(targetURL, http.DefaultTransport)
			proxy.SetAgentIngressResolver(func(context.Context, string, string, string, string) (AgentIngressTarget, error) {
				return AgentIngressTarget{OrgUUID: tc.consumerOrg, ExportPath: exportPath, ExportName: "edges-export", ExportCluster: tc.boundCluster}, nil
			})
			request := httptest.NewRequest(http.MethodGet, "/services/providers/edges/agent/clusters/consumer/kubernetesclusters/edge/proxy", nil)
			request.Header.Set("Authorization", "Bearer old-edge-token")
			request.Header.Set("X-Railgrid-SSH-PrivateKey", "old-host-key")
			response := httptest.NewRecorder()
			proxy.ServeHTTP(response, request)
			wantStatus := http.StatusServiceUnavailable
			if tc.wantForward {
				wantStatus = http.StatusOK
			}
			if response.Code != wantStatus || forwarded != tc.wantForward {
				t.Fatalf("status=%d forwarded=%v", response.Code, forwarded)
			}
		})
	}
}

func agentIngressTestRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/services/providers/edges/agent/clusters/consumer/kubernetesclusters/edge/proxy", nil)
	r.Header.Set("Authorization", "Bearer bogus")
	return r
}

func TestAgentIngressRateLimitPrecedesCredentialReads(t *testing.T) {
	p := NewBackendProxy(NewRegistry(), logr.Discard())
	calls := 0
	p.SetAgentIngressResolver(func(context.Context, string, string, string, string) (AgentIngressTarget, error) {
		calls++
		return AgentIngressTarget{}, errors.New("invalid")
	})
	p.agentIngressAdmission.limiter = rate.NewLimiter(rate.Every(time.Hour), 1)
	first := httptest.NewRecorder()
	p.ServeHTTP(first, agentIngressTestRequest())
	second := httptest.NewRecorder()
	request := agentIngressTestRequest()
	request.Header.Set("X-Forwarded-For", "203.0.113.1")
	p.ServeHTTP(second, request)
	if first.Code != http.StatusUnauthorized || second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") != "1" || calls != 1 {
		t.Fatalf("statuses=%d/%d calls=%d", first.Code, second.Code, calls)
	}
}

type allowAgentIngress struct{}

func (allowAgentIngress) Allow() bool { return true }

func TestAgentIngressBoundsConcurrentCredentialChecks(t *testing.T) {
	p := NewBackendProxy(NewRegistry(), logr.Discard())
	started, release := make(chan struct{}, 2), make(chan struct{})
	var calls atomic.Int32
	p.SetAgentIngressResolver(func(context.Context, string, string, string, string) (AgentIngressTarget, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return AgentIngressTarget{}, errors.New("invalid")
	})
	p.agentIngressAdmission.limiter = allowAgentIngress{}
	p.agentIngressAdmission.active = make(chan struct{}, 2)
	done := make(chan struct{}, 2)
	for range 2 {
		go func() { p.ServeHTTP(httptest.NewRecorder(), agentIngressTestRequest()); done <- struct{}{} }()
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("credential checks did not start")
		}
	}
	blocked := httptest.NewRecorder()
	p.ServeHTTP(blocked, agentIngressTestRequest())
	if blocked.Code != http.StatusTooManyRequests || calls.Load() != 2 {
		close(release)
		t.Fatalf("parallel checks were not bounded: status=%d calls=%d", blocked.Code, calls.Load())
	}
	close(release)
	for range 2 {
		<-done
	}
	// All slots must be released after credential resolution, including failures.
	next := httptest.NewRecorder()
	p.ServeHTTP(next, agentIngressTestRequest())
	if next.Code != http.StatusUnauthorized || calls.Load() != 3 {
		t.Fatalf("slot leaked: status=%d calls=%d", next.Code, calls.Load())
	}
}

func TestAgentIngressRejectsMalformedBearerBeforeCredentialReads(t *testing.T) {
	for _, token := range []string{"has spaces", strings.Repeat("a", 64*1024+1)} {
		p := NewBackendProxy(NewRegistry(), logr.Discard())
		p.SetAgentIngressResolver(func(context.Context, string, string, string, string) (AgentIngressTarget, error) {
			t.Fatal("malformed token reached credential reads")
			return AgentIngressTarget{}, nil
		})
		r := agentIngressTestRequest()
		r.Header.Set("Authorization", "Bearer "+token)
		result := httptest.NewRecorder()
		p.ServeHTTP(result, r)
		if result.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", result.Code)
		}
	}
}

func TestAgentIngressCanceledRequestSkipsCredentialReads(t *testing.T) {
	p := NewBackendProxy(NewRegistry(), logr.Discard())
	p.SetAgentIngressResolver(func(context.Context, string, string, string, string) (AgentIngressTarget, error) {
		t.Fatal("canceled request reached credential reads")
		return AgentIngressTarget{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := httptest.NewRecorder()
	p.ServeHTTP(result, agentIngressTestRequest().WithContext(ctx))
	if result.Code != http.StatusRequestTimeout || len(p.agentIngressAdmission.active) != 0 {
		t.Fatalf("status=%d active=%d", result.Code, len(p.agentIngressAdmission.active))
	}
}
