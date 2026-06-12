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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	aiv1alpha1 "github.com/faroshq/faros-kedge/apis/ai/v1alpha1"
	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
	"github.com/faroshq/faros-kedge/pkg/hub/tenant"
	projectstore "github.com/faroshq/faros-kedge/providers/projects/store"
)

// ===== fakes =====

type fakeOps struct {
	mu sync.Mutex

	// Storage
	orgWorkspaces     map[string]bool              // orgUUID set
	orgMemberships    map[string]map[string]string // orgUUID → user → role
	childWorkspaces   map[string]map[string]bool   // orgUUID → wsUUID set
	wsDisplayNames    map[wsKey]string             // (org,ws) → display
	wsDeletionAnnos   map[wsKey]time.Time          // (org,ws) → timestamp
	mcpServerCalls    map[wsKey]int                // (org,ws) → count
	kedgeBindingCalls map[wsKey]int                // (org,ws) → count
	workspaceAdmins   map[wsKey]map[string]bool    // (org,ws) → rbacIdentity set
}

type wsKey struct{ Org, WS string }

func newFakeOps() *fakeOps {
	return &fakeOps{
		orgWorkspaces:     map[string]bool{},
		orgMemberships:    map[string]map[string]string{},
		childWorkspaces:   map[string]map[string]bool{},
		wsDisplayNames:    map[wsKey]string{},
		wsDeletionAnnos:   map[wsKey]time.Time{},
		mcpServerCalls:    map[wsKey]int{},
		kedgeBindingCalls: map[wsKey]int{},
		workspaceAdmins:   map[wsKey]map[string]bool{},
	}
}

func (f *fakeOps) EnsureOrgWorkspace(_ context.Context, orgUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orgWorkspaces[orgUUID] = true
	return nil
}

func (f *fakeOps) EnsureOrgMembership(_ context.Context, orgUUID, userName, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orgMemberships[orgUUID] == nil {
		f.orgMemberships[orgUUID] = map[string]string{}
	}
	f.orgMemberships[orgUUID][userName] = role
	return nil
}

func (f *fakeOps) ListOrgMemberships(_ context.Context, orgUUID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.orgMemberships[orgUUID]))
	for u := range f.orgMemberships[orgUUID] {
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeOps) GetOrgMembershipRole(_ context.Context, orgUUID, userName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.orgMemberships[orgUUID]; ok {
		if role, ok := m[userName]; ok {
			return role, nil
		}
	}
	return "", fmt.Errorf("membership %s in org %s not found", userName, orgUUID)
}

func (f *fakeOps) PatchOrgMembershipRole(_ context.Context, orgUUID, userName, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.orgMemberships[orgUUID]; !ok {
		return fmt.Errorf("org %s not found", orgUUID)
	}
	f.orgMemberships[orgUUID][userName] = role
	return nil
}

func (f *fakeOps) DeleteOrgMembership(_ context.Context, orgUUID, userName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.orgMemberships[orgUUID]; ok {
		delete(m, userName)
	}
	return nil
}

func (f *fakeOps) EnsureChildWorkspace(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.childWorkspaces[orgUUID] == nil {
		f.childWorkspaces[orgUUID] = map[string]bool{}
	}
	f.childWorkspaces[orgUUID][wsUUID] = true
	return nil
}

func (f *fakeOps) EnsureChildWorkspaceKedgeBinding(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kedgeBindingCalls[wsKey{orgUUID, wsUUID}]++
	return nil
}

func (f *fakeOps) EnsureChildWorkspaceDefaultMCPServer(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mcpServerCalls[wsKey{orgUUID, wsUUID}]++
	return nil
}

// EnsureProviderAPIBinding is the test stub for the server-side
// provider-enable handler. The handler is exercised via its own
// dedicated tests; for the existing org/workspace flows it just needs
// to not error.
func (f *fakeOps) EnsureProviderAPIBinding(_ context.Context, _, _, _, _, _ string, _ []kcp.ProviderClaim) error {
	return nil
}

// ListProviderAPIBindings is the test stub for the read-side
// provider-enable handler. Returns empty so existing tests treat the
// workspace as having no enabled providers.
func (f *fakeOps) ListProviderAPIBindings(_ context.Context, _, _ string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (f *fakeOps) DeleteProviderAPIBinding(_ context.Context, _, _, _ string) error {
	return nil
}

func (f *fakeOps) EnsureProviderEdgeProxyGrant(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (f *fakeOps) RemoveProviderEdgeProxyGrant(_ context.Context, _, _, _ string) error {
	return nil
}

func (f *fakeOps) ListChildWorkspaces(_ context.Context, orgUUID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.childWorkspaces[orgUUID]))
	for ws := range f.childWorkspaces[orgUUID] {
		out = append(out, ws)
	}
	return out, nil
}

func (f *fakeOps) GetWorkspaceDisplayName(_ context.Context, orgUUID, wsUUID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.childWorkspaces[orgUUID][wsUUID]; !ok {
		return "", fmt.Errorf("workspace not found")
	}
	return f.wsDisplayNames[wsKey{orgUUID, wsUUID}], nil
}

func (f *fakeOps) SetWorkspaceDisplayName(_ context.Context, orgUUID, wsUUID, displayName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wsDisplayNames[wsKey{orgUUID, wsUUID}] = displayName
	return nil
}

func (f *fakeOps) GetWorkspaceDeletionRequestedAt(_ context.Context, orgUUID, wsUUID string) (*time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.wsDeletionAnnos[wsKey{orgUUID, wsUUID}]
	if !ok {
		return nil, false, nil
	}
	tt := t
	return &tt, true, nil
}

func (f *fakeOps) SetWorkspaceDeletionAnnotation(_ context.Context, orgUUID, wsUUID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wsDeletionAnnos[wsKey{orgUUID, wsUUID}] = at
	return nil
}

func (f *fakeOps) ClearWorkspaceDeletionAnnotation(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.wsDeletionAnnos, wsKey{orgUUID, wsUUID})
	return nil
}

func (f *fakeOps) GetChildWorkspaceClusterName(_ context.Context, orgUUID, wsUUID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.childWorkspaces[orgUUID][wsUUID]; !ok {
		return "", fmt.Errorf("workspace not found")
	}
	// Deterministic fake cluster name; the kubeconfig handler only needs a
	// non-empty value to build a valid /clusters/<name> URL in tests.
	return "fake-" + wsUUID, nil
}

func (f *fakeOps) EnsureChildWorkspaceAdmin(_ context.Context, orgUUID, wsUUID, rbacIdentity string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.childWorkspaces[orgUUID][wsUUID]; !ok {
		return fmt.Errorf("workspace not found")
	}
	if f.workspaceAdmins == nil {
		f.workspaceAdmins = map[wsKey]map[string]bool{}
	}
	key := wsKey{orgUUID, wsUUID}
	if f.workspaceAdmins[key] == nil {
		f.workspaceAdmins[key] = map[string]bool{}
	}
	f.workspaceAdmins[key][rbacIdentity] = true
	return nil
}

// ===== test fixtures =====

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

func newTestManager(t *testing.T, objects ...runtime.Object) (*Manager, *fakeOps, dynamic.Interface) {
	t.Helper()
	scheme := newTestScheme(t)
	gvrToListKind := map[schema.GroupVersionResource]string{
		kedgeclient.OrganizationGVR:        "OrganizationList",
		kedgeclient.UserGVR:                "UserList",
		kedgeclient.UserMembershipIndexGVR: "UserMembershipIndexList",
		kedgeclient.ProjectGVR:             "ProjectList",
	}
	// Use the customListKinds variant with no seed objects, then seed
	// via the dynamic client so the GVR/Kind mapping is exercised
	// the same way our handlers exercise it (via Create on the
	// underlying client). Lets us seed UMI objects whose default
	// pluralization (usermembershipindexs) wouldn't match the
	// canonical GVR (usermembershipindices).
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
	client := kedgeclient.NewFromDynamic(dyn)
	for _, obj := range objects {
		seedObject(t, client, obj)
	}
	ops := newFakeOps()
	mgr := NewManager(client, ops)
	mgr.WithProjectMessageStore(projectstore.NewMemoryStore())
	return mgr, ops, dyn
}

// seedObject writes a fixture into the fake via the typed client
// surface so the GVR mapping is identical to what handlers use.
func seedObject(t *testing.T, client *kedgeclient.Client, obj runtime.Object) {
	t.Helper()
	ctx := context.Background()
	switch o := obj.(type) {
	case *tenancyv1alpha1.Organization:
		if _, err := client.Organizations().Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding Organization: %v", err)
		}
	case *tenancyv1alpha1.User:
		if _, err := client.Users().Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding User: %v", err)
		}
	case *tenancyv1alpha1.UserMembershipIndex:
		if _, err := client.UserMembershipIndices().Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding UMI: %v", err)
		}
	case *aiv1alpha1.Project:
		if _, err := client.Projects().Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding Project: %v", err)
		}
	default:
		t.Fatalf("seedObject: unsupported type %T", obj)
	}
}

func attachProjectClient(t *testing.T, mgr *Manager, objects ...runtime.Object) *kedgeclient.Client {
	t.Helper()
	scheme := newTestScheme(t)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kedgeclient.ProjectGVR: "ProjectList",
	})
	dyn.PrependReactor("update", "projects/status", func(action clienttesting.Action) (bool, runtime.Object, error) {
		updateAction, ok := action.(clienttesting.UpdateAction)
		if !ok {
			return false, nil, nil
		}
		obj := updateAction.GetObject()
		if err := dyn.Tracker().Update(kedgeclient.ProjectGVR, obj, ""); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	c := kedgeclient.NewFromDynamic(dyn)
	for _, obj := range objects {
		seedObject(t, c, obj)
	}
	mgr.WithProjectClientFactory(func(context.Context, string, string) (*kedgeclient.Client, error) {
		return c, nil
	})
	return c
}

func newTestServer(t *testing.T, mgr *Manager, tc tenant.TenantContext) *httptest.Server {
	t.Helper()
	h := NewHandler(mgr)
	r := mux.NewRouter()

	userOnly := r.PathPrefix("/api").Subrouter()
	userOnly.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{User: tc.User})))
		})
	})
	h.RegisterUserOnly(userOnly)

	tenantSub := r.PathPrefix("/api/orgs").Subrouter()
	tenantSub.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), tc)))
		})
	})
	h.RegisterTenantScoped(tenantSub)
	return httptest.NewServer(r)
}

func adminTC(user, org, ws string) tenant.TenantContext {
	return tenant.TenantContext{User: user, OrgUUID: org, WorkspaceUUID: ws, Role: tenancyv1alpha1.MembershipRoleAdmin}
}

func memberTC(user, org, ws string) tenant.TenantContext {
	return tenant.TenantContext{User: user, OrgUUID: org, WorkspaceUUID: ws, Role: tenancyv1alpha1.MembershipRoleMember}
}

func parseProjectStreamEvents(raw []byte) []projectMessageStreamEvent {
	var events []projectMessageStreamEvent
	parts := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n\n")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lines := strings.Split(part, "\n")
		eventType := ""
		data := ""
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event:") {
				eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, "data:") {
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if data == "" {
			continue
		}
		var event projectMessageStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		if event.Type == "" && eventType != "" {
			event.Type = eventType
		}
		events = append(events, event)
	}
	return events
}

func writeChatStream(t *testing.T, w http.ResponseWriter, events ...map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal stream event: %v", err)
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func chatStreamContent(content string) map[string]any {
	return map[string]any{
		"choices": []map[string]any{{
			"delta": map[string]any{"content": content},
		}},
	}
}

func chatStreamToolCall(name, arguments string, extra map[string]any) map[string]any {
	toolCall := map[string]any{
		"index": 0,
		"id":    "tool-1",
		"type":  "function",
		"function": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}
	if extra != nil {
		toolCall["extra_content"] = extra
	}
	return map[string]any{
		"choices": []map[string]any{{
			"delta": map[string]any{
				"tool_calls": []map[string]any{toolCall},
			},
		}},
	}
}

func postProjectMessageStream(t *testing.T, serverURL, project, content string) (int, []projectMessageStreamEvent, string) {
	t.Helper()
	body, _ := json.Marshal(CreateProjectMessageRequest{Content: content})
	req, _ := http.NewRequest(http.MethodPost, serverURL+"/api/orgs/org-a/workspaces/ws-1/projects/"+project+"/messages/stream", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST message stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, parseProjectStreamEvents(payload), string(payload)
}

func projectStreamDoneItems(t *testing.T, events []projectMessageStreamEvent) []aiv1alpha1.ProjectMessage {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("expected stream events, got none")
	}
	last := events[len(events)-1]
	if last.Type != "done" {
		t.Fatalf("expected final done event, got %#v", last)
	}
	var assistantID string
	var content strings.Builder
	for _, event := range events {
		if event.Type == "chunk" {
			if assistantID == "" {
				assistantID = event.AssistantMessageID
			}
			content.WriteString(event.Content)
		}
	}
	if assistantID == "" && last.AssistantMessageID != "" {
		assistantID = last.AssistantMessageID
	}
	if assistantID == "" {
		return nil
	}
	return []aiv1alpha1.ProjectMessage{{
		ID:        assistantID,
		Role:      aiv1alpha1.ProjectMessageRoleAssistant,
		Content:   content.String(),
		CreatedAt: metav1.Time{Time: time.Now().UTC()},
	}}
}

func testGoogleServiceAccountJSON(t *testing.T, projectID, tokenURI string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate service account test key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal service account test key: %v", err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	payload := map[string]string{
		"type":         "service_account",
		"project_id":   projectID,
		"private_key":  privateKey,
		"client_email": "projects-test@" + projectID + ".iam.gserviceaccount.com",
		"token_uri":    tokenURI,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal service account test JSON: %v", err)
	}
	return string(raw)
}

// ===== Org tests =====

func TestListOrgs_SuppressesSoftDeleted(t *testing.T) {
	now := metav1.NewTime(time.Now())
	umi := &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec: tenancyv1alpha1.UserMembershipIndexSpec{
			Entries: []tenancyv1alpha1.MembershipIndexEntry{
				{OrgUUID: "org-a", OrgDisplayName: "A", Role: "admin"},
				{OrgUUID: "org-b", OrgDisplayName: "B", Role: "member", SoftDeletedAt: &now},
			},
		},
	}
	mgr, _, _ := newTestManager(t, umi)
	srv := newTestServer(t, mgr, adminTC("alice", "", ""))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/orgs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	var list ListResponse[OrgView]
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].UUID != "org-a" {
		t.Errorf("Items: %#v", list.Items)
	}
}

func TestCreateOrg_ValidatesAndPersists(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	srv := newTestServer(t, mgr, adminTC("alice", "", ""))
	defer srv.Close()

	// missing displayName → 400
	body, _ := json.Marshal(CreateOrgRequest{})
	resp, _ := http.Post(srv.URL+"/api/orgs", "application/json", jsonBody(body))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing displayName: got %d, want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// happy path
	body, _ = json.Marshal(CreateOrgRequest{DisplayName: "acme"})
	resp, _ = http.Post(srv.URL+"/api/orgs", "application/json", jsonBody(body))
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("happy: got %d, want 201", resp.StatusCode)
	}
	var view OrgView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = resp.Body.Close()
	if view.DisplayName != "acme" || view.UUID == "" {
		t.Errorf("view: %#v", view)
	}
	if !ops.orgWorkspaces[view.UUID] {
		t.Error("EnsureOrgWorkspace not called")
	}
	if ops.orgMemberships[view.UUID]["alice"] != "admin" {
		t.Errorf("alice's membership: %v", ops.orgMemberships[view.UUID])
	}
}

func TestDeleteAndUndeleteOrg(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	mgr, _, _ := newTestManager(t, org)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("DELETE status: got %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Confirm DeletionRequestedAt set.
	got, _ := mgr.client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if got.Status.DeletionRequestedAt == nil {
		t.Error("expected DeletionRequestedAt set")
	}

	// Undelete
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/orgs/org-a/undelete", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("undelete: %v", err)
	}
	_ = resp.Body.Close()
	got, _ = mgr.client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if got.Status.DeletionRequestedAt != nil {
		t.Error("expected DeletionRequestedAt cleared")
	}
}

func TestDeleteOrg_RequiresAdmin(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	mgr, _, _ := newTestManager(t, org)
	srv := newTestServer(t, mgr, memberTC("alice", "org-a", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// ===== Workspace tests =====

func TestCreateWorkspace_RestrictedToAdmin(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName: "A", WorkspaceCreation: tenancyv1alpha1.WorkspaceCreationAdmin,
		},
	}
	mgr, _, _ := newTestManager(t, org)
	srv := newTestServer(t, mgr, memberTC("bob", "org-a", ""))
	defer srv.Close()

	body, _ := json.Marshal(CreateWorkspaceRequest{DisplayName: "ws"})
	resp, _ := http.Post(srv.URL+"/api/orgs/org-a/workspaces", "application/json", jsonBody(body))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestCreateWorkspace_HappyPath(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName:       "A",
			WorkspaceCreation: tenancyv1alpha1.WorkspaceCreationMembers,
		},
	}
	alice := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec:       tenancyv1alpha1.UserSpec{Email: "alice@example.com", RBACIdentity: "kedge:alice@example.com"},
	}
	mgr, ops, _ := newTestManager(t, org, alice)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	body, _ := json.Marshal(CreateWorkspaceRequest{DisplayName: "platform"})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/workspaces", "application/json", jsonBody(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", resp.StatusCode)
	}
	var view WorkspaceView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.DisplayName != "platform" || view.UUID == "" {
		t.Errorf("view: %#v", view)
	}
	if !ops.childWorkspaces["org-a"][view.UUID] {
		t.Error("EnsureChildWorkspace not called")
	}
	if ops.kedgeBindingCalls[wsKey{"org-a", view.UUID}] != 1 {
		t.Errorf("kedge binding call count: got %d", ops.kedgeBindingCalls[wsKey{"org-a", view.UUID}])
	}
	if ops.wsDisplayNames[wsKey{"org-a", view.UUID}] != "platform" {
		t.Errorf("display name not set: %v", ops.wsDisplayNames)
	}
	// Regression guard for the v0.0.63 workspace-switch 403: createWorkspace
	// must seed the caller's cluster-admin CRB; without it the freshly-
	// minted workspace 403s from the GraphQL gateway the moment the user
	// switches into it.
	if !ops.workspaceAdmins[wsKey{"org-a", view.UUID}]["kedge:alice@example.com"] {
		t.Errorf("EnsureChildWorkspaceAdmin not called for caller; admins=%v",
			ops.workspaceAdmins[wsKey{"org-a", view.UUID}])
	}
}

func TestDeleteWorkspace_SetsAnnotation(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureChildWorkspace(context.Background(), "org-a", "ws-1")
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/workspaces/ws-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
	if _, ok := ops.wsDeletionAnnos[wsKey{"org-a", "ws-1"}]; !ok {
		t.Error("deletion annotation not set")
	}
}

// ===== Project tests =====

func TestWriteProjectError_InitializingDiscoveryMiss(t *testing.T) {
	err := &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Reason:  metav1.StatusReasonNotFound,
		Message: "the server could not find the requested resource",
		Code:    http.StatusNotFound,
	}}

	rec := httptest.NewRecorder()
	writeProjectError(rec, err)

	resp := rec.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
	if got := resp.Header.Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["reason"] != "ServiceUnavailable" {
		t.Fatalf("reason = %v, want ServiceUnavailable", body["reason"])
	}
}

func TestNormalizeLLMBaseURLForGoogleAIStudio(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "google generateContent model URL",
			raw:  "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.5-flash:generateContent",
			want: "https://generativelanguage.googleapis.com/v1beta/openai",
		},
		{
			name: "strip chat completions",
			raw:  "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
			want: "https://generativelanguage.googleapis.com/v1beta/openai",
		},
		{
			name: "non-google URL unchanged",
			raw:  "https://api.openai.com/v1/",
			want: "https://api.openai.com/v1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeLLMBaseURL(tc.raw)
			if err != nil {
				t.Fatalf("normalizeLLMBaseURL(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeLLMBaseURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestProjects_LLMSettingsGoogleProviderNormalizesCredentialFormats(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	provider := projectLLMProviderGoogle
	model := "google-model"
	baseURL := defaultProjectLLMBaseURL
	apiKey := "test-gemini-key"
	settingsBody, _ := json.Marshal(PatchProjectLLMSettingsRequest{
		Provider: &provider,
		BaseURL:  &baseURL,
		Model:    &model,
		APIKey:   &apiKey,
	})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH google LLM settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("settings status: got %d, want 200: %s", resp.StatusCode, string(payload))
	}
	var got ProjectLLMSettingsView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	_ = resp.Body.Close()
	if got.Provider != projectLLMProviderGoogle {
		t.Fatalf("provider: %#v", got)
	}
	if got.BaseURL != defaultProjectLLMGoogleBaseURL {
		t.Fatalf("baseURL: got %q, want %q", got.BaseURL, defaultProjectLLMGoogleBaseURL)
	}
	if !got.Configured {
		t.Fatalf("expected configured google settings: %#v", got)
	}

	serviceAccountJSON := testGoogleServiceAccountJSON(t, "svc-project", "https://oauth2.googleapis.com/token")
	settingsBody, _ = json.Marshal(PatchProjectLLMSettingsRequest{
		Provider: &provider,
		BaseURL:  &baseURL,
		Model:    &model,
		APIKey:   &serviceAccountJSON,
	})
	req, _ = http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH service-account google LLM settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("service-account settings status: got %d, want 200: %s", resp.StatusCode, string(payload))
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode service-account settings: %v", err)
	}
	_ = resp.Body.Close()
	wantCloudBaseURL := "https://aiplatform.googleapis.com/v1/projects/svc-project/locations/global/endpoints/openapi"
	if got.BaseURL != wantCloudBaseURL {
		t.Fatalf("service-account baseURL: got %q, want %q", got.BaseURL, wantCloudBaseURL)
	}
	if !got.Configured {
		t.Fatalf("expected configured service-account google settings: %#v", got)
	}

	cases := []struct {
		name string
		key  string
	}{
		{
			name: "invalid-service-account-json",
			key:  `{"type":"service_account","client_email":"svc@example.iam.gserviceaccount.com"}`,
		},
		{
			name: "jwt",
			key:  "eyJhbGciOiJSUzI1NiIsImtpZCI6IjEifQ.eyJzdWIiOiJmb28ifQ.signature",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			badKey := tc.key
			settingsBody, _ := json.Marshal(PatchProjectLLMSettingsRequest{
				Provider: &provider,
				BaseURL:  &baseURL,
				Model:    &model,
				APIKey:   &badKey,
			})
			req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PATCH google LLM settings: %v", err)
			}
			payload, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("settings status: got %d, want 400: %s", resp.StatusCode, string(payload))
			}
			if !strings.Contains(string(payload), "service-account JSON") && !strings.Contains(string(payload), "OAuth/JWT token") {
				t.Fatalf("expected Google credential validation message, got: %s", string(payload))
			}
		})
	}

	dottedAPIKey := "opaque.part.value"
	settingsBody, _ = json.Marshal(PatchProjectLLMSettingsRequest{
		Provider: &provider,
		BaseURL:  &baseURL,
		Model:    &model,
		APIKey:   &dottedAPIKey,
	})
	req, _ = http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH dotted google LLM settings: %v", err)
	}
	payload, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dotted API key status: got %d, want 200: %s", resp.StatusCode, string(payload))
	}
}

func TestProjects_CreateListGetDelete(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	body, _ := json.Marshal(CreateProjectRequest{
		DisplayName: "Customer Portal",
		Description: "Internal customer management application",
	})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects", "application/json", jsonBody(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status: got %d, want 201", resp.StatusCode)
	}
	var created ProjectView
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	_ = resp.Body.Close()
	if created.Name != "customer-portal" || created.DisplayName != "Customer Portal" {
		t.Fatalf("created project: %#v", created)
	}
	if created.Memory.Goals == nil || created.Memory.Requirements == nil || created.Memory.Constraints == nil {
		t.Fatalf("expected default empty memory arrays, got %#v", created.Memory)
	}

	resp, err = http.Get(srv.URL + "/api/orgs/org-a/workspaces/ws-1/projects")
	if err != nil {
		t.Fatalf("GET list: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("list status: got %d, want 200: %s", resp.StatusCode, string(body))
	}
	var list ListResponse[ProjectView]
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	_ = resp.Body.Close()
	if len(list.Items) != 1 || list.Items[0].Name != "customer-portal" {
		t.Fatalf("list items: %#v", list.Items)
	}

	resp, err = http.Get(srv.URL + "/api/orgs/org-a/workspaces/ws-1/projects/customer-portal")
	if err != nil {
		t.Fatalf("GET project: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status: got %d, want 200", resp.StatusCode)
	}
	var got ProjectView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	_ = resp.Body.Close()
	if got.Description != "Internal customer management application" {
		t.Fatalf("get project: %#v", got)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/customer-portal", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status: got %d, want 204", resp.StatusCode)
	}
}

func TestProjects_MessageStreamStreamsAssistantWithLLM(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-portal"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Customer Portal",
			Description: "Internal customer management application",
			Memory: aiv1alpha1.ProjectMemory{
				Goals:        []string{"ship an MVP"},
				Requirements: []string{"persistent chat"},
				Constraints:  []string{"no deployment UI"},
			},
		},
	}
	var sawPrompt bool
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode LLM request: %v", err)
		}
		for _, msg := range req.Messages {
			if msg.Role == "system" && strings.Contains(msg.Content, "ship an MVP") && strings.Contains(msg.Content, "no deployment UI") {
				sawPrompt = true
			}
		}
		writeChatStream(t, w, chatStreamContent("Build the lead inbox next."))
	}))
	defer llmSrv.Close()

	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr, project)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	baseURL := llmSrv.URL
	model := "test-model"
	apiKey := "test-key"
	settingsBody, _ := json.Marshal(PatchProjectLLMSettingsRequest{
		BaseURL: &baseURL,
		Model:   &model,
		APIKey:  &apiKey,
	})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH LLM settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("settings status: got %d, want 200: %s", resp.StatusCode, string(payload))
	}
	_ = resp.Body.Close()

	body, _ := json.Marshal(CreateProjectMessageRequest{Content: "What should we build next?"})
	req, _ = http.NewRequest(
		http.MethodPost,
		srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/customer-portal/messages/stream",
		jsonBody(body),
	)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST message stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("message stream status: got %d, want 200: %s", resp.StatusCode, string(payload))
	}
	payload, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	events := parseProjectStreamEvents(payload)
	if len(events) < 2 {
		t.Fatalf("expected chunk and done events, got %#v", events)
	}
	if events[0].Type != "chunk" || events[0].Content == "" || events[0].AssistantMessageID == "" {
		t.Fatalf("unexpected first stream event: %#v", events[0])
	}
	if events[len(events)-1].Type != "done" {
		t.Fatalf("expected final done event, got %#v", events[len(events)-1])
	}
	if events[len(events)-1].AssistantMessageID == "" {
		t.Fatalf("expected assistant message id in done event, got %#v", events[len(events)-1])
	}

	resp, err = http.Get(srv.URL + "/api/orgs/org-a/workspaces/ws-1/projects/customer-portal/messages")
	if err != nil {
		t.Fatalf("GET messages: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("messages status: got %d, want 200: %s", resp.StatusCode, string(payload))
	}
	var persisted ProjectMessagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&persisted); err != nil {
		t.Fatalf("decode persisted messages: %v", err)
	}
	_ = resp.Body.Close()
	if len(persisted.Items) != 2 {
		t.Fatalf("expected 2 persisted messages, got %#v", persisted.Items)
	}
	if persisted.Items[0].Role != aiv1alpha1.ProjectMessageRoleUser || persisted.Items[1].Role != aiv1alpha1.ProjectMessageRoleAssistant {
		t.Fatalf("unexpected roles in persisted messages: %#v", persisted.Items)
	}
	if persisted.Items[1].Content != "Build the lead inbox next." {
		t.Fatalf("persisted assistant content mismatch: %#v", persisted.Items[1])
	}
	if !sawPrompt {
		t.Fatalf("LLM request did not include project metadata and memory")
	}
}

func TestProjects_AssistantStoredContentPrefersStreamedContent(t *testing.T) {
	if got, want := projectAssistantStoredContent("final reply", "streamed reply"), "streamed reply"; got != want {
		t.Fatalf("stored content = %q, want %q", got, want)
	}
	if got, want := projectAssistantStoredContent("final reply", "   "), "final reply"; got != want {
		t.Fatalf("stored content fallback = %q, want %q", got, want)
	}
}

func TestProjects_ShouldPersistInterruptedAssistantWithPartialContent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if !shouldPersistInterruptedProjectAssistant(ctx, context.Canceled, nil, "partial answer") {
		t.Fatal("expected canceled partial assistant content to persist")
	}
	if shouldPersistInterruptedProjectAssistant(ctx, context.Canceled, nil, "   ") {
		t.Fatal("did not expect blank interrupted assistant content to persist")
	}
}

func TestProjects_AppendInterruptedAssistantMessagePersistsStatus(t *testing.T) {
	store := projectstore.NewMemoryStore()
	scope := projectstore.Scope{
		OrgUUID:       "org-a",
		WorkspaceUUID: "ws-1",
		ProjectName:   "customer-portal",
	}

	if err := appendInterruptedProjectAssistantMessage(context.Background(), store, scope, "msg-assistant", "partial answer"); err != nil {
		t.Fatalf("append interrupted assistant: %v", err)
	}
	page, err := store.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected one message, got %#v", page.Items)
	}
	msg := page.Items[0]
	if msg.Content != "partial answer" {
		t.Fatalf("content = %q, want partial answer", msg.Content)
	}
	if got, want := msg.Metadata[projectMessageMetadataStatus], any(projectMessageStatusInterrupted); got != want {
		t.Fatalf("status metadata = %#v, want %#v", got, want)
	}
}

func TestProjects_MessageStreamPersistsUserMessageWhenLLMFails(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-portal"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Customer Portal",
			Description: "Internal customer management application",
		},
	}
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "LLM unavailable", http.StatusInternalServerError)
	}))
	defer llmSrv.Close()

	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr, project)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	baseURL := llmSrv.URL
	model := "test-model"
	apiKey := "test-key"
	settingsBody, _ := json.Marshal(PatchProjectLLMSettingsRequest{
		BaseURL: &baseURL,
		Model:   &model,
		APIKey:  &apiKey,
	})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH LLM settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("settings status: got %d, want 200: %s", resp.StatusCode, string(body))
	}
	_ = resp.Body.Close()

	body, _ := json.Marshal(CreateProjectMessageRequest{Content: "What should we build next?"})
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/customer-portal/messages/stream", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST message stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("message stream status: got %d, want 200: %s", resp.StatusCode, string(payload))
	}
	payload, _ := io.ReadAll(resp.Body)
	events := parseProjectStreamEvents(payload)
	if len(events) != 1 || events[0].Type != "error" {
		t.Fatalf("expected single error event, got %#v", events)
	}
	if !strings.Contains(events[0].Error, "assistant generation failed") {
		t.Fatalf("unexpected stream error: %#v", events[0])
	}

	resp, err = http.Get(srv.URL + "/api/orgs/org-a/workspaces/ws-1/projects/customer-portal/messages")
	if err != nil {
		t.Fatalf("GET messages: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("messages status: got %d, want 200", resp.StatusCode)
	}
	var persisted ProjectMessagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&persisted); err != nil {
		t.Fatalf("decode persisted messages: %v", err)
	}
	if len(persisted.Items) != 1 {
		t.Fatalf("expected only user message to persist, got %#v", persisted.Items)
	}
	if persisted.Items[0].Role != aiv1alpha1.ProjectMessageRoleUser || persisted.Items[0].Content != "What should we build next?" {
		t.Fatalf("persisted user message mismatch: %#v", persisted.Items[0])
	}
}

func TestProjects_NonStreamingMessageCreateRouteRemoved(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-portal"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Customer Portal",
		},
	}
	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr, project)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	body, _ := json.Marshal(CreateProjectMessageRequest{Content: "What should we build next?"})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/customer-portal/messages", "application/json", jsonBody(body))
	if err != nil {
		t.Fatalf("POST legacy message route: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("legacy route status: got %d, want 404: %s", resp.StatusCode, string(payload))
	}
}

func TestProjects_MessageStreamUsesMCPToolsWhenAvailable(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-portal"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Customer Portal",
			Description: "Internal customer management application",
			Memory: aiv1alpha1.ProjectMemory{
				Goals:        []string{"ship an MVP"},
				Requirements: []string{"persistent chat"},
				Constraints:  []string{"no deployment UI"},
			},
		},
	}

	mcpPath := "/services/mcpserver/root:kedge:orgs:org-a:ws-1/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp"
	listCalled := 0
	callCalled := 0
	var toolInput map[string]any
	mcpSrv := http.NewServeMux()
	mcpSrv.HandleFunc(mcpPath, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		switch req.Method {
		case "tools/list":
			listCalled++
			writeJSON(w, http.StatusOK, map[string]any{
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "get_customer",
						"description": "Echo text back",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"text": map[string]any{"type": "string"},
							},
							"required": []string{"text"},
						},
					}},
				},
			})
		case "tools/call":
			callCalled++
			var params struct {
				Arguments map[string]any `json:"arguments"`
				Name      string         `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &params)
			toolInput = params.Arguments
			writeJSON(w, http.StatusOK, map[string]any{
				"result": map[string]any{
					"content": []map[string]string{{
						"type": "text",
						"text": "tool-result:" + fmt.Sprint(params.Arguments["text"]),
					}},
				},
			})
		default:
			t.Fatalf("unexpected mcp method %q", req.Method)
		}
	})

	llmStage := 0
	var sawToolChoice bool
	var sawToolMessage bool
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode LLM request: %v", err)
		}
		if llmStage == 0 {
			if len(req.Tools) == 0 || req.ToolChoice != "auto" {
				t.Fatalf("expected MCP tools + tool_choice on first request")
			}
			sawToolChoice = true
			for _, m := range req.Messages {
				if m.Role == aiv1alpha1.ProjectMessageRoleUser && m.Content == "What should we build next?" {
					break
				}
			}
			writeChatStream(t, w, chatStreamToolCall("get_customer", `{"text":"customer"}`, nil))
			llmStage++
			return
		}

		for _, msg := range req.Messages {
			if msg.Role == "tool" && msg.Content == "tool-result:customer" {
				sawToolMessage = true
			}
		}
		writeChatStream(t, w, chatStreamContent("Replied using tool data."))
	}))
	defer llmSrv.Close()

	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr, project)
	baseURL := llmSrv.URL
	model := "test-model"
	apiKey := "test-key"
	settingsBody, _ := json.Marshal(PatchProjectLLMSettingsRequest{
		BaseURL: &baseURL,
		Model:   &model,
		APIKey:  &apiKey,
	})

	router := mux.NewRouter()
	h := NewHandler(mgr)

	userOnly := router.PathPrefix("/api").Subrouter()
	userOnly.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{User: "alice"})))
		})
	})
	h.RegisterUserOnly(userOnly)

	tenantSub := router.PathPrefix("/api/orgs").Subrouter()
	tenantSub.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), adminTC("alice", "org-a", "ws-1"))))
		})
	})
	h.RegisterTenantScoped(tenantSub)
	router.PathPrefix("/services").Handler(mcpSrv)

	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH LLM settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("settings status: got %d, want 200: %s", resp.StatusCode, string(payload))
	}
	_ = resp.Body.Close()

	status, events, payload := postProjectMessageStream(t, srv.URL, "customer-portal", "What should we build next?")
	if status != http.StatusOK {
		t.Fatalf("message stream status: got %d, want 200: %s", status, payload)
	}
	items := projectStreamDoneItems(t, events)

	if len(items) != 1 {
		t.Fatalf("expected synthesized assistant message, got %#v", items)
	}
	if items[0].Content != "Replied using tool data." {
		t.Fatalf("unexpected assistant reply: %#v", items[0])
	}
	if listCalled != 1 {
		t.Fatalf("expected exactly one tools/list, got %d", listCalled)
	}
	if callCalled != 1 {
		t.Fatalf("expected exactly one tools/call, got %d", callCalled)
	}
	if !sawToolChoice {
		t.Fatalf("did not observe tool-enabled first LLM request")
	}
	if !sawToolMessage {
		t.Fatalf("did not observe tool message in second LLM request")
	}
	if toolInput["text"] != "customer" {
		t.Fatalf("tool input mismatch: %#v", toolInput)
	}
}

func TestProjects_MessageStreamUsesMCPToolsForGoogleProvider(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-portal"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Customer Portal",
			Description: "Internal customer management application",
		},
	}
	listCalled := 0
	callCalled := 0
	var toolInput map[string]any
	mcpPath := "/services/mcpserver/root:kedge:orgs:org-a:ws-1/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp"
	mcpSrv := http.NewServeMux()
	mcpSrv.HandleFunc(mcpPath, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode mcp request: %v", err)
		}
		switch req.Method {
		case "tools/list":
			listCalled++
			writeJSON(w, http.StatusOK, map[string]any{
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "get_customer",
						"description": "Echo text back",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"text": map[string]any{"type": "string"},
							},
							"required": []string{"text"},
						},
					}},
				},
			})
		case "tools/call":
			callCalled++
			var params struct {
				Arguments map[string]any `json:"arguments"`
				Name      string         `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &params)
			toolInput = params.Arguments
			writeJSON(w, http.StatusOK, map[string]any{
				"result": map[string]any{
					"content": []map[string]string{{
						"type": "text",
						"text": "tool-result:" + fmt.Sprint(params.Arguments["text"]),
					}},
				},
			})
		default:
			t.Fatalf("unexpected mcp method %q", req.Method)
		}
	})

	llmStage := 0
	var sawToolChoice bool
	var sawMetadata bool
	var sawToolMessage bool
	var sawThoughtSignature bool
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode LLM request: %v", err)
		}
		if req.Metadata != nil {
			sawMetadata = true
		}
		if llmStage == 0 {
			if len(req.Tools) == 0 {
				t.Fatalf("expected MCP tools on first request")
			}
			if req.ToolChoice != "auto" {
				t.Fatalf("expected tool_choice auto")
			}
			sawToolChoice = true
			for _, m := range req.Messages {
				if m.Role == aiv1alpha1.ProjectMessageRoleUser && m.Content == "What should we build next?" {
					break
				}
			}
			writeChatStream(t, w, chatStreamToolCall("get_customer", `{"text":"customer"}`, nil))
			llmStage++
			return
		}

		for _, msg := range req.Messages {
			if msg.Role == "tool" && msg.Content == "tool-result:customer" {
				sawToolMessage = true
			}
			if len(msg.ToolCalls) > 0 && msg.Role == aiv1alpha1.ProjectMessageRoleAssistant {
				if msg.ToolCalls[0].ExtraContent != nil {
					if google, ok := msg.ToolCalls[0].ExtraContent["google"].(map[string]any); ok {
						if google["thought_signature"] == googleThoughtSignatureSkipValue {
							sawThoughtSignature = true
						}
					}
				}
			}
		}
		writeChatStream(t, w, chatStreamContent("Replied using tool data."))
	}))
	defer llmSrv.Close()

	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr, project)

	router := mux.NewRouter()
	userOnly := router.PathPrefix("/api").Subrouter()
	userOnly.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{User: "alice"})))
		})
	})
	h := NewHandler(mgr)
	h.RegisterUserOnly(userOnly)

	tenantSub := router.PathPrefix("/api/orgs").Subrouter()
	tenantSub.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), adminTC("alice", "org-a", "ws-1"))))
		})
	})
	h.RegisterTenantScoped(tenantSub)
	router.PathPrefix("/services").Handler(mcpSrv)

	srv := httptest.NewServer(router)
	defer srv.Close()

	baseURL := llmSrv.URL + "/v1beta/openai"
	model := "google-model"
	provider := projectLLMProviderGoogle
	apiKey := "test-key"
	settingsBody, _ := json.Marshal(PatchProjectLLMSettingsRequest{
		BaseURL:  &baseURL,
		Model:    &model,
		Provider: &provider,
		APIKey:   &apiKey,
	})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH LLM settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("settings status: got %d, want 200: %s", resp.StatusCode, string(payload))
	}
	_ = resp.Body.Close()

	status, events, payload := postProjectMessageStream(t, srv.URL, "customer-portal", "What should we build next?")
	if status != http.StatusOK {
		t.Fatalf("message stream status: got %d, want 200: %s", status, payload)
	}
	items := projectStreamDoneItems(t, events)

	if len(items) != 1 {
		t.Fatalf("expected synthesized assistant message, got %#v", items)
	}
	if items[0].Content != "Replied using tool data." {
		t.Fatalf("unexpected assistant reply: %#v", items[0])
	}
	if listCalled != 1 {
		t.Fatalf("expected exactly one tools/list, got %d", listCalled)
	}
	if callCalled != 1 {
		t.Fatalf("expected exactly one tools/call, got %d", callCalled)
	}
	if !sawToolChoice {
		t.Fatalf("did not observe tool-enabled first LLM request")
	}
	if !sawToolMessage {
		t.Fatalf("did not observe tool message in second LLM request")
	}
	if !sawThoughtSignature {
		t.Fatalf("did not observe tool call thought signature in second LLM request")
	}
	if sawMetadata {
		t.Fatalf("expected google-compatible request to omit metadata")
	}
	if toolInput["text"] != "customer" {
		t.Fatalf("tool input mismatch: %#v", toolInput)
	}
}

func TestProjects_MessageStreamReportsMCPFailureInSystemPrompt(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-portal"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Customer Portal",
			Description: "Internal customer management application",
			Memory: aiv1alpha1.ProjectMemory{
				Goals:        []string{"ship an MVP"},
				Requirements: []string{"persistent chat"},
				Constraints:  []string{"no deployment UI"},
			},
		},
	}

	mcpPath := "/services/mcpserver/root:kedge:orgs:org-a:ws-1/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp"
	mcpSrv := http.NewServeMux()
	mcpSrv.HandleFunc(mcpPath, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		if req.Method != "tools/list" {
			t.Fatalf("expected only tools/list call, got %q", req.Method)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{
				"code":    1,
				"message": "mcp unavailable",
			},
		})
	})

	var sawSystemPrompt bool
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode LLM request: %v", err)
		}
		for _, m := range req.Messages {
			if m.Role == "system" && strings.Contains(m.Content, "MCP tool discovery failed") {
				sawSystemPrompt = true
			}
		}
		writeChatStream(t, w, chatStreamContent("I do not have external tools right now."))
	}))
	defer llmSrv.Close()

	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr, project)
	baseURL := llmSrv.URL
	model := "test-model"
	apiKey := "test-key"
	settingsBody, _ := json.Marshal(PatchProjectLLMSettingsRequest{
		BaseURL: &baseURL,
		Model:   &model,
		APIKey:  &apiKey,
	})

	router := mux.NewRouter()
	userOnly := router.PathPrefix("/api").Subrouter()
	userOnly.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{User: "alice"})))
		})
	})
	h := NewHandler(mgr)
	h.RegisterUserOnly(userOnly)

	tenantSub := router.PathPrefix("/api/orgs").Subrouter()
	tenantSub.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), adminTC("alice", "org-a", "ws-1"))))
		})
	})
	h.RegisterTenantScoped(tenantSub)
	router.PathPrefix("/services").Handler(mcpSrv)

	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH LLM settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("settings status: got %d, want 200: %s", resp.StatusCode, string(payload))
	}
	_ = resp.Body.Close()

	status, events, payload := postProjectMessageStream(t, srv.URL, "customer-portal", "What should we build next?")
	if status != http.StatusOK {
		t.Fatalf("message stream status: got %d, want 200: %s", status, payload)
	}
	items := projectStreamDoneItems(t, events)
	if len(items) != 1 {
		t.Fatalf("expected synthesized assistant message, got %d: %#v", len(items), items)
	}
	if !sawSystemPrompt {
		t.Fatalf("expected MCP discovery failure system prompt in LLM request")
	}
}

func TestProjects_MessageStreamReportsMCPToolCallFailureAsToolMessage(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-portal"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Customer Portal",
			Description: "Internal customer management application",
		},
	}

	listCalled := 0
	callCalled := 0
	var sawToolError bool
	mcpPath := "/services/mcpserver/root:kedge:orgs:org-a:ws-1/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp"
	mcpSrv := http.NewServeMux()
	mcpSrv.HandleFunc(mcpPath, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		switch req.Method {
		case "tools/list":
			listCalled++
			writeJSON(w, http.StatusOK, map[string]any{
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "list_targets",
						"description": "List deployment targets",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{},
						},
					}},
				},
			})
		case "tools/call":
			callCalled++
			writeJSON(w, http.StatusOK, map[string]any{
				"error": map[string]any{
					"code":    0,
					"message": "no edge name specified and no connected edges available",
				},
			})
		default:
			t.Fatalf("unexpected mcp method %q", req.Method)
		}
	})

	llmStage := 0
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode LLM request: %v", err)
		}
		if llmStage == 0 {
			writeChatStream(t, w, chatStreamToolCall("list_targets", `{}`, nil))
			llmStage++
			return
		}

		for _, m := range req.Messages {
			if m.Role == "tool" && strings.Contains(m.Content, "no edge name specified and no connected edges available") {
				sawToolError = true
			}
		}
		writeChatStream(t, w, chatStreamContent("No edges are connected yet."))
	}))
	defer llmSrv.Close()

	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr, project)
	baseURL := llmSrv.URL
	model := "test-model"
	apiKey := "test-key"
	settingsBody, _ := json.Marshal(PatchProjectLLMSettingsRequest{
		BaseURL: &baseURL,
		Model:   &model,
		APIKey:  &apiKey,
	})

	router := mux.NewRouter()
	userOnly := router.PathPrefix("/api").Subrouter()
	userOnly.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{User: "alice"})))
		})
	})
	h := NewHandler(mgr)
	h.RegisterUserOnly(userOnly)

	tenantSub := router.PathPrefix("/api/orgs").Subrouter()
	tenantSub.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), adminTC("alice", "org-a", "ws-1"))))
		})
	})
	h.RegisterTenantScoped(tenantSub)
	router.PathPrefix("/services").Handler(mcpSrv)

	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH LLM settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("settings status: got %d, want 200: %s", resp.StatusCode, string(payload))
	}
	_ = resp.Body.Close()

	status, events, payload := postProjectMessageStream(t, srv.URL, "customer-portal", "Show deployment targets")
	if status != http.StatusOK {
		t.Fatalf("message stream status: got %d, want 200: %s", status, payload)
	}
	items := projectStreamDoneItems(t, events)

	if len(items) != 1 {
		t.Fatalf("expected synthesized assistant message, got %#v", items)
	}
	if items[0].Content != "No edges are connected yet." {
		t.Fatalf("unexpected assistant reply: %#v", items[0])
	}
	if listCalled != 1 {
		t.Fatalf("expected exactly one tools/list, got %d", listCalled)
	}
	if callCalled != 1 {
		t.Fatalf("expected exactly one tools/call, got %d", callCalled)
	}
	if !sawToolError {
		t.Fatalf("expected tool-call error to be included in second LLM request")
	}
}

func TestProjects_MessageStreamGeneratesAssistantWithGoogleAIStudioCompatNoMetadata(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-portal"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Customer Portal",
			Description: "Internal customer management application",
		},
	}
	var sawMetadata bool
	mcpPath := "/services/mcpserver/root:kedge:orgs:org-a:ws-1/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp"
	mcpSrv := http.NewServeMux()
	mcpSrv.HandleFunc(mcpPath, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode mcp request: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"tools": []map[string]any{}}})
	})
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode LLM request: %v", err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization header = %q, want Bearer test-key", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "" {
			t.Fatalf("unexpected x-goog-api-key header: %q", got)
		}
		if req.Model != "google-model" {
			t.Errorf("model = %q, want google-model", req.Model)
		}
		if req.Metadata != nil {
			sawMetadata = true
		}
		writeChatStream(t, w, chatStreamContent("Build the lead inbox next."))
	}))
	defer llmSrv.Close()

	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr, project)
	router := mux.NewRouter()
	userOnly := router.PathPrefix("/api").Subrouter()
	userOnly.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{User: "alice"})))
		})
	})
	h := NewHandler(mgr)
	h.RegisterUserOnly(userOnly)

	tenantSub := router.PathPrefix("/api/orgs").Subrouter()
	tenantSub.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), adminTC("alice", "org-a", "ws-1"))))
		})
	})
	h.RegisterTenantScoped(tenantSub)
	router.PathPrefix("/services").Handler(mcpSrv)

	srv := httptest.NewServer(router)
	defer srv.Close()

	baseURL := llmSrv.URL + "/v1beta/openai"
	model := "google-model"
	apiKey := "test-key"
	provider := projectLLMProviderGoogle
	settingsBody, _ := json.Marshal(PatchProjectLLMSettingsRequest{
		BaseURL:  &baseURL,
		Model:    &model,
		Provider: &provider,
		APIKey:   &apiKey,
	})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH LLM settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("settings status: got %d, want 200: %s", resp.StatusCode, string(body))
	}
	_ = resp.Body.Close()

	status, events, payload := postProjectMessageStream(t, srv.URL, "customer-portal", "What should we build next?")
	if status != http.StatusOK {
		t.Fatalf("message stream status: got %d, want 200: %s", status, payload)
	}
	_ = projectStreamDoneItems(t, events)
	if sawMetadata {
		t.Fatalf("expected google-compatible request to omit metadata")
	}
}

func TestProjects_MessageStreamUsesGoogleServiceAccountJSON(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-portal"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Customer Portal",
			Description: "Internal customer management application",
		},
	}
	tokenCalled := 0
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalled++
		if r.Method != http.MethodPost {
			t.Fatalf("token method: got %s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse token form: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Fatalf("grant_type = %q", got)
		}
		if r.Form.Get("assertion") == "" {
			t.Fatalf("expected JWT bearer assertion")
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "ya29.service-account-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer tokenSrv.Close()

	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ya29.service-account-token" {
			t.Fatalf("authorization header = %q, want service account access token", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "" {
			t.Fatalf("unexpected x-goog-api-key header: %q", got)
		}
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode LLM request: %v", err)
		}
		if req.Model != "google/gemini-test" {
			t.Fatalf("model = %q, want google/gemini-test", req.Model)
		}
		writeChatStream(t, w, chatStreamContent("Build the lead inbox next."))
	}))
	defer llmSrv.Close()

	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr, project)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	provider := projectLLMProviderGoogle
	baseURL := llmSrv.URL
	model := "google/gemini-test"
	serviceAccountJSON := testGoogleServiceAccountJSON(t, "svc-project", tokenSrv.URL)
	settingsBody, _ := json.Marshal(PatchProjectLLMSettingsRequest{
		Provider: &provider,
		BaseURL:  &baseURL,
		Model:    &model,
		APIKey:   &serviceAccountJSON,
	})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/llm-settings", jsonBody(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH service-account LLM settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("settings status: got %d, want 200: %s", resp.StatusCode, string(body))
	}
	_ = resp.Body.Close()

	status, events, payload := postProjectMessageStream(t, srv.URL, "customer-portal", "What should we build next?")
	if status != http.StatusOK {
		t.Fatalf("message stream status: got %d, want 200: %s", status, payload)
	}
	_ = projectStreamDoneItems(t, events)
	if tokenCalled != 1 {
		t.Fatalf("expected one token exchange, got %d", tokenCalled)
	}
}

func TestProjects_MessageStreamRequiresLLMSettings(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-portal"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Customer Portal",
			Memory:      emptyProjectMemory(),
		},
	}
	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr, project)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	status, events, payload := postProjectMessageStream(t, srv.URL, "customer-portal", "What should we build next?")
	if status != http.StatusOK {
		t.Fatalf("message stream status: got %d, want 200: %s", status, payload)
	}
	if len(events) != 1 || events[0].Type != "error" {
		t.Fatalf("expected single error event, got %#v", events)
	}
	if !strings.Contains(events[0].Error, "project LLM API key is not configured") {
		t.Fatalf("missing-key response: %s", payload)
	}
}

func TestProjects_PatchMemoryPreservesOmittedFields(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-portal"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Customer Portal",
			Memory: aiv1alpha1.ProjectMemory{
				Goals:        []string{"ship MVP"},
				Requirements: []string{"persistent chat"},
				Constraints:  []string{"no editor"},
			},
		},
	}
	mgr, _, _ := newTestManager(t)
	attachProjectClient(t, mgr, project)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	requirements := []string{"persistent chat", "memory API"}
	body, _ := json.Marshal(PatchProjectMemoryRequest{Requirements: &requirements})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/workspaces/ws-1/projects/customer-portal/memory", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH memory: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status: got %d, want 200", resp.StatusCode)
	}
	var memory aiv1alpha1.ProjectMemory
	if err := json.NewDecoder(resp.Body).Decode(&memory); err != nil {
		t.Fatalf("decode memory: %v", err)
	}
	_ = resp.Body.Close()
	if fmt.Sprint(memory.Goals) != "[ship MVP]" {
		t.Fatalf("goals changed: %#v", memory.Goals)
	}
	if fmt.Sprint(memory.Requirements) != "[persistent chat memory API]" {
		t.Fatalf("requirements not patched: %#v", memory.Requirements)
	}
	if fmt.Sprint(memory.Constraints) != "[no editor]" {
		t.Fatalf("constraints changed: %#v", memory.Constraints)
	}
}

func TestProjectMCPRequestRetriesInsecureTLSTrustForLocalEndpoint(t *testing.T) {
	received := 0
	mcpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received++
		writeJSON(w, http.StatusOK, map[string]any{
			"result": map[string]any{
				"tools": []map[string]any{{
					"name":        "get_ping",
					"description": "health-check helper",
					"inputSchema": map[string]any{"type": "object"},
				}},
			},
		})
	}))
	defer mcpServer.Close()

	request := httptest.NewRequest(http.MethodPost, "https://localhost", nil)

	_, err := fetchProjectMCPTools(context.Background(), mcpServer.URL, request, false)
	if err != nil {
		t.Fatalf("fetchProjectMCPTools: %v", err)
	}
	if received == 0 {
		t.Fatal("expected MCP server to receive a request")
	}
}

func TestProjectMCPShouldRetryInsecureForTLSErrors(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		err     error
		skipTLS bool
		want    bool
	}{
		{
			name: "unknown authority",
			url:  "https://localhost:9443",
			err: &url.Error{
				Op:  "POST",
				URL: "https://localhost:9443",
				Err: &x509.UnknownAuthorityError{},
			},
			skipTLS: false,
			want:    true,
		},
		{
			name: "hostname mismatch",
			url:  "https://127.0.0.1",
			err: &url.Error{
				Op:  "POST",
				URL: "https://127.0.0.1",
				Err: &x509.HostnameError{Host: "example.invalid", Certificate: &x509.Certificate{}},
			},
			skipTLS: false,
			want:    true,
		},
		{
			name: "non-local skips retry",
			url:  "https://example.com",
			err: &url.Error{
				Op:  "POST",
				URL: "https://example.com",
				Err: &x509.UnknownAuthorityError{},
			},
			skipTLS: false,
			want:    false,
		},
		{
			name:    "skipTLS already true",
			url:     "https://localhost:9443",
			err:     &x509.UnknownAuthorityError{},
			skipTLS: true,
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectMCPShouldRetryInsecure(tc.url, tc.err, tc.skipTLS); got != tc.want {
				t.Fatalf("projectMCPShouldRetryInsecure(%q): got %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestProjectMCPToolAllowlist(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "get_customer", want: true},
		{name: "list_workspaces", want: true},
		{name: "describe-project", want: true},
		{name: "readFile", want: true},
		{name: "default_api:list_targets", want: true},
		{name: "infrastructure__list_templates", want: true},
		{name: "echo", want: false},
		{name: "write_customer", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectMCPToolAllowed(tc.name); got != tc.want {
				t.Fatalf("projectMCPToolAllowed(%q): got %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestResolveProjectToolCallsRejectsDisallowedToolName(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://example.com", nil)
	req = req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{OrgUUID: "org-a", WorkspaceUUID: "ws-1"}))
	h := &Handler{}

	got, err := h.resolveProjectToolCalls(req.Context(), []chatToolCall{{
		ID:   "tool-1",
		Type: "function",
		Function: chatToolCallFunction{
			Name:      "delete_all",
			Arguments: `{}`,
		},
	}}, req)
	if err != nil {
		t.Fatalf("resolveProjectToolCalls: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 tool message, got %#v", got)
	}
	if got[0].Content != "Tool call failed: disallowed MCP tool name" {
		t.Fatalf("unexpected tool message: %#v", got[0])
	}
}

// ===== Membership tests =====

func TestAddOrgMembership_UpdatesCRAndUMI(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	mgr, ops, _ := newTestManager(t, org)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	body, _ := json.Marshal(MembershipAddRequest{User: "bob", Role: "member"})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/memberships", "application/json", jsonBody(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want 201", resp.StatusCode)
	}
	if ops.orgMemberships["org-a"]["bob"] != "member" {
		t.Errorf("membership: %v", ops.orgMemberships)
	}
	idx, _ := mgr.client.UserMembershipIndices().Get(context.Background(), "bob", metav1.GetOptions{})
	if len(idx.Spec.Entries) != 1 || idx.Spec.Entries[0].Role != "member" {
		t.Errorf("UMI: %#v", idx)
	}
}

func TestDeleteOrgMembership_RemovesFromBootstrapper(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureOrgMembership(context.Background(), "org-a", "bob", "member")
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/memberships/bob", nil)
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
	if _, ok := ops.orgMemberships["org-a"]["bob"]; ok {
		t.Error("Membership CR not deleted")
	}
}

func TestDeleteOrgMembership_CascadeFlagReadsQueryParam(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureOrgMembership(context.Background(), "org-a", "bob", "member")
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/memberships/bob?cascade=true", nil)
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	// 204 even when there's no UMI to scrub; the handler walks the
	// cascade branch and short-circuits cleanly.
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
}

func TestPatchOrgMembershipRole(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureOrgMembership(context.Background(), "org-a", "bob", "member")
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	body, _ := json.Marshal(MembershipPatchRequest{Role: "admin"})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/memberships/bob", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	if ops.orgMemberships["org-a"]["bob"] != "admin" {
		t.Errorf("CR not patched: %v", ops.orgMemberships)
	}
}

func TestSelfLeaveOrg(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureOrgMembership(context.Background(), "org-a", "bob", "member")
	srv := newTestServer(t, mgr, memberTC("bob", "org-a", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/memberships/me", nil)
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
	if _, ok := ops.orgMemberships["org-a"]["bob"]; ok {
		t.Error("Membership CR not deleted")
	}
}

// ===== User self-delete =====

func TestDeleteSelfUser_StampsTimestamp(t *testing.T) {
	u := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}
	mgr, _, _ := newTestManager(t, u)
	srv := newTestServer(t, mgr, adminTC("alice", "", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/users/me", nil)
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
	got, _ := mgr.client.Users().Get(context.Background(), "alice", metav1.GetOptions{})
	if got.Status.DeletionRequestedAt == nil {
		t.Error("DeletionRequestedAt not set")
	}
}

// ===== Kubeconfig download tests =====

func TestDownloadKubeconfig_InstallVariant(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureChildWorkspace(context.Background(), "org-a", "ws-1")
	mgr.WithKubeconfig(KubeconfigConfig{
		HubExternalURL: "https://hub.test",
		OIDCIssuerURL:  "https://issuer.test",
		OIDCClientID:   "test-client",
	})
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	cases := []struct {
		name        string
		query       string
		wantStatus  int
		wantCommand string // empty if status != 200
	}{
		{"default", "", http.StatusOK, "kedge"},
		{"explicit kedge", "?install=kedge", http.StatusOK, "kedge"},
		{"krew alias", "?install=krew", http.StatusOK, "kubectl-kedge"},
		{"explicit kubectl-kedge", "?install=kubectl-kedge", http.StatusOK, "kubectl-kedge"},
		{"unknown", "?install=bogus", http.StatusBadRequest, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + "/api/orgs/org-a/workspaces/ws-1/kubeconfig" + tc.query)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			body, _ := io.ReadAll(resp.Body)
			// We only assert on the substring; a YAML parse here would drag in
			// clientcmd just to re-check what the handler already produces.
			want := "command: " + tc.wantCommand
			if !bytes.Contains(body, []byte(want)) {
				t.Errorf("response missing %q\nbody:\n%s", want, body)
			}
		})
	}
}

// ===== helpers =====

// jsonBody wraps a []byte as a Reader for http.Post.
func jsonBody(b []byte) io.Reader { return bytes.NewReader(b) }
