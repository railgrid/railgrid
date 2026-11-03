/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package providers

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
)

type actionTestTransport func(*http.Request) (*http.Response, error)

func (f actionTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestShardActionRoutesBoundExportWithoutCallerCredentials(t *testing.T) {
	p, _ := newEdgeBackedProxy(t, testOrg)
	prov := orgInfra(t, p)
	prov.APIExportPath = "root:railgrid:tenants:" + testOrg + ":providers:infrastructure"
	prov.APIExportName = "infrastructure.providers.railgrid.ai"
	prov.WorkspaceCluster = "provider-original"
	prov.BackendURL, _ = url.Parse("http://provider.private.svc:8080")
	p.reg.Upsert(prov)
	hit := false
	p.kcpTransport = actionTestTransport(func(r *http.Request) (*http.Response, error) {
		hit = true
		if !strings.Contains(r.URL.Path, "/services/provider-infrastructure/proxy/clusters/consumer/apis/") {
			t.Fatal(r.URL.Path)
		}
		if r.Header.Get(dataplane.HeaderActionProof) != "hub-signed-action-proof" {
			t.Fatal("hub action proof was lost on the edge hop")
		}
		if r.URL.RawQuery != "component=web" {
			t.Fatal(r.URL.RawQuery)
		}
		for _, name := range []string{"Authorization", "Cookie", "Impersonate-User", dataplane.HeaderUpstreamAuthorization, dataplane.HeaderRemoteUser} {
			if r.Header.Get(name) != "" {
				t.Fatalf("credential or raw identity leaked: %s", name)
			}
		}
		h, err := dataplane.DecodeUpstreamIdentity(r.Header.Get(dataplane.HeaderUpstreamIdentity))
		if err != nil || h.Get(dataplane.HeaderRemoteUser) != "alice" {
			t.Fatalf("identity lost: %v", err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
	})
	req, _ := http.NewRequest("POST", prov.BackendURL.String()+"/clusters/consumer/apis/example.io/v1/widgets/one/run?component=web", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer user-secret")
	req.Header.Set("Cookie", "secret")
	req.Header.Set(dataplane.HeaderActionProof, "hub-signed-action-proof")
	req.Header.Set("Impersonate-User", "mallory")
	req.Header.Set(dataplane.HeaderRemoteUser, "alice")
	req.Header.Set(dataplane.HeaderUpstreamAuthorization, "Bearer forged")
	response, err := p.ForwardShardAction(req, prov.APIExportPath, prov.APIExportName, prov.WorkspaceCluster)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !hit || req.Header.Get("Authorization") != "Bearer user-secret" {
		t.Fatal("did not forward or mutated original")
	}
	for _, exportCluster := range []string{"", "provider-replaced"} {
		hit = false
		if _, err := p.ForwardShardAction(req, prov.APIExportPath, prov.APIExportName, exportCluster); err == nil || hit {
			t.Fatalf("missing/replaced bound export identity %q forwarded to current registration", exportCluster)
		}
	}
	replacement := prov
	replacement.WorkspaceCluster = "replacement-provider"
	p.reg.Upsert(replacement)
	hit = false
	if _, err := p.ForwardShardAction(req, prov.APIExportPath, prov.APIExportName, prov.WorkspaceCluster); err == nil || hit {
		t.Fatal("old binding was forwarded to a same-name replacement registration")
	}
	p.reg.Upsert(prov)
	for _, path := range []string{strings.Replace(prov.APIExportPath, testOrg, "other-org", 1), prov.APIExportPath} {
		hit = false
		other := req.Clone(req.Context())
		u := *req.URL
		other.URL = &u
		other.URL.Host = "attacker.invalid"
		if _, err := p.ForwardShardAction(other, path, prov.APIExportName, prov.WorkspaceCluster); err == nil || hit {
			t.Fatal("wrong organization/endpoint accepted")
		}
	}
}
