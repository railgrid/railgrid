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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

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
