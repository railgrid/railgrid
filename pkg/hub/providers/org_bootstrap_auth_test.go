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

package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-sdk/dataplane"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

func TestOrgBootstrapAuthenticatorBindsRegisteredWorkspaceAndLiveIdentity(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong service account", "wrong verified cluster", "wrong provider path", "platform workspace", "wrong registered cluster", "unmarked workspace", "unmarked service account", "replaced service account", "kcp unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			path := kcppaths.OrgProviderPath("org-a", "planner")
			lc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster", "metadata": map[string]any{"name": "cluster", "annotations": map[string]any{"kcp.io/cluster": "provider-cluster", "kcp.io/path": path}}}}
			ws := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace", "metadata": map[string]any{"name": "planner", "annotations": map[string]any{OrgProviderBootstrapPolicyAnnotation: OrgProviderBootstrapPolicy}}, "spec": map[string]any{"cluster": "provider-cluster"}}}
			sa := &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: metav1.ObjectMeta{Name: ProviderSAName, Namespace: ProviderSANamespace, UID: "provider-uid", Annotations: map[string]string{OrgProviderBootstrapPolicyAnnotation: OrgProviderBootstrapPolicy}}}
			review := &authnv1.TokenReview{TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}, Status: authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: ProviderSAUsername, UID: "provider-uid", Extra: map[string]authnv1.ExtraValue{dataplane.ClusterNameExtra: {"provider-cluster"}}}}}
			switch scenario {
			case "wrong service account":
				review.Status.User.Username = "system:serviceaccount:default:other"
			case "wrong verified cluster":
				review.Status.User.Extra[dataplane.ClusterNameExtra] = authnv1.ExtraValue{"other-cluster"}
			case "wrong provider path":
				lc.SetAnnotations(map[string]string{"kcp.io/cluster": "provider-cluster", "kcp.io/path": kcppaths.OrgProviderPath("org-a", "other")})
			case "platform workspace":
				lc.SetAnnotations(map[string]string{"kcp.io/cluster": "provider-cluster", "kcp.io/path": kcppaths.ProviderPath("planner")})
			case "wrong registered cluster":
				_ = unstructured.SetNestedField(ws.Object, "other-cluster", "spec", "cluster")
			case "unmarked workspace":
				ws.SetAnnotations(nil)
			case "unmarked service account":
				sa.Annotations = nil
			case "replaced service account":
				sa.UID = "new-uid"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "kcp unavailable" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				var object any
				switch r.URL.Path {
				case "/clusters/provider-cluster/apis/authentication.k8s.io/v1/tokenreviews":
					var input authnv1.TokenReview
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					if input.Spec.Token != orgHeartbeatToken("provider-cluster") {
						t.Error("wrong reviewed token")
					}
					object = review
				case "/clusters/provider-cluster/apis/core.kcp.io/v1alpha1/logicalclusters/cluster":
					object = lc
				case "/clusters/root:railgrid:tenants:org-a:providers/apis/tenancy.kcp.io/v1alpha1/workspaces/planner":
					object = ws
				case "/clusters/provider-cluster/api/v1/namespaces/default/serviceaccounts/provider":
					object = sa
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(object); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			authenticate, err := NewOrgProviderBootstrapAuthenticator(&rest.Config{Host: srv.URL + "/clusters/root"})
			if err != nil {
				t.Fatal(err)
			}
			req := heartbeatRequestWithBearer("planner", orgHeartbeatToken("provider-cluster"))
			identity, err := authenticate(context.Background(), req, "planner")
			if scenario == "valid" {
				if err != nil || identity.OrgUUID != "org-a" || identity.Cluster != "provider-cluster" || identity.WorkspacePath != path {
					t.Fatalf("identity=%+v err=%v", identity, err)
				}
				// A successful bootstrap does not cache away a later enrollment removal.
				ws.SetAnnotations(nil)
				if _, err := authenticate(context.Background(), req, "planner"); !errors.Is(err, ErrOrgProviderBootstrapUnauthorized) {
					t.Fatalf("revoked enrollment error=%v", err)
				}
			} else {
				want := ErrOrgProviderBootstrapUnauthorized
				if scenario == "kcp unavailable" {
					want = ErrOrgProviderBootstrapUnavailable
				}
				if !errors.Is(err, want) {
					t.Fatalf("error=%v, want %v", err, want)
				}
			}
		})
	}
}

func TestOrgBootstrapRejectsUnsafeClusterHintsBeforeCallingKCP(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "unexpected", 500) }))
	defer srv.Close()
	authenticate, err := NewOrgProviderBootstrapAuthenticator(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, hint := range []string{"", "root:railgrid:providers:planner", "../other", "a/b", "a?b", strings.Repeat("a", 64)} {
		if _, err := authenticate(context.Background(), heartbeatRequestWithBearer("planner", orgHeartbeatToken(hint)), "planner"); !errors.Is(err, ErrOrgProviderBootstrapUnauthorized) {
			t.Errorf("hint %q: %v", hint, err)
		}
	}
	if calls != 0 {
		t.Fatalf("made %d requests for invalid hints", calls)
	}
}
