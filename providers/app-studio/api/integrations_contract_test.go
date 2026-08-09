/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/tenant"
)

var testDatabricksTableGVR = schema.GroupVersionResource{
	Group: "databricks.kedge.faros.sh", Version: "v1alpha1", Resource: "tables",
}

const (
	projectIntegrationProviderDatabricks = "databricks"
	projectIntegrationActionQueryTable   = "query_table"
	projectIntegrationActionVersionV1    = "v1"
	testProjectActionSchemaDigest        = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	databricksTableAPIVersion            = "databricks.kedge.faros.sh/v1alpha1"
	databricksTableKind                  = "Table"
	databricksTableResource              = "tables"
)

func TestProviderReferenceReconcileOnlyGetsAndNeverOwnsTarget(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Demo",
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
				Name: "development", Mode: aiv1alpha1.ProjectEnvironmentModeLive,
				Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
					Name: "sales", Provider: projectIntegrationProviderDatabricks,
					Kind: aiv1alpha1.ProjectBindingKindProviderReference,
					ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
						Name: "orders", APIVersion: databricksTableAPIVersion,
						Kind: databricksTableKind, Resource: databricksTableResource,
					},
				}},
			}},
		},
	}
	table := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databricksTableAPIVersion,
		"kind":       databricksTableKind,
		"metadata":   map[string]any{"name": "orders"},
		"spec":       map[string]any{"catalog": "sales", "schema": "gold", "table": "orders"},
	}}
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add App Studio scheme: %v", err)
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		asclient.ProjectGVR: "ProjectList", testDatabricksTableGVR: "TableList",
	}, project, table)
	for _, verb := range []string{"create", "update", "delete", "patch"} {
		verb := verb
		dyn.PrependReactor(verb, "tables", func(k8stesting.Action) (bool, runtime.Object, error) {
			t.Fatalf("provider reference reconcile attempted %s on the referenced Table", verb)
			return true, nil, nil
		})
	}
	c := asclient.NewFromDynamic(dyn)
	if _, err := (&Server{}).reconcileProjectLiveBindings(context.Background(), c, project, identity{}); err != nil {
		t.Fatalf("reconcileProjectLiveBindings: %v", err)
	}
	got, err := c.Resource(providerBindingResource(testDatabricksTableGVR, databricksTableKind), "").Get(context.Background(), "orders", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get referenced Table: %v", err)
	}
	if got.GetOwnerReferences() != nil {
		t.Fatalf("referenced Table gained owner references: %+v", got.GetOwnerReferences())
	}
}

func TestProviderReferenceProjectCleanupDoesNotDeleteTarget(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{
				{
					Name: "development", Mode: aiv1alpha1.ProjectEnvironmentModeLive,
					Bindings: []aiv1alpha1.ProjectProviderBindingSpec{
						{
							Name: "sales", Provider: projectIntegrationProviderDatabricks,
							Kind: aiv1alpha1.ProjectBindingKindProviderReference,
							ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
								Name: "orders", APIVersion: databricksTableAPIVersion,
								Kind: databricksTableKind, Resource: databricksTableResource,
							},
						},
					},
				},
			},
		},
	}
	table := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databricksTableAPIVersion, "kind": databricksTableKind,
		"metadata": map[string]any{"name": "orders"},
	}}
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add App Studio scheme: %v", err)
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		asclient.ProjectGVR: "ProjectList", testDatabricksTableGVR: "TableList",
	}, project, table)
	dyn.PrependReactor("delete", "tables", func(k8stesting.Action) (bool, runtime.Object, error) {
		t.Fatalf("project cleanup deleted provider-owned Table")
		return true, nil, nil
	})
	if err := (&Server{}).deleteProjectProviderResources(context.Background(), asclient.NewFromDynamic(dyn), project, identity{}); err != nil {
		t.Fatalf("deleteProjectProviderResources: %v", err)
	}
}

func TestProviderActionInputRejectsBoundContextOverrides(t *testing.T) {
	for _, field := range []string{
		`{"resourceRef":{"name":"other"}}`,
		`{"provider":"other"}`,
		`{"providerURL":"https://attacker.invalid"}`,
		`{"credentials":"secret"}`,
		`{"nested":{"clusterID":"other"}}`,
	} {
		if _, err := normalizeProjectProviderActionInput(json.RawMessage(field)); err == nil {
			t.Fatalf("provider action input accepted bound-context override: %s", field)
		}
	}
	input, err := normalizeProjectProviderActionInput(json.RawMessage(`{"columns":["id"],"limit":25,"sql":"select 1"}`))
	if err != nil {
		t.Fatalf("generic action input rejected: %v", err)
	}
	if string(input) == "" {
		t.Fatal("normalized generic action input was empty")
	}
}

func TestProviderActionForwardingNeverRetriesWithInsecureTLS(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requestID":"unexpected","provider":"other","action":"lookup","actionVersion":"v1","resourceRef":{"name":"item","apiVersion":"example/v1","kind":"Item","resource":"items"},"result":{}}`))
	}))
	defer upstream.Close()

	s := &Server{hubBase: upstream.URL, actionsExternalURL: "https://hub.example", mcpInsecureSkipTLSVerify: true}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	ref := &aiv1alpha1.ProjectProviderResourceReference{
		Name: "item", APIVersion: "example/v1", Kind: "Item", Resource: "items",
	}
	status, envelope, err := s.forwardProjectProviderAction(request, identity{clusterID: "cluster-a"}, "other", "lookup", "v1", testProjectActionSchemaDigest, ref, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected verified TLS failure against self-signed upstream")
	}
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", status, http.StatusBadGateway)
	}
	if envelope.Error == nil || envelope.Error.Code != "provider_action_unavailable" {
		t.Fatalf("error envelope = %#v, want provider_action_unavailable", envelope.Error)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream received %d calls; action forwarding retried with insecure TLS", got)
	}
}

func TestProviderActionForwardingAppendsConfiguredCAToSystemTrust(t *testing.T) {
	var calls atomic.Int32
	ref := &aiv1alpha1.ProjectProviderResourceReference{
		Name: "item", APIVersion: "example/v1", Kind: "Item", Resource: "items",
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(projectProviderActionEnvelope{
			RequestID: "request-ca", Provider: "other", Action: "lookup", ActionVersion: "v1", ResourceRef: ref, Result: json.RawMessage(`{"ok":true}`),
		})
	}))
	defer upstream.Close()

	caBundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw})
	s := &Server{hubBase: upstream.URL, actionsExternalURL: "https://hub.example", actionsCABundle: string(caBundle), mcpInsecureSkipTLSVerify: true}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	status, envelope, err := s.forwardProjectProviderAction(request, identity{clusterID: "cluster-a"}, "other", "lookup", "v1", testProjectActionSchemaDigest, ref, json.RawMessage(`{}`))
	if err != nil || status != http.StatusOK || envelope.Error != nil {
		t.Fatalf("forward with configured CA = status %d envelope %#v err %v", status, envelope, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want one verified request", got)
	}
}

func TestProviderActionForwardingUsesVerifiedOrgWorkspaceHeaders(t *testing.T) {
	ref := &aiv1alpha1.ProjectProviderResourceReference{Name: "item", APIVersion: "example/v1", Kind: "Item", Resource: "items"}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Kedge-Org"); got != "org-verified" {
			t.Errorf("X-Kedge-Org = %q, want org-verified", got)
		}
		if got := r.Header.Get("X-Kedge-Workspace"); got != "workspace-verified" {
			t.Errorf("X-Kedge-Workspace = %q, want workspace-verified", got)
		}
		if got := r.Header.Get("X-Kedge-Tenant"); got != "root:kedge:tenants:org-verified:workspace-verified" {
			t.Errorf("X-Kedge-Tenant = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer caller-token" {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(projectProviderActionEnvelope{
			RequestID: "request-1", Provider: "other", Action: "lookup", ActionVersion: "v1", ResourceRef: ref, Result: json.RawMessage(`{"ok":true}`),
		})
	}))
	defer upstream.Close()
	s := &Server{hubBase: upstream.URL, actionsExternalURL: "https://hub.example"}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-Org", "spoofed")
	request.Header.Set("X-Kedge-Workspace", "spoofed")
	status, envelope, err := s.forwardProjectProviderAction(request, identity{
		tenantPath: "root:kedge:tenants:org-verified:workspace-verified", orgUUID: "org-verified", workspaceUUID: "workspace-verified", token: "caller-token", clusterID: "cluster-a",
	}, "other", "lookup", "v1", testProjectActionSchemaDigest, ref, json.RawMessage(`{}`))
	if err != nil || status != http.StatusOK || envelope.Error != nil {
		t.Fatalf("forward = status %d envelope %#v err %v", status, envelope, err)
	}
}

func TestProviderActionForwardingRejectsRedirectWithoutLeakingBearer(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("redirect target received Authorization %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/sink", http.StatusFound)
	}))
	defer redirect.Close()
	ref := &aiv1alpha1.ProjectProviderResourceReference{Name: "item", APIVersion: "example/v1", Kind: "Item", Resource: "items"}
	s := &Server{hubBase: redirect.URL, actionsExternalURL: "https://hub.example"}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Authorization", "Bearer caller-token")
	status, envelope, err := s.forwardProjectProviderAction(request, identity{clusterID: "cluster-a"}, "other", "lookup", "v1", testProjectActionSchemaDigest, ref, json.RawMessage(`{}`))
	if err == nil || status != http.StatusBadGateway || envelope.Error == nil {
		t.Fatalf("redirect forward = status %d envelope %#v err %v", status, envelope, err)
	}
	if got := targetCalls.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests", got)
	}
}

func TestIntegrationActionNormalizationAndRevocation(t *testing.T) {
	name, version, err := normalizeIntegrationAction("query_table/v1", "")
	if err != nil || name != "query_table" || version != "v1" {
		t.Fatalf("normalize action = %q/%q/%v", name, version, err)
	}
	if _, _, err := normalizeIntegrationAction("query_table/v1", "v2"); err == nil {
		t.Fatal("mismatched explicit action version was accepted")
	}
	actions, err := normalizeProjectIntegrationActions([]aiv1alpha1.ProjectProviderActionSpec{{Name: "query_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest, Revoked: true}})
	if err != nil || len(actions) != 1 || !actions[0].Revoked {
		t.Fatalf("normalized revoked action = %#v, err %v", actions, err)
	}
}

// integrationHTTPFixture backs the provider's GraphQL client and generic hub
// action endpoint without involving a real hub. It intentionally keeps
// the project and Table as serialized tenant resources: this exercises the
// same GraphQL-backed client path used by the HTTP handlers.
type integrationHTTPFixture struct {
	mu sync.Mutex

	projectYAML     string
	tableYAML       string
	applicationYAML string

	graphql    *httptest.Server
	hub        *httptest.Server
	actionReqs []integrationActionRequest
}

type integrationActionRequest struct {
	URL     string
	Headers http.Header
	Body    map[string]any
}

func newIntegrationHTTPFixture(t *testing.T, project *aiv1alpha1.Project) *integrationHTTPFixture {
	t.Helper()
	projectObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(project)
	if err != nil {
		t.Fatalf("convert project: %v", err)
	}
	projectYAML, err := yaml.Marshal(projectObject)
	if err != nil {
		t.Fatalf("marshal project: %v", err)
	}
	table := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databricksTableAPIVersion,
		"kind":       databricksTableKind,
		"metadata":   map[string]any{"name": "orders"},
		"spec": map[string]any{
			"catalog": "sales", "schema": "gold", "table": "orders",
		},
		"status": map[string]any{
			"columns": []any{
				map[string]any{"name": "id"},
				map[string]any{"name": "total"},
			},
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
		},
	}}
	tableYAML, err := yaml.Marshal(table.Object)
	if err != nil {
		t.Fatalf("marshal table: %v", err)
	}
	f := &integrationHTTPFixture{projectYAML: string(projectYAML), tableYAML: string(tableYAML)}
	f.graphql = httptest.NewServer(http.HandlerFunc(f.serveGraphQL))
	f.hub = httptest.NewServer(http.HandlerFunc(f.serveProviderAction))
	t.Cleanup(func() {
		f.graphql.Close()
		f.hub.Close()
	})
	return f
}

func (f *integrationHTTPFixture) serveGraphQL(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Query     string                     `json:"query"`
		Variables map[string]json.RawMessage `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid graphql request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(request.Query, "ProjectYaml"):
		writeIntegrationGraphQLData(w, map[string]any{
			"ai_kedge_faros_sh": map[string]any{
				"v1alpha1": map[string]any{"ProjectYaml": f.projectYAML},
			},
		})
	case strings.Contains(request.Query, "TableYaml"):
		writeIntegrationGraphQLData(w, map[string]any{
			"databricks_kedge_faros_sh": map[string]any{
				"v1alpha1": map[string]any{"TableYaml": f.tableYAML},
			},
		})
	case strings.Contains(request.Query, "ApplicationYaml"):
		writeIntegrationGraphQLData(w, map[string]any{
			"infrastructure_kedge_faros_sh": map[string]any{
				"v1alpha1": map[string]any{"ApplicationYaml": f.applicationYAML},
			},
		})
	case strings.Contains(request.Query, "applyStatusYaml"):
		writeIntegrationGraphQLData(w, map[string]any{"applyStatusYaml": f.projectYAML})
	case strings.Contains(request.Query, "applyYaml"):
		raw, ok := request.Variables["yaml"]
		if !ok {
			http.Error(w, "applyYaml missing yaml variable", http.StatusBadRequest)
			return
		}
		var applied string
		if err := json.Unmarshal(raw, &applied); err != nil {
			http.Error(w, "applyYaml yaml variable is not a string", http.StatusBadRequest)
			return
		}
		var object map[string]any
		if err := yaml.Unmarshal([]byte(applied), &object); err != nil {
			http.Error(w, "applyYaml payload is not YAML", http.StatusBadRequest)
			return
		}
		switch object["kind"] {
		case "Project":
			f.projectYAML = applied
		case "Application":
			f.applicationYAML = applied
		}
		writeIntegrationGraphQLData(w, map[string]any{"applyYaml": applied})
	default:
		http.Error(w, fmt.Sprintf("unexpected GraphQL query: %s", request.Query), http.StatusInternalServerError)
	}
}

func writeIntegrationGraphQLData(w http.ResponseWriter, data map[string]any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// serveProviderAction emulates a provider's action endpoint behind the hub
// backend proxy: identity is parsed from the data-plane route
// (/services/providers/{provider}/actions/clusters/{cluster}/{resource}/{name}/{action}/{version})
// and the body carries only input, mirroring the real wire contract.
func (f *integrationHTTPFixture) serveProviderAction(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid provider action request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.actionReqs = append(f.actionReqs, integrationActionRequest{
		URL: r.URL.String(), Headers: r.Header.Clone(), Body: body,
	})
	f.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// services providers {provider} actions clusters {cluster} {resource} {name} {action} {version}
	if len(parts) != 10 || parts[0] != "services" || parts[1] != "providers" || parts[3] != "actions" || parts[4] != "clusters" {
		http.Error(w, "unexpected provider action route", http.StatusNotFound)
		return
	}
	provider, resourceName, action, actionVersion := parts[2], parts[7], parts[8], parts[9]
	w.Header().Set("Content-Type", "application/json")
	result := map[string]any{"echo": body["input"]}
	if provider == "databricks" && action == "query_table" && actionVersion == "v1" {
		result = map[string]any{"actionVersion": "v1", "tableRef": resourceName, "rows": []any{map[string]any{"id": 1}}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"requestID": "hub-request-1", "provider": provider, "action": action, "actionVersion": actionVersion,
		"result": result,
	})
}

func (f *integrationHTTPFixture) setProject(t *testing.T, project *aiv1alpha1.Project) {
	t.Helper()
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(project)
	if err != nil {
		t.Fatalf("convert project: %v", err)
	}
	raw, err := yaml.Marshal(object)
	if err != nil {
		t.Fatalf("marshal project: %v", err)
	}
	f.mu.Lock()
	f.projectYAML = string(raw)
	f.actionReqs = nil
	f.mu.Unlock()
}

func (f *integrationHTTPFixture) project(t *testing.T) *aiv1alpha1.Project {
	t.Helper()
	f.mu.Lock()
	raw := []byte(f.projectYAML)
	f.mu.Unlock()
	var object map[string]any
	if err := yaml.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode project YAML: %v", err)
	}
	project := &aiv1alpha1.Project{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object, project); err != nil {
		t.Fatalf("convert project YAML: %v", err)
	}
	return project
}

func (f *integrationHTTPFixture) setApplication(t *testing.T, application *unstructured.Unstructured) {
	t.Helper()
	raw, err := yaml.Marshal(application.Object)
	if err != nil {
		t.Fatalf("marshal Application: %v", err)
	}
	f.mu.Lock()
	f.applicationYAML = string(raw)
	f.mu.Unlock()
}

func (f *integrationHTTPFixture) application(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	f.mu.Lock()
	raw := []byte(f.applicationYAML)
	f.mu.Unlock()
	var object map[string]any
	if err := yaml.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode Application YAML: %v", err)
	}
	return &unstructured.Unstructured{Object: object}
}

func (f *integrationHTTPFixture) table(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	f.mu.Lock()
	raw := []byte(f.tableYAML)
	f.mu.Unlock()
	var object map[string]any
	if err := yaml.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode Table YAML: %v", err)
	}
	return &unstructured.Unstructured{Object: object}
}

func (f *integrationHTTPFixture) actionRequests() []integrationActionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	requests := make([]integrationActionRequest, len(f.actionReqs))
	copy(requests, f.actionReqs)
	return requests
}

func projectWithTableIntegration(revoked bool) *aiv1alpha1.Project {
	grantedAt := metav1.Now()
	return &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Demo",
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
				Name: "development", Mode: aiv1alpha1.ProjectEnvironmentModeLive,
				Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
					Name: "sales", Provider: projectIntegrationProviderDatabricks,
					Kind: aiv1alpha1.ProjectBindingKindProviderReference,
					ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
						Name: "orders", APIVersion: databricksTableAPIVersion,
						Kind: databricksTableKind, Resource: databricksTableResource,
					},
					AllowedActions: []aiv1alpha1.ProjectProviderActionSpec{{Name: projectIntegrationActionQueryTable, Version: projectIntegrationActionVersionV1, SchemaDigest: testProjectActionSchemaDigest, GrantedBy: "alice@example.com", GrantedAt: &grantedAt, Revoked: revoked}},
				}},
			}},
		},
	}
}

func projectWithDevelopmentRuntimeBinding() *aiv1alpha1.Project {
	project := &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Demo",
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
				Name: "development", Mode: aiv1alpha1.ProjectEnvironmentModeLive,
				Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
					Name: projectDevelopmentBindingName, Provider: projectDevelopmentProviderAppStudio,
					Kind: aiv1alpha1.ProjectBindingKindProviderResource,
					ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
						Name: "demo-dev", APIVersion: "infrastructure.kedge.faros.sh/v1alpha1", Kind: "Application", Resource: "applications",
					},
					Values: runtime.RawExtension{Raw: []byte(`{
						"name":"demo-dev",
						"kedgeMode":"development",
						"kedgeActionsExchangeURL":"https://stale.example/api/provider-actions/workload/exchange",
						"kedgeActionsBaseURL":"https://stale.example/services/providers/app-studio",
						"kedgeActionsTenantPath":"stale-tenant"
					}`)},
				}},
			}},
		},
	}
	return project
}

func developmentApplicationObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "demo-dev"},
		"spec": map[string]any{
			"name":                    "demo-dev",
			"kedgeMode":               "development",
			"kedgeActionsExchangeURL": "https://stale.example/api/provider-actions/workload/exchange",
			"kedgeActionsBaseURL":     "https://stale.example/services/providers/app-studio",
			"kedgeActionsTenantPath":  "stale-tenant",
			"kedgeActionsProject":     "stale-project",
			"kedgeActionsProjectUID":  "stale-project-uid",
			"kedgeActionsEnvironment": "stale-environment",
			"kedgeActionsInstance":    "stale-instance",
			"kedgeActionsOrg":         "stale-org",
			"kedgeActionsWorkspace":   "stale-workspace",
		},
	}}
}

func integrationTestCatalogResolver(context.Context, identity) ([]providerCatalogEntry, error) {
	return []providerCatalogEntry{
		{
			Name: "databricks", Ready: true,
			Actions: []providerCatalogAction{{
				ID: "query_table/v1", SchemaDigest: testProjectActionSchemaDigest,
				BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource},
			}},
		},
		{
			Name: "other", Ready: true,
			Actions: []providerCatalogAction{{
				ID: "query_table/v1", SchemaDigest: testProjectActionSchemaDigest,
				BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource},
			}},
		},
	}, nil
}

func integrationHTTPTestRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-User", "alice@example.com")
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Org", "org-a")
	request.Header.Set("X-Kedge-Workspace", "workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	return request
}

func TestProjectIntegrationCRUDInvokeAndForwardingContract(t *testing.T) {
	fixture := newIntegrationHTTPFixture(t, &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
	})
	server := NewWithWorkspace(tenant.NewGraphQLClient(fixture.graphql.URL, false), nil, nil, fixture.hub.URL, false)
	server.actionsExternalURL = "https://actions.example"
	server.providerActionCatalogResolver = integrationTestCatalogResolver
	router := mux.NewRouter()
	server.Register(router)

	add := integrationHTTPTestRequest(http.MethodPost, "/api/projects/demo/integrations", `{"alias":"sales","provider":"databricks","resourceRef":{"name":"orders","apiVersion":"databricks.kedge.faros.sh/v1alpha1","kind":"Table","resource":"tables"},"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"`+testProjectActionSchemaDigest+`"}]}`)
	addResponse := httptest.NewRecorder()
	router.ServeHTTP(addResponse, add)
	if addResponse.Code != http.StatusCreated {
		t.Fatalf("add integration status = %d: %s", addResponse.Code, addResponse.Body.String())
	}
	created := fixture.project(t)
	binding := created.Spec.Environments[0].Bindings[0]
	if binding.Kind != aiv1alpha1.ProjectBindingKindProviderReference || binding.ResourceRef == nil || binding.ResourceRef.Name != "orders" {
		t.Fatalf("created binding = %#v, want non-owning reference to orders", binding)
	}

	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, integrationHTTPTestRequest(http.MethodGet, "/api/projects/demo/integrations", ""))
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"alias":"sales"`) {
		t.Fatalf("list integration response = %d %s", listResponse.Code, listResponse.Body.String())
	}

	patchResponse := httptest.NewRecorder()
	router.ServeHTTP(patchResponse, integrationHTTPTestRequest(http.MethodPatch, "/api/projects/demo/integrations/sales", `{"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"`+testProjectActionSchemaDigest+`"}]}`))
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("patch integration status = %d: %s", patchResponse.Code, patchResponse.Body.String())
	}

	invokeResponse := httptest.NewRecorder()
	invokeRequest := integrationHTTPTestRequest(http.MethodPost, "/api/projects/demo/integrations/sales/invoke", `{"action":"query_table/v1","input":{"columns":["id"],"limit":2}}`)
	invokeRequest.Header.Set("Idempotency-Key", "idem-1")
	invokeRequest.Header.Set("X-Request-ID", "request-1")
	invokeRequest.Header.Set("X-Kedge-Action-Deadline-Ms", "45000")
	router.ServeHTTP(invokeResponse, invokeRequest)
	if invokeResponse.Code != http.StatusOK {
		t.Fatalf("invoke status = %d: %s", invokeResponse.Code, invokeResponse.Body.String())
	}
	var invokeBody projectProviderActionEnvelope
	if err := json.Unmarshal(invokeResponse.Body.Bytes(), &invokeBody); err != nil {
		t.Fatalf("decode invoke response: %v", err)
	}
	if invokeBody.RequestID != "hub-request-1" || invokeBody.Provider != "databricks" || invokeBody.Action != "query_table" || invokeBody.ActionVersion != "v1" {
		t.Fatalf("invoke response = %#v, want stable query_table/v1 envelope", invokeBody)
	}
	requests := fixture.actionRequests()
	if len(requests) != 1 {
		t.Fatalf("provider action calls = %d, want exactly one hub call", len(requests))
	}
	actionRequest := requests[0]
	// The route IS the resource reference: provider, cluster, resource, name,
	// action, and version are all addressed in the path on the provider's
	// embedded virtual workspace, reached through the hub backend proxy.
	if actionRequest.URL != "/services/providers/databricks/actions/clusters/cluster-a/tables/orders/query_table/v1" {
		t.Fatalf("provider action URL = %q, want data-plane action route", actionRequest.URL)
	}
	if actionRequest.Headers.Get("Authorization") != "Bearer caller-token" ||
		actionRequest.Headers.Get("X-Kedge-Tenant") != "root:kedge:tenants:org-a:workspace-a" ||
		actionRequest.Headers.Get("X-Kedge-Cluster") != "cluster-a" ||
		actionRequest.Headers.Get("Idempotency-Key") != "idem-1" ||
		actionRequest.Headers.Get("X-Request-ID") != "request-1" ||
		actionRequest.Headers.Get("X-Kedge-Action-Deadline-Ms") != "45000" {
		t.Fatalf("provider action caller headers = %#v, want propagated auth/tenant/correlation/deadline", actionRequest.Headers)
	}
	for _, field := range []string{"provider", "action", "actionVersion", "schemaDigest", "resourceRef"} {
		if _, present := actionRequest.Body[field]; present {
			t.Fatalf("provider action body carried identity field %q; identity must live in the route only", field)
		}
	}
	input, ok := actionRequest.Body["input"].(map[string]any)
	if !ok || input["limit"] != float64(2) {
		t.Fatalf("provider action input = %#v, want bounded caller input", actionRequest.Body["input"])
	}
	if _, exposed := input["resourceRef"]; exposed {
		t.Fatal("provider action input exposed an overrideable resourceRef")
	}

	deleteResponse := httptest.NewRecorder()
	router.ServeHTTP(deleteResponse, integrationHTTPTestRequest(http.MethodDelete, "/api/projects/demo/integrations/sales", ""))
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete integration status = %d: %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	removed := fixture.project(t)
	if len(removed.Spec.Environments) != 1 || len(removed.Spec.Environments[0].Bindings) != 0 || len(fixture.actionRequests()) != 1 {
		t.Fatalf("after deletion project/invocation state = %#v/%d, want empty development bindings and no extra provider action call", removed.Spec.Environments, len(fixture.actionRequests()))
	}
	if got := fixture.table(t).GetName(); got != "orders" {
		t.Fatalf("referenced Table after binding removal = %q, want orders", got)
	}
}

func TestProjectIntegrationMutationsDoNotReconcileDevelopmentActionContext(t *testing.T) {
	fixture := newIntegrationHTTPFixture(t, projectWithDevelopmentRuntimeBinding())
	fixture.setApplication(t, developmentApplicationObject())
	server := NewWithWorkspace(tenant.NewGraphQLClient(fixture.graphql.URL, false), nil, nil, fixture.hub.URL, false)
	server.actionsExternalURL = "https://actions.example"
	server.providerActionCatalogResolver = integrationTestCatalogResolver
	router := mux.NewRouter()
	server.Register(router)

	add := integrationHTTPTestRequest(http.MethodPost, "/api/projects/demo/integrations", `{"alias":"sales","provider":"databricks","resourceRef":{"name":"orders","apiVersion":"databricks.kedge.faros.sh/v1alpha1","kind":"Table","resource":"tables"},"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"`+testProjectActionSchemaDigest+`"}]}`)
	addResponse := httptest.NewRecorder()
	router.ServeHTTP(addResponse, add)
	if addResponse.Code != http.StatusCreated {
		t.Fatalf("add integration status = %d: %s", addResponse.Code, addResponse.Body.String())
	}
	addedApplication := fixture.application(t)
	addedSpec, ok := addedApplication.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("Application spec = %#v, want object", addedApplication.Object["spec"])
	}
	if got := addedSpec["kedgeActionsExchangeURL"]; got != "https://stale.example/api/provider-actions/workload/exchange" {
		t.Fatalf("after grant kedgeActionsExchangeURL = %v, want unchanged provider resource", got)
	}
	if got := addedSpec["kedgeActionsBaseURL"]; got != "https://stale.example/services/providers/app-studio" {
		t.Fatalf("after grant kedgeActionsBaseURL = %v, want unchanged provider resource", got)
	}

	// Revocation remains a Project-spec mutation even when the external origin
	// is absent. The controller will clear the owning runtime transport on its
	// next reconciliation; this request does not mutate the provider object.
	server.actionsExternalURL = ""
	revoke := integrationHTTPTestRequest(http.MethodPatch, "/api/projects/demo/integrations/sales", `{"allowedActions":[{"name":"query_table","version":"v1","revoked":true}]}`)
	revokeResponse := httptest.NewRecorder()
	router.ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusOK {
		t.Fatalf("revoke integration status = %d: %s", revokeResponse.Code, revokeResponse.Body.String())
	}
	revokedApplication := fixture.application(t)
	revokedSpec, ok := revokedApplication.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("reconciled Application spec after revoke = %#v, want object", revokedApplication.Object["spec"])
	}
	for field, want := range map[string]string{
		"kedgeActionsExchangeURL": "https://stale.example/api/provider-actions/workload/exchange",
		"kedgeActionsBaseURL":     "https://stale.example/services/providers/app-studio",
	} {
		if got := revokedSpec[field]; got != want {
			t.Fatalf("after grant revocation %s = %v, want unchanged provider resource", field, got)
		}
	}

	remove := integrationHTTPTestRequest(http.MethodDelete, "/api/projects/demo/integrations/sales", "")
	removeResponse := httptest.NewRecorder()
	router.ServeHTTP(removeResponse, remove)
	if removeResponse.Code != http.StatusNoContent {
		t.Fatalf("remove integration status = %d: %s", removeResponse.Code, removeResponse.Body.String())
	}
	removedApplication := fixture.application(t)
	removedSpec, ok := removedApplication.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("reconciled Application spec after removal = %#v, want object", removedApplication.Object["spec"])
	}
	for field, want := range map[string]string{
		"kedgeActionsExchangeURL": "https://stale.example/api/provider-actions/workload/exchange",
		"kedgeActionsBaseURL":     "https://stale.example/services/providers/app-studio",
	} {
		if got := removedSpec[field]; got != want {
			t.Fatalf("after grant removal %s = %v, want unchanged provider resource", field, got)
		}
	}
}

func TestProjectIntegrationAddRejectsMissingActionsURLWithoutMutation(t *testing.T) {
	initial := projectWithDevelopmentRuntimeBinding()
	fixture := newIntegrationHTTPFixture(t, initial)
	fixture.setApplication(t, developmentApplicationObject())
	before := fixture.project(t)
	server := NewWithWorkspace(tenant.NewGraphQLClient(fixture.graphql.URL, false), nil, nil, fixture.hub.URL, false)
	server.providerActionCatalogResolver = integrationTestCatalogResolver
	router := mux.NewRouter()
	server.Register(router)

	add := integrationHTTPTestRequest(http.MethodPost, "/api/projects/demo/integrations", `{"alias":"sales","provider":"databricks","resourceRef":{"name":"orders","apiVersion":"databricks.kedge.faros.sh/v1alpha1","kind":"Table","resource":"tables"},"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"`+testProjectActionSchemaDigest+`"}]}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, add)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("add without actions URL status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "KEDGE_ACTIONS_EXTERNAL_URL") {
		t.Fatalf("add without actions URL error = %s, want configuration guidance", response.Body.String())
	}
	if got := fixture.project(t); !reflect.DeepEqual(got.Spec, before.Spec) {
		t.Fatalf("Project changed after rejected grant: got %#v, want %#v", got.Spec, before.Spec)
	}
	application := fixture.application(t)
	spec, ok := application.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("runtime Application spec = %#v, want object", application.Object["spec"])
	}
	if got := spec["kedgeActionsExchangeURL"]; got != "https://stale.example/api/provider-actions/workload/exchange" {
		t.Fatalf("runtime changed after rejected grant: kedgeActionsExchangeURL = %v", got)
	}
}

func TestProjectIntegrationPatchRejectsReactivationWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "missing"},
		{name: "http", url: "http://hub.example"},
		{name: "path", url: "https://hub.example/actions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testProjectIntegrationPatchPreflight(t, tc.url)
		})
	}
}

func testProjectIntegrationPatchPreflight(t *testing.T, actionsURL string) {
	t.Helper()
	initial := projectWithDevelopmentRuntimeBinding()
	initial.Spec.Environments[0].Bindings = append(initial.Spec.Environments[0].Bindings,
		aiv1alpha1.ProjectProviderBindingSpec{
			Name: "sales", Provider: projectIntegrationProviderDatabricks,
			Kind: aiv1alpha1.ProjectBindingKindProviderReference,
			ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
				Name: "orders", APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource,
			},
			AllowedActions: []aiv1alpha1.ProjectProviderActionSpec{{
				Name: projectIntegrationActionQueryTable, Version: projectIntegrationActionVersionV1,
				SchemaDigest: testProjectActionSchemaDigest, Revoked: true,
			}},
		})
	fixture := newIntegrationHTTPFixture(t, initial)
	fixture.setApplication(t, developmentApplicationObject())
	before := fixture.project(t)
	server := NewWithWorkspace(tenant.NewGraphQLClient(fixture.graphql.URL, false), nil, nil, fixture.hub.URL, false)
	server.actionsExternalURL = actionsURL
	server.providerActionCatalogResolver = integrationTestCatalogResolver
	router := mux.NewRouter()
	server.Register(router)

	patch := integrationHTTPTestRequest(http.MethodPatch, "/api/projects/demo/integrations/sales", `{"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"`+testProjectActionSchemaDigest+`"}]}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, patch)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("reactivation with actions URL %q status = %d, want %d: %s", actionsURL, response.Code, http.StatusBadRequest, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "KEDGE_ACTIONS_EXTERNAL_URL") {
		t.Fatalf("reactivation with actions URL %q error = %s, want configuration guidance", actionsURL, response.Body.String())
	}
	if got := fixture.project(t); !reflect.DeepEqual(got.Spec, before.Spec) {
		t.Fatalf("Project changed after rejected reactivation: got %#v, want %#v", got.Spec, before.Spec)
	}
	application := fixture.application(t)
	spec, ok := application.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("runtime Application spec = %#v, want object", application.Object["spec"])
	}
	if got := spec["kedgeActionsExchangeURL"]; got != "https://stale.example/api/provider-actions/workload/exchange" {
		t.Fatalf("runtime changed after rejected reactivation: kedgeActionsExchangeURL = %v", got)
	}
}

func TestProjectIntegrationInvokeRejectsBeforeHubForward(t *testing.T) {
	fixture := newIntegrationHTTPFixture(t, projectWithTableIntegration(false))
	server := NewWithWorkspace(tenant.NewGraphQLClient(fixture.graphql.URL, false), nil, nil, fixture.hub.URL, false)
	server.actionsExternalURL = "https://actions.example"
	server.providerActionCatalogResolver = integrationTestCatalogResolver
	router := mux.NewRouter()
	server.Register(router)

	tests := []struct {
		name        string
		alias       string
		project     *aiv1alpha1.Project
		body        string
		wantStatus  int
		wantForward bool
	}{
		{name: "unbound", alias: "missing", project: projectWithTableIntegration(false), body: `{"action":"query_table/v1","input":{}}`, wantStatus: http.StatusNotFound},
		{name: "ambiguous", alias: "sales", project: integrationAmbiguousProject(), body: `{"action":"query_table/v1","input":{}}`, wantStatus: http.StatusBadRequest},
		{name: "unknown-action", alias: "sales", project: projectWithTableIntegration(false), body: `{"action":"drop_table/v1","input":{}}`, wantStatus: http.StatusForbidden},
		{name: "mismatched-version", alias: "sales", project: projectWithTableIntegration(false), body: `{"action":"query_table/v1","actionVersion":"v2","input":{}}`, wantStatus: http.StatusBadRequest},
		{name: "revoked", alias: "sales", project: projectWithTableIntegration(true), body: `{"action":"query_table/v1","input":{}}`, wantStatus: http.StatusForbidden},
		{name: "credentials", alias: "sales", project: projectWithTableIntegration(false), body: `{"action":"query_table/v1","input":{"credentials":"secret"}}`, wantStatus: http.StatusBadRequest},
		{name: "table-ref", alias: "sales", project: projectWithTableIntegration(false), body: `{"action":"query_table/v1","input":{"tableRef":"other"}}`, wantStatus: http.StatusBadRequest},
		{name: "provider-override", alias: "sales", project: projectWithTableIntegration(false), body: `{"action":"query_table/v1","input":{"provider":"other"}}`, wantStatus: http.StatusBadRequest},
		// endpoint is provider-domain action input, not a platform-owned
		// routing field. The generic gateway forwards it to the selected
		// provider; provider schemas own whether it is meaningful or allowed.
		{name: "endpoint-provider-input", alias: "sales", project: projectWithTableIntegration(false), body: `{"action":"query_table/v1","input":{"endpoint":"https://attacker.invalid"}}`, wantStatus: http.StatusOK, wantForward: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture.setProject(t, tt.project)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, integrationHTTPTestRequest(http.MethodPost, "/api/projects/demo/integrations/"+tt.alias+"/invoke", tt.body))
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.wantStatus, response.Body.String())
			}
			if got := len(fixture.actionRequests()); tt.wantForward {
				if got != 1 {
					t.Fatalf("forwarded invocation reached hub provider-action endpoint %d time(s), want one", got)
				}
			} else if got != 0 {
				t.Fatalf("rejected invocation reached hub provider-action endpoint %d time(s)", got)
			}
		})
	}
}

func TestProjectIntegrationInvokeForwardsGenericProviderAndInput(t *testing.T) {
	fixture := newIntegrationHTTPFixture(t, integrationProjectWithProvider("other"))
	server := NewWithWorkspace(tenant.NewGraphQLClient(fixture.graphql.URL, false), nil, nil, fixture.hub.URL, false)
	server.actionsExternalURL = "https://actions.example"
	server.providerActionCatalogResolver = integrationTestCatalogResolver
	router := mux.NewRouter()
	server.Register(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, integrationHTTPTestRequest(http.MethodPost, "/api/projects/demo/integrations/sales/invoke", `{"action":"query_table/v1","input":{"sql":"provider-defined","options":{"limit":2}}}`))
	if response.Code != http.StatusOK {
		t.Fatalf("generic provider action status = %d: %s", response.Code, response.Body.String())
	}
	requests := fixture.actionRequests()
	if len(requests) != 1 || !strings.HasPrefix(requests[0].URL, "/services/providers/other/actions/clusters/") {
		t.Fatalf("generic provider action calls = %#v, want one forward routed to provider other", requests)
	}
	input, ok := requests[0].Body["input"].(map[string]any)
	if !ok || input["sql"] != "provider-defined" {
		t.Fatalf("generic action input = %#v, want provider-defined payload unchanged", requests[0].Body["input"])
	}
}

func integrationProjectWithProvider(provider string) *aiv1alpha1.Project {
	project := projectWithTableIntegration(false)
	project.Spec.Environments[0].Bindings[0].Provider = provider
	return project
}

func integrationAmbiguousProject() *aiv1alpha1.Project {
	project := projectWithTableIntegration(false)
	second := project.Spec.Environments[0]
	second.Name = "production"
	second.Mode = aiv1alpha1.ProjectEnvironmentModeArtifact
	project.Spec.Environments = append(project.Spec.Environments, second)
	return project
}

func TestProviderReferenceSurvivesTemplateSwitchPromotionAndProjectCleanup(t *testing.T) {
	project := projectWithTableIntegration(false)
	project.Spec.Environments[0].Bindings = append(project.Spec.Environments[0].Bindings,
		aiv1alpha1.ProjectProviderBindingSpec{
			Name: projectDevelopmentBindingName, Provider: projectDevelopmentProviderAppStudio,
			Kind: aiv1alpha1.ProjectBindingKindProviderResource,
			ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
				Name: "demo-dev", APIVersion: "infrastructure.kedge.faros.sh/v1alpha1", Kind: "Application", Resource: "applications",
			},
		})
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add App Studio scheme: %v", err)
	}
	applicationGVR := schema.GroupVersionResource{Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1", Resource: "applications"}
	oldApplication := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "demo-dev"},
	}}
	table := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databricksTableAPIVersion, "kind": databricksTableKind,
		"metadata": map[string]any{"name": "orders"},
	}}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		asclient.ProjectGVR: "ProjectList", testDatabricksTableGVR: "TableList", applicationGVR: "ApplicationList",
	}, project, table, oldApplication)
	for _, verb := range []string{"create", "update", "delete", "patch"} {
		verb := verb
		dyn.PrependReactor(verb, "tables", func(k8stesting.Action) (bool, runtime.Object, error) {
			t.Fatalf("providerReference Table was mutated during %s", verb)
			return true, nil, nil
		})
	}
	c := asclient.NewFromDynamic(dyn)
	id := identity{tenantPath: "root:kedge:tenants:org-a:workspace-a", clusterID: "cluster-a"}

	if err := (&Server{}).deleteProjectDevelopmentBindingResources(context.Background(), c, project, id); err != nil {
		t.Fatalf("delete old template binding: %v", err)
	}
	info, err := projectTemplateInfoFromUnstructured(applicationTemplateObject())
	if err != nil {
		t.Fatalf("template info: %v", err)
	}
	if err := applyProjectDevelopmentTemplateWithContext(project, info, projectTemplateBindingContext{
		ActionsExchangeURL: "https://hub.example/api/provider-actions/workload/exchange",
		ActionsBaseURL:     "https://hub.example/services/providers/app-studio",
	}); err != nil {
		t.Fatalf("switch template: %v", err)
	}
	upsertProjectProductionBinding(project, aiv1alpha1.ProjectProviderBindingSpec{
		Name: projectProductionBindingName, Provider: projectDevelopmentProviderAppStudio,
		Kind: aiv1alpha1.ProjectBindingKindProviderResource,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			Name: "demo-prod", APIVersion: "infrastructure.kedge.faros.sh/v1alpha1", Kind: "Application", Resource: "applications",
		},
	})
	if _, err := (&Server{actionsExternalURL: "https://hub.example"}).reconcileProjectLiveBindings(context.Background(), c, project, id); err != nil {
		t.Fatalf("reconcile after template switch/promotion: %v", err)
	}
	if err := (&Server{}).deleteProjectProviderResources(context.Background(), c, project, id); err != nil {
		t.Fatalf("project cleanup: %v", err)
	}
	if _, err := c.Resource(providerBindingResource(testDatabricksTableGVR, databricksTableKind), "").Get(context.Background(), "orders", metav1.GetOptions{}); err != nil {
		t.Fatalf("referenced Table did not survive template switch, promotion, and cleanup: %v", err)
	}
	refFound := false
	for _, env := range project.Spec.Environments {
		for _, binding := range env.Bindings {
			if binding.Name == "sales" && binding.Kind == aiv1alpha1.ProjectBindingKindProviderReference {
				refFound = true
			}
		}
	}
	if !refFound {
		t.Fatal("template switch/promotion removed the providerReference binding")
	}
}
