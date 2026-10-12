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

package appauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	k8stesting "k8s.io/client-go/testing"

	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

func TestIdentityUserLookupRequiresProofAndUpdateSARBeforeDirectLookup(t *testing.T) {
	allowed := true
	var reviews []*authorizationv1.SubjectAccessReview
	var appCalls, accountCalls int
	factory := func(string) (authorizationv1client.SubjectAccessReviewInterface, error) {
		client := kubefake.NewClientset()
		client.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			review := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview).DeepCopy()
			reviews = append(reviews, review)
			out := review.DeepCopy()
			out.Status.Allowed = allowed
			return true, out, nil
		})
		return client.AuthorizationV1().SubjectAccessReviews(), nil
	}
	resolvedInstanceUID := "instance-uid"
	handler, err := NewIdentityUserLookupHandler(IdentityUserLookupConfig{
		SARClient: factory,
		IdentityApp: func(_ context.Context, ref InstanceRef) (IdentityApp, error) {
			appCalls++
			if ref.Cluster != "cluster-id" || ref.Group != "infrastructure.railgrid.ai" || ref.Resource != "instances" || ref.Name != "instance-one" {
				t.Fatalf("app descriptor resolved for wrong instance: %+v", ref)
			}
			return IdentityApp{InstanceUID: resolvedInstanceUID, AppID: "project-1/production", Environment: "production", PublishedOrigin: "https://app.example.test", Enabled: true}, nil
		},
		WorkspaceCluster: func(_ context.Context, org, workspace string) (string, error) {
			if org != "org-one" || workspace != "workspace-one" {
				t.Fatalf("workspace scope = %s/%s", org, workspace)
			}
			return "cluster-id", nil
		},
		AccountLookup: func(_ context.Context, user string) (IdentityAccount, error) {
			accountCalls++
			if user != "user-existing" {
				t.Fatalf("account lookup user = %q", user)
			}
			return IdentityAccount{User: user, RBACIdentity: "railgrid:existing@example.test"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	orgRouter := router.PathPrefix("/api/orgs").Subrouter()
	handler.Register(orgRouter)

	request := func(body string, call tenant.DelegatedCall) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/orgs/org-one/workspaces/workspace-one/app-auth/user", strings.NewReader(body))
		ctx := tenant.WithContext(r.Context(), tenant.TenantContext{User: "user-caller", OrgUUID: "org-one", WorkspaceUUID: "workspace-one"})
		ctx = tenant.WithDelegatedCall(ctx, call)
		r = r.WithContext(ctx)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		return response
	}
	body := `{"user":"user-existing","group":"infrastructure.railgrid.ai","resource":"instances","name":"instance-one","appId":"project-1/production"}`
	baseCall := tenant.DelegatedCall{
		User: "user-caller", Provider: "app-studio", Capability: "app-auth.users.resolve", Scope: "workspace",
		ActionProof: true, RBACIdentity: "railgrid:caller@example.test", Groups: []string{"verified-group"},
		ActionUser: "user-existing", ActionAppID: "project-1/production",
		AppAccessOperation: "create", AppAccessGroup: "infrastructure.railgrid.ai", AppAccessResource: "instances",
		AppAccessName: "instance-one", AppAccessUID: "instance-uid", AppAccessProject: "project-one", AppAccessProjectUID: "project-1",
		AppAccessUser: "user-existing",
		Extra:         map[string][]string{"example.test/claim": {"verified-value"}},
	}

	response := request(body, baseCall)
	if response.Code != http.StatusOK {
		t.Fatalf("valid lookup status=%d body=%s", response.Code, response.Body.String())
	}
	var result identityUserLookupResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.User != "user-existing" || result.RBACIdentity != "railgrid:existing@example.test" {
		t.Fatalf("lookup response=%+v err=%v", result, err)
	}
	if len(reviews) != 1 || appCalls != 1 || accountCalls != 1 {
		t.Fatalf("reviews=%d appReads=%d accountReads=%d", len(reviews), appCalls, accountCalls)
	}
	resolvedInstanceUID = "replacement-instance-uid"
	beforeAccounts := accountCalls
	replaced := request(body, baseCall)
	if replaced.Code != http.StatusForbidden || accountCalls != beforeAccounts {
		t.Fatalf("replaced target status=%d accountReads=%d, want forbidden without User lookup", replaced.Code, accountCalls-beforeAccounts)
	}
	resolvedInstanceUID = "instance-uid"
	sar := reviews[0]
	attrs := sar.Spec.ResourceAttributes
	if sar.Spec.User != "railgrid:caller@example.test" || len(sar.Spec.Groups) != 1 || sar.Spec.Groups[0] != "verified-group" ||
		sar.Spec.Extra["example.test/claim"][0] != "verified-value" || attrs.Group != "infrastructure.railgrid.ai" ||
		attrs.Resource != "instances" || attrs.Name != "instance-one" || attrs.Verb != "update" || attrs.Subresource != "" {
		t.Fatalf("update SAR = %+v", sar.Spec)
	}

	for _, tc := range []struct {
		name string
		call tenant.DelegatedCall
		body string
	}{
		{name: "missing action proof", call: func() tenant.DelegatedCall { c := baseCall; c.ActionProof = false; return c }(), body: body},
		{name: "wrong capability", call: func() tenant.DelegatedCall { c := baseCall; c.Capability = "memberships.read"; return c }(), body: body},
		{name: "wrong provider", call: func() tenant.DelegatedCall { c := baseCall; c.Provider = "infrastructure"; return c }(), body: body},
		{name: "wrong account", call: baseCall, body: strings.Replace(body, "user-existing", "user-other", 1)},
		{name: "wrong app audience", call: baseCall, body: strings.Replace(body, "project-1/production", "project-2/production", 1)},
		{name: "wrong target name", call: baseCall, body: strings.Replace(body, "instance-one", "other-instance", 1)},
		{name: "wrong signed target UID", call: func() tenant.DelegatedCall { c := baseCall; c.AppAccessUID = "replacement-instance-uid"; return c }(), body: body},
		{name: "missing target proof", call: func() tenant.DelegatedCall { c := baseCall; c.AppAccessUID = ""; return c }(), body: body},
		{name: "other resource", call: baseCall, body: strings.Replace(body, "\"instances\"", "\"projects\"", 1)},
		{name: "unknown JSON field", call: baseCall, body: strings.TrimSuffix(body, "}") + `,"extra":true}`},
		{name: "trailing JSON", call: baseCall, body: body + `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := accountCalls
			rec := request(tc.body, tc.call)
			if rec.Code < 400 {
				t.Fatalf("status=%d body=%s, want refusal", rec.Code, rec.Body.String())
			}
			if accountCalls != before {
				t.Fatal("refused request reached direct User lookup")
			}
		})
	}

	allowed = false
	beforeAccounts, beforeApps := accountCalls, appCalls
	denied := request(body, baseCall)
	if denied.Code != http.StatusForbidden || accountCalls != beforeAccounts || appCalls != beforeApps {
		t.Fatalf("SAR-denied lookup status=%d accountReads=%d appReads=%d", denied.Code, accountCalls-beforeAccounts, appCalls-beforeApps)
	}
}

func TestIdentityUserLookupIsProductionOnlyAndNeverListsOrCreatesAccounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		app  IdentityApp
		user IdentityAccount
		err  error
		want int
	}{
		{name: "development descriptor", app: IdentityApp{InstanceUID: "uid", AppID: "project-1/development", Environment: "development", PublishedOrigin: "https://app.example.test", Enabled: true}, user: IdentityAccount{User: "user-existing", RBACIdentity: "railgrid:user"}, want: http.StatusForbidden},
		{name: "disabled descriptor", app: IdentityApp{InstanceUID: "uid", AppID: "project-1/production", Environment: "production", PublishedOrigin: "https://app.example.test", Enabled: false}, user: IdentityAccount{User: "user-existing", RBACIdentity: "railgrid:user"}, want: http.StatusForbidden},
		{name: "soft deleted existing account", app: IdentityApp{InstanceUID: "uid", AppID: "project-1/production", Environment: "production", PublishedOrigin: "https://app.example.test", Enabled: true}, user: IdentityAccount{User: "user-existing", Deleted: true, RBACIdentity: "railgrid:user"}, want: http.StatusNotFound},
		{name: "missing existing account", app: IdentityApp{InstanceUID: "uid", AppID: "project-1/production", Environment: "production", PublishedOrigin: "https://app.example.test", Enabled: true}, err: apierrors.NewNotFound(schema.GroupResource{Resource: "users"}, "user-existing"), want: http.StatusNotFound},
		{name: "transient account lookup error", app: IdentityApp{InstanceUID: "uid", AppID: "project-1/production", Environment: "production", PublishedOrigin: "https://app.example.test", Enabled: true}, err: errors.New("storage unavailable"), want: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accountCalls := 0
			handler, err := NewIdentityUserLookupHandler(IdentityUserLookupConfig{
				SARClient: func(string) (authorizationv1client.SubjectAccessReviewInterface, error) {
					client := kubefake.NewClientset()
					client.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
						out := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview).DeepCopy()
						out.Status.Allowed = true
						return true, out, nil
					})
					return client.AuthorizationV1().SubjectAccessReviews(), nil
				},
				IdentityApp:      func(context.Context, InstanceRef) (IdentityApp, error) { return tc.app, nil },
				WorkspaceCluster: func(context.Context, string, string) (string, error) { return "cluster-id", nil },
				AccountLookup: func(context.Context, string) (IdentityAccount, error) {
					accountCalls++
					return tc.user, tc.err
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			router := mux.NewRouter()
			orgRouter := router.PathPrefix("/api/orgs").Subrouter()
			handler.Register(orgRouter)
			request := httptest.NewRequest(http.MethodPost, "/api/orgs/org-one/workspaces/workspace-one/app-auth/user", strings.NewReader(`{"user":"user-existing","group":"infrastructure.railgrid.ai","resource":"instances","name":"instance-one","appId":"project-1/production"}`))
			request = request.WithContext(tenant.WithContext(request.Context(), tenant.TenantContext{User: "user-caller", OrgUUID: "org-one", WorkspaceUUID: "workspace-one"}))
			request = request.WithContext(tenant.WithDelegatedCall(request.Context(), tenant.DelegatedCall{
				User: "user-caller", Provider: "app-studio", Capability: "app-auth.users.resolve", Scope: "workspace",
				ActionProof: true, RBACIdentity: "railgrid:caller", ActionUser: "user-existing", ActionAppID: "project-1/production",
				AppAccessOperation: "create", AppAccessGroup: "infrastructure.railgrid.ai", AppAccessResource: "instances",
				AppAccessName: "instance-one", AppAccessUID: "uid", AppAccessProject: "project-one", AppAccessProjectUID: "project-1",
				AppAccessUser: "user-existing",
			}))
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("status=%d body=%s, want %d", recorder.Code, recorder.Body.String(), tc.want)
			}
			if tc.app.Environment != "production" || !tc.app.Enabled {
				if accountCalls != 0 {
					t.Fatalf("account lookup called for disabled or development app %d times", accountCalls)
				}
			}
		})
	}
}
