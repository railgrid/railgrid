// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/railgrid/provider-sdk/dataplane"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/browsersession"
	"github.com/railgrid/railgrid/pkg/hub/actionproof"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

func TestActionProofInviteIntentPreservesOriginalRequest(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		invalid          bool
	}{
		{name: "invite", body: `{"user":" guest@example.test ","invite":true}`, want: "guest@example.test"},
		{name: "existing member", body: `{"user":"member","invite":false}`},
		{name: "default member", body: `{"user":"member"}`},
		{name: "existing member with invite flag", body: `{"user":"member","invite":true}`},
		{name: "invalid", body: `{"user":`, invalid: true},
		{name: "oversized", body: strings.Repeat(" ", (64<<10)+1), invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			got, err := actionProofInviteUser(r)
			if (err != nil) != tc.invalid || got != tc.want {
				t.Fatalf("invite recipient = %q, error %v", got, err)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != tc.body {
				t.Fatalf("provider request body changed: %v", err)
			}
		})
	}
}

func TestProviderActionProofBridgeRechecksMembershipAndRBAC(t *testing.T) {
	bindingGVR := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{bindingGVR: "APIBindingList"},
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster", "metadata": map[string]any{"name": "cluster", "annotations": map[string]any{"kcp.io/path": "root:railgrid:tenants:org:ws"}}}},
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": "app-studio"}, "spec": map[string]any{"reference": map[string]any{"export": map[string]any{"name": "ai.railgrid.ai", "path": "root:railgrid:providers:app-studio"}}}, "status": map[string]any{"phase": "Bound"}}},
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project", "metadata": map[string]any{"name": "demo"}}},
	)
	allowed, member := true, true
	dyn.PrependReactor("create", "subjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		obj := a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		user, _, _ := unstructured.NestedString(obj.Object, "spec", "user")
		groups, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "groups")
		if user != "railgrid:alice" || len(groups) != 1 || groups[0] != "verified-group" {
			t.Fatalf("review used unverified caller: %#v", obj.Object)
		}
		return true, &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"allowed": allowed}}}, nil
	})
	registry := providers.NewRegistry()
	registry.Upsert(providers.Provider{Name: "app-studio", APIExportName: "ai.railgrid.ai", APIExportPath: "root:railgrid:providers:app-studio", Export: &providersv1alpha1.ProviderExport{
		Name: "ai.railgrid.ai", Resources: []providersv1alpha1.ProviderExportResource{{Name: "projects", APIVersion: "ai.railgrid.ai/v1alpha1", Kind: "Project", Verbs: []providersv1alpha1.ProviderVerb{{Name: "publishing-members"}}}},
	}})
	b := &providerActionProofBridge{
		keys: serviceaccounts.StaticProofKeySource([]byte("01234567890123456789012345678901")), registry: registry,
		identify: func(*http.Request) (browsersession.Identity, error) {
			return browsersession.Identity{UserID: "alice", RBACIdentity: "railgrid:alice"}, nil
		},
		memberships: tenant.MembershipLookupFunc(func(context.Context, string) (*tenancyv1alpha1.UserMembershipIndex, error) {
			if !member {
				return nil, errors.New("membership revoked")
			}
			return &tenancyv1alpha1.UserMembershipIndex{Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{{OrgUUID: "org", Role: "admin"}}}}, nil
		}),
		clients: func(cluster string) (dynamic.Interface, error) {
			if cluster != "tenant1" {
				t.Fatalf("wrong cluster %s", cluster)
			}
			return dyn, nil
		},
		caller: func(context.Context, *http.Request, string) (authnv1.UserInfo, error) {
			return authnv1.UserInfo{Username: "railgrid:alice", Groups: []string{"verified-group"}}, nil
		},
		authenticate: func(_ context.Context, r *http.Request, _ actionproof.Claims) error {
			if r.Header.Get("Authorization") != "Bearer provider-token" {
				return errors.New("wrong provider")
			}
			return nil
		},
	}
	var proof string
	h := b.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proof = r.Header.Get(actionproof.Header)
		if proof == "" || proof == "forged" {
			t.Fatal("missing hub-issued proof")
		}
		if r.Header.Get("Authorization") != "Bearer human-token" {
			t.Fatal("native kcp authentication changed")
		}
		w.WriteHeader(204)
	}))
	r := httptest.NewRequest(http.MethodGet, "/clusters/tenant1/apis/ai.railgrid.ai/v1alpha1/projects/demo/publishing-members", nil)
	r.Header.Set("Authorization", "Bearer human-token")
	r.Header.Set(actionproof.Header, "forged")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("issue failed: %d %s", w.Code, w.Body.String())
	}
	redeem := httptest.NewRequest(http.MethodGet, "/api/orgs/org/memberships", nil)
	redeem.Header.Set("Authorization", "Bearer provider-token")
	redeem.Header.Set(actionproof.Header, proof)
	redeem.Header.Set("X-Railgrid-Org", "org")
	redeem.Header.Set("X-Railgrid-Workspace", "ws")
	redeem.Header.Set("X-Railgrid-Cluster", "tenant1")
	redeem.Header.Set("X-Railgrid-Tenant", "tenant1")
	claims, err := b.verifier().Verify(context.Background(), redeem)
	if err != nil || claims.RBACIdentity != "railgrid:alice" {
		t.Fatalf("valid redemption: %#v %v", claims, err)
	}
	member = false
	if _, err = b.verifier().Verify(context.Background(), redeem); err == nil {
		t.Fatal("revoked membership accepted")
	}
	member = true
	allowed = false
	if _, err = b.verifier().Verify(context.Background(), redeem); err == nil {
		t.Fatal("revoked parent/verb RBAC accepted")
	}
	allowed = true
	redeem.Header.Set("Authorization", "Bearer unrelated-provider")
	if _, err = b.verifier().Verify(context.Background(), redeem); err == nil {
		t.Fatal("proof alone authenticated another provider")
	}
}

func TestProviderActionProofBridgeStripsUntrustedProofs(t *testing.T) {
	b := &providerActionProofBridge{identify: func(*http.Request) (browsersession.Identity, error) {
		return browsersession.Identity{}, errors.New("not a human")
	}}
	for _, path := range []string{"/clusters/tenant1/apis/ai.railgrid.ai/v1alpha1/projects/demo/publishing-members", "/clusters/tenant1/apis/other/v1/widgets/foo/bar", "/api/v1"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set(actionproof.Header, "forged")
		w := httptest.NewRecorder()
		b.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(actionproof.Header) != "" {
				t.Fatal("client proof was forwarded")
			}
			w.WriteHeader(204)
		})).ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatalf("native path changed: %d", w.Code)
		}
	}
}

func TestActionProofAppAuthGrantBindsProjectUIDAudienceFromBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost,
		"/clusters/cluster-id/apis/ai.railgrid.ai/v1alpha1/projects/project-name/authentication-grants",
		strings.NewReader(`{"user":"existing-user","appId":"project-uid/production"}`))
	user, appID, err := actionProofAppAuthGrant(request)
	if err != nil {
		t.Fatalf("authentication-grant action with stable Project UID audience failed: %v", err)
	}
	if user != "existing-user" || appID != "project-uid/production" {
		t.Fatalf("parsed intent = %q/%q", user, appID)
	}
}

func TestActionProofAppAuthGrantRejectsMalformedBody(t *testing.T) {
	for _, body := range []string{
		`{"user":"existing-user","appId":"project-uid/production"}{}`,
		`{"user":"existing-user","appId":"project-uid/production","extra":true}`,
		`{"user":"existing-user","appId":" "}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/unused", strings.NewReader(body))
		if _, _, err := actionProofAppAuthGrant(request); err == nil {
			t.Errorf("accepted malformed authentication-grant body %q", body)
		}
	}
}

func TestBindAppAccessTargetAllowsOnlySafeMissingRuntimeProofs(t *testing.T) {
	projectGVR := schema.GroupVersionResource{Group: "ai.railgrid.ai", Version: "v1alpha1", Resource: "projects"}
	instanceGVR := schema.GroupVersionResource{Group: "infrastructure.railgrid.ai", Version: "v1alpha1", Resource: "instances"}
	projectWithBinding := func(environment, binding string) *unstructured.Unstructured {
		resourceBinding := map[string]any{
			"name": binding, "provider": "app-studio", "kind": "providerResource",
			"resourceRef": map[string]any{
				"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Instance",
				"resource": "instances", "name": "demo-" + binding,
			},
		}
		env := map[string]any{"name": environment, "bindings": []any{resourceBinding}}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project",
			"metadata": map[string]any{"name": "demo", "uid": "project-uid"},
			"spec":     map[string]any{"environments": []any{env}},
		}}
	}
	instance := func(name, ownerName, ownerUID string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Instance",
			"metadata": map[string]any{
				"name": name, "uid": "instance-uid",
				"labels": map[string]any{"app-studio.railgrid.ai/project": "demo"},
				"ownerReferences": []any{map[string]any{
					"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project", "name": ownerName, "uid": ownerUID,
				}},
			},
		}}
	}
	makeRoute := func(verb string) dataplane.SubresourceRequest {
		return dataplane.SubresourceRequest{
			Request: dataplane.Request{ClusterID: "tenant1", Resource: "projects", Name: "demo", Verb: verb},
			Group:   "ai.railgrid.ai", APIVersion: "v1alpha1",
		}
	}
	newClaims := func(operation, method string) actionproof.Claims {
		return actionproof.Claims{
			Method:             method,
			AppAccessOperation: operation, AppAccessGroup: "infrastructure.railgrid.ai", AppAccessResource: "instances",
			AppAccessName: "caller-chosen", AppAccessUID: "caller-uid", AppAccessProject: "demo",
			AppAccessProjectUID: "caller-project-uid", AppAccessUser: "user-bob", AppAccessGrant: "grant-bob",
		}
	}
	newClient := func(objects ...runtime.Object) dynamic.Interface {
		return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			projectGVR: "ProjectList", instanceGVR: "InstanceList",
		}, objects...)
	}
	duplicateEnvironmentProject := projectWithBinding("production", "prod")
	duplicateEnvironmentSpec := duplicateEnvironmentProject.Object["spec"].(map[string]any)
	duplicateEnvironmentSpec["environments"] = append([]any{map[string]any{"name": "production"}}, duplicateEnvironmentSpec["environments"].([]any)...)
	malformedEnvironmentProject := projectWithBinding("production", "prod")
	malformedEnvironment := malformedEnvironmentProject.Object["spec"].(map[string]any)["environments"].([]any)[0].(map[string]any)
	malformedEnvironment["name"] = " production "

	for _, tc := range []struct {
		name         string
		verb         string
		operation    string
		sourceMethod string
		wantName     string
		project      *unstructured.Unstructured
		instance     *unstructured.Unstructured
		apiFailure   bool
		missingOwner bool
		wantCleared  bool
		wantError    bool
	}{
		{name: "live production binding", verb: "publishing", operation: "list", sourceMethod: http.MethodGet, wantName: "demo-prod", project: projectWithBinding("production", "prod"), instance: instance("demo-prod", "demo", "project-uid")},
		{name: "live development binding", verb: "preview", operation: "list", sourceMethod: http.MethodGet, wantName: "demo-dev", project: projectWithBinding("development", "dev"), instance: instance("demo-dev", "demo", "project-uid")},
		{name: "new project without runtime", verb: "publishing", operation: "list", sourceMethod: http.MethodGet, project: &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project", "metadata": map[string]any{"name": "demo", "uid": "project-uid"},
		}}, wantCleared: true},
		{name: "missing production instance reset", verb: "publishing", operation: "reset", sourceMethod: http.MethodDelete, project: projectWithBinding("production", "prod"), wantCleared: true},
		{name: "missing development instance read", verb: "preview", operation: "list", sourceMethod: http.MethodGet, project: projectWithBinding("development", "dev"), wantCleared: true},
		{name: "preview policy POST with missing runtime reaches provider without broker target", verb: "preview", operation: "list", sourceMethod: http.MethodPost, project: projectWithBinding("development", "dev"), wantCleared: true},
		{name: "publishing policy POST with missing runtime reaches provider without broker target", verb: "publishing", operation: "list", sourceMethod: http.MethodPost, project: projectWithBinding("production", "prod"), wantCleared: true},
		{name: "duplicate target environments fail closed", verb: "publishing", operation: "list", sourceMethod: http.MethodPost, project: duplicateEnvironmentProject, wantError: true},
		{name: "malformed environment name fails closed", verb: "publishing", operation: "list", sourceMethod: http.MethodPost, project: malformedEnvironmentProject, wantError: true},
		{name: "foreign owner reference cannot back a write", verb: "publishing", operation: "list", sourceMethod: http.MethodPost, project: projectWithBinding("production", "prod"), instance: instance("demo-prod", "other", "other-project-uid"), wantError: true},
		{name: "missing owner reference cannot back a write", verb: "preview", operation: "list", sourceMethod: http.MethodPost, project: projectWithBinding("development", "dev"), instance: instance("demo-dev", "demo", "project-uid"), missingOwner: true, wantError: true},
		{name: "owner reference without matching project uid cannot back a write", verb: "publishing", operation: "list", sourceMethod: http.MethodPost, project: projectWithBinding("production", "prod"), instance: instance("demo-prod", "demo", "old-project-uid"), wantError: true},
		{name: "instance API failure fails closed on write", verb: "publishing", operation: "list", sourceMethod: http.MethodPost, project: projectWithBinding("production", "prod"), apiFailure: true, wantError: true},
		{name: "grant mutation cannot omit missing target", verb: "authentication-grants", operation: "create", sourceMethod: http.MethodPost, project: projectWithBinding("production", "prod"), wantError: true},
		{name: "grant mutation rejects forged owner reference", verb: "authentication-grants", operation: "create", sourceMethod: http.MethodPost, project: projectWithBinding("production", "prod"), instance: instance("demo-prod", "other", "other-project-uid"), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := []runtime.Object{tc.project}
			if tc.instance != nil {
				if tc.missingOwner {
					unstructured.RemoveNestedField(tc.instance.Object, "metadata", "ownerReferences")
				}
				objects = append(objects, tc.instance)
			}
			client := newClient(objects...)
			if tc.apiFailure {
				client.(*fake.FakeDynamicClient).PrependReactor("get", "instances", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("temporary API failure")
				})
			}
			claims := newClaims(tc.operation, tc.sourceMethod)
			err := bindAppAccessTarget(context.Background(), client, makeRoute(tc.verb), &claims)
			if (err != nil) != tc.wantError {
				t.Fatalf("bind error=%v, wantError=%v", err, tc.wantError)
			}
			if tc.wantCleared {
				if err != nil || claims.AppAccessOperation != "" || claims.AppAccessGroup != "" || claims.AppAccessResource != "" ||
					claims.AppAccessName != "" || claims.AppAccessUID != "" || claims.AppAccessProject != "" ||
					claims.AppAccessProjectUID != "" || claims.AppAccessUser != "" || claims.AppAccessGrant != "" {
					t.Fatalf("missing runtime did not clear the entire broker target: claims=%+v err=%v", claims, err)
				}
				return
			}
			if tc.wantError {
				return
			}
			if claims.AppAccessName != tc.wantName || claims.AppAccessUID != "instance-uid" || claims.AppAccessProjectUID != "project-uid" {
				t.Fatalf("did not bind persisted production Instance identity: %+v", claims)
			}
		})
	}

	// A not-found response for a resource endpoint is not proof that the
	// Project's named runtime is absent. It has no object identity details and
	// must not be downgraded to a no-broker proof.
	malformedAPIClient := newClient(projectWithBinding("production", "prod"))
	malformedAPIClient.(*fake.FakeDynamicClient).PrependReactor("get", "instances", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "infrastructure.railgrid.ai", Resource: "instances"}, "")
	})
	claims := newClaims("list", http.MethodGet)
	claims.Method = http.MethodPost
	if err := bindAppAccessTarget(context.Background(), malformedAPIClient, makeRoute("publishing"), &claims); err == nil {
		t.Fatal("resource-level not found was treated as an absent runtime")
	}
}
