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

package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"k8s.io/klog/v2"
)

// TestServeServiceAccountForwardPath covers which workspace a ServiceAccount
// request is forwarded to.
//
// The clusterName claim says where the ServiceAccount lives. It is the answer
// only for a request that names no workspace of its own; a data-plane verb on
// another provider's object always names one, and prepending the claim to that
// produces /clusters/{sa}/clusters/{tenant}/... which kcp answers with 404.
func TestServeServiceAccountForwardPath(t *testing.T) {
	t.Parallel()

	const (
		saCluster     = "10nnqvmyoi3hr4bq"
		tenantCluster = "2o2jhielc30g19jx"
		verbPath      = "/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/e2e-engage/k8s/api"
	)

	for _, tc := range []struct {
		name        string
		requestPath string
		wantPath    string
		wantStatus  int
	}{{
		// The SA acting in its own workspace names no cluster, so the claim is
		// what resolves it.
		name:        "a path with no cluster is resolved from the claim",
		requestPath: "/api/v1/namespaces",
		wantPath:    "/clusters/" + saCluster + "/api/v1/namespaces",
	}, {
		// An agent kubeconfig whose server URL already carries its own cluster.
		// Forwarding unchanged is what the old prefix strip achieved.
		name:        "its own cluster in the path is not doubled",
		requestPath: "/clusters/" + saCluster + "/api/v1/namespaces",
		wantPath:    "/clusters/" + saCluster + "/api/v1/namespaces",
	}, {
		// The case that 404'd: a verb on an object in a tenant workspace.
		name:        "another workspace named in the path is honoured",
		requestPath: "/clusters/" + tenantCluster + verbPath,
		wantPath:    "/clusters/" + tenantCluster + verbPath,
	}, {
		// The claim is gated against Org workspaces -- one segment under
		// root:railgrid:tenants: -- so an addressed one must be too, or naming it
		// in the path would walk around the check. A child workspace under an Org
		// carries a second segment and stays reachable, which the tenant case
		// above covers by cluster id.
		name:        "an addressed org workspace is refused",
		requestPath: "/clusters/root:railgrid:tenants:acme/api/v1/namespaces",
		wantStatus:  http.StatusForbidden,
	}, {
		name:        "an addressed cluster carrying a traversal is refused",
		requestPath: "/clusters/..%2Fetc/api/v1/namespaces",
		wantStatus:  http.StatusUnauthorized,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var forwarded string
			var authorization string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded = r.URL.Path
				authorization = r.Header.Get("Authorization")
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(upstream.Close)

			target, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatalf("parse upstream: %v", err)
			}

			p := &KCPProxy{
				kcpTarget:            target,
				passthroughTransport: upstream.Client().Transport,
				logger:               klog.Background(),
			}

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "http://hub"+tc.requestPath, nil)
			p.serveServiceAccount(recorder, request, "sa-token", saCluster)

			if tc.wantStatus != 0 {
				if recorder.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
				}
				if forwarded != "" {
					t.Fatalf("a refused request reached the upstream at %q", forwarded)
				}
				return
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", recorder.Code, recorder.Body.String())
			}
			if forwarded != tc.wantPath {
				t.Fatalf("forwarded %q, want %q", forwarded, tc.wantPath)
			}
			// kcp authenticates and authorizes the caller itself, which is what
			// makes honouring an addressed workspace safe, so the token has to
			// arrive as it was sent.
			if authorization != "Bearer sa-token" {
				t.Fatalf("upstream saw Authorization %q, want the SA token unchanged", authorization)
			}
		})
	}
}
