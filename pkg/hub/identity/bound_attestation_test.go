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
package identity

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"

	"github.com/railgrid/railgrid/pkg/hub/providers"
)

func TestBoundProviderAttestationUsesActualCopy(t *testing.T) {
	const platformPath = "root:railgrid:providers:edges"
	const orgPath = "root:railgrid:tenants:org:providers:edges"
	for _, tc := range []struct {
		name, exportPath, boundCluster, credentialCluster, tenantPath, condition string
		deleting, notReady, unregistered, want                                   bool
	}{
		{name: "org copy", exportPath: orgPath, boundCluster: "org-provider", credentialCluster: "org-provider", tenantPath: "root:railgrid:tenants:org:workspace", want: true},
		{name: "platform copy despite org shadow", exportPath: platformPath, boundCluster: "platform-provider", credentialCluster: "platform-provider", want: true},
		{name: "org token cannot mint for platform target", exportPath: platformPath, boundCluster: "platform-provider", credentialCluster: "org-provider"},
		{name: "platform token cannot mint for org target", exportPath: orgPath, boundCluster: "org-provider", credentialCluster: "platform-provider", tenantPath: "root:railgrid:tenants:org:workspace"},
		{name: "forged resolved cluster", exportPath: orgPath, boundCluster: "platform-provider", credentialCluster: "org-provider", tenantPath: "root:railgrid:tenants:org:workspace"},
		{name: "another organization", exportPath: orgPath, boundCluster: "org-provider", credentialCluster: "org-provider", tenantPath: "root:railgrid:tenants:foreign:workspace"},
		{name: "terminating binding", exportPath: orgPath, boundCluster: "org-provider", credentialCluster: "org-provider", tenantPath: "root:railgrid:tenants:org:workspace", deleting: true},
		{name: "invalid export", exportPath: orgPath, boundCluster: "org-provider", credentialCluster: "org-provider", tenantPath: "root:railgrid:tenants:org:workspace", condition: "APIExportValid"},
		{name: "binding not ready", exportPath: orgPath, boundCluster: "org-provider", credentialCluster: "org-provider", tenantPath: "root:railgrid:tenants:org:workspace", condition: "Ready"},
		{name: "provider not ready", exportPath: orgPath, boundCluster: "org-provider", credentialCluster: "org-provider", tenantPath: "root:railgrid:tenants:org:workspace", notReady: true},
		{name: "registration removed", exportPath: orgPath, boundCluster: "org-provider", credentialCluster: "org-provider", tenantPath: "root:railgrid:tenants:org:workspace", unregistered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": "arbitrary-binding-name"}, "spec": map[string]any{"reference": map[string]any{"export": map[string]any{"path": tc.exportPath, "name": "edges-export"}}}, "status": map[string]any{"phase": "Bound", "apiExportClusterName": tc.boundCluster}}}
			if tc.deleting {
				binding.Object["metadata"].(map[string]any)["deletionTimestamp"] = "2026-11-02T00:00:00Z"
			}
			if tc.condition != "" {
				binding.Object["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": tc.condition, "status": "False"}}
			}
			logical := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster", "metadata": map[string]any{"name": "cluster", "annotations": map[string]any{"kcp.io/path": tc.tenantPath}}}}
			client := fake.NewSimpleDynamicClient(runtime.NewScheme(), binding, logical)
			reg := providers.NewRegistry()
			reg.Upsert(providers.Provider{Name: "edges", CatalogEntryCluster: "legacy-system-catalog", WorkspaceCluster: "platform-provider", APIExportPath: platformPath, APIExportName: "edges-export", EndpointsValid: true})
			if !tc.unregistered {
				reg.Upsert(providers.Provider{Name: "edges", OrgUUID: "org", CatalogEntryCluster: "org-provider", WorkspaceCluster: "org-provider", APIExportPath: orgPath, APIExportName: "edges-export", EndpointsValid: !tc.notReady})
			}
			calls := 0
			attestor := &BoundProviderAttestor{bindings: NewBindingCheckerForClient(client, reg), review: func(_ context.Context, cluster, token string) (*authnv1.TokenReview, error) {
				calls++
				if token != "provider-token" {
					t.Fatal("reviewed another bearer")
				}
				return &authnv1.TokenReview{Status: authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: providers.ProviderSAUsername, Extra: map[string]authnv1.ExtraValue{"authentication.kcp.io/cluster-name": {tc.credentialCluster}}}}}, nil
			}}
			request := httptest.NewRequest("POST", "/api/identities", nil)
			request.Header.Set("Authorization", "Bearer provider-token")
			request.Header.Set("X-Railgrid-Org", "forged")
			subject, err := attestor.AttestInWorkspace(context.Background(), request, "edges", "consumer")
			if (err == nil) != tc.want {
				t.Fatalf("subject=%q error=%v calls=%d", subject, err, calls)
			}
			if tc.want && subject != providers.ProviderSAUsername {
				t.Fatal(subject)
			}
			if tc.want && calls != 1 {
				t.Fatalf("TokenReview calls=%d", calls)
			}
			cleanupWant := tc.want || tc.deleting || tc.notReady || tc.condition == "Ready"
			_, cleanupErr := attestor.AttestCleanupInWorkspace(context.Background(), request, "edges", "consumer")
			if (cleanupErr == nil) != cleanupWant {
				t.Fatalf("cleanup authorization: %v, want allowed=%v", cleanupErr, cleanupWant)
			}
			if tc.want {
				// No stale positive authentication survives removal of its registry entry.
				reg.DeleteScoped(map[bool]string{true: "org", false: ""}[tc.boundCluster == "org-provider"], "edges")
				if _, err := attestor.AttestInWorkspace(context.Background(), request, "edges", "consumer"); err == nil {
					t.Fatal("accepted removed registration through cached authentication")
				}
			}
		})
	}
}

func TestBoundProviderAttestationRejectsHumanAndRevokedTokens(t *testing.T) {
	// TokenReview subject is checked after matching the binding, independently of
	// a token's syntax or unverified claims.
	for _, subject := range []string{"alice", "system:serviceaccount:default:other"} {
		t.Run(subject, func(t *testing.T) {
			reg := providers.NewRegistry()
			reg.Upsert(providers.Provider{Name: "edges", CatalogEntryCluster: "legacy-system-catalog", WorkspaceCluster: "provider", APIExportPath: "root:railgrid:providers:edges", APIExportName: "edges-export", EndpointsValid: true})
			binding := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": "edges"}, "spec": map[string]any{"reference": map[string]any{"export": map[string]any{"path": "root:railgrid:providers:edges", "name": "edges-export"}}}, "status": map[string]any{"phase": "Bound", "apiExportClusterName": "provider"}}}
			client := fake.NewSimpleDynamicClient(runtime.NewScheme(), binding)
			attestor := &BoundProviderAttestor{bindings: NewBindingCheckerForClient(client, reg), review: func(context.Context, string, string) (*authnv1.TokenReview, error) {
				return &authnv1.TokenReview{Status: authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: subject}}}, nil
			}}
			r := httptest.NewRequest("POST", "/api/identities", nil)
			r.Header.Set("Authorization", "Bearer token")
			if _, err := attestor.AttestInWorkspace(context.Background(), r, "edges", "consumer"); err == nil {
				t.Fatal("accepted unrelated subject")
			}
			attestor.review = func(context.Context, string, string) (*authnv1.TokenReview, error) {
				return nil, fmt.Errorf("revoked/unavailable")
			}
			if _, err := attestor.AttestInWorkspace(context.Background(), r, "edges", "consumer"); err == nil {
				t.Fatal("accepted failed TokenReview")
			}
		})
	}
}
