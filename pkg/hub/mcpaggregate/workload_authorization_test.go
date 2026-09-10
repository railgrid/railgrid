/*
Copyright 2026 The Faros Authors.

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

package mcpaggregate

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

func TestWorkloadMCPAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        authorizationv1.SubjectAccessReviewStatus
		reviewErr     error
		wantErr       bool
		wantForbidden bool
	}{
		{name: "explicit grant", status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}},
		{name: "no grant", wantErr: true, wantForbidden: true},
		{name: "explicit denial wins", status: authorizationv1.SubjectAccessReviewStatus{Allowed: true, Denied: true}, wantErr: true, wantForbidden: true},
		{name: "review outage", reviewErr: errors.New("unavailable"), wantErr: true},
		{name: "evaluation error", status: authorizationv1.SubjectAccessReviewStatus{Allowed: true, EvaluationError: "authorizer unavailable"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := authnv1.UserInfo{Username: "system:serviceaccount:default:project", UID: "project-uid", Groups: []string{"system:serviceaccounts"}, Extra: map[string]authnv1.ExtraValue{"scope": {"verified"}}}
			cs := kubefake.NewClientset()
			cs.PrependReactor("create", "tokenreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
				req := action.(clienttesting.CreateAction).GetObject().(*authnv1.TokenReview)
				if req.Spec.Token != "project-token" {
					t.Fatalf("unexpected token review: %#v", req.Spec)
				}
				return true, &authnv1.TokenReview{Status: authnv1.TokenReviewStatus{Authenticated: true, User: user}}, nil
			})
			checked := false
			cs.PrependReactor("create", "subjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
				checked = true
				req := action.(clienttesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
				want := authorizationv1.SubjectAccessReviewSpec{User: user.Username, UID: user.UID, Groups: user.Groups, Extra: map[string]authorizationv1.ExtraValue{"scope": {"verified"}}, ResourceAttributes: &authorizationv1.ResourceAttributes{Group: "faros.sh", Resource: "mcpservers", Name: "default", Verb: "use"}}
				if !reflect.DeepEqual(req.Spec, want) {
					t.Fatalf("review = %#v, want %#v", req.Spec, want)
				}
				return true, &authorizationv1.SubjectAccessReview{Status: tc.status}, tc.reviewErr
			})
			v := NewVerifier(func(cluster string) *rest.Config {
				if cluster != "tenant-a" {
					t.Fatalf("unexpected tenant %q", cluster)
				}
				return &rest.Config{Host: "https://kcp.test/clusters/tenant-a"}
			}, WithKubeClientFactory(func(cfg *rest.Config) (kubernetes.Interface, error) {
				if cfg.Host != "https://kcp.test/clusters/tenant-a" {
					t.Fatalf("wrong review workspace: %s", cfg.Host)
				}
				return cs, nil
			}), WithClusterPathResolver(func(_ context.Context, cluster string) (string, error) {
				return tenantPathRoot + "org-a:ws-a", nil
			}))
			caller, err := v.Verify(request(t, "project-token"), "project-token", "tenant-a", "default")
			if (err != nil) != tc.wantErr || errors.Is(err, ErrForbidden) != tc.wantForbidden {
				t.Fatalf("Verify() = %v", err)
			}
			if !checked {
				t.Fatal("workload authorization was skipped")
			}
			if !tc.wantErr {
				want := Caller{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ServiceAccount: user.Username}
				if caller != want {
					t.Fatalf("Verify() caller = %+v, want %+v", caller, want)
				}
			}
			// Exercise the real handler gate: a denial or review failure must
			// not even enumerate providers, let alone forward the credential.
			if tc.wantErr {
				h := New(Options{Verifier: v, Providers: func(context.Context, Caller) []ProviderTarget {
					t.Fatal("rejected workload reached federation")
					return nil
				}})
				response := toolsList(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					r.URL.Path = "/tenant-a/apis/faros.sh/v1alpha1/mcpservers/default/mcp"
					h.ServeHTTP(w, r)
				}), "project-token")
				wantStatus := http.StatusServiceUnavailable
				if tc.wantForbidden {
					wantStatus = http.StatusForbidden
				}
				if response.Code != wantStatus {
					t.Fatalf("handler status = %d, want %d", response.Code, wantStatus)
				}
			}
		})
	}
}
