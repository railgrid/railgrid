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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

func TestAddonCredentialsBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body, edge, owner string
		gateDenied              bool
		want, reads, writes     int
	}{
		{name: "read configured auth", body: `{"addon":"runner","authSecretRef":{"name":"auth"}}`, want: 200, reads: 1},
		{name: "another edge", body: `{"addon":"runner","authSecretRef":{"name":"auth"}}`, edge: "other", want: 403},
		{name: "arbitrary secret", body: `{"addon":"runner","authSecretRef":{"name":"unrelated"}}`, want: 409},
		{name: "unlabelled auth", body: `{"addon":"runner","authSecretRef":{"name":"auth"}}`, owner: "other", want: 403, reads: 1},
		{name: "publish own token", body: `{"addon":"runner","uid":"uid1","token":"token"}`, want: 204, reads: 1, writes: 1},
		{name: "stale incarnation", body: `{"addon":"runner","uid":"old","token":"token"}`, want: 409},
		{name: "gate denied", body: `{"addon":"runner","uid":"uid1","token":"token"}`, gateDenied: true, want: 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, writes := 0, 0
			edge := tc.edge
			if edge == "" {
				edge = "mac"
			}
			owner := tc.owner
			if owner == "" {
				owner = "edges"
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/addons/runner") {
					_, _ = w.Write([]byte(`{"apiVersion":"edges.railgrid.ai/v1alpha1","kind":"Addon","metadata":{"name":"runner","uid":"uid1"},"spec":{"type":"runner","edgeRef":{"kind":"MacOSServer","name":"` + edge + `"},"runner":{"codex":{"authSecretRef":{"name":"auth"}}}}}`))
					return
				}
				if r.Method == "GET" && strings.Contains(r.URL.Path, "/secrets/") {
					reads++
					if strings.HasSuffix(r.URL.Path, "/runner-runner-token") {
						w.WriteHeader(404)
						_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","reason":"NotFound","code":404}`))
						return
					}
					writeJSON(t, w, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default", Labels: map[string]string{"railgrid.ai/owner": owner}}, Data: map[string][]byte{"auth.json": []byte("session"), "unrelated": []byte("do not return")}})
					return
				}
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/secrets") {
					writes++
					var s corev1.Secret
					if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
						t.Error(err)
					}
					if s.Name != "runner-runner-token" || string(s.Data["token"]) != "token" || len(s.OwnerReferences) != 1 || s.OwnerReferences[0].UID != "uid1" || s.Labels["railgrid.ai/owner"] != "edges" {
						t.Errorf("incorrect token publication metadata")
					}
					writeJSON(t, w, &s)
					return
				}
				t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(500)
			}))
			defer api.Close()
			s := testServer("/services/providers/edges/dataplane")
			s.logger = klog.Background()
			s.kcpConfig = &rest.Config{Host: api.URL}
			s.tenantConfig = func(context.Context, string) (*rest.Config, error) { return &rest.Config{Host: api.URL}, nil }
			s.gateFn = func(context.Context, *Server, string, dataplane.Request) (*unstructured.Unstructured, error) {
				if tc.gateDenied {
					return nil, dataplane.ErrDenied
				}
				return &unstructured.Unstructured{}, nil
			}
			req := httptest.NewRequest("POST", "/dataplane/clusters/tenant/macosservers/mac/addon-credentials", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer edge-token")
			rr := httptest.NewRecorder()
			s.buildEdgesProxyHandler().ServeHTTP(rr, req)
			if rr.Code != tc.want || reads != tc.reads || writes != tc.writes {
				t.Fatalf("status=%d reads=%d writes=%d; want %d %d %d; body=%s", rr.Code, reads, writes, tc.want, tc.reads, tc.writes, rr.Body.String())
			}
			if tc.want == 200 && strings.Contains(rr.Body.String(), "unrelated") {
				t.Error("returned unrelated Secret key")
			}
		})
	}
}
