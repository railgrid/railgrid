// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/providerrelay"
)

type testRelayVerifier struct{ key ed25519.PublicKey }

func (v testRelayVerifier) Verify(_ context.Context, token, cluster, service, method, path, query string) (providerrelay.Claims, error) {
	return providerrelay.Verify(v.key, token, cluster, service, method, path, query, time.Now())
}

func TestProviderRelayRestoresVerifiedConsumerAndConsumesProof(t *testing.T) {
	path := "/clusters/consumer/apis/planner.example/v1/boards/demo/run"
	key, _ := providerrelay.SigningKey(bytes.Repeat([]byte{1}, 32))
	claims := providerrelay.Claims{Cluster: "tenant-a", Service: "mac-service", Method: "POST", Path: path, Identity: dataplane.ProxiedIdentity{User: "alice", Groups: []string{"team"}, Extra: map[string][]string{dataplane.ClusterNameExtra: {"consumer"}}}, Hops: 2}
	proof, err := providerrelay.Sign(key, claims, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "wrong-target"}[invalid], func(t *testing.T) {
			srv, dialer := newServiceProxyTestServer(t, macOSServerKind, "edge")
			srv.SetProviderRelayVerifier(testRelayVerifier{key.Public().(ed25519.PublicKey)})
			r := httptest.NewRequest(http.MethodPost, "http://edges.invalid/proxy"+path, nil)
			r.Header.Set(providerrelay.Header, proof)
			r.Header.Set(dataplane.HeaderRemoteUser, "kcp-admin")
			r.Header.Set(dataplane.HeaderRemoteGroup, "system:masters")
			r.Header.Set("Authorization", "hub-admin-secret")
			// The serve adapter already removes Authorization on a real request.
			r.Header.Del("Authorization")
			if invalid {
				r.URL.RawQuery = "changed=1"
			}
			w := httptest.NewRecorder()
			srv.serveService(w, r, svcRequest(VerbProxy, path[1:]), svcObject(macOSServerKind, "edge"))
			if invalid {
				if w.Code != http.StatusForbidden || dialer.dialed {
					t.Fatalf("bad proof status %d dialed %v", w.Code, dialer.dialed)
				}
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("status %d %s", w.Code, w.Body.String())
			}
			out := <-dialer.request
			if out.Header.Get(providerrelay.Header) != "" || out.Header.Get("Authorization") != "" {
				t.Fatal("proof or credential reached tenant")
			}
			identity, err := dataplane.ProxiedCaller(out)
			if err != nil {
				t.Fatal(err)
			}
			if identity.User != "alice" || len(identity.Groups) != 1 || identity.Groups[0] != "team" || identity.ClusterName() != "consumer" {
				t.Fatalf("wrong downstream identity: %#v", identity)
			}
			if out.URL.Path != "/svc"+path {
				t.Fatalf("path %s", out.URL.Path)
			}
		})
	}
}
