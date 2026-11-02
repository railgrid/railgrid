// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

// Package orgdelegation verifies that embedded native authentication still
// accepts service-account tokens but the Railgrid request hook retires the old
// tenant delegation identities even when organization limits are disabled.
package orgdelegation

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

var (
	workspaces          = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}
	namespaces          = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	clusterRoles        = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	clusterRoleBindings = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
)

func TestNativeDelegationRetirement(t *testing.T) {
	if os.Getenv("RAILGRID_E2E_ORG_DELEGATION") != "true" {
		t.Skip("run make test-native-delegation-retirement to start isolated embedded kcp")
	}
	f := start(t)
	if err := f.native.BYOLimitsReady(); err == nil {
		t.Fatal("organization limits unexpectedly enabled in this fixture")
	}
	cluster := f.workspace(t, "root", "delegation-retirement")
	dynamicClient := f.dynamic(t, cluster)
	_, err := dynamicClient.Resource(namespaces).Create(f.ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "default"},
	}}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	createNativeRBAC(t, dynamicClient)
	legacyToken := f.serviceAccountToken(t, cluster, "railgrid-du-retired")
	platformToken := f.serviceAccountToken(t, cluster, "railgrid-pdu-platform")
	resourceURL := f.server + "/clusters/" + cluster + "/api/v1/namespaces/default/configmaps"

	legacy := f.requestAs(t, http.MethodPost, resourceURL, legacyToken, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"legacy-denied","namespace":"default"},"data":{"ok":"true"}}`, "system:admin", []string{"system:masters"})
	if legacy.StatusCode != http.StatusUnauthorized {
		t.Fatalf("authenticated legacy cluster-admin token with Impersonate-User returned %d, want 401: %s", legacy.StatusCode, legacy.Body)
	}
	platform := f.request(t, http.MethodPost, resourceURL, platformToken, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"platform-allowed","namespace":"default"},"data":{"ok":"true"}}`)
	if platform.StatusCode != http.StatusCreated {
		t.Fatalf("new platform delegated token with identical RBAC returned %d, want 201: %s", platform.StatusCode, platform.Body)
	}
	virtualURL := f.server + "/services/apiexport/root/tenancy.kcp.io/clusters/" + cluster + "/apis/tenancy.kcp.io/v1alpha1/workspaces"
	virtualLegacy := f.requestAs(t, http.MethodGet, virtualURL, legacyToken, "", "system:admin", []string{"system:masters"})
	if virtualLegacy.StatusCode != http.StatusUnauthorized {
		t.Fatalf("virtual workspace accepted retired cluster-admin identity with impersonation: %d: %s", virtualLegacy.StatusCode, virtualLegacy.Body)
	}
	virtualPlatform := f.request(t, http.MethodGet, virtualURL, platformToken, "")
	if virtualPlatform.StatusCode != http.StatusForbidden {
		t.Fatalf("virtual workspace replacement identity returned %d, want native authorization 403 after successful authentication: %s", virtualPlatform.StatusCode, virtualPlatform.Body)
	}
	f.verifyHistoricalChildContainment(t, cluster)
	f.verifyHistoricalInventory(t)
	f.verifyHubIssuedAudience(t, cluster)
	opaque := f.request(t, http.MethodGet, resourceURL, validOrgBackendToken(t), "")
	if opaque.StatusCode != http.StatusUnauthorized {
		t.Fatalf("opaque BYO credential sent to native kcp returned %d, want 401: %s", opaque.StatusCode, opaque.Body)
	}
}

func validOrgBackendToken(t *testing.T) string {
	t.Helper()
	claims := serviceaccounts.OrgBackendClaims{
		User: "alice@example.com", UserUID: "user-uid", OrgUUID: "org", WorkspaceUUID: "workspace",
		ClusterID: "cluster-id", Provider: "planner", ProviderCluster: "provider-cluster",
		ExportPath: "root:railgrid:org-providers:org:planner", ExportName: "planner.providers.railgrid.ai",
		BindingUID: "binding-uid",
	}
	key := serviceaccounts.StaticProofKeySource([]byte(strings.Repeat("x", 32)))
	token, _, err := serviceaccounts.MintOrgBackendToken(context.Background(), key, claims, time.Now())
	if err != nil {
		t.Fatalf("mint signed opaque organization-backend token: %v", err)
	}
	return token
}

type fixture struct {
	ctx    context.Context
	base   *rest.Config
	server string
	native *kcp.EmbeddedKCP
}

func start(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	ports := freePorts(t, 3)
	native := kcp.NewEmbeddedKCP(kcp.EmbeddedKCPOptions{RootDir: t.TempDir(), SecurePort: ports[0], EtcdClientPort: ports[1], EtcdPeerPort: ports[2], BindAddress: "127.0.0.1"})
	done := make(chan error, 1)
	go func() { defer close(done); done <- native.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("embedded kcp did not stop")
		}
	})
	select {
	case <-native.Ready():
	case err := <-done:
		t.Fatalf("embedded kcp startup: %v", err)
	case <-ctx.Done():
		t.Fatalf("embedded kcp startup: %v", ctx.Err())
	}
	base := native.AdminConfig()
	return &fixture{ctx: ctx, base: base, server: strings.Split(base.Host, "/clusters/")[0], native: native}
}

func (f *fixture) dynamic(t *testing.T, cluster string) dynamic.Interface {
	t.Helper()
	config := rest.CopyConfig(f.base)
	config.Host = f.server + "/clusters/" + cluster
	config.QPS, config.Burst = 100, 300
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func (f *fixture) workspace(t *testing.T, parent, name string) string {
	t.Helper()
	_, err := f.dynamic(t, parent).Resource(workspaces).Create(f.ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace",
		"metadata": map[string]any{"name": name},
		"spec":     map[string]any{"type": map[string]any{"name": "universal", "path": "root"}},
	}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, time.Minute)
	defer cancel()
	for {
		got, err := f.dynamic(t, parent).Resource(workspaces).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
			if phase == "Ready" {
				cluster, _, _ := unstructured.NestedString(got.Object, "spec", "cluster")
				if cluster == "" {
					t.Fatal("ready Workspace has no cluster identity")
				}
				return cluster
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Workspace %s/%s not ready: %v", parent, name, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func createNativeRBAC(t *testing.T, client dynamic.Interface) {
	t.Helper()
	_, err := client.Resource(clusterRoles).Create(t.Context(), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": map[string]any{"name": "delegation-cluster-admin"},
		"rules": []any{
			map[string]any{"apiGroups": []any{"*"}, "resources": []any{"*"}, "verbs": []any{"*"}},
			map[string]any{"nonResourceURLs": []any{"*"}, "verbs": []any{"*"}},
		},
	}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Resource(clusterRoleBindings).Create(t.Context(), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": "delegation-cluster-admin"},
		"subjects": []any{
			map[string]any{"kind": "ServiceAccount", "name": "railgrid-du-retired", "namespace": "default"},
			map[string]any{"kind": "ServiceAccount", "name": "railgrid-pdu-platform", "namespace": "default"},
		},
		"roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "delegation-cluster-admin"},
	}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) serviceAccountToken(t *testing.T, cluster, name string) string {
	t.Helper()
	config := rest.CopyConfig(f.base)
	config.Host = f.server + "/clusters/" + cluster
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	if _, err := client.CoreV1().ServiceAccounts("default").Create(f.ctx, sa, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	token, err := client.CoreV1().ServiceAccounts("default").CreateToken(f.ctx, name, &authenticationv1.TokenRequest{}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if token.Status.Token == "" {
		t.Fatal("native TokenRequest returned an empty token")
	}
	return token.Status.Token
}

type response struct {
	StatusCode int
	Body       string
}

func (f *fixture) request(t *testing.T, method, url, bearer, body string) response {
	return f.requestAs(t, method, url, bearer, body, "", nil)
}

func (f *fixture) requestAs(t *testing.T, method, url, bearer, body, impersonatedUser string, impersonatedGroups []string) response {
	t.Helper()
	config := rest.CopyConfig(f.base)
	config.BearerToken = ""
	config.Username, config.Password = "", ""
	client, err := rest.HTTPClientFor(config)
	if err != nil {
		t.Fatal(err)
	}
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(f.ctx, method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if impersonatedUser != "" {
		req.Header.Set("Impersonate-User", impersonatedUser)
	}
	for _, group := range impersonatedGroups {
		req.Header.Add("Impersonate-Group", group)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{StatusCode: resp.StatusCode, Body: string(data)}
}

func freePorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	ports := make([]int, 0, count)
	for range count {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		address, ok := listener.Addr().(*net.TCPAddr)
		if !ok {
			t.Fatalf("unexpected listener address %T", listener.Addr())
		}
		ports = append(ports, address.Port)
	}
	return ports
}

// verifyHubIssuedAudience checks the actual pinned native TokenRequest and
// TokenReview behavior, including a co-member token minted for the same SA.
func (f *fixture) verifyHubIssuedAudience(t *testing.T, cluster string) {
	t.Helper()
	config := rest.CopyConfig(f.base)
	config.Host = f.server + "/clusters/" + cluster
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	const tenantPath = "root:railgrid:tenants:org:workspace"
	const provider = "infrastructure"
	identity := serviceaccounts.Identity{User: "alice"}
	sa, err := client.CoreV1().ServiceAccounts("default").Create(f.ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: serviceaccounts.DelegatedUserServiceAccountName(tenantPath, identity, provider), Namespace: "default",
		Labels: map[string]string{serviceaccounts.LabelWorkloadIdentity: "true", serviceaccounts.LabelDelegatedUser: "true"},
		Annotations: map[string]string{serviceaccounts.AnnotationWorkloadIdentityTenantPath: tenantPath,
			serviceaccounts.AnnotationDelegatedUser: "alice", serviceaccounts.AnnotationDelegatedOrg: "org", serviceaccounts.AnnotationDelegatedWorkspace: "workspace", serviceaccounts.AnnotationDelegatedProvider: provider},
	}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	keys := serviceaccounts.StaticProofKeySource([]byte(strings.Repeat("k", 32)))
	if err := serviceaccounts.SignDelegatedUserServiceAccount(keys, sa, tenantPath, identity, provider); err != nil {
		t.Fatal(err)
	}
	sa, err = client.CoreV1().ServiceAccounts("default").Update(f.ctx, sa, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	audience, err := serviceaccounts.DelegatedTokenAudience(f.ctx, keys, sa)
	if err != nil {
		t.Fatal(err)
	}
	for _, hubIssued := range []bool{false, true} {
		audiences := []string{serviceaccounts.WorkloadIdentityTokenAudience}
		if hubIssued {
			audiences = append(audiences, audience)
		}
		issued, err := client.CoreV1().ServiceAccounts("default").CreateToken(f.ctx, sa.Name, &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{Audiences: audiences}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = serviceaccounts.VerifyWorkloadServiceAccountDetails(f.ctx, config, issued.Status.Token, tenantPath, keys)
		if (err == nil) != hubIssued {
			t.Fatalf("native token hubIssued=%v verification = %v", hubIssued, err)
		}
	}
}
