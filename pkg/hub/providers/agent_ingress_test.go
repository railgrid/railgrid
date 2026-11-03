// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

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
				w.WriteHeader(http.StatusOK)
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
			p.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
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
