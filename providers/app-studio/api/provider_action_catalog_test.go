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

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	appskills "github.com/faroshq/provider-app-studio/skills"
	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestFetchProviderActionCatalogRejectsSelfSignedByDefault(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	_, err := (&Server{hubBase: upstream.URL}).fetchProviderActionCatalog(context.Background(), identity{token: "caller-token"})
	if err == nil {
		t.Fatal("catalog lookup accepted a self-signed hub without an explicit insecure opt-in")
	}
}

func TestProviderAssistantSkillSourceKeepsCatalogPackagesAcrossReadinessChanges(t *testing.T) {
	valid := appskills.ProviderSkillPackage{
		ProviderName: "databricks",
		PackageName:  "databricks-app-integration",
		Version:      "1.0.0",
		Skill:        "---\nname: databricks-app-integration\ndescription: integration guidance\n---\nbody\n",
	}
	digest, err := appskills.ProviderSkillPackageDigest(valid)
	if err != nil {
		t.Fatalf("provider skill digest: %v", err)
	}
	valid.Digest = digest
	ready := true
	server := &Server{}
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return []providerCatalogEntry{
			{Name: "databricks", Ready: ready, AssistantSkills: []providerCatalogAssistantSkill{{
				PackageName: valid.PackageName,
				Version:     valid.Version,
				Digest:      valid.Digest,
				Skill:       valid.Skill,
			}}},
		}, nil
	}
	source, err := server.providerAssistantSkillSource(context.Background(), identity{})
	if err != nil {
		t.Fatalf("providerAssistantSkillSource: %v", err)
	}
	list, err := source.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("provider skill source list: %v", err)
	}
	if len(list.Packages) != 1 || list.Packages[0].Path != "providers/databricks/databricks-app-integration" {
		t.Fatalf("provider packages = %#v, want registered catalog package", list.Packages)
	}
	if list.Packages[0].Digest != digest {
		t.Fatalf("provider package digest = %q, want %q", list.Packages[0].Digest, digest)
	}
	firstSnapshot, err := appskills.Build(context.Background(), appskills.CatalogOptions{Sources: []appskills.Source{source}})
	if err != nil {
		t.Fatalf("ready catalog build: %v", err)
	}
	ready = false
	offlineSource, err := server.providerAssistantSkillSource(context.Background(), identity{})
	if err != nil {
		t.Fatalf("offline providerAssistantSkillSource: %v", err)
	}
	offlineSnapshot, err := appskills.Build(context.Background(), appskills.CatalogOptions{Sources: []appskills.Source{offlineSource}})
	if err != nil {
		t.Fatalf("offline catalog build: %v", err)
	}
	if firstSnapshot.CatalogDigest != offlineSnapshot.CatalogDigest || len(offlineSnapshot.Entries) != 1 || offlineSnapshot.Entries[0].Digest != digest {
		t.Fatalf("readiness changed provider skill catalog: ready=%#v offline=%#v", firstSnapshot, offlineSnapshot)
	}
}

func TestProviderAssistantSkillSourceWithoutBearerOmitsOptionalPackages(t *testing.T) {
	source, err := (&Server{hubBase: "https://hub.invalid"}).providerAssistantSkillSource(context.Background(), identity{})
	if err != nil {
		t.Fatalf("missing-bearer provider source: %v", err)
	}
	list, err := source.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("missing-bearer source list: %v", err)
	}
	if len(list.Packages) != 0 || len(list.Warnings) != 0 {
		t.Fatalf("missing-bearer provider source = %#v, want empty optional source", list)
	}
}

func TestProjectAssistantSkillCatalogResolverFailureIsolated(t *testing.T) {
	server := &Server{}
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return nil, errors.New("provider catalog backend secret should not escape")
	}
	snapshot, err := server.projectAssistantSkillCatalogSnapshot(context.Background(), workspace.Scope{}, identity{token: "caller-token"})
	if err != nil {
		t.Fatalf("catalog snapshot = %v, want source failure isolation", err)
	}
	if len(snapshot.Entries) == 0 {
		t.Fatal("bundled skills disappeared after optional provider catalog failure")
	}
	foundWarning := false
	for _, warning := range snapshot.Warnings {
		if warning.Code == "source_list_failed" && warning.Scope == appskills.ScopeSystem {
			foundWarning = true
		}
		if strings.Contains(warning.Message, "secret") || strings.Contains(warning.Message, "provider catalog") {
			t.Fatalf("provider fetch error escaped warning sanitization: %#v", warning)
		}
	}
	if !foundWarning {
		t.Fatalf("snapshot warnings = %#v, want sanitized provider source_list_failed", snapshot.Warnings)
	}
}

func TestVerifyProjectActionGrantsPropagatesCatalogFailure(t *testing.T) {
	expected := errors.New("provider catalog backend unavailable")
	server := &Server{}
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return nil, expected
	}
	ref := &aiv1alpha1.ProjectProviderResourceReference{
		Name:       "orders",
		APIVersion: databricksTableAPIVersion,
		Kind:       databricksTableKind,
		Resource:   databricksTableResource,
	}
	actions := []aiv1alpha1.ProjectProviderActionSpec{{
		Name:         projectIntegrationActionQueryTable,
		Version:      projectIntegrationActionVersionV1,
		SchemaDigest: testProjectActionSchemaDigest,
	}}
	_, err := server.verifyProjectActionGrants(context.Background(), identity{user: "alice@example.com"}, "databricks", ref, actions, false)
	if !errors.Is(err, expected) {
		t.Fatalf("verifyProjectActionGrants() error = %v, want provider catalog failure %v", err, expected)
	}
}

func TestFetchProviderActionCatalogInsecureOptInPreservesCallerHeaders(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != providerCatalogPath {
			t.Errorf("catalog request = %s %s, want GET %s", r.Method, r.URL.Path, providerCatalogPath)
		}
		wantHeaders := map[string]string{
			"Authorization":     "Bearer caller-token",
			"X-Kedge-Tenant":    "root:kedge:tenants:org-1:workspace-1",
			"X-Kedge-Cluster":   "cluster-1",
			"X-Kedge-Org":       "org-1",
			"X-Kedge-Workspace": "workspace-1",
			"X-Kedge-User":      "alice@example.com",
		}
		for name, want := range wantHeaders {
			if got := r.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"name":"databricks","ready":true}]}`))
	}))
	defer upstream.Close()

	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("http.DefaultTransport is not an *http.Transport")
	}
	baseTLS := base.TLSClientConfig
	var baseInsecure bool
	if baseTLS != nil {
		baseInsecure = baseTLS.InsecureSkipVerify
	}

	s := &Server{hubBase: upstream.URL, mcpInsecureSkipTLSVerify: true}
	catalog, err := s.fetchProviderActionCatalog(context.Background(), identity{
		tenantPath:    "root:kedge:tenants:org-1:workspace-1",
		clusterID:     "cluster-1",
		orgUUID:       "org-1",
		workspaceUUID: "workspace-1",
		user:          "alice@example.com",
		token:         "caller-token",
	})
	if err != nil {
		t.Fatalf("insecure catalog lookup failed: %v", err)
	}
	if len(catalog) != 1 || catalog[0].Name != "databricks" || !catalog[0].Ready {
		t.Fatalf("catalog = %#v, want one ready databricks entry", catalog)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("catalog server calls = %d, want 1", got)
	}
	if got, ok := http.DefaultTransport.(*http.Transport); !ok || got != base {
		t.Fatalf("catalog lookup replaced http.DefaultTransport")
	}
	if base.TLSClientConfig != baseTLS {
		t.Fatalf("catalog lookup mutated http.DefaultTransport TLS config pointer")
	}
	if baseTLS != nil && baseTLS.InsecureSkipVerify != baseInsecure {
		t.Fatalf("catalog lookup mutated http.DefaultTransport InsecureSkipVerify")
	}
}

func TestFetchProviderActionCatalogRejectsRedirect(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+providerCatalogPath, http.StatusFound)
	}))
	defer redirect.Close()

	_, err := (&Server{hubBase: redirect.URL, mcpInsecureSkipTLSVerify: true}).fetchProviderActionCatalog(context.Background(), identity{token: "caller-token"})
	if err == nil {
		t.Fatal("catalog lookup followed a redirect")
	}
	if got := targetCalls.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests, want 0", got)
	}
}

func TestProjectIntegrationGrantRequiresConsentAndOwnsAudit(t *testing.T) {
	fixture := newIntegrationHTTPFixture(t, &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
	})
	catalog := []providerCatalogEntry{{
		Name: "databricks", Ready: true,
		Actions: []providerCatalogAction{{
			ID: "query_table/v1", SchemaDigest: testProjectActionSchemaDigest,
			BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource},
			Consent:       providerCatalogActionConsent{Required: true, Prompt: "Allow table queries?", Scope: "orders"},
		}},
	}}
	server := NewWithWorkspace(tenant.NewGraphQLClient(fixture.graphql.URL, false), nil, nil, fixture.hub.URL, false)
	server.actionsExternalURL = "https://actions.example"
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return catalog, nil
	}
	router := newIntegrationRouter(server)

	withoutConsent := integrationHTTPTestRequest(http.MethodPost, "/api/projects/demo/integrations", fmt.Sprintf(`{"alias":"sales","provider":"databricks","resourceRef":{"name":"orders","apiVersion":"%s","kind":"Table","resource":"tables"},"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"%s"}]}`, databricksTableAPIVersion, testProjectActionSchemaDigest))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, withoutConsent)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("grant without consent status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	if got := fixture.project(t).Spec.Environments; len(got) != 0 {
		t.Fatalf("project changed after consent rejection: %#v", got)
	}

	accepted := integrationHTTPTestRequest(http.MethodPost, "/api/projects/demo/integrations", fmt.Sprintf(`{"alias":"sales","provider":"databricks","resourceRef":{"name":"orders","apiVersion":"%s","kind":"Table","resource":"tables"},"consentAccepted":true,"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"%s","grantedBy":"spoofed","grantedAt":"2000-01-01T00:00:00Z"}]}`, databricksTableAPIVersion, testProjectActionSchemaDigest))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, accepted)
	if response.Code != http.StatusCreated {
		t.Fatalf("grant with consent status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	binding := fixture.project(t).Spec.Environments[0].Bindings[0]
	grant := binding.AllowedActions[0]
	if grant.SchemaDigest != testProjectActionSchemaDigest || grant.GrantedBy != "alice@example.com" {
		t.Fatalf("stored grant = %#v, want catalog digest and authenticated caller", grant)
	}
	if grant.GrantedAt == nil || grant.GrantedAt.IsZero() {
		t.Fatalf("stored grant has no server-owned grantedAt: %#v", grant)
	}
	if grant.GrantedBy == "spoofed" || grant.GrantedAt.String() == "2000-01-01T00:00:00Z" {
		t.Fatalf("client grant audit fields were accepted: %#v", grant)
	}
}

func TestProjectIntegrationGrantRejectsDigestDriftWithoutMutation(t *testing.T) {
	fixture := newIntegrationHTTPFixture(t, &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
	})
	currentDigest := testProjectActionSchemaDigest
	server := NewWithWorkspace(tenant.NewGraphQLClient(fixture.graphql.URL, false), nil, nil, fixture.hub.URL, false)
	server.actionsExternalURL = "https://actions.example"
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return []providerCatalogEntry{{
			Name: "databricks", Ready: true,
			Actions: []providerCatalogAction{{
				ID: "query_table/v1", SchemaDigest: currentDigest,
				BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource},
			}},
		}}, nil
	}
	router := newIntegrationRouter(server)
	createBody := fmt.Sprintf(`{"alias":"sales","provider":"databricks","resourceRef":{"name":"orders","apiVersion":"%s","kind":"Table","resource":"tables"},"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"%s"}]}`, databricksTableAPIVersion, testProjectActionSchemaDigest)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, integrationHTTPTestRequest(http.MethodPost, "/api/projects/demo/integrations", createBody))
	if response.Code != http.StatusCreated {
		t.Fatalf("initial grant status = %d: %s", response.Code, response.Body.String())
	}
	prior := fixture.project(t).Spec.Environments[0].Bindings[0].AllowedActions[0]
	currentDigest = "sha256:" + strings.Repeat("f", 64)
	patchBody := fmt.Sprintf(`{"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"%s"}]}`, testProjectActionSchemaDigest)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, integrationHTTPTestRequest(http.MethodPatch, "/api/projects/demo/integrations/sales", patchBody))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("digest-drift patch status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	after := fixture.project(t).Spec.Environments[0].Bindings[0].AllowedActions[0]
	if after.SchemaDigest != prior.SchemaDigest || after.GrantedBy != prior.GrantedBy || after.GrantedAt == nil || prior.GrantedAt == nil || !after.GrantedAt.Time.Equal(prior.GrantedAt.Time) {
		t.Fatalf("digest-drift patch mutated prior grant: before %#v after %#v", prior, after)
	}
}

func TestProjectIntegrationRevokePreservesAuditWithoutProvider(t *testing.T) {
	fixture := newIntegrationHTTPFixture(t, &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
	})
	catalog := []providerCatalogEntry{{
		Name: "databricks", Ready: true,
		Actions: []providerCatalogAction{{
			ID: "query_table/v1", SchemaDigest: testProjectActionSchemaDigest,
			BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource},
		}},
	}}
	catalogCalls := 0
	catalogUnavailable := false
	server := NewWithWorkspace(tenant.NewGraphQLClient(fixture.graphql.URL, false), nil, nil, fixture.hub.URL, false)
	server.actionsExternalURL = "https://actions.example"
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		catalogCalls++
		if catalogUnavailable || catalog == nil {
			return nil, errors.New("provider catalog unavailable")
		}
		return catalog, nil
	}
	router := newIntegrationRouter(server)
	createBody := fmt.Sprintf(`{"alias":"sales","provider":"databricks","resourceRef":{"name":"orders","apiVersion":"%s","kind":"Table","resource":"tables"},"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"%s"}]}`, databricksTableAPIVersion, testProjectActionSchemaDigest)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, integrationHTTPTestRequest(http.MethodPost, "/api/projects/demo/integrations", createBody))
	if response.Code != http.StatusCreated {
		t.Fatalf("initial grant status = %d: %s", response.Code, response.Body.String())
	}
	prior := fixture.project(t).Spec.Environments[0].Bindings[0].AllowedActions[0]
	if prior.GrantedAt == nil {
		t.Fatal("initial grant has no grantedAt")
	}
	initialCatalogCalls := catalogCalls

	// Drift the catalog to an unready/deprecated action. The revoke must not
	// consult the provider's current readiness, deprecation, or digest state.
	catalog = []providerCatalogEntry{{
		Name: "databricks", Ready: false,
		Actions: []providerCatalogAction{{
			ID: "query_table/v1", SchemaDigest: "sha256:" + strings.Repeat("f", 64),
			BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource},
			Deprecation:   &providerCatalogDeprecation{Deprecated: true},
		}},
	}}
	catalogUnavailable = true
	staleDigest := "sha256:" + strings.Repeat("f", 64)
	revokeBody := fmt.Sprintf(`{"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"%s","revoked":true,"grantedBy":"spoof-granter","grantedAt":"2000-01-01T00:00:00Z","revokedBy":"spoof-revoker","revokedAt":"2000-01-02T00:00:00Z"}]}`, staleDigest)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, integrationHTTPTestRequest(http.MethodPatch, "/api/projects/demo/integrations/sales", revokeBody))
	if response.Code != http.StatusOK {
		t.Fatalf("revoke while provider unavailable status = %d: %s", response.Code, response.Body.String())
	}
	if catalogCalls != initialCatalogCalls {
		t.Fatalf("revoke consulted provider catalog %d times after create, want %d", catalogCalls, initialCatalogCalls)
	}
	revoked := fixture.project(t).Spec.Environments[0].Bindings[0].AllowedActions[0]
	if !revoked.Revoked || revoked.SchemaDigest != prior.SchemaDigest || revoked.GrantedBy != prior.GrantedBy || revoked.GrantedAt == nil || !revoked.GrantedAt.Time.Equal(prior.GrantedAt.Time) {
		t.Fatalf("revoke did not preserve original grant audit/digest: before %#v after %#v", prior, revoked)
	}
	if revoked.RevokedBy != "alice@example.com" || revoked.RevokedAt == nil || revoked.RevokedAt.IsZero() {
		t.Fatalf("revoke audit = %#v, want authenticated server-owned audit", revoked)
	}
	if revoked.RevokedBy == "spoof-revoker" || revoked.RevokedAt.String() == "2000-01-02T00:00:00Z" || revoked.GrantedBy == "spoof-granter" {
		t.Fatalf("spoofed grant/revoke audit was accepted: %#v", revoked)
	}

	// A repeated revoke is idempotent and must not replace the first revoke
	// audit, even when the request attempts to do so.
	revokedAt := revoked.RevokedAt.DeepCopy()
	response = httptest.NewRecorder()
	router.ServeHTTP(response, integrationHTTPTestRequest(http.MethodPatch, "/api/projects/demo/integrations/sales", revokeBody))
	if response.Code != http.StatusOK {
		t.Fatalf("idempotent revoke status = %d: %s", response.Code, response.Body.String())
	}
	again := fixture.project(t).Spec.Environments[0].Bindings[0].AllowedActions[0]
	if again.RevokedBy != revoked.RevokedBy || again.RevokedAt == nil || !again.RevokedAt.Time.Equal(revokedAt.Time) {
		t.Fatalf("idempotent revoke replaced audit: first %#v second %#v", revoked, again)
	}
	if catalogCalls != initialCatalogCalls {
		t.Fatalf("idempotent revoke consulted provider catalog %d times, want %d", catalogCalls, initialCatalogCalls)
	}
}

func TestProjectIntegrationReactivationRequiresFreshCatalogConsent(t *testing.T) {
	fixture := newIntegrationHTTPFixture(t, projectWithTableIntegration(true))
	server := NewWithWorkspace(tenant.NewGraphQLClient(fixture.graphql.URL, false), nil, nil, fixture.hub.URL, false)
	server.actionsExternalURL = "https://actions.example"
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return []providerCatalogEntry{{
			Name: "databricks", Ready: true,
			Actions: []providerCatalogAction{{
				ID: "query_table/v1", SchemaDigest: testProjectActionSchemaDigest,
				BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource},
				Consent:       providerCatalogActionConsent{Required: true},
			}},
		}}, nil
	}
	router := newIntegrationRouter(server)
	activeBody := fmt.Sprintf(`{"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"%s"}]}`, testProjectActionSchemaDigest)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, integrationHTTPTestRequest(http.MethodPatch, "/api/projects/demo/integrations/sales", activeBody))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("reactivation without consent status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	if got := fixture.project(t).Spec.Environments[0].Bindings[0].AllowedActions[0]; !got.Revoked {
		t.Fatalf("reactivation without consent changed grant: %#v", got)
	}
	activeBody = fmt.Sprintf(`{"consentAccepted":true,"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"%s"}]}`, testProjectActionSchemaDigest)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, integrationHTTPTestRequest(http.MethodPatch, "/api/projects/demo/integrations/sales", activeBody))
	if response.Code != http.StatusOK {
		t.Fatalf("reactivation with consent status = %d: %s", response.Code, response.Body.String())
	}
	active := fixture.project(t).Spec.Environments[0].Bindings[0].AllowedActions[0]
	if active.Revoked || active.GrantedBy != "alice@example.com" || active.RevokedBy != "" || active.RevokedAt != nil || active.GrantedAt == nil {
		t.Fatalf("reactivated grant = %#v, want fresh active grant audit", active)
	}
}

func TestFindProviderCatalogActionRejectsUnavailableMetadata(t *testing.T) {
	ref := &aiv1alpha1.ProjectProviderResourceReference{Name: "orders", APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource}
	grant := aiv1alpha1.ProjectProviderActionSpec{Name: "query_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest}
	cases := []struct {
		name    string
		catalog []providerCatalogEntry
	}{
		{name: "unknown provider", catalog: nil},
		{name: "unready provider", catalog: []providerCatalogEntry{{Name: "databricks", Actions: []providerCatalogAction{{ID: "query_table/v1", SchemaDigest: testProjectActionSchemaDigest, BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource}}}}}},
		{name: "unknown action", catalog: []providerCatalogEntry{{Name: "databricks", Ready: true}}},
		{name: "deprecated action", catalog: []providerCatalogEntry{{Name: "databricks", Ready: true, Actions: []providerCatalogAction{{ID: "query_table/v1", SchemaDigest: testProjectActionSchemaDigest, BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource}, Deprecation: &providerCatalogDeprecation{Deprecated: true}}}}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := findProviderCatalogAction(tt.catalog, "databricks", ref, grant); err == nil {
				t.Fatalf("catalog metadata accepted unavailable action")
			}
		})
	}
}

func newIntegrationRouter(server *Server) *mux.Router {
	router := mux.NewRouter()
	server.Register(router)
	return router
}
