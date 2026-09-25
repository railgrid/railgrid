// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseSubresourcePathReadsWhatTheShardForwards(t *testing.T) {
	// The exact shape kcp's virtual-resources proxy sends: the endpoint URL,
	// then /clusters/{id}, then the request's own kube path.
	got, err := ParseSubresourcePath("/clusters/1v98kgkp03uox9qw/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/addon-credentials")
	if err != nil {
		t.Fatalf("ParseSubresourcePath: %v", err)
	}
	if got.ClusterID != "1v98kgkp03uox9qw" || got.Group != "edges.railgrid.ai" || got.APIVersion != "v1alpha1" {
		t.Fatalf("addressing = %+v", got)
	}
	if got.Resource != "linuxservers" || got.Name != "edge-1" || got.Verb != "addon-credentials" {
		t.Fatalf("route = %+v", got)
	}
	if got.Tail != "" {
		t.Fatalf("tail = %q, want none", got.Tail)
	}

	withTail, err := ParseSubresourcePath("clusters/1v98kgkp03uox9qw/apis/code.railgrid.ai/v1alpha1/repositories/app/feedback/pull/7")
	if err != nil {
		t.Fatalf("ParseSubresourcePath with a tail: %v", err)
	}
	if withTail.Tail != "pull/7" {
		t.Fatalf("tail = %q", withTail.Tail)
	}
}

func TestParseSubresourcePathRefusesWhatItMust(t *testing.T) {
	for name, path := range map[string]string{
		"workspace path instead of a cluster ID": "/clusters/root:railgrid:tenants:acme/apis/g/v1/rs/n/verb",
		"no apis segment":                        "/clusters/1v98kgkp03uox9qw/g/v1/rs/n/verb",
		"truncated":                              "/clusters/1v98kgkp03uox9qw/apis/g/v1/rs/n",
		"status is not a provider verb":          "/clusters/1v98kgkp03uox9qw/apis/g/v1/rs/n/status",
		"scale is not a provider verb":           "/clusters/1v98kgkp03uox9qw/apis/g/v1/rs/n/scale",
		"empty":                                  "/",
		"dot segment":                            "/clusters/1v98kgkp03uox9qw/apis/g/v1/rs/../verb",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSubresourcePath(path); err == nil {
				t.Fatal("path was accepted")
			}
		})
	}
}

func TestParseSubresourceRequestReadsTheComponentQuery(t *testing.T) {
	good := "/clusters/1v98kgkp03uox9qw/apis/infrastructure.railgrid.ai/v1alpha1/instances/site/log/follow?component=app&since=1h"
	got, err := ParseSubresourceRequest(httptest.NewRequest(http.MethodGet, good, nil))
	if err != nil {
		t.Fatalf("ParseSubresourceRequest: %v", err)
	}
	if got.Component != "app" || got.Verb != "log" || got.Tail != "follow" || got.Name != "site" {
		t.Fatalf("route = %+v", got)
	}
	plain, err := ParseSubresourceRequest(httptest.NewRequest(http.MethodGet, "/clusters/1v98kgkp03uox9qw/apis/g/v1/rs/n/verb", nil))
	if err != nil || plain.Component != "" {
		t.Fatalf("plain = %+v, %v", plain, err)
	}
	for name, target := range map[string]string{
		"two components":         "/clusters/1v98kgkp03uox9qw/apis/g/v1/rs/n/verb?component=a&component=b",
		"separator in component": "/clusters/1v98kgkp03uox9qw/apis/g/v1/rs/n/verb?component=a%2Fb",
		"dot component":          "/clusters/1v98kgkp03uox9qw/apis/g/v1/rs/n/verb?component=..",
		"empty component":        "/clusters/1v98kgkp03uox9qw/apis/g/v1/rs/n/verb?component=",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSubresourceRequest(httptest.NewRequest(http.MethodGet, target, nil)); !errors.Is(err, ErrBadPath) {
				t.Fatalf("err = %v, want ErrBadPath", err)
			}
		})
	}
	encoded := httptest.NewRequest(http.MethodGet, "/clusters/1v98kgkp03uox9qw/apis/g/v1/rs/n%2Fx/verb", nil)
	if _, err := ParseSubresourceRequest(encoded); !errors.Is(err, ErrBadPath) {
		t.Fatalf("percent-encoded path: err = %v, want ErrBadPath", err)
	}
}

func TestProxiedCallerReadsTheStampedIdentityAndRefusesNone(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/clusters/x/apis/g/v1/rs/n/verb", nil)
	r.Header.Set(HeaderRemoteUser, "alice")
	r.Header.Add(HeaderRemoteGroup, "system:authenticated")
	r.Header.Add(HeaderRemoteGroup, "engineering")
	r.Header.Set(HeaderRemoteExtraPrefix+"Scopes", "cluster:1v98kgkp03uox9qw")

	identity, err := ProxiedCaller(r)
	if err != nil {
		t.Fatalf("ProxiedCaller: %v", err)
	}
	if identity.User != "alice" || len(identity.Groups) != 2 {
		t.Fatalf("identity = %+v", identity)
	}
	if got := identity.Extra["scopes"]; len(got) != 1 || got[0] != "cluster:1v98kgkp03uox9qw" {
		t.Fatalf("extra = %v", identity.Extra)
	}

	// No headers is a refusal, never anonymous and never the provider itself.
	bare := httptest.NewRequest(http.MethodPost, "/clusters/x/apis/g/v1/rs/n/verb", nil)
	if _, err := ProxiedCaller(bare); !errors.Is(err, ErrNoProxiedIdentity) {
		t.Fatalf("err = %v, want ErrNoProxiedIdentity", err)
	}
}

func TestCheckHopsRefusesARoundTrippingRequest(t *testing.T) {
	for name, tc := range map[string]struct {
		header  string
		wantErr bool
	}{
		"absent":       {header: "", wantErr: false},
		"first hop":    {header: "1", wantErr: false},
		"at the bound": {header: "10", wantErr: false},
		"over":         {header: "11", wantErr: true},
		"nonsense":     {header: "many", wantErr: false},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			if tc.header != "" {
				r.Header.Set(HeaderHops, tc.header)
			}
			err := CheckHops(r)
			if tc.wantErr != (err != nil) {
				t.Fatalf("CheckHops(%q) = %v", tc.header, err)
			}
		})
	}
}

// kcp percent-encodes an extra's key into the header name; the identity must
// carry the decoded key, which is what every consumer compares against.
func TestProxiedCallerDecodesExtraKeys(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(HeaderRemoteUser, "system:serviceaccount:default:provider")
	r.Header.Set(HeaderRemoteExtraPrefix+"authentication.kcp.io%2Fcluster-name", "1v98kgkp03uox9qw")
	id, err := ProxiedCaller(r)
	if err != nil {
		t.Fatalf("ProxiedCaller: %v", err)
	}
	if got := id.ClusterName(); got != "1v98kgkp03uox9qw" {
		t.Fatalf("ClusterName = %q; extras = %v", got, id.Extra)
	}
}

func TestIsForeignProvider(t *testing.T) {
	const tenant = "1v98kgkp03uox9qw"
	sa := func(home string) ProxiedIdentity {
		return ProxiedIdentity{User: "system:serviceaccount:default:provider", Extra: map[string][]string{ClusterNameExtra: {home}}}
	}
	if !sa("3l7xqpgvrz4pwkbx").IsForeignProvider(tenant) {
		t.Error("a ServiceAccount from another logical cluster is a foreign provider")
	}
	if sa(tenant).IsForeignProvider(tenant) {
		t.Error("the tenant's own ServiceAccount is not foreign")
	}
	if (ProxiedIdentity{User: "system:serviceaccount:default:provider"}).IsForeignProvider(tenant) {
		t.Error("a ServiceAccount with no cluster-name extra cannot be placed and is not foreign")
	}
	if (ProxiedIdentity{User: "alice", Extra: map[string][]string{ClusterNameExtra: {"elsewhere"}}}).IsForeignProvider(tenant) {
		t.Error("a user is never a foreign provider")
	}
}

func TestRouteTravelsInTheContext(t *testing.T) {
	if _, ok := RouteFrom(context.Background()); ok {
		t.Fatal("an unrouted context reported a route")
	}
	route := SubresourceRequest{Request: Request{ClusterID: "1v98kgkp03uox9qw", Resource: "rs", Name: "n", Verb: "v", Version: "v1"}, Group: "g", APIVersion: "v1"}
	got, ok := RouteFrom(WithRoute(context.Background(), route))
	if !ok || got != route {
		t.Fatalf("RouteFrom = %+v, %v", got, ok)
	}
}
