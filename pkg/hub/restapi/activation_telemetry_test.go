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

package restapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	clientgotesting "k8s.io/client-go/testing"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/hub/kcp"
	hubproviders "github.com/faroshq/faros/pkg/hub/providers"
	hubtelemetry "github.com/faroshq/faros/pkg/hub/telemetry"
	"github.com/faroshq/faros/pkg/hub/tenant"
)

type activationTelemetryRecorder struct {
	mu     sync.Mutex
	events []hubtelemetry.Event
	err    error
}

func (r *activationTelemetryRecorder) TrackPlatform(_ context.Context, event hubtelemetry.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return r.err
}

func (r *activationTelemetryRecorder) snapshot() []hubtelemetry.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hubtelemetry.Event(nil), r.events...)
}

type activationOps struct {
	*fakeOps
	ensureOrgWorkspaceErr            error
	ensureOrgMembershipErr           error
	ensureChildWorkspaceErr          error
	ensureChildWorkspaceBindingErr   error
	setWorkspaceDisplayNameErr       error
	ensureChildWorkspaceAdminErr     error
	ensureChildWorkspaceMCPServerErr error
	ensureProviderAPIBindingErr      error
	ensureProviderEdgeProxyGrantErr  error
	getWorkspaceDisplayNameErr       error
}

func (f *activationOps) EnsureOrgWorkspace(ctx context.Context, orgUUID string) error {
	if f.ensureOrgWorkspaceErr != nil {
		return f.ensureOrgWorkspaceErr
	}
	return f.fakeOps.EnsureOrgWorkspace(ctx, orgUUID)
}

func (f *activationOps) EnsureOrgMembership(ctx context.Context, orgUUID, userName, role string) error {
	if f.ensureOrgMembershipErr != nil {
		return f.ensureOrgMembershipErr
	}
	return f.fakeOps.EnsureOrgMembership(ctx, orgUUID, userName, role)
}

func (f *activationOps) EnsureChildWorkspace(ctx context.Context, orgUUID, wsUUID string) error {
	if f.ensureChildWorkspaceErr != nil {
		return f.ensureChildWorkspaceErr
	}
	return f.fakeOps.EnsureChildWorkspace(ctx, orgUUID, wsUUID)
}

func (f *activationOps) EnsureChildWorkspaceFarosBinding(ctx context.Context, orgUUID, wsUUID string) error {
	if f.ensureChildWorkspaceBindingErr != nil {
		return f.ensureChildWorkspaceBindingErr
	}
	return f.fakeOps.EnsureChildWorkspaceFarosBinding(ctx, orgUUID, wsUUID)
}

func (f *activationOps) SetWorkspaceDisplayName(ctx context.Context, orgUUID, wsUUID, displayName string) error {
	if f.setWorkspaceDisplayNameErr != nil {
		return f.setWorkspaceDisplayNameErr
	}
	return f.fakeOps.SetWorkspaceDisplayName(ctx, orgUUID, wsUUID, displayName)
}

func (f *activationOps) EnsureChildWorkspaceAdmin(ctx context.Context, orgUUID, wsUUID, identity string) error {
	if f.ensureChildWorkspaceAdminErr != nil {
		return f.ensureChildWorkspaceAdminErr
	}
	return f.fakeOps.EnsureChildWorkspaceAdmin(ctx, orgUUID, wsUUID, identity)
}

func (f *activationOps) EnsureChildWorkspaceDefaultMCPServer(ctx context.Context, orgUUID, wsUUID string) error {
	if f.ensureChildWorkspaceMCPServerErr != nil {
		return f.ensureChildWorkspaceMCPServerErr
	}
	return f.fakeOps.EnsureChildWorkspaceDefaultMCPServer(ctx, orgUUID, wsUUID)
}

func (f *activationOps) EnsureProviderAPIBinding(ctx context.Context, orgUUID, wsUUID, bindingName, exportPath, exportName string, claims []kcp.ProviderClaim) (bool, error) {
	if f.ensureProviderAPIBindingErr != nil {
		return false, f.ensureProviderAPIBindingErr
	}
	return f.fakeOps.EnsureProviderAPIBinding(ctx, orgUUID, wsUUID, bindingName, exportPath, exportName, claims)
}

func (f *activationOps) EnsureProviderEdgeProxyGrant(ctx context.Context, orgUUID, wsUUID, providerName, subject string) error {
	if f.ensureProviderEdgeProxyGrantErr != nil {
		return f.ensureProviderEdgeProxyGrantErr
	}
	return f.fakeOps.EnsureProviderEdgeProxyGrant(ctx, orgUUID, wsUUID, providerName, subject)
}

func (f *activationOps) GetWorkspaceDisplayName(ctx context.Context, orgUUID, wsUUID string) (string, error) {
	if f.getWorkspaceDisplayNameErr != nil {
		return "", f.getWorkspaceDisplayNameErr
	}
	return f.fakeOps.GetWorkspaceDisplayName(ctx, orgUUID, wsUUID)
}

func activationRequest(t *testing.T, method, path string, tc tenant.TenantContext, body any, vars map[string]string) *http.Request {
	t.Helper()
	var payload *bytes.Reader
	if body == nil {
		payload = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		payload = bytes.NewReader(encoded)
	}
	r := httptest.NewRequest(method, path, payload)
	r = r.WithContext(tenant.WithContext(r.Context(), tc))
	return mux.SetURLVars(r, vars)
}

func serveActivation(h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	h(response, req)
	return response
}

func requireSingleActivationEvent(t *testing.T, recorder *activationTelemetryRecorder) hubtelemetry.Event {
	t.Helper()
	events := recorder.snapshot()
	if len(events) != 1 {
		t.Fatalf("telemetry events = %d, want 1 (%#v)", len(events), events)
	}
	return events[0]
}

func requireNoActivationEvents(t *testing.T, recorder *activationTelemetryRecorder) {
	t.Helper()
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("telemetry events = %#v, want none", events)
	}
}

func TestManagerPlatformTelemetryDefaultsToNoop(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	if _, ok := mgr.telemetry.(noopPlatformTelemetry); !ok {
		t.Fatalf("default telemetry = %T, want noopPlatformTelemetry", mgr.telemetry)
	}
	if got := mgr.WithTelemetry(nil); got != mgr {
		t.Fatal("WithTelemetry should return the Manager")
	}
	if _, ok := mgr.telemetry.(noopPlatformTelemetry); !ok {
		t.Fatalf("nil telemetry = %T, want noopPlatformTelemetry", mgr.telemetry)
	}
}

func TestCreateOrgTracksOnlyAfterFullSuccessAndIgnoresTelemetryError(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	telemetry := &activationTelemetryRecorder{err: errors.New("telemetry unavailable")}
	mgr.WithTelemetry(telemetry)
	h := NewHandler(mgr)
	response := serveActivation(h.createOrg, activationRequest(t, http.MethodPost, "/api/orgs", tenant.TenantContext{User: "user-123"}, CreateOrgRequest{DisplayName: "Acme"}, nil))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body)
	}
	var view OrgView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	event := requireSingleActivationEvent(t, telemetry)
	if event.Action != "organization_created" || event.OrgID != view.UUID || event.Actor != "user-123" {
		t.Fatalf("event identity = %#v, want action organization_created, org %q, actor user-123", event, view.UUID)
	}
	if outcome, ok := event.Properties["outcome"]; !ok || outcome != "success" {
		t.Fatalf("event properties = %#v, want outcome=success", event.Properties)
	}
	if event.OccurredAt.IsZero() || event.OccurredAt.Location() != time.UTC {
		t.Fatalf("occurred_at = %v, want explicit UTC timestamp", event.OccurredAt)
	}
	if !ops.orgWorkspaces[view.UUID] || ops.orgMemberships[view.UUID]["user-123"] != tenancyv1alpha1.MembershipRoleAdmin {
		t.Fatalf("authoritative org setup incomplete: workspaces=%v memberships=%v", ops.orgWorkspaces, ops.orgMemberships)
	}
}

func TestCreateOrgDoesNotTrackOnAuthoritativePartialFailures(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*activationOps, dynamic.Interface)
	}{
		{name: "organization CR", configure: func(_ *activationOps, dyn dynamic.Interface) {
			dyn.(interface {
				PrependReactor(string, string, clientgotesting.ReactionFunc)
			}).PrependReactor("create", "organizations", func(clientgotesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("organization CR failed")
			})
		}},
		{name: "org workspace", configure: func(ops *activationOps, _ dynamic.Interface) {
			ops.ensureOrgWorkspaceErr = errors.New("workspace failed")
		}},
		{name: "org membership", configure: func(ops *activationOps, _ dynamic.Interface) {
			ops.ensureOrgMembershipErr = errors.New("membership failed")
		}},
		{name: "UMI", configure: func(_ *activationOps, dyn dynamic.Interface) {
			dyn.(interface {
				PrependReactor(string, string, clientgotesting.ReactionFunc)
			}).PrependReactor("create", "usermembershipindices", func(clientgotesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("UMI failed")
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, baseOps, dyn := newTestManager(t)
			ops := &activationOps{fakeOps: baseOps}
			mgr.bootstrapper = ops
			telemetry := &activationTelemetryRecorder{}
			mgr.WithTelemetry(telemetry)
			tt.configure(ops, dyn)
			response := serveActivation(NewHandler(mgr).createOrg, activationRequest(t, http.MethodPost, "/api/orgs", tenant.TenantContext{User: "user-123"}, CreateOrgRequest{DisplayName: "Acme"}, nil))
			if response.Code == http.StatusCreated {
				t.Fatalf("status = 201 after %s failure", tt.name)
			}
			requireNoActivationEvents(t, telemetry)
		})
	}
}

func TestCreateWorkspaceTracksFullSuccessAndFallbackProjection(t *testing.T) {
	org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-123"}, Spec: tenancyv1alpha1.OrganizationSpec{DisplayName: "Acme", WorkspaceCreation: tenancyv1alpha1.WorkspaceCreationMembers}}
	user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "user-123"}, Spec: tenancyv1alpha1.UserSpec{RBACIdentity: "faros:user-123"}}
	mgr, baseOps, _ := newTestManager(t, org, user)
	ops := &activationOps{fakeOps: baseOps, getWorkspaceDisplayNameErr: errors.New("projection temporarily unavailable")}
	mgr.bootstrapper = ops
	telemetry := &activationTelemetryRecorder{}
	mgr.WithTelemetry(telemetry)
	response := serveActivation(NewHandler(mgr).createWorkspace, activationRequest(t, http.MethodPost, "/api/orgs/org-123/workspaces", adminTC("user-123", "org-123", ""), CreateWorkspaceRequest{DisplayName: "Dev"}, map[string]string{"org": "org-123"}))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body)
	}
	var view WorkspaceView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	event := requireSingleActivationEvent(t, telemetry)
	if event.Action != "workspace_created" || event.OrgID != "org-123" || event.WorkspaceID != view.UUID || event.Actor != "user-123" {
		t.Fatalf("event identity = %#v, want org org-123, workspace %q, actor user-123", event, view.UUID)
	}
	if event.Properties["outcome"] != "success" {
		t.Fatalf("event properties = %#v, want outcome=success", event.Properties)
	}
}

func TestCreateWorkspaceDoesNotTrackOnAuthoritativePartialFailures(t *testing.T) {
	org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-123"}, Spec: tenancyv1alpha1.OrganizationSpec{DisplayName: "Acme", WorkspaceCreation: tenancyv1alpha1.WorkspaceCreationMembers}}
	user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "user-123"}, Spec: tenancyv1alpha1.UserSpec{RBACIdentity: "faros:user-123"}}
	tests := []struct {
		name      string
		configure func(*activationOps, dynamic.Interface)
	}{
		{name: "child workspace", configure: func(ops *activationOps, _ dynamic.Interface) {
			ops.ensureChildWorkspaceErr = errors.New("child workspace failed")
		}},
		{name: "faros binding", configure: func(ops *activationOps, _ dynamic.Interface) {
			ops.ensureChildWorkspaceBindingErr = errors.New("binding failed")
		}},
		{name: "display name", configure: func(ops *activationOps, _ dynamic.Interface) {
			ops.setWorkspaceDisplayNameErr = errors.New("display name failed")
		}},
		{name: "admin grant", configure: func(ops *activationOps, _ dynamic.Interface) {
			ops.ensureChildWorkspaceAdminErr = errors.New("admin failed")
		}},
		{name: "default MCP", configure: func(ops *activationOps, _ dynamic.Interface) {
			ops.ensureChildWorkspaceMCPServerErr = errors.New("MCP failed")
		}},
		{name: "UMI", configure: func(_ *activationOps, dyn dynamic.Interface) {
			dyn.(interface {
				PrependReactor(string, string, clientgotesting.ReactionFunc)
			}).PrependReactor("create", "usermembershipindices", func(clientgotesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("UMI failed")
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, baseOps, dyn := newTestManager(t, org, user)
			ops := &activationOps{fakeOps: baseOps}
			mgr.bootstrapper = ops
			telemetry := &activationTelemetryRecorder{}
			mgr.WithTelemetry(telemetry)
			tt.configure(ops, dyn)
			response := serveActivation(NewHandler(mgr).createWorkspace, activationRequest(t, http.MethodPost, "/api/orgs/org-123/workspaces", adminTC("user-123", "org-123", ""), CreateWorkspaceRequest{DisplayName: "Dev"}, map[string]string{"org": "org-123"}))
			if response.Code == http.StatusCreated {
				t.Fatalf("status = 201 after %s failure", tt.name)
			}
			requireNoActivationEvents(t, telemetry)
		})
	}
}

func TestEnableProviderTracksPlatformSuccessWithStableIDs(t *testing.T) {
	mgr, baseOps, _ := newTestManager(t)
	mgr.bootstrapper = &activationOps{fakeOps: baseOps}
	registry := hubproviders.NewRegistry()
	registry.Upsert(hubproviders.Provider{Name: "app-studio", APIExportPath: "root:faros:providers:app-studio", APIExportName: "app-studio.providers.faros.sh", EdgeProxyAccess: true, WorkspaceCluster: "provider-cluster"})
	mgr.WithProviderRegistry(registry)
	telemetry := &activationTelemetryRecorder{}
	mgr.WithTelemetry(telemetry)
	response := serveActivation(NewHandler(mgr).enableProvider, activationRequest(t, http.MethodPost, "/api/orgs/org-123/workspaces/ws-123/providers/app-studio/enable", adminTC("user-123", "org-123", "ws-123"), EnableProviderRequest{}, map[string]string{"org": "org-123", "ws": "ws-123", "name": "app-studio"}))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body)
	}
	event := requireSingleActivationEvent(t, telemetry)
	if event.Action != "provider_enabled" || event.OrgID != "org-123" || event.WorkspaceID != "ws-123" || event.Actor != "user-123" || event.ResourceID != "app-studio" {
		t.Fatalf("event identity = %#v", event)
	}
	if event.Properties["provider"] != "app-studio" || event.Properties["outcome"] != "success" {
		t.Fatalf("event properties = %#v", event.Properties)
	}
	response = serveActivation(NewHandler(mgr).enableProvider, activationRequest(t, http.MethodPost, "/api/orgs/org-123/workspaces/ws-123/providers/app-studio/enable", adminTC("user-123", "org-123", "ws-123"), EnableProviderRequest{}, map[string]string{"org": "org-123", "ws": "ws-123", "name": "app-studio"}))
	if response.Code != http.StatusOK {
		t.Fatalf("idempotent status = %d, want 200; body=%s", response.Code, response.Body)
	}
	if got := len(telemetry.snapshot()); got != 1 {
		t.Fatalf("idempotent enable emitted %d events, want one transition event", got)
	}
}

func TestEnableProviderDoesNotTrackPartialSetupOrBYOProvider(t *testing.T) {
	tests := []struct {
		name      string
		provider  hubproviders.Provider
		configure func(*activationOps)
	}{
		{name: "API binding", provider: hubproviders.Provider{Name: "app-studio", APIExportPath: "root:faros:providers:app-studio", APIExportName: "app-studio.providers.faros.sh"}, configure: func(ops *activationOps) {
			ops.ensureProviderAPIBindingErr = errors.New("binding failed")
		}},
		{name: "edge proxy grant", provider: hubproviders.Provider{Name: "app-studio", APIExportPath: "root:faros:providers:app-studio", APIExportName: "app-studio.providers.faros.sh", EdgeProxyAccess: true, WorkspaceCluster: "provider-cluster"}, configure: func(ops *activationOps) {
			ops.ensureProviderEdgeProxyGrantErr = errors.New("grant failed")
		}},
		{name: "BYO provider", provider: hubproviders.Provider{Name: "app-studio", OrgUUID: "org-123", APIExportPath: "root:faros:tenants:org-123:providers:app-studio", APIExportName: "app-studio.providers.faros.sh"}, configure: func(_ *activationOps) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, baseOps, _ := newTestManager(t)
			ops := &activationOps{fakeOps: baseOps}
			mgr.bootstrapper = ops
			registry := hubproviders.NewRegistry()
			registry.Upsert(tt.provider)
			mgr.WithProviderRegistry(registry)
			telemetry := &activationTelemetryRecorder{}
			mgr.WithTelemetry(telemetry)
			tt.configure(ops)
			response := serveActivation(NewHandler(mgr).enableProvider, activationRequest(t, http.MethodPost, "/api/orgs/org-123/workspaces/ws-123/providers/app-studio/enable", adminTC("user-123", "org-123", "ws-123"), EnableProviderRequest{}, map[string]string{"org": "org-123", "ws": "ws-123", "name": "app-studio"}))
			if tt.name == "BYO provider" && response.Code != http.StatusOK {
				t.Fatalf("BYO status = %d, want 200; body=%s", response.Code, response.Body)
			}
			if tt.name != "BYO provider" && response.Code == http.StatusOK {
				t.Fatalf("status = 200 after %s failure", tt.name)
			}
			requireNoActivationEvents(t, telemetry)
		})
	}
}
