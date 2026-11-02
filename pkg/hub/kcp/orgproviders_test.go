/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

func TestOrgProviderWorkspaceReportsDeletionTimestamp(t *testing.T) {
	const deletionTimestamp = "2026-11-01T12:00:00Z"
	workspace := map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata": map[string]any{
			"name":              "quickstart",
			"deletionTimestamp": deletionTimestamp,
			"annotations": map[string]any{
				EdgeRouteWorkspaceAnnotation: "workspace-a",
				EdgeRouteEdgeAnnotation:      "prod",
			},
		},
		"spec":   map[string]any{"cluster": "provider-cluster"},
		"status": map[string]any{"phase": "Ready"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/workspaces/quickstart"):
			_ = json.NewEncoder(w).Encode(workspace)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/workspaces"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"apiVersion": "tenancy.kcp.io/v1alpha1",
				"kind":       "WorkspaceList",
				"items":      []any{workspace},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	ctx := context.Background()
	listed, err := b.ListOrgProviderWorkspaces(ctx, "org-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !listed[0].Deleting || listed[0].HostingEdgeWorkspace != "workspace-a" || listed[0].HostingEdgeName != "prod" {
		t.Fatalf("listed provider workspace = %+v, want deleting workspace with its recorded Edge", listed)
	}
	got, err := b.GetOrgProviderWorkspace(ctx, "org-a", "quickstart")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.Deleting || got.HostingEdgeWorkspace != "workspace-a" || got.HostingEdgeName != "prod" {
		t.Fatalf("provider workspace = %+v, want deleting workspace with its recorded Edge", got)
	}
}

func TestOrgProviderWorkspaceDoesNotExposeMissingOrPartialEdgeAnnotations(t *testing.T) {
	for _, tc := range []struct {
		name        string
		annotations map[string]any
	}{
		{name: "legacy workspace without annotations"},
		{
			name: "partial binding",
			annotations: map[string]any{
				EdgeRouteEdgeAnnotation: "prod",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "tenancy.kcp.io/v1alpha1",
				"kind":       "Workspace",
				"metadata": map[string]any{
					"name":        "quickstart",
					"annotations": tc.annotations,
				},
			}}
			got := orgProviderWorkspace(workspace)
			if got.HostingEdgeName != "" || got.HostingEdgeWorkspace != "" {
				t.Fatalf("provider workspace exposed incomplete edge metadata: %+v", got)
			}
		})
	}
}

func TestDeleteOrgProviderWorkspaceDeletesExportsAndWaitsForOwnedCache(t *testing.T) {
	const exportUID = "export-uid"
	export := orgProviderDeleteExport("kuery.providers.railgrid.ai", exportUID)
	cached := orgProviderDeleteCachedResource("cached-endpoints", exportUID)
	state := &orgProviderDeleteState{
		workspaceExists: true,
		clusterID:       "org-provider-cluster",
		exports:         []map[string]any{export},
		cachedResources: []map[string]any{cached},
	}
	server := newOrgProviderDeleteServer(t, state)
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	if err := b.DeleteOrgProviderWorkspace(context.Background(), "org-a", "kuery"); err != nil {
		t.Fatalf("DeleteOrgProviderWorkspace: %v", err)
	}
	if !state.workspaceDeleted {
		t.Fatal("provider Workspace was not deleted after its APIExport cache drained")
	}
	if state.workspaceDeleteBeforeCacheDrain {
		t.Fatal("provider Workspace was deleted while an APIExport-owned cache entry remained")
	}
	if !state.foregroundExportDelete {
		t.Fatal("APIExport deletion did not request foreground propagation")
	}
	if !state.exportDeleteUIDPrecondition {
		t.Fatal("APIExport deletion was not fenced to the observed UID")
	}
	if !state.workspaceDeleteUIDPrecondition {
		t.Fatal("Workspace deletion was not fenced to the observed UID")
	}
	if state.exportDeleteCalls == 0 || state.cacheListCalls < 2 {
		t.Fatalf("expected APIExport delete and a cache retry, got export deletes=%d cache lists=%d", state.exportDeleteCalls, state.cacheListCalls)
	}
	if got := strings.Join(state.events, ","); !strings.Contains(got, "api-export-delete") || !strings.Contains(got, "workspace-delete") {
		t.Fatalf("operation events = %v, missing teardown steps", state.events)
	}
	if eventIndex(state.events, "api-export-delete") > eventIndex(state.events, "workspace-delete") {
		t.Fatalf("Workspace deletion preceded APIExport deletion: %v", state.events)
	}
	if eventIndex(state.events, "cached-resource-list") > eventIndex(state.events, "workspace-delete") {
		t.Fatalf("Workspace deletion preceded cache inspection: %v", state.events)
	}
}

func TestDeleteOrgProviderWorkspaceRetryWaitsForOrphanedOwnedCache(t *testing.T) {
	state := &orgProviderDeleteState{
		workspaceExists: true,
		clusterID:       "org-provider-cluster",
		// The export was removed by the previous request, but its cache entry
		// has not finished deleting. A foreign owner kind must not hold teardown.
		cachedResources: []map[string]any{
			orgProviderDeleteCachedResourceWithOwner("orphaned-cache", "apis.kcp.io/v1alpha1", "APIExport", "old-export-uid"),
			orgProviderDeleteCachedResourceWithOwner("other-cache", "widgets.example.test/v1", "Widget", "widget-uid"),
		},
		removeCachedOnSecondList: true,
	}
	server := newOrgProviderDeleteServer(t, state)
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	if err := b.DeleteOrgProviderWorkspace(context.Background(), "org-a", "kuery"); err != nil {
		t.Fatalf("retry DeleteOrgProviderWorkspace: %v", err)
	}
	if state.exportDeleteCalls != 0 {
		t.Fatalf("deleted %d APIExports when retry found none", state.exportDeleteCalls)
	}
	if !state.workspaceDeleted || state.cacheListCalls < 2 {
		t.Fatalf("retry did not wait for the orphaned APIExport cache: workspaceDeleted=%v cacheLists=%d", state.workspaceDeleted, state.cacheListCalls)
	}
}

func TestDeleteOrgProviderWorkspaceRetryWaitsForTerminatingExportAndOwnedCache(t *testing.T) {
	export := orgProviderDeleteExport("kuery.providers.railgrid.ai", "export-uid")
	export["metadata"].(map[string]any)["deletionTimestamp"] = "2026-11-01T12:00:00Z"
	state := &orgProviderDeleteState{
		workspaceExists: true,
		clusterID:       "org-provider-cluster",
		exports:         []map[string]any{export},
		cachedResources: []map[string]any{orgProviderDeleteCachedResource("cached-endpoints", "export-uid")},
	}
	server := newOrgProviderDeleteServer(t, state)
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	err := b.DeleteOrgProviderWorkspace(ctx, "org-a", "kuery")
	cancel()
	if err == nil {
		t.Fatal("DeleteOrgProviderWorkspace succeeded while a terminating APIExport and its owned cache remained")
	}
	if state.workspaceDeleted {
		t.Fatal("provider Workspace was deleted while its APIExport was still terminating")
	}
	if state.exportDeleteCalls != 0 {
		t.Fatalf("reissued DELETE %d times for an APIExport already terminating", state.exportDeleteCalls)
	}
	if state.cacheListCalls == 0 {
		t.Fatal("teardown did not continue checking the terminating export's owned cache")
	}

	// A later request can observe the export gone while its cached-resource
	// owner remains. It must continue waiting rather than deleting the Workspace.
	state.exports = nil
	ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
	err = b.DeleteOrgProviderWorkspace(ctx, "org-a", "kuery")
	cancel()
	if err == nil {
		t.Fatal("retry succeeded while an APIExport-owned cached resource remained")
	}
	if state.workspaceDeleted {
		t.Fatal("retry deleted the provider Workspace before the APIExport-owned cache drained")
	}
	if state.exportDeleteCalls != 0 {
		t.Fatalf("retry issued %d DELETEs for an APIExport that was already terminating", state.exportDeleteCalls)
	}

	// A final retry after both resources disappear should complete the original
	// UID-fenced Workspace deletion.
	state.cachedResources = nil
	if err := b.DeleteOrgProviderWorkspace(context.Background(), "org-a", "kuery"); err != nil {
		t.Fatalf("retry after export and cache removal: %v", err)
	}
	if !state.workspaceDeleted || !state.workspaceDeleteUIDPrecondition {
		t.Fatalf("final retry did not delete the original Workspace with its UID fence: deleted=%v uidFenced=%v", state.workspaceDeleted, state.workspaceDeleteUIDPrecondition)
	}
	if state.exportDeleteCalls != 0 {
		t.Fatalf("retry issued %d unnecessary APIExport DELETEs", state.exportDeleteCalls)
	}
	if state.workspaceDeleteBeforeCacheDrain {
		t.Fatal("provider Workspace was deleted before the APIExport and owned cache were gone")
	}
	if state.cacheListCalls < 3 {
		t.Fatalf("expected cache checks across retries, got %d", state.cacheListCalls)
	}
}

func TestDeleteOrgProviderWorkspaceDoesNotDeleteWorkspaceOnCacheTimeout(t *testing.T) {
	state := &orgProviderDeleteState{
		workspaceExists: true,
		clusterID:       "org-provider-cluster",
		cachedResources: []map[string]any{
			orgProviderDeleteCachedResource("orphaned-cache", "old-export-uid"),
		},
	}
	server := newOrgProviderDeleteServer(t, state)
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := b.DeleteOrgProviderWorkspace(ctx, "org-a", "kuery"); err == nil {
		t.Fatal("DeleteOrgProviderWorkspace succeeded while an owned cached resource remained")
	}
	if state.workspaceDeleted {
		t.Fatal("provider Workspace was deleted despite incomplete APIExport cache cleanup")
	}
}

func TestDeleteOrgProviderWorkspaceMissingIsIdempotent(t *testing.T) {
	state := &orgProviderDeleteState{}
	state.workspaceExists = false
	server := newOrgProviderDeleteServer(t, state)
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	if err := b.DeleteOrgProviderWorkspace(context.Background(), "org-a", "kuery"); err != nil {
		t.Fatalf("deleting absent provider workspace: %v", err)
	}
	if state.childRequests != 0 || state.workspaceDeleted {
		t.Fatalf("absent workspace triggered teardown: childRequests=%d workspaceDeleted=%v", state.childRequests, state.workspaceDeleted)
	}
}

func TestDeleteOrgProviderWorkspaceWithNoLogicalClusterSkipsChildCleanup(t *testing.T) {
	state := &orgProviderDeleteState{workspaceExists: true}
	server := newOrgProviderDeleteServer(t, state)
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	if err := b.DeleteOrgProviderWorkspace(context.Background(), "org-a", "kuery"); err != nil {
		t.Fatalf("deleting unscheduled provider workspace: %v", err)
	}
	if state.childRequests != 0 || !state.workspaceDeleted || !state.workspaceDeleteUIDPrecondition {
		t.Fatalf("unscheduled workspace teardown: childRequests=%d deleted=%v uidFenced=%v", state.childRequests, state.workspaceDeleted, state.workspaceDeleteUIDPrecondition)
	}
}

func TestDeleteOrgProviderWorkspaceRetryAcceptsAlreadyGoneLogicalCluster(t *testing.T) {
	state := &orgProviderDeleteState{
		workspaceExists:         true,
		workspaceDeleting:       true,
		clusterID:               "org-provider-cluster",
		childLogicalClusterGone: true,
	}
	server := newOrgProviderDeleteServer(t, state)
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	if err := b.DeleteOrgProviderWorkspace(context.Background(), "org-a", "kuery"); err != nil {
		t.Fatalf("retry for deleting Workspace with a gone logical cluster: %v", err)
	}
	if state.childRequests == 0 || state.workspaceDeleted {
		t.Fatalf("retry did not accept the already-gone logical cluster on a Workspace already terminating: childRequests=%d duplicateDelete=%v", state.childRequests, state.workspaceDeleted)
	}
}

type orgProviderDeleteState struct {
	workspaceExists                 bool
	workspaceDeleted                bool
	workspaceDeleting               bool
	childLogicalClusterGone         bool
	workspaceDeleteBeforeCacheDrain bool
	workspaceDeleteUIDPrecondition  bool
	clusterID                       string
	exports                         []map[string]any
	exportDeleteCalls               int
	foregroundExportDelete          bool
	exportDeleteUIDPrecondition     bool
	cachedResources                 []map[string]any
	cacheListCalls                  int
	removeCachedOnSecondList        bool
	childRequests                   int
	events                          []string
}

func newOrgProviderDeleteServer(t *testing.T, state *orgProviderDeleteState) *httptest.Server {
	t.Helper()
	parentPath := "/clusters/" + kcppaths.OrgProvidersParent("org-a")
	childPath := "/clusters/" + state.clusterID
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isParent := strings.HasPrefix(r.URL.Path, parentPath+"/")
		isChild := strings.HasPrefix(r.URL.Path, childPath+"/")
		if isChild {
			state.childRequests++
			if state.childLogicalClusterGone {
				writeOrgProviderNotFound(w)
				return
			}
		}
		switch {
		case r.Method == http.MethodGet && isParent && strings.HasSuffix(r.URL.Path, "/workspaces/kuery"):
			state.events = append(state.events, "workspace-get")
			if !state.workspaceExists {
				writeOrgProviderNotFound(w)
				return
			}
			metadata := map[string]any{"name": "kuery", "uid": "workspace-uid"}
			if state.workspaceDeleting {
				metadata["deletionTimestamp"] = "2026-11-01T12:00:00Z"
			}
			writeOrgProviderJSON(w, http.StatusOK, map[string]any{
				"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace",
				"metadata": metadata,
				"spec":     map[string]any{"cluster": state.clusterID},
			})
		case r.Method == http.MethodGet && isChild && strings.HasSuffix(r.URL.Path, "/apis/apis.kcp.io/v1alpha2/apiexports"):
			state.events = append(state.events, "api-export-list")
			writeOrgProviderJSON(w, http.StatusOK, map[string]any{
				"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIExportList", "items": state.exports,
			})
		case r.Method == http.MethodDelete && isChild && strings.Contains(r.URL.Path, "/apis/apis.kcp.io/v1alpha2/apiexports/"):
			state.events = append(state.events, "api-export-delete")
			state.exportDeleteCalls++
			var options metav1.DeleteOptions
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Errorf("decoding APIExport DeleteOptions: %v", err)
			} else {
				state.foregroundExportDelete = options.PropagationPolicy != nil && *options.PropagationPolicy == metav1.DeletePropagationForeground
				state.exportDeleteUIDPrecondition = options.Preconditions != nil && options.Preconditions.UID != nil && string(*options.Preconditions.UID) == "export-uid"
			}
			writeOrgProviderStatus(w, http.StatusOK)
		case r.Method == http.MethodGet && isChild && strings.HasSuffix(r.URL.Path, "/apis/cache.kcp.io/v1alpha1/clustercachedresources"):
			state.events = append(state.events, "cached-resource-list")
			state.cacheListCalls++
			items := state.cachedResources
			if state.removeCachedOnSecondList && state.cacheListCalls >= 2 && len(items) > 1 {
				items = items[1:]
			} else if state.removeCachedOnSecondList && state.cacheListCalls >= 2 && len(items) == 1 {
				items = nil
			}
			if state.removeCachedOnSecondList && state.cacheListCalls >= 2 {
				state.cachedResources = items
			}
			if state.exportDeleteCalls > 0 && state.cacheListCalls >= 2 {
				state.cachedResources = nil
				state.exports = nil
				items = nil
			}
			writeOrgProviderJSON(w, http.StatusOK, map[string]any{
				"apiVersion": "cache.kcp.io/v1alpha1", "kind": "ClusterCachedResourceList", "items": items,
			})
		case r.Method == http.MethodDelete && isParent && strings.HasSuffix(r.URL.Path, "/workspaces/kuery"):
			state.events = append(state.events, "workspace-delete")
			var options metav1.DeleteOptions
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Errorf("decoding Workspace DeleteOptions: %v", err)
			} else {
				state.workspaceDeleteUIDPrecondition = options.Preconditions != nil && options.Preconditions.UID != nil && string(*options.Preconditions.UID) == "workspace-uid"
			}
			if hasAPIExportOwnedCachedResource(state.cachedResources) || len(state.exports) != 0 {
				state.workspaceDeleteBeforeCacheDrain = true
			}
			state.workspaceDeleted = true
			state.workspaceExists = false
			writeOrgProviderStatus(w, http.StatusOK)
		default:
			t.Errorf("unexpected provider deletion request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func hasAPIExportOwnedCachedResource(resources []map[string]any) bool {
	for _, resource := range resources {
		object := &unstructured.Unstructured{Object: resource}
		for _, owner := range object.GetOwnerReferences() {
			if got := schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind).GroupKind(); got == (schema.GroupKind{Group: "apis.kcp.io", Kind: "APIExport"}) {
				return true
			}
		}
	}
	return false
}

func orgProviderDeleteExport(name, uid string) map[string]any {
	return map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIExport",
		"metadata": map[string]any{"name": name, "uid": uid},
	}
}

func orgProviderDeleteCachedResource(name, exportUID string) map[string]any {
	return orgProviderDeleteCachedResourceWithOwner(name, "apis.kcp.io/v1alpha2", "APIExport", exportUID)
}

func orgProviderDeleteCachedResourceWithOwner(name, apiVersion, kind, uid string) map[string]any {
	return map[string]any{
		"apiVersion": "cache.kcp.io/v1alpha1", "kind": "ClusterCachedResource",
		"metadata": map[string]any{
			"name":            name,
			"ownerReferences": []any{map[string]any{"apiVersion": apiVersion, "kind": kind, "name": "kuery", "uid": uid}},
		},
	}
}

func writeOrgProviderJSON(w http.ResponseWriter, status int, object any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(object); err != nil {
		panic(fmt.Sprintf("encoding fake kcp response: %v", err))
	}
}

func writeOrgProviderStatus(w http.ResponseWriter, status int) {
	writeOrgProviderJSON(w, status, map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Success", "code": status})
}

func writeOrgProviderNotFound(w http.ResponseWriter) {
	writeOrgProviderJSON(w, http.StatusNotFound, map[string]any{
		"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "NotFound", "message": "workspace not found", "code": http.StatusNotFound,
	})
}

func eventIndex(events []string, target string) int {
	for i, event := range events {
		if event == target {
			return i
		}
	}
	return -1
}

// providerNameFromExportPath decides which APIBindings count as "this provider
// is enabled here". Getting it wrong either hides enabled providers or reports
// another Org's provider as enabled.
func TestProviderNameFromExportPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		orgUUID  string
		wantName string
		wantOK   bool
	}{
		{
			name:     "platform provider",
			path:     kcppaths.ProviderPath("edges"),
			orgUUID:  "org1",
			wantName: "edges",
			wantOK:   true,
		},
		{
			name:     "own org provider",
			path:     kcppaths.OrgProviderPath("org1", "vault"),
			orgUUID:  "org1",
			wantName: "vault",
			wantOK:   true,
		},
		{
			// The isolation case: a binding pointing at another Org's export
			// must never be reported as an enabled provider here.
			name:    "another org's provider",
			path:    kcppaths.OrgProviderPath("org2", "vault"),
			orgUUID: "org1",
		},
		{
			name:    "core platform export is not a provider",
			path:    kcppaths.SystemControllers,
			orgUUID: "org1",
		},
		{
			name:    "team workspace path is not a provider",
			path:    kcppaths.WorkspacePath("org1", "ws1"),
			orgUUID: "org1",
		},
		{
			name:    "providers parent alone names no provider",
			path:    kcppaths.ProvidersParent,
			orgUUID: "org1",
		},
		{
			name:    "nested path below a platform provider",
			path:    kcppaths.ProviderPath("edges") + ":nested",
			orgUUID: "org1",
		},
		{
			name:    "empty path",
			path:    "",
			orgUUID: "org1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := providerNameFromExportPath(tc.path, tc.orgUUID)
			if ok != tc.wantOK {
				t.Fatalf("providerNameFromExportPath(%q, %q) ok = %v, want %v", tc.path, tc.orgUUID, ok, tc.wantOK)
			}
			if got != tc.wantName {
				t.Errorf("providerNameFromExportPath(%q, %q) = %q, want %q", tc.path, tc.orgUUID, got, tc.wantName)
			}
		})
	}
}
