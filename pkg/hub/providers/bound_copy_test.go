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
)

func TestBackendBoundCopyRouting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		org    bool
		reject bool
	}{{name: "platform binding despite org shadow"}, {name: "org binding", org: true}, {name: "removed or foreign binding", reject: true}} {
		t.Run(tc.name, func(t *testing.T) {
			proxy, edge := newEdgeBackedProxy(t, testOrg)
			platformHit := false
			platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				platformHit = true
				if r.Header.Get("X-Railgrid-Cluster") == "forged-cluster" {
					t.Error("forged cluster forwarded")
				}
				w.WriteHeader(200)
			}))
			defer platform.Close()
			target, _ := url.Parse(platform.URL)
			p, _ := proxy.reg.Get("infrastructure")
			p.BackendURL = target
			proxy.reg.Upsert(p)
			calls := 0
			proxy.SetBoundCopyResolver(func(_ context.Context, path, cluster, name string) (Provider, error) {
				calls++
				expectedPath := "root:railgrid:tenants:" + testOrg + ":" + testWS
				if path != expectedPath || cluster != testClusterIDFor(expectedPath) || name != "infrastructure" {
					t.Errorf("resolver received unverified addressing: %q %q %q", path, cluster, name)
				}
				if tc.reject {
					return Provider{}, errors.New("binding does not match a current registration")
				}
				if tc.org {
					p, _ := proxy.reg.GetForOrg(testOrg, name)
					return p, nil
				}
				p, _ := proxy.reg.Get(name)
				return p, nil
			})
			req := httptest.NewRequest("GET", "/services/providers/infrastructure/mcp", nil)
			req.Header.Set("Authorization", "Bearer "+callerBearer)
			req.Header.Set("X-Railgrid-Org", "foreign-org")
			req.Header.Set("X-Railgrid-Workspace", "foreign-workspace")
			req.Header.Set("X-Railgrid-Cluster", "forged-cluster")
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, req)
			if calls != 1 {
				t.Fatalf("binding lookup calls=%d", calls)
			}
			if tc.reject {
				if rec.Code != 404 || edge.hit || platformHit {
					t.Fatalf("unknown copy leaked to backend: status=%d platform=%v org=%v", rec.Code, platformHit, edge.hit)
				}
				return
			}
			if rec.Code != 200 || platformHit == tc.org || edge.hit != tc.org {
				t.Fatalf("wrong route: status=%d platform=%v org=%v", rec.Code, platformHit, edge.hit)
			}
		})
	}
}

func TestBackendBoundCopyRejectsUnverifiedCaller(t *testing.T) {
	proxy, upstream := newEdgeBackedProxy(t, testOrg)
	proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) { return "", "", errors.New("not a member") }))
	proxy.SetBoundCopyResolver(func(context.Context, string, string, string) (Provider, error) {
		t.Fatal("unverified caller reached binding resolver")
		return Provider{}, nil
	})
	req := httptest.NewRequest("GET", "/services/providers/infrastructure/mcp", nil)
	req.Header.Set("Authorization", "Bearer bad")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != 404 || upstream.hit {
		t.Fatalf("unverified request forwarded: %d", rec.Code)
	}
}

func TestBackendBoundCopyPreservesExternalWebhookAuthentication(t *testing.T) {
	proxy, _ := newEdgeBackedProxy(t, testOrg)
	hit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		if r.Header.Get("Authorization") != "Bearer external-webhook-secret" {
			t.Error("external webhook authentication changed")
		}
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	platform, _ := proxy.reg.Get("infrastructure")
	platform.BackendURL = target
	proxy.reg.Upsert(platform)
	proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) { return "", "", errors.New("external credential") }))
	proxy.SetBoundCopyResolver(func(context.Context, string, string, string) (Provider, error) {
		t.Fatal("external webhook entered tenant binding resolver")
		return Provider{}, nil
	})
	req := httptest.NewRequest("POST", "/services/providers/infrastructure/webhooks/event", nil)
	req.Header.Set("Authorization", "Bearer external-webhook-secret")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != 200 || !hit {
		t.Fatalf("external webhook routing changed: %d", rec.Code)
	}
}
