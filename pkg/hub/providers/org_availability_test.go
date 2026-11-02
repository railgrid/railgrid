// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
	sdkinstall "github.com/railgrid/provider-sdk/install"
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

func TestOrgProviderCredentialOperationsRequireContainmentBeforeKCPAccess(t *testing.T) {
	for _, gate := range []string{"unconfigured", "unavailable"} {
		for _, operation := range []string{"ensure", "mint", "rotate"} {
			t.Run(gate+"/"+operation, func(t *testing.T) {
				calls := 0
				p := NewProvisioner(nil)
				if gate == "unavailable" {
					p = NewProvisioner(nil, WithOrgProviderAvailability(func(context.Context) error { calls++; return fmt.Errorf("native containment unavailable") }))
				}
				path := kcppaths.OrgProviderPath("org-a", "planner")
				var err error
				switch operation {
				case "ensure":
					err = p.EnsureProviderSAAtPath(t.Context(), path)
				case "mint":
					_, err = p.MintProviderKubeconfigAtPath(t.Context(), path, "https://hub.test")
				case "rotate":
					_, err = p.RotateProviderCredentialAtPath(t.Context(), path, "planner", "https://hub.test")
				}
				if err == nil || !strings.Contains(err.Error(), "containment") {
					t.Fatalf("expected containment refusal before nil-config KCP access, got %v", err)
				}
				if gate == "unavailable" && calls != 1 {
					t.Fatalf("availability calls=%d", calls)
				}
			})
		}
	}
	p := NewProvisioner(nil, WithOrgProviderAvailability(func(context.Context) error {
		t.Error("platform provider consulted org containment")
		return fmt.Errorf("unavailable")
	}))
	if err := p.requireOrgProviderAvailability(t.Context(), kcppaths.ProviderPath("planner")); err != nil {
		t.Fatalf("platform provider refused: %v", err)
	}
}

func TestOrgProviderCredentialsRequireStorageValidationAfterEnrollment(t *testing.T) {
	for _, audit := range []string{"unconfigured", "unavailable", "ready"} {
		for _, operation := range []string{"mint", "rotate"} {
			t.Run(audit+"/"+operation, func(t *testing.T) {
				path := kcppaths.OrgProviderPath("org-a", "planner")
				var workspaceReads, credentialWrites, auditCalls int
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && r.URL.Path == "/clusters/root:railgrid:tenants:org-a:providers/apis/tenancy.kcp.io/v1alpha1/workspaces/planner" {
						workspaceReads++
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"apiVersion":"tenancy.kcp.io/v1alpha1","kind":"Workspace","metadata":{"name":"planner","annotations":{"providers.railgrid.ai/bootstrap-policy":"restricted-v1"}}}`)
						return
					}
					if r.Method != http.MethodGet && (strings.Contains(r.URL.Path, "/secrets") || strings.Contains(r.URL.Path, "/serviceaccounts")) {
						credentialWrites++
					}
					http.Error(w, "unexpected request", http.StatusInternalServerError)
				}))
				defer server.Close()

				opts := []ProvisionerOption{WithOrgProviderAvailability(func(context.Context) error { return nil })}
				switch audit {
				case "unavailable":
					opts = append(opts, WithOrgProviderStorageValidation(func(context.Context, string) error {
						auditCalls++
						return fmt.Errorf("storage quota unavailable")
					}))
				case "ready":
					opts = append(opts, WithOrgProviderStorageValidation(func(_ context.Context, workspacePath string) error {
						auditCalls++
						if workspacePath != path {
							t.Errorf("storage validation path=%q want %q", workspacePath, path)
						}
						return nil
					}))
				}
				p := NewProvisioner(&rest.Config{Host: server.URL}, opts...)
				var err error
				var kubeconfig []byte
				var rotated *RotatedCredential
				switch operation {
				case "mint":
					kubeconfig, err = p.MintProviderKubeconfigAtPath(t.Context(), path, "https://hub.test")
				case "rotate":
					rotated, err = p.RotateProviderCredentialAtPath(t.Context(), path, "planner", "https://hub.test")
				}
				if err == nil {
					t.Fatal("expected credential operation to stop before token operations")
				}
				if len(kubeconfig) != 0 || rotated != nil {
					t.Fatal("denied credential operation returned a credential")
				}
				if workspaceReads != 1 {
					t.Fatalf("authoritative workspace reads=%d want 1", workspaceReads)
				}
				if credentialWrites != 0 {
					t.Fatalf("credential writes=%d want 0", credentialWrites)
				}
				wantAuditCalls := 1
				if audit == "unconfigured" {
					wantAuditCalls = 0
					if !strings.Contains(err.Error(), "storage validation is not configured") {
						t.Fatalf("error=%v", err)
					}
				} else if audit == "unavailable" && !strings.Contains(err.Error(), "storage quota unavailable") {
					t.Fatalf("error=%v", err)
				}
				if auditCalls != wantAuditCalls {
					t.Fatalf("storage validation calls=%d want %d", auditCalls, wantAuditCalls)
				}
			})
		}
	}

	calls := 0
	p := NewProvisioner(nil, WithOrgProviderStorageValidation(func(context.Context, string) error {
		calls++
		return fmt.Errorf("must not run for platform provider")
	}))
	if err := p.requireOrgProviderStorageValidation(t.Context(), kcppaths.ProviderPath("planner")); err != nil {
		t.Fatalf("platform provider refused: %v", err)
	}
	if calls != 0 {
		t.Fatalf("platform provider invoked storage validation %d times", calls)
	}
}

func TestOrgBootstrapAvailabilityRunsAfterAuthenticationBeforeDeclarations(t *testing.T) {
	for _, scenario := range []string{"unconfigured", "unavailable", "unauthenticated", "consumer unconfigured", "consumer unavailable", "changed export"} {
		t.Run(scenario, func(t *testing.T) {
			calls, checks, preparations := 0, 0, 0
			consumerCase := strings.HasPrefix(scenario, "consumer") || scenario == "changed export"
			kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var object any
				switch r.URL.Path {
				case "/clusters/provider-cluster/apis/authentication.k8s.io/v1/tokenreviews":
					object = &authnv1.TokenReview{TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}, Status: authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: ProviderSAUsername, UID: "provider-uid", Extra: map[string]authnv1.ExtraValue{dataplane.ClusterNameExtra: {"provider-cluster"}}}}}
				case "/clusters/provider-cluster/apis/core.kcp.io/v1alpha1/logicalclusters/cluster":
					object = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster", "metadata": map[string]any{"name": "cluster", "annotations": map[string]any{"kcp.io/cluster": "provider-cluster", "kcp.io/path": kcppaths.OrgProviderPath("org-a", "planner")}}}}
				case "/clusters/root:railgrid:tenants:org-a:providers/apis/tenancy.kcp.io/v1alpha1/workspaces/planner":
					object = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace", "metadata": map[string]any{"name": "planner", "annotations": map[string]any{OrgProviderBootstrapPolicyAnnotation: OrgProviderBootstrapPolicy}}, "spec": map[string]any{"cluster": "provider-cluster"}}}
				case "/clusters/provider-cluster/api/v1/namespaces/default/serviceaccounts/provider":
					object = &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: metav1.ObjectMeta{Name: ProviderSAName, Namespace: ProviderSANamespace, UID: "provider-uid", Annotations: map[string]string{OrgProviderBootstrapPolicyAnnotation: OrgProviderBootstrapPolicy}}}
				case "/clusters/provider-cluster/apis/providers.railgrid.ai/v1alpha1/catalogentries/planner":
					if scenario == "changed export" {
						object = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "providers.railgrid.ai/v1alpha1", "kind": "CatalogEntry", "metadata": map[string]any{"name": "planner"}, "spec": map[string]any{"export": map[string]any{"name": "old-export"}}}}
					} else {
						w.WriteHeader(http.StatusNotFound)
						object = &metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound}
					}
				default:
					t.Errorf("availability refusal reached declaration path %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected", 500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(object); err != nil {
					t.Error(err)
				}
			}))
			defer kcp.Close()
			h, err := NewOrgProviderBootstrapHandler(&rest.Config{Host: kcp.URL}, NewRegistry(), "https://relay.test")
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "unconfigured" {
				h.SetAvailabilityCheck(func(context.Context) error {
					checks++
					if consumerCase {
						return nil
					}
					return fmt.Errorf("not ready")
				})
			}
			locked := false
			if consumerCase {
				h.SetLifecycleLock(func(ctx context.Context, org string) (func(), error) {
					if org != "org-a" {
						t.Error("bootstrap lock has wrong organization scope")
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Error("bootstrap lock context is unbounded")
					}
					locked = true
					return func() { locked = false }, nil
				})
			}
			if scenario == "consumer unavailable" || scenario == "changed export" {
				h.SetConsumerPreparation(func(ctx context.Context, org, name string, admitted sdkinstall.OrganizationBootstrap) error {
					preparations++
					if !locked {
						t.Error("bootstrap preparation ran outside lifecycle lock")
					}
					if org != "org-a" || name != "planner" || admitted.APIExport == nil || len(admitted.Schemas) == 0 || len(admitted.CatalogEntry.GetAnnotations()) != 0 {
						t.Error("consumer preparation did not receive the authenticated scope and validated declaration")
					}
					return fmt.Errorf("consumer quota unavailable")
				})
			}
			body := []byte("invalid declaration must not be decoded")
			if consumerCase {
				declaration := quickstartBootstrap(t)
				declaration.CatalogEntry.SetName("planner")
				declaration.CatalogEntry.SetAnnotations(map[string]string{"untrusted": "removed"})
				body, err = json.Marshal(declaration)
				if err != nil {
					t.Fatal(err)
				}
			}
			req := httptest.NewRequest(http.MethodPost, "/api/providers/planner/bootstrap", bytes.NewReader(body))
			if scenario != "unauthenticated" {
				req.Header.Set("Authorization", "Bearer "+orgHeartbeatToken("provider-cluster"))
			}
			response := httptest.NewRecorder()
			if consumerCase {
				// A live writer supports the upload deadlines required by bootstrap.
				server := httptest.NewServer(h)
				defer server.Close()
				outbound, err := http.NewRequest(http.MethodPost, server.URL+req.URL.Path, bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				outbound.Header = req.Header
				result, err := http.DefaultClient.Do(outbound)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = result.Body.Close() }()
				response.Code = result.StatusCode
				if _, err := io.Copy(response.Body, result.Body); err != nil {
					t.Fatal(err)
				}
			} else {
				h.ServeHTTP(response, req)
			}
			want := http.StatusServiceUnavailable
			if scenario == "unauthenticated" {
				want = http.StatusUnauthorized
				if calls != 0 || checks != 0 {
					t.Fatal("unauthenticated request consulted KCP or availability")
				}
			} else {
				wantCalls := 4
				if consumerCase {
					wantCalls = 9
				}
				if calls != wantCalls {
					t.Fatalf("expected identity/declaration-read-only KCP calls=%d, got %d: %s", wantCalls, calls, response.Body.String())
				}
			}
			if scenario == "consumer unavailable" && preparations != 1 {
				t.Fatalf("consumer preparations=%d", preparations)
			}
			if scenario == "changed export" {
				want = http.StatusConflict
				if preparations != 0 {
					t.Fatal("changed export reached consumer preparation")
				}
			}
			if scenario == "unavailable" && checks != 1 {
				t.Fatalf("availability checks=%d", checks)
			}
			if response.Code != want {
				t.Fatalf("status=%d want %d: %s", response.Code, want, response.Body.String())
			}
			if locked {
				t.Error("bootstrap did not release lifecycle lock")
			}
		})
	}
}
