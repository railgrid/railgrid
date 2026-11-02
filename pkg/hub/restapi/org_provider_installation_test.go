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

package restapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providerinstall"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

type fakeConsoleInstaller struct {
	deny               bool
	started            bool
	identity           string
	retry              bool
	startHook          func(context.Context)
	prepareDeleteHook  func(context.Context) error
	cleanupCalls       int
	prepareDeleteCalls int
	status             *providerinstall.Status
}

func (f *fakeConsoleInstaller) Authorize(_ context.Context, _, _, _, user string) error {
	f.identity = user
	if f.deny {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "kubernetesclusters/k8s"}, "devbox", nil)
	}
	return nil
}
func (*fakeConsoleInstaller) Target(context.Context, string, string) (*kcp.EdgeRoute, error) {
	return &kcp.EdgeRoute{WorkspaceUUID: "ws-1", EdgeName: "devbox"}, nil
}
func (f *fakeConsoleInstaller) Start(ctx context.Context, _, _ string, p *providerinstall.Plan, credential []byte, retry bool) (*providerinstall.Status, error) {
	f.started = true
	if f.startHook != nil {
		f.startHook(ctx)
	}
	f.retry = retry
	return &providerinstall.Status{Phase: "Queued", Message: "Waiting for installer.", Version: p.Version, Attempt: 1}, nil
}
func (f *fakeConsoleInstaller) Status(context.Context, string, string, bool) (*providerinstall.Status, error) {
	if f.status != nil {
		return f.status, nil
	}
	return &providerinstall.Status{Phase: "NotStarted"}, nil
}
func (f *fakeConsoleInstaller) Cleanup(context.Context, string, string, string) { f.cleanupCalls++ }
func (f *fakeConsoleInstaller) PrepareDeleteWithUID(ctx context.Context, _, _, _ string) (string, error) {
	f.prepareDeleteCalls++
	if f.prepareDeleteHook != nil {
		if err := f.prepareDeleteHook(ctx); err != nil {
			return "", err
		}
	}
	return "registration-uid", nil
}

func TestConsoleInstallAuthorizesBeforeProvisioningAndReturnsNoCredential(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "authorized", true: "edge denied"}[denied], func(t *testing.T) {
			org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}}
			user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}, Spec: tenancyv1alpha1.UserSpec{RBACIdentity: "railgrid:alice@example.com"}}
			mgr, workspaces, _ := newTestManager(t, org, user)
			_ = workspaces.EnsureOrgMembership(context.Background(), "org-a", "alice", tenancyv1alpha1.MembershipRoleAdmin)
			_ = workspaces.EnsureChildWorkspace(context.Background(), "org-a", "ws-1")
			ops := &fakeOrgProviderOps{targets: []kcp.EdgeInstallTarget{connectedEdge("ws-1", "devbox")}}
			mgr.WithOrgProviders(ops, &fakeCredMinter{})
			registry := providers.NewRegistry()
			registry.Upsert(providers.Provider{Name: "kuery", EndpointsValid: true, SelfHosting: &providers.SelfHosting{Supported: true, ChartRepo: "oci://ghcr.io/railgrid/charts", ChartName: "railgrid-kuery-provider", ChartVersion: "1.0.0"}})
			mgr.WithProviderRegistry(registry).WithKubeconfig(KubeconfigConfig{HubExternalURL: "https://hub.test"})
			installer := &fakeConsoleInstaller{deny: denied}
			mgr.WithProviderInstaller(installer)
			srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
			defer srv.Close()
			res, err := http.Post(srv.URL+"/api/orgs/org-a/providers/kuery/installation", "application/json", strings.NewReader(`{"edge":{"workspace":"ws-1","name":"devbox"},"retry":true}`))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			if denied {
				if res.StatusCode != http.StatusForbidden || len(ops.registered) != 0 || installer.started {
					t.Fatalf("denied Edge provisioned resources: status=%d registered=%v", res.StatusCode, ops.registered)
				}
				return
			}
			if res.StatusCode != http.StatusAccepted {
				var body any
				_ = json.NewDecoder(res.Body).Decode(&body)
				t.Fatalf("status=%d body=%v", res.StatusCode, body)
			}
			var body map[string]any
			if err = json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["kubeconfig"] != nil || body["instructions"] != nil || !installer.started || !installer.retry {
				t.Fatalf("unexpected console response: %+v", body)
			}
			if installer.identity != "railgrid:alice@example.com" {
				t.Fatalf("authorized the User UUID instead of its Kubernetes identity: %s", installer.identity)
			}
		})
	}
}

func TestConsoleProgressDoesNotGrantInstallationPermission(t *testing.T) {
	org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}}
	user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}, Spec: tenancyv1alpha1.UserSpec{RBACIdentity: "railgrid:alice@example.com"}}
	mgr, workspaces, _ := newTestManager(t, org, user)
	_ = workspaces.EnsureOrgMembership(context.Background(), "org-a", "alice", tenancyv1alpha1.MembershipRoleMember)
	ops := &fakeOrgProviderOps{workspaces: []kcp.OrgProviderWorkspace{{Name: "kuery", Cluster: "provider-cluster", Phase: "Ready"}}}
	installer := &fakeConsoleInstaller{}
	mgr.WithOrgProviders(ops, &fakeCredMinter{}).WithProviderInstaller(installer)
	srv := newTestServer(t, mgr, memberTC("alice", "org-a", ""))
	defer srv.Close()
	response, err := http.Get(srv.URL + "/api/orgs/org-a/providers/kuery/installation")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("member could not read progress: %d", response.StatusCode)
	}
	response, err = http.Post(srv.URL+"/api/orgs/org-a/providers/kuery/installation", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || installer.started || installer.identity != "" {
		t.Fatalf("reading progress allowed a privileged install: status=%d", response.StatusCode)
	}
}

func TestConsoleInstallDoesNotDiscloseInaccessiblePinnedEdge(t *testing.T) {
	for _, workspaceRole := range []string{tenancyv1alpha1.MembershipRoleMember, tenancyv1alpha1.MembershipRoleAdmin} {
		t.Run(workspaceRole, func(t *testing.T) {
			org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}, Spec: tenancyv1alpha1.OrganizationSpec{CatalogEntryCreation: tenancyv1alpha1.CatalogEntryCreationMembers}}
			user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}, Spec: tenancyv1alpha1.UserSpec{RBACIdentity: "railgrid:alice@example.com"}}
			mgr, workspaces, _ := newTestManager(t, org, user)
			_ = workspaces.EnsureOrgMembership(context.Background(), "org-a", "alice", tenancyv1alpha1.MembershipRoleMember)
			_ = workspaces.EnsureChildWorkspace(context.Background(), "org-a", "ws-1")
			ops := &fakeOrgProviderOps{workspaces: []kcp.OrgProviderWorkspace{{Name: "kuery", Cluster: "provider-cluster", Phase: "Ready"}}, targets: []kcp.EdgeInstallTarget{connectedEdge("ws-1", "devbox")}}
			installer := &fakeConsoleInstaller{}
			mgr.WithOrgProviders(ops, &fakeCredMinter{}).WithProviderInstaller(installer)
			tc := memberTC("alice", "org-a", "")
			tc.Role = workspaceRole
			srv := newTestServer(t, mgr, tc)
			defer srv.Close()
			response, err := http.Post(srv.URL+"/api/orgs/org-a/providers/kuery/installation", "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			var body map[string]any
			if err = json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(body)
			if response.StatusCode != http.StatusConflict || strings.Contains(string(encoded), "ws-1") || strings.Contains(string(encoded), "devbox") || installer.started || installer.identity != "" {
				t.Fatalf("hidden Edge leaked or was authorized: %d %s", response.StatusCode, encoded)
			}
		})
	}
}

func TestClosingConsoleDoesNotCancelAuthorizedJobSubmission(t *testing.T) {
	org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}}
	user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}, Spec: tenancyv1alpha1.UserSpec{RBACIdentity: "railgrid:alice@example.com"}}
	mgr, workspaces, _ := newTestManager(t, org, user)
	_ = workspaces.EnsureOrgMembership(context.Background(), "org-a", "alice", tenancyv1alpha1.MembershipRoleAdmin)
	_ = workspaces.EnsureChildWorkspace(context.Background(), "org-a", "ws-1")
	mgr.WithOrgProviders(&fakeOrgProviderOps{targets: []kcp.EdgeInstallTarget{connectedEdge("ws-1", "devbox")}}, &fakeCredMinter{})
	registry := providers.NewRegistry()
	registry.Upsert(providers.Provider{Name: "kuery", EndpointsValid: true, SelfHosting: &providers.SelfHosting{Supported: true, ChartRepo: "oci://ghcr.io/railgrid/charts", ChartName: "railgrid-kuery-provider", ChartVersion: "1.0.0"}})
	mgr.WithProviderRegistry(registry).WithKubeconfig(KubeconfigConfig{HubExternalURL: "https://hub.test"})
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	installer := &fakeConsoleInstaller{startHook: func(ctx context.Context) { close(started); <-release; finished <- ctx.Err() }}
	mgr.WithProviderInstaller(installer)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/orgs/org-a/providers/kuery/installation", strings.NewReader(`{"edge":{"workspace":"ws-1","name":"devbox"}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		res, err := http.DefaultClient.Do(request)
		if err == nil {
			_ = res.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("installer did not start")
	}
	cancel()
	<-clientDone
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("browser cancellation aborted Job submission: %v", err)
	}
}

func TestDeleteOrgProviderCleansInstallerBeforeDeletingWorkspace(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanupFails=%t", cleanupFails), func(t *testing.T) {
			org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}}
			user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}
			mgr, workspaces, _ := newTestManager(t, org, user)
			_ = workspaces.EnsureOrgMembership(context.Background(), "org-a", "alice", tenancyv1alpha1.MembershipRoleAdmin)
			ops := &fakeOrgProviderOps{workspaces: []kcp.OrgProviderWorkspace{{Name: "kuery", Cluster: "provider-cluster"}}}
			installer := &fakeConsoleInstaller{}
			installer.prepareDeleteHook = func(ctx context.Context) error {
				if len(ops.deleted) != 0 {
					t.Fatal("workspace was deleted before installer cleanup")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("cleanup must use bounded authorized deletion context")
				}
				if cleanupFails {
					return fmt.Errorf("edge unavailable")
				}
				return nil
			}
			mgr.WithOrgProviders(ops, &fakeCredMinter{}).WithProviderInstaller(installer)
			srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
			defer srv.Close()
			req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/providers/kuery", nil)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			if installer.prepareDeleteCalls != 1 {
				t.Fatalf("cleanup calls=%d", installer.prepareDeleteCalls)
			}
			if cleanupFails {
				if res.StatusCode < 400 || len(ops.deleted) != 0 {
					t.Fatalf("cleanup failure lost registration: status=%d deletes=%v", res.StatusCode, ops.deleted)
				}
			} else if res.StatusCode != http.StatusNoContent || len(ops.deleted) != 1 {
				t.Fatalf("successful cleanup did not delete registration: status=%d deletes=%v", res.StatusCode, ops.deleted)
			}
		})
	}
}

func TestConsoleProgressReadsDoNotPerformPrivilegedCleanup(t *testing.T) {
	org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}}
	user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}
	mgr, workspaces, _ := newTestManager(t, org, user)
	_ = workspaces.EnsureOrgMembership(context.Background(), "org-a", "alice", tenancyv1alpha1.MembershipRoleMember)
	ops := &fakeOrgProviderOps{workspaces: []kcp.OrgProviderWorkspace{{Name: "kuery", Cluster: "provider-cluster"}}}
	installer := &fakeConsoleInstaller{status: &providerinstall.Status{Phase: "Checking", Job: "install-kuery"}}
	mgr.WithOrgProviders(ops, &fakeCredMinter{}).WithProviderInstaller(installer)
	srv := newTestServer(t, mgr, memberTC("alice", "org-a", ""))
	defer srv.Close()
	for range 2 {
		res, err := http.Get(srv.URL + "/api/orgs/org-a/providers/kuery/installation")
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status read returned %d", res.StatusCode)
		}
		_ = res.Body.Close()
	}
	if installer.cleanupCalls != 0 || installer.prepareDeleteCalls != 0 || installer.started {
		t.Fatal("read-only status performed installer mutations")
	}
}

func TestDeleteOrgProviderKeepsCleanedUIDAndIsIdempotentWhenAbsent(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(fmt.Sprintf("absent=%t", absent), func(t *testing.T) {
			org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}}
			user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}
			mgr, workspaces, _ := newTestManager(t, org, user)
			_ = workspaces.EnsureOrgMembership(context.Background(), "org-a", "alice", tenancyv1alpha1.MembershipRoleAdmin)
			ops := &fakeOrgProviderOps{}
			installer := &fakeConsoleInstaller{prepareDeleteHook: func(context.Context) error {
				if absent {
					return apierrors.NewNotFound(schema.GroupResource{Resource: "workspaces"}, "kuery")
				}
				return nil
			}}
			ops.deleteIfUIDHook = func(uid string) error {
				if uid != "registration-uid" {
					t.Fatalf("deleted UID=%q, want the registration cleaned by PrepareDelete", uid)
				}
				// A same-name replacement appeared after cleanup. The fenced
				// deleter rejects it rather than selecting it by name again.
				return apierrors.NewConflict(schema.GroupResource{Resource: "workspaces"}, "kuery", fmt.Errorf("replacement registration"))
			}
			mgr.WithOrgProviders(ops, &fakeCredMinter{}).WithProviderInstaller(installer)
			srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
			defer srv.Close()
			req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/providers/kuery", nil)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			want := http.StatusConflict
			if absent {
				want = http.StatusNoContent
			}
			if res.StatusCode != want {
				t.Fatalf("DELETE status=%d, want %d", res.StatusCode, want)
			}
			if len(ops.deleted) != 0 {
				t.Fatal("stale or absent registration triggered an unfenced delete")
			}
		})
	}
}
