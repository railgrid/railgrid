// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package provideractions

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	testProject     = "provider-actions-e2e"
	testAlias       = "taxi"
	tableRef        = "taxi-trips"
	pat             = "e2e-pat-not-an-app-input"
	runtimeInstance = "provider-actions-e2e-runtime"
)

func TestProviderActionQueryThroughGeneratedNodeSDK(t *testing.T) {
	requireLocalSuite(t)
	orgUUID, workspaceUUID, tenantCluster := setupTenantWorkspace(t)
	tenant := kcpDynamic(t, tenantCluster, staticToken)
	wrongOrgUUID, wrongWorkspaceUUID, _ := setupTenantWorkspace(t)

	bindProvider(t, tenant, "databricks", databricksWorkspace, databricksExport, []string{"get"})
	bindProvider(t, tenant, "app-studio", appStudioWorkspace, appStudioExport, []string{"get", "list", "watch", "create", "update", "delete"})
	waitTenantProxyContext(t, orgUUID, workspaceUUID)
	tenantHeaders := map[string]string{"X-Faros-Org": orgUUID, "X-Faros-Workspace": workspaceUUID}

	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "e2e-databricks-token", "namespace": "default"},
		"type":     "Opaque",
		"data":     map[string]any{"token": base64.StdEncoding.EncodeToString([]byte(pat))},
	}}
	createOrUpdate(t, tenant.Resource(secretGVR).Namespace("default"), secret)
	t.Cleanup(func() {
		_ = tenant.Resource(secretGVR).Namespace("default").Delete(context.Background(), secret.GetName(), metav1.DeleteOptions{})
	})

	connection := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "databricks.faros.sh/v1alpha1", "kind": "Connection",
		"metadata": map[string]any{"name": "e2e-databricks-connection"},
		"spec": map[string]any{
			"host": fakeDB.URL(), "authType": "pat",
			"secretRef": map[string]any{"name": secret.GetName(), "namespace": "default", "key": "token"},
		},
	}}
	createOrUpdate(t, tenant.Resource(connectionGVR), connection)
	t.Cleanup(func() {
		_ = tenant.Resource(connectionGVR).Delete(context.Background(), connection.GetName(), metav1.DeleteOptions{})
	})
	waitCondition(t, 90*time.Second, func() (bool, string) {
		return readyCondition(tenant, connectionGVR, connection.GetName(), "Ready")
	}, "Connection Ready")

	warehouse := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "databricks.faros.sh/v1alpha1", "kind": "Warehouse",
		"metadata": map[string]any{"name": "e2e-databricks-warehouse"},
		"spec":     map[string]any{"connectionRef": connection.GetName(), "warehouseID": "e2e-warehouse"},
	}}
	createOrUpdate(t, tenant.Resource(warehouseGVR), warehouse)
	t.Cleanup(func() {
		_ = tenant.Resource(warehouseGVR).Delete(context.Background(), warehouse.GetName(), metav1.DeleteOptions{})
	})
	waitCondition(t, 90*time.Second, func() (bool, string) {
		return readyCondition(tenant, warehouseGVR, warehouse.GetName(), "Ready")
	}, "Warehouse Ready")

	table := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "databricks.faros.sh/v1alpha1", "kind": "Table",
		"metadata": map[string]any{"name": tableRef},
		"spec": map[string]any{
			"connectionRef": connection.GetName(), "warehouseRef": warehouse.GetName(),
			"catalog": "analytics", "schema": "gold", "table": "taxi_trips",
		},
	}}
	createOrUpdate(t, tenant.Resource(tableGVR), table)
	t.Cleanup(func() {
		_ = tenant.Resource(tableGVR).Delete(context.Background(), table.GetName(), metav1.DeleteOptions{})
	})
	waitCondition(t, 90*time.Second, func() (bool, string) {
		return readyCondition(tenant, tableGVR, table.GetName(), "Ready")
	}, "Table Ready")

	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.faros.sh/v1alpha1", "kind": "Project",
		"metadata": map[string]any{"name": testProject},
		"spec":     map[string]any{"displayName": "Provider actions E2E"},
	}}
	createOrUpdate(t, tenant.Resource(projectGVR), project)
	t.Cleanup(func() {
		_ = tenant.Resource(projectGVR).Delete(context.Background(), project.GetName(), metav1.DeleteOptions{})
	})

	digest := catalogActionSchemaDigest(t, "databricks", "query_table/v1")
	addBody := map[string]any{
		"environment": "development",
		"alias":       testAlias,
		"provider":    "databricks",
		"resourceRef": map[string]any{
			"name": tableRef, "apiVersion": "databricks.faros.sh/v1alpha1", "kind": "Table", "resource": "tables",
		},
		"allowedActions": []any{map[string]any{"name": "query_table", "version": "v1", "schemaDigest": digest}},
	}
	status, body := postJSON(t, hubURL+"/services/providers/app-studio/api/projects/"+testProject+"/integrations", staticToken, addBody, tenantHeaders)
	if status != http.StatusCreated {
		t.Fatalf("add project integration: status=%d body=%s", status, body)
	}
	ensureInfrastructureBinding(t, tenant, runtimeInstance)
	assertProjectReferenceBinding(t, tenant, table, digest, runtimeInstance)
	projectObject := getProject(t, tenant)
	projectUID := string(projectObject.GetUID())
	if projectUID == "" {
		t.Fatal("project has no UID for workload exchange")
	}
	tenantPath := "root:faros:tenants:" + orgUUID + ":" + workspaceUUID
	runtimeToken := exchangeWorkloadToken(t, tenantPath, projectUID, runtimeInstance, bootstrapToken)
	tokenFile := filepath.Join(dataDir, "provider-actions-runtime.token")
	if err := os.WriteFile(tokenFile, []byte(runtimeToken+"\n"), 0o600); err != nil {
		t.Fatalf("write workload token file: %v", err)
	}
	info, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatalf("stat workload token file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("workload token file mode=%#o, want 0600", got)
	}
	if requests := fakeAttestor.Requests(); len(requests) != 1 || requests[0].TenantPath != tenantPath || requests[0].Project != testProject || requests[0].ProjectUID != projectUID || requests[0].Environment != "development" || requests[0].Instance != runtimeInstance {
		t.Fatalf("unexpected Infrastructure attestation requests: %#v", requests)
	}
	if status, body := postJSON(t, hubURL+"/api/provider-actions/workload/exchange", staticToken, map[string]any{
		"tenantPath": tenantPath, "project": testProject, "projectUID": projectUID, "environment": "development", "instance": runtimeInstance,
	}); status != http.StatusForbidden {
		t.Fatalf("non-bootstrap exchange status=%d body=%s, want 403", status, body)
	}

	assertDatabricksMCPUnavailable(t)
	assertWorkloadReviewReserved(t, tenantHeaders)
	assertDirectActionAuthorization(t, tenantCluster, runtimeToken, tenantHeaders)

	input := map[string]any{"columns": []any{"trip_id", "fare_amount"}, "limit": 2}
	stdout, stderr, err := runGeneratedApp(t, hubURL, testProject, testAlias, "query_table/v1", input, tokenFile, tenantHeaders)
	if err != nil {
		t.Fatalf("generated app query failed: %v (stderr=%s)", err, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode generated app result: %v (stdout=%s)", err, stdout)
	}
	if got := result["actionVersion"]; got != "v1" {
		t.Fatalf("actionVersion=%v, want v1", got)
	}
	if got := result["tableRef"]; got != tableRef {
		t.Fatalf("tableRef=%v, want %s", got, tableRef)
	}
	rows, ok := result["rows"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("rows=%#v, want exactly two bounded rows", result["rows"])
	}
	wantRows := []any{
		map[string]any{"trip_id": float64(101), "fare_amount": 18.25},
		map[string]any{"trip_id": float64(202), "fare_amount": 27.50},
	}
	if !reflect.DeepEqual(rows, wantRows) {
		t.Fatalf("rows=%#v, want %#v", rows, wantRows)
	}

	selectRequest, ok := fakeDB.LastSelect()
	if !ok {
		t.Fatal("fake upstream received no SELECT statement")
	}
	wantSQL := "SELECT `trip_id`, `fare_amount` FROM `analytics`.`gold`.`taxi_trips` LIMIT 2"
	if selectRequest.Statement != wantSQL {
		t.Fatalf("fake SELECT=%q, want %q", selectRequest.Statement, wantSQL)
	}
	if selectRequest.WarehouseID != "e2e-warehouse" {
		t.Fatalf("fake warehouse_id=%q, want e2e-warehouse", selectRequest.WarehouseID)
	}
	if selectRequest.Authorization != "Bearer "+pat {
		t.Fatalf("fake Authorization=%q, want provider-resolved PAT", selectRequest.Authorization)
	}
	var sawDescribe bool
	for _, request := range fakeDB.Requests() {
		if request.Statement == "DESCRIBE TABLE `analytics`.`gold`.`taxi_trips`" {
			sawDescribe = true
		}
	}
	if !sawDescribe {
		t.Fatal("fake upstream did not receive the Table schema DESCRIBE target")
	}
	for _, unsafe := range []string{fakeDB.URL(), pat, runtimeToken} {
		if strings.Contains(stdout, unsafe) || strings.Contains(stderr, unsafe) {
			t.Fatalf("generated app output leaked provider URL or PAT: %q", unsafe)
		}
	}
	writeEvidence("invocation.json", map[string]any{
		"surface": "generated-app -> generic @faros/actions-node -> App Studio integration gateway -> hub backend proxy -> Databricks /actions/clusters/{cluster}/tables/{name}/query_table/v1",
		"project": testProject, "integration": testAlias, "action": "query_table/v1",
		"input": input, "directProviderURL": false, "patInInput": false, "mcp": false, "workloadTokenFile": true,
	})
	writeEvidence("result.json", map[string]any{
		"actionVersion": result["actionVersion"], "tableRef": result["tableRef"], "rows": rows,
		"fakeStatement": selectRequest.Statement, "warehouseID": selectRequest.WarehouseID,
		"interactionVerified": true,
	})

	beforeFailures := len(fakeDB.Requests())
	if _, stderr, err := runGeneratedApp(t, hubURL, testProject, "unbound", "query_table/v1", input, tokenFile, tenantHeaders); err == nil {
		t.Fatalf("unbound integration unexpectedly succeeded (stderr=%s)", stderr)
	} else if !strings.Contains(stderr, "404") {
		t.Fatalf("unbound integration did not fail closed with 404: %s", stderr)
	}
	if _, stderr, err := runGeneratedApp(t, hubURL, testProject, testAlias, "query_table/v2", input, tokenFile, tenantHeaders); err == nil {
		t.Fatalf("unsupported action version unexpectedly succeeded (stderr=%s)", stderr)
	} else if !strings.Contains(stderr, "403") {
		t.Fatalf("unsupported action version did not fail closed with 403: %s", stderr)
	}
	if _, stderr, err := runGeneratedApp(t, hubURL, testProject, testAlias, "query_table/v1", input, tokenFile, map[string]string{"X-Faros-Org": wrongOrgUUID, "X-Faros-Workspace": wrongWorkspaceUUID}); err == nil {
		t.Fatalf("wrong-tenant action unexpectedly succeeded (stderr=%s)", stderr)
	} else if !strings.Contains(stderr, "401") && !strings.Contains(stderr, "403") && !strings.Contains(stderr, "404") {
		t.Fatalf("wrong-tenant action did not fail closed: %s", stderr)
	}
	if got := len(fakeDB.Requests()); got != beforeFailures {
		t.Fatalf("wrong-tenant action reached fake upstream: request count %d before %d", got, beforeFailures)
	}
	// A validly-shaped but stale digest is rejected by App Studio's
	// invoke-time catalog re-verification before the provider endpoint is
	// contacted. Mutate the live Project through the tenant API to model
	// catalog drift without bypassing the production App Studio gateway.
	projectObject = getProject(t, tenant)
	if err := setProjectActionDigest(projectObject, "sha256:"+strings.Repeat("0", 64)); err != nil {
		t.Fatalf("set drifted action digest: %v", err)
	}
	updatedProject, err := tenant.Resource(projectGVR).Update(context.Background(), projectObject, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("persist drifted action digest: %v", err)
	}
	projectObject = updatedProject
	if _, stderr, err := runGeneratedApp(t, hubURL, testProject, testAlias, "query_table/v1", input, tokenFile, tenantHeaders); err == nil {
		t.Fatalf("catalog-drifted action unexpectedly succeeded (stderr=%s)", stderr)
	} else if !strings.Contains(stderr, "409") {
		t.Fatalf("catalog-drifted action did not fail closed with 409: %s", stderr)
	}
	if got := len(fakeDB.Requests()); got != beforeFailures {
		t.Fatalf("catalog-drifted action reached fake upstream: request count %d before %d", got, beforeFailures)
	}
	if err := setProjectActionDigest(projectObject, digest); err != nil {
		t.Fatalf("restore action digest: %v", err)
	}
	_, err = tenant.Resource(projectGVR).Update(context.Background(), projectObject, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("restore action digest: %v", err)
	}
	patchBody := map[string]any{"allowedActions": []any{map[string]any{"name": "query_table", "version": "v1", "schemaDigest": digest, "revoked": true}}}
	status, body = patchJSON(t, hubURL+"/services/providers/app-studio/api/projects/"+testProject+"/integrations/"+testAlias, staticToken, patchBody, tenantHeaders)
	if status != http.StatusOK {
		t.Fatalf("revoke integration: status=%d body=%s", status, body)
	}
	if _, stderr, err := runGeneratedApp(t, hubURL, testProject, testAlias, "query_table/v1", input, tokenFile, tenantHeaders); err == nil {
		t.Fatalf("revoked action unexpectedly succeeded (stderr=%s)", stderr)
	} else if !strings.Contains(stderr, "403") {
		t.Fatalf("revoked action did not fail closed with 403: %s", stderr)
	}
	if got := len(fakeDB.Requests()); got != beforeFailures {
		t.Fatalf("fail-closed calls reached fake upstream: request count %d before %d", got, beforeFailures)
	}

	// Revocation is enforced in kcp RBAC too, not only at the App Studio
	// gateway: a fresh workload exchange reconciles the ClusterRole without
	// the revoked action's subresource rule, so the direct data-plane route
	// refuses the runtime identity as well.
	revokedToken := exchangeWorkloadToken(t, tenantPath, projectUID, runtimeInstance, bootstrapToken)
	status, body = postJSON(t, hubURL+"/services/providers/databricks/actions/clusters/"+tenantCluster+"/tables/"+tableRef+"/query_table/v1", revokedToken, map[string]any{
		"input": map[string]any{"limit": 1},
	}, tenantHeaders)
	if status != http.StatusForbidden {
		t.Fatalf("revoked workload direct action status=%d body=%s, want 403 from the verb SSAR", status, body)
	}
	if got := len(fakeDB.Requests()); got != beforeFailures {
		t.Fatalf("revoked direct action reached fake upstream: request count %d before %d", got, beforeFailures)
	}
}

// TestOptionalLiveProviderActionSDK is intentionally opt-in. It exercises the
// same generated-app process against an existing local hub/project without
// creating resources or talking to a provider URL directly.
func TestOptionalLiveProviderActionSDK(t *testing.T) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("FAROS_E2E_PROVIDER_ACTIONS_LIVE")), "true") {
		t.Skip("set FAROS_E2E_PROVIDER_ACTIONS_LIVE=true for the bounded live smoke")
	}
	hub := strings.TrimRight(strings.TrimSpace(os.Getenv("FAROS_LIVE_HUB_URL")), "/")
	project := strings.TrimSpace(os.Getenv("FAROS_LIVE_PROJECT"))
	tokenFile := strings.TrimSpace(os.Getenv("FAROS_LIVE_ACTIONS_TOKEN_FILE"))
	if hub == "" || project == "" || tokenFile == "" {
		t.Skip("FAROS_LIVE_HUB_URL, FAROS_LIVE_PROJECT, and FAROS_LIVE_ACTIONS_TOKEN_FILE are required")
	}
	alias := strings.TrimSpace(os.Getenv("FAROS_LIVE_ACTION_ALIAS"))
	if alias == "" {
		alias = testAlias
	}
	var tenantHeaders map[string]string
	if org, workspace := strings.TrimSpace(os.Getenv("FAROS_LIVE_ORG")), strings.TrimSpace(os.Getenv("FAROS_LIVE_WORKSPACE")); org != "" && workspace != "" {
		tenantHeaders = map[string]string{"X-Faros-Org": org, "X-Faros-Workspace": workspace}
	}
	stdout, stderr, err := runGeneratedApp(t, hub, project, alias, "query_table/v1", map[string]any{"limit": 2}, tokenFile, tenantHeaders)
	if err != nil {
		t.Fatalf("live generated app query failed: %v (stderr=%s)", err, stderr)
	}
	var result struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode live generated app output: %v", err)
	}
	if len(result.Rows) > 2 {
		t.Fatalf("live result returned %d rows, want bounded <=2", len(result.Rows))
	}
	t.Logf("live interaction verified through App Studio SDK: rows=%d; provider URL/PAT were not supplied to the app", len(result.Rows))
}

// TestOptionalPublishedActionsSDKCleanInstall is intentionally opt-in. It is
// the only provider-actions test that talks to an npm registry: deterministic
// unit/E2E lanes stage the SDK fixture locally and never require network
// access. The fresh temp directory verifies the published artifact and exact
// consumer alias resolve together before importing the stable package name.
func TestOptionalPublishedActionsSDKCleanInstall(t *testing.T) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("FAROS_E2E_PROVIDER_ACTIONS_NPM_SMOKE")), "true") {
		t.Skip("set FAROS_E2E_PROVIDER_ACTIONS_NPM_SMOKE=true for the registry-backed SDK smoke")
	}
	registry := strings.TrimSpace(os.Getenv("FAROS_E2E_PROVIDER_ACTIONS_NPM_REGISTRY"))
	if registry == "" {
		registry = "https://registry.npmjs.org"
	}
	appDir := t.TempDir()
	manifest := filepath.Join(repoRoot, "test", "e2e", "provideractions", "generated-app", "package.json")
	copyGeneratedAppFile(t, manifest, filepath.Join(appDir, "package.json"))
	install := exec.CommandContext(ctxWithTimeout(t, 5*time.Minute), "npm", "install", "--ignore-scripts", "--no-audit", "--no-fund", "--package-lock=false", "--registry", registry)
	install.Dir = appDir
	output, err := install.CombinedOutput()
	if err != nil {
		t.Fatalf("npm clean install from %s failed: %v\n%s", registry, err, output)
	}
	verify := exec.CommandContext(ctxWithTimeout(t, time.Minute), "node", "--input-type=module", "-e", "import { createActionsClient } from '@faros/actions-node'; if (typeof createActionsClient !== 'function') process.exit(1)")
	verify.Dir = appDir
	if output, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("published Actions SDK import failed after npm install: %v\n%s", err, output)
	}
	t.Logf("registry-backed clean install verified for @crwilhit/faros-actions-node@0.1.0 via %s", registry)
}

func bindProvider(t *testing.T, tenant dynamic.Interface, name, workspace, export string, secretVerbs []string) {
	t.Helper()
	claims := make([]any, 0, 1)
	if len(secretVerbs) > 0 {
		verbs := make([]any, 0, len(secretVerbs))
		for _, verb := range secretVerbs {
			verbs = append(verbs, verb)
		}
		claims = append(claims, map[string]any{
			"resource": "secrets", "verbs": verbs,
			"selector": map[string]any{"matchAll": true}, "state": "Accepted",
		})
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding",
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"reference":        map[string]any{"export": map[string]any{"path": workspace, "name": export}},
			"permissionClaims": claims,
		},
	}}
	createOrUpdate(t, tenant.Resource(apiBindingGVR), binding)
	waitCondition(t, 90*time.Second, func() (bool, string) {
		obj, err := tenant.Resource(apiBindingGVR).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		ready := false
		conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
		for _, raw := range conditions {
			condition, _ := raw.(map[string]any)
			if condition["type"] == "Ready" && condition["status"] == "True" {
				ready = true
				break
			}
		}
		return phase == "Bound" && ready, fmt.Sprintf("phase=%s ready=%t", phase, ready)
	}, "APIBinding "+name+" Bound")
}

func getProject(t *testing.T, tenant dynamic.Interface) *unstructured.Unstructured {
	t.Helper()
	project, err := tenant.Resource(projectGVR).Get(context.Background(), testProject, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	return project
}

func ensureInfrastructureBinding(t *testing.T, tenant dynamic.Interface, instance string) {
	t.Helper()
	project := getProject(t, tenant)
	envs, ok, err := unstructured.NestedSlice(project.Object, "spec", "environments")
	if err != nil || !ok || len(envs) == 0 {
		t.Fatalf("project environments=%#v (err=%v), want development environment", envs, err)
	}
	found := false
	for _, raw := range envs {
		env, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(env, "name")
		if name != "development" {
			continue
		}
		bindings, _, _ := unstructured.NestedSlice(env, "bindings")
		for _, bindingRaw := range bindings {
			binding, _ := bindingRaw.(map[string]any)
			if binding == nil {
				continue
			}
			kind, _, _ := unstructured.NestedString(binding, "kind")
			provider, _, _ := unstructured.NestedString(binding, "provider")
			ref, _, _ := unstructured.NestedMap(binding, "resourceRef")
			name, _, _ := unstructured.NestedString(ref, "name")
			if kind == "providerResource" && provider == "infrastructure" && name == instance {
				found = true
			}
		}
		if !found {
			bindings = append(bindings, map[string]any{
				"name": "runtime", "provider": "infrastructure", "kind": "providerResource",
				"resourceRef": map[string]any{
					"name": instance, "apiVersion": "infrastructure.faros.sh/v1alpha1",
					"kind": "Application", "resource": "applications",
				},
			})
			if err := unstructured.SetNestedField(env, bindings, "bindings"); err != nil {
				t.Fatalf("set infrastructure binding: %v", err)
			}
		}
	}
	if !found {
		if err := unstructured.SetNestedField(project.Object, envs, "spec", "environments"); err != nil {
			t.Fatalf("set project environments: %v", err)
		}
		if _, err := tenant.Resource(projectGVR).Update(context.Background(), project, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("persist infrastructure project binding: %v", err)
		}
	}
}

func assertProjectReferenceBinding(t *testing.T, tenant dynamic.Interface, table *unstructured.Unstructured, digest, instance string) {
	t.Helper()
	project := getProject(t, tenant)
	envs, ok, err := unstructured.NestedSlice(project.Object, "spec", "environments")
	if err != nil || !ok || len(envs) != 1 {
		t.Fatalf("project environments=%#v (err=%v), want one development environment", envs, err)
	}
	env, _ := envs[0].(map[string]any)
	bindings, _, _ := unstructured.NestedSlice(env, "bindings")
	var reference, infrastructure map[string]any
	for _, raw := range bindings {
		binding, _ := raw.(map[string]any)
		if binding == nil {
			continue
		}
		kind, _, _ := unstructured.NestedString(binding, "kind")
		provider, _, _ := unstructured.NestedString(binding, "provider")
		ref, _, _ := unstructured.NestedMap(binding, "resourceRef")
		refName, _, _ := unstructured.NestedString(ref, "name")
		if kind == "providerReference" && provider == "databricks" {
			reference = binding
		}
		if kind == "providerResource" && provider == "infrastructure" && refName == instance {
			infrastructure = binding
		}
	}
	if reference == nil || infrastructure == nil {
		t.Fatalf("project bindings=%#v, want Databricks providerReference and Infrastructure instance %s", bindings, instance)
	}
	ref, _, _ := unstructured.NestedMap(reference, "resourceRef")
	if ref["name"] != tableRef || ref["kind"] != "Table" {
		t.Fatalf("binding resourceRef=%#v, want existing Table %s", ref, tableRef)
	}
	actions, _, _ := unstructured.NestedSlice(reference, "allowedActions")
	if len(actions) != 1 {
		t.Fatalf("allowedActions=%#v, want one catalog grant", actions)
	}
	action, _ := actions[0].(map[string]any)
	if action["name"] != "query_table" || action["version"] != "v1" || action["schemaDigest"] != digest || strings.TrimSpace(fmt.Sprint(action["grantedBy"])) == "" || strings.TrimSpace(fmt.Sprint(action["grantedAt"])) == "" {
		t.Fatalf("action grant=%#v, want exact catalog digest and audit fields", action)
	}
	latestTable, err := tenant.Resource(tableGVR).Get(context.Background(), table.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get bound Table: %v", err)
	}
	if owners := latestTable.GetOwnerReferences(); len(owners) != 0 {
		t.Fatalf("provider-owned Table unexpectedly gained App Studio owner references: %#v", owners)
	}
}

func catalogActionSchemaDigest(t *testing.T, providerName, actionID string) string {
	t.Helper()
	client := kcpDynamic(t, "root:faros:system:providers", adminToken)
	entry, err := client.Resource(catalogEntryGVR).Get(context.Background(), providerName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s catalog entry: %v", providerName, err)
	}
	actions, found, err := unstructured.NestedSlice(entry.Object, "spec", "actions")
	if err != nil || !found {
		t.Fatalf("%s catalog actions=%#v (err=%v)", providerName, actions, err)
	}
	for _, raw := range actions {
		action, _ := raw.(map[string]any)
		id, _, _ := unstructured.NestedString(action, "id")
		if id != actionID {
			continue
		}
		digest, _, _ := unstructured.NestedString(action, "schemaDigest")
		if digest == "" {
			t.Fatalf("%s catalog action %s has no schemaDigest", providerName, actionID)
		}
		return digest
	}
	t.Fatalf("%s catalog action %s not found", providerName, actionID)
	return ""
}

func setProjectActionDigest(project *unstructured.Unstructured, digest string) error {
	envs, found, err := unstructured.NestedSlice(project.Object, "spec", "environments")
	if err != nil || !found {
		return fmt.Errorf("project environments are missing")
	}
	for _, raw := range envs {
		env, _ := raw.(map[string]any)
		bindings, _, _ := unstructured.NestedSlice(env, "bindings")
		for _, bindingRaw := range bindings {
			binding, _ := bindingRaw.(map[string]any)
			if binding == nil {
				continue
			}
			name, _, _ := unstructured.NestedString(binding, "name")
			kind, _, _ := unstructured.NestedString(binding, "kind")
			if name != testAlias || kind != "providerReference" {
				continue
			}
			actions, found, err := unstructured.NestedSlice(binding, "allowedActions")
			if err != nil || !found || len(actions) != 1 {
				return fmt.Errorf("integration allowedActions are missing")
			}
			action, ok := actions[0].(map[string]any)
			if !ok {
				return fmt.Errorf("integration action is malformed")
			}
			action["schemaDigest"] = digest
			if err := unstructured.SetNestedField(binding, actions, "allowedActions"); err != nil {
				return err
			}
			// NestedSlice returns a deep copy. Reattach each changed level so
			// the subsequent Project update actually persists the drift.
			if err := unstructured.SetNestedField(env, bindings, "bindings"); err != nil {
				return err
			}
			return unstructured.SetNestedField(project.Object, envs, "spec", "environments")
		}
	}
	return fmt.Errorf("integration %q not found", testAlias)
}

func exchangeWorkloadToken(t *testing.T, tenantPath, projectUID, instance, bootstrap string) string {
	t.Helper()
	status, body := postJSON(t, hubURL+"/api/provider-actions/workload/exchange", bootstrap, map[string]any{
		"tenantPath": tenantPath, "project": testProject, "projectUID": projectUID,
		"environment": "development", "instance": instance,
	})
	if status != http.StatusOK {
		t.Fatalf("workload exchange: status=%d body=%s", status, body)
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil || strings.TrimSpace(response.Token) == "" {
		t.Fatalf("decode workload exchange: err=%v body=%s", err, body)
	}
	return strings.TrimSpace(response.Token)
}

func assertDatabricksMCPUnavailable(t *testing.T) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+databricksPort+"/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0"}`))
	if err != nil {
		t.Fatalf("build MCP probe: %v", err)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("MCP probe: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		t.Fatalf("Databricks /mcp unexpectedly available: status=%d", response.StatusCode)
	}
}

// assertWorkloadReviewReserved proves the attestation endpoint is hub-only:
// the backend proxy refuses the /workload-identities prefix, so a caller
// bearer can never turn the provider into a TokenReview oracle.
func assertWorkloadReviewReserved(t *testing.T, tenantHeaders map[string]string) {
	t.Helper()
	before := len(fakeAttestor.Requests())
	status, body := postJSON(t, hubURL+"/services/providers/infrastructure/workload-identities/review", staticToken, map[string]any{
		"tenantPath": "root:faros:tenants:x:y", "project": "p", "projectUID": "u", "environment": "development", "instance": "i",
	}, tenantHeaders)
	if status != http.StatusNotFound {
		t.Fatalf("proxied attestation endpoint status=%d body=%s, want 404", status, body)
	}
	if got := len(fakeAttestor.Requests()); got != before {
		t.Fatalf("proxied attestation request reached the attestor: count %d before %d", got, before)
	}
}

// assertDirectActionAuthorization pins the data-plane authorization model:
// the action route through the backend proxy is legitimate, and it is the
// provider's caller-scoped SSAR gates — kcp RBAC — that decide. A workload
// token addressing a table outside its grants is refused before the provider
// backend is contacted, and the retired body-addressed route no longer
// exists.
func assertDirectActionAuthorization(t *testing.T, tenantCluster, workloadToken string, tenantHeaders map[string]string) {
	t.Helper()
	before := len(fakeDB.Requests())
	status, body := postJSON(t, hubURL+"/services/providers/databricks/actions/query_table/v1", staticToken, map[string]any{
		"input": map[string]any{"limit": 1},
	}, tenantHeaders)
	if status != http.StatusNotFound {
		t.Fatalf("legacy body-addressed action URL status=%d body=%s, want 404", status, body)
	}
	status, body = postJSON(t, hubURL+"/services/providers/databricks/actions/clusters/"+tenantCluster+"/tables/ungranted-table/query_table/v1", workloadToken, map[string]any{
		"input": map[string]any{"limit": 1},
	}, tenantHeaders)
	if status != http.StatusForbidden && status != http.StatusNotFound {
		t.Fatalf("ungranted direct action status=%d body=%s, want caller-scoped refusal", status, body)
	}
	if got := len(fakeDB.Requests()); got != before {
		t.Fatalf("refused direct action reached upstream: request count %d before %d", got, before)
	}
}

func createOrUpdate(t *testing.T, resource dynamic.ResourceInterface, object *unstructured.Unstructured) {
	t.Helper()
	created, err := resource.Create(context.Background(), object, metav1.CreateOptions{})
	if err == nil {
		*object = *created
		return
	}
	if !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create %s/%s: %v", object.GetKind(), object.GetName(), err)
	}
	existing, err := resource.Get(context.Background(), object.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get existing %s/%s: %v", object.GetKind(), object.GetName(), err)
	}
	object.SetResourceVersion(existing.GetResourceVersion())
	updated, err := resource.Update(context.Background(), object, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("update %s/%s: %v", object.GetKind(), object.GetName(), err)
	}
	*object = *updated
}

func readyCondition(client dynamic.Interface, resource schema.GroupVersionResource, name, conditionType string) (bool, string) {
	object, err := client.Resource(resource).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return false, err.Error()
	}
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		if condition["type"] == conditionType {
			return condition["status"] == "True", fmt.Sprintf("%s=%v reason=%v", conditionType, condition["status"], condition["reason"])
		}
	}
	return false, "condition absent"
}

func waitCondition(t *testing.T, timeout time.Duration, check func() (bool, string), label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		ok, msg := check()
		if ok {
			return
		}
		last = msg
		time.Sleep(time.Second)
	}
	t.Fatalf("%s never became ready: %s", label, last)
}

func runGeneratedApp(t *testing.T, hub, project, alias, action string, input map[string]any, tokenFile string, tenantHeaders ...map[string]string) (string, string, error) {
	t.Helper()
	b, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("encode generated app input: %v", err)
	}
	script := stageGeneratedApp(t)
	cmd := exec.CommandContext(ctxWithTimeout(t, 2*time.Minute), "node", script)
	cmd.Dir = filepath.Dir(script)
	env := make([]string, 0, len(os.Environ())+6)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.Contains(strings.ToUpper(key), "CALLER_TOKEN") {
			continue
		}
		env = append(env, value)
	}
	cmd.Env = append(env,
		"FAROS_ACTIONS_BASE_URL="+strings.TrimRight(hub, "/")+"/services/providers/app-studio",
		"FAROS_PROJECT="+project,
		"FAROS_ACTION_ALIAS="+alias,
		"FAROS_ACTION="+action,
		"FAROS_ACTIONS_TOKEN_FILE="+tokenFile,
		"FAROS_ACTION_INPUT_JSON="+string(b),
	)
	if len(tenantHeaders) > 0 && len(tenantHeaders[0]) > 0 {
		headers, marshalErr := json.Marshal(tenantHeaders[0])
		if marshalErr != nil {
			t.Fatalf("encode generated app tenant headers: %v", marshalErr)
		}
		cmd.Env = append(cmd.Env, "FAROS_ACTION_HEADERS_JSON="+string(headers))
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

// stageGeneratedApp mirrors the production generated-app layout: the app
// imports @faros/actions-node by package name from a normal node_modules tree.
// The E2E copies the package into a temporary directory so it never relies on
// a monorepo-relative runtime import or mutates the checkout under test.
func stageGeneratedApp(t *testing.T) string {
	t.Helper()
	appDir := t.TempDir()
	sourceDir := filepath.Join(repoRoot, "test", "e2e", "provideractions", "generated-app")
	script := filepath.Join(appDir, "run.mjs")
	sourceScript := filepath.Join(sourceDir, "run.mjs")
	sourceManifest := filepath.Join(sourceDir, "package.json")
	manifestContents, err := os.ReadFile(sourceManifest)
	if err != nil {
		t.Fatalf("read generated app package manifest: %v", err)
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(manifestContents, &manifest); err != nil {
		t.Fatalf("decode generated app package manifest: %v", err)
	}
	const exactAlias = "npm:@crwilhit/faros-actions-node@0.1.0"
	if got := manifest.Dependencies["@faros/actions-node"]; got != exactAlias {
		t.Fatalf("generated app Actions SDK dependency = %q, want exact alias %q", got, exactAlias)
	}
	copyGeneratedAppFile(t, sourceManifest, filepath.Join(appDir, "package.json"))
	scriptContents, err := os.ReadFile(sourceScript)
	if err != nil {
		t.Fatalf("read generated app entrypoint: %v", err)
	}
	for _, want := range []string{
		"import { createActionsClient } from '@faros/actions-node';",
		"tokenFile: actionsTokenFile",
	} {
		if !strings.Contains(string(scriptContents), want) {
			t.Fatalf("generated app entrypoint missing production SDK contract %q", want)
		}
	}
	copyGeneratedAppFile(t, sourceScript, script)

	packageDir := filepath.Join(appDir, "node_modules", "@faros", "actions-node")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatalf("create generated app package fixture: %v", err)
	}
	sdkDir := filepath.Join(repoRoot, "provider-sdk", "actions-node")
	for _, name := range []string{"package.json", "index.mjs", "index.d.ts", "README.md"} {
		copyGeneratedAppFile(t, filepath.Join(sdkDir, name), filepath.Join(packageDir, name))
	}
	return script
}

func copyGeneratedAppFile(t *testing.T, source, destination string) {
	t.Helper()
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read generated app fixture %s: %v", source, err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat generated app fixture %s: %v", source, err)
	}
	if err := os.WriteFile(destination, contents, info.Mode().Perm()); err != nil {
		t.Fatalf("write generated app fixture %s: %v", destination, err)
	}
}

// setupTenantWorkspace creates a real child workspace for the harness control
// token and waits for its kcp logical-cluster ID. App Studio deliberately
// rejects org-only context for project routes; the generated app therefore
// receives only ordinary tenant-selection headers and a workload token-file
// path, never a static caller token, provider URL, or PAT.
func setupTenantWorkspace(t *testing.T) (string, string, string) {
	t.Helper()
	var orgUUID string
	waitCondition(t, 90*time.Second, func() (bool, string) {
		req, err := http.NewRequest(http.MethodGet, hubURL+"/api/orgs", nil)
		if err != nil {
			return false, err.Error()
		}
		req.Header.Set("Authorization", "Bearer "+staticToken)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return false, err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return false, fmt.Sprintf("status=%d body=%s", resp.StatusCode, body)
		}
		var out struct {
			Items []struct {
				UUID     string `json:"uuid"`
				Personal bool   `json:"personal"`
			} `json:"items"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return false, err.Error()
		}
		for _, item := range out.Items {
			if item.Personal && item.UUID != "" {
				orgUUID = item.UUID
				return true, "personal org found"
			}
		}
		return false, "personal org absent"
	}, "personal organization")

	status, body := doJSON(t, http.MethodPost, hubURL+"/api/orgs/"+orgUUID+"/workspaces", staticToken,
		map[string]any{"displayName": "provider-actions-e2e"}, map[string]string{"X-Faros-Org": orgUUID})
	if status != http.StatusCreated {
		t.Fatalf("create provider-actions workspace: status=%d body=%s", status, body)
	}
	var workspace struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal([]byte(body), &workspace); err != nil || workspace.UUID == "" {
		t.Fatalf("decode provider-actions workspace: err=%v body=%s", err, body)
	}

	var clusterName string
	headers := map[string]string{"X-Faros-Org": orgUUID, "X-Faros-Workspace": workspace.UUID}
	waitCondition(t, 90*time.Second, func() (bool, string) {
		status, body := doJSON(t, http.MethodGet, hubURL+"/api/orgs/"+orgUUID+"/workspaces/"+workspace.UUID, staticToken, nil, headers)
		if status != http.StatusOK {
			return false, fmt.Sprintf("status=%d body=%s", status, body)
		}
		var out struct {
			ClusterName string `json:"clusterName"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			return false, err.Error()
		}
		clusterName = strings.TrimSpace(out.ClusterName)
		return clusterName != "", "clusterName=" + clusterName
	}, "provider-actions workspace cluster")
	return orgUUID, workspace.UUID, clusterName
}

// waitTenantProxyContext confirms that the selected workspace is both
// authorized by the hub resolver and initialized in App Studio. The 503
// response is expected while the tenant APIBinding settles; 404 for a
// deliberately missing project is the first interaction-safe readiness
// signal for this provider route.
func waitTenantProxyContext(t *testing.T, orgUUID, workspaceUUID string) {
	t.Helper()
	headers := map[string]string{"X-Faros-Org": orgUUID, "X-Faros-Workspace": workspaceUUID}
	waitCondition(t, 3*time.Minute, func() (bool, string) {
		status, body := doJSON(t, http.MethodGet, hubURL+"/services/providers/app-studio/api/projects/__tenant_probe__", staticToken, nil, headers)
		if status == http.StatusNotFound {
			return true, "App Studio workspace context ready"
		}
		return false, fmt.Sprintf("status=%d body=%s", status, body)
	}, "provider-actions workspace proxy context")
}

func postJSON(t *testing.T, url, token string, payload any, extraHeaders ...map[string]string) (int, string) {
	return doJSON(t, http.MethodPost, url, token, payload, extraHeaders...)
}

func patchJSON(t *testing.T, url, token string, payload any, extraHeaders ...map[string]string) (int, string) {
	return doJSON(t, http.MethodPatch, url, token, payload, extraHeaders...)
}

func doJSON(t *testing.T, method, url, token string, payload any, extraHeaders ...map[string]string) (int, string) {
	t.Helper()
	var requestBody io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode %s: %v", method, err)
		}
		requestBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		t.Fatalf("build %s: %v", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, headers := range extraHeaders {
		for key, value := range headers {
			req.Header.Set(key, value)
		}
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(responseBody)
}
