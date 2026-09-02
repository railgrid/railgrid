/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func newRunSandboxTestClient(objects ...runtime.Object) *asclient.Client {
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{runSandboxInstancesResource.GVR: "InstanceList"},
		objects...,
	)
	return asclient.NewFromDynamic(dynamicClient)
}

func newRunSandboxTestInstance(name, state string, now time.Time) *unstructured.Unstructured {
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": projectAssistantRunSandboxAPIVersion,
		"kind":       projectAssistantRunSandboxKind,
		"metadata": map[string]any{
			"name":   name,
			"labels": map[string]any{projectAssistantRunSandboxLabel: "true"},
		},
		"spec": map[string]any{"template": projectAssistantRunSandboxDefaultTemplate},
	}}
	instance.SetCreationTimestamp(metav1.NewTime(now))
	instance.SetAnnotations(map[string]string{
		projectAssistantRunSandboxLabel:           "true",
		projectAssistantRunSandboxCacheState:      state,
		projectAssistantRunSandboxLastActivity:    now.Format(time.RFC3339Nano),
		projectAssistantRunSandboxIdleExpiry:      now.Add(projectAssistantRunSandboxIdleTTL).Format(time.RFC3339Nano),
		projectAssistantRunSandboxHardExpiry:      now.Add(projectAssistantRunSandboxHardTTL).Format(time.RFC3339Nano),
		projectAssistantRunSandboxCacheGeneration: "seed",
	})
	return instance
}

func readyRunSandboxStatus(generation int64) map[string]any {
	return map[string]any{
		"observedGeneration": generation,
		"phase":              "Ready",
		"farosNetworkPhase":  "runtime",
		"runtimeNamespace":   "run-ns",
		"controlSecretRef":   map[string]any{"name": "run-control"},
		"conditions": []any{map[string]any{
			"type":               "Ready",
			"status":             "True",
			"observedGeneration": generation,
		}},
		"components": map[string]any{"workspace": map[string]any{
			"ready":             true,
			"controlServiceRef": map[string]any{"name": "run-workspace-control"},
		}},
	}
}

func TestProjectAssistantRunSandboxNameIsProjectScoped(t *testing.T) {
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "shop", ProjectUID: "uid-a"}
	one := projectAssistantRunSandboxName(scope, &aiv1alpha1.Project{}, "run-a")
	if one == "" || len(one) > projectAssistantRunSandboxNameMaxLength {
		t.Fatalf("sandbox name = %q", one)
	}
	if got := projectAssistantRunSandboxName(scope, &aiv1alpha1.Project{}, "run-a"); got != one {
		t.Fatalf("name is not deterministic: %q vs %q", one, got)
	}
	for _, changed := range []struct {
		name  string
		scope workspace.Scope
		runID string
	}{
		{name: "tenant", scope: workspace.Scope{OrgUUID: "org-b", WorkspaceUUID: "ws-a", ProjectName: "shop", ProjectUID: "uid-a"}, runID: "run-a"},
		{name: "project", scope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "other", ProjectUID: "uid-a"}, runID: "run-a"},
	} {
		if got := projectAssistantRunSandboxName(changed.scope, nil, changed.runID); got == one {
			t.Errorf("%s input collided with %q", changed.name, one)
		}
	}
	if got := projectAssistantRunSandboxName(scope, &aiv1alpha1.Project{}, "run-b"); got != one {
		t.Fatalf("run ID changed project cache identity: %q vs %q", got, one)
	}
}

func TestProjectAssistantRunSandboxDirtyUsesRemoteCheckpointFence(t *testing.T) {
	tests := []struct {
		name     string
		metadata projectAssistantRunSandboxMetadata
		want     bool
	}{
		{
			name: "warm no-op ignores local source revision",
			metadata: projectAssistantRunSandboxMetadata{
				SourceRevision: 99, SourceDigest: "local-new",
				RemoteRevision: 3, RemoteDigest: "remote-base",
				CheckpointRevision: 3, CheckpointDigest: "remote-base",
			},
			want: false,
		},
		{
			name: "remote revision changed",
			metadata: projectAssistantRunSandboxMetadata{
				SourceRevision: 99, SourceDigest: "local-new",
				RemoteRevision: 4, RemoteDigest: "remote-base",
				CheckpointRevision: 3, CheckpointDigest: "remote-base",
			},
			want: true,
		},
		{
			name: "remote digest changed",
			metadata: projectAssistantRunSandboxMetadata{
				SourceRevision: 3, SourceDigest: "local-base",
				RemoteRevision: 3, RemoteDigest: "remote-new",
				CheckpointRevision: 3, CheckpointDigest: "remote-base",
			},
			want: true,
		},
		{
			name: "missing remote checkpoint fence fails closed",
			metadata: projectAssistantRunSandboxMetadata{
				SourceRevision: 3, SourceDigest: "remote-base",
				RemoteRevision: 3, RemoteDigest: "remote-base",
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectAssistantRunSandboxDirty(tt.metadata); got != tt.want {
				t.Fatalf("dirty = %t, want %t for metadata %#v", got, tt.want, tt.metadata)
			}
		})
	}
}

func TestProjectAssistantRunSandboxOwnerReferenceIsDurableAndRejectsCollision(t *testing.T) {
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "shop", UID: "project-uid"}}
	instance := newRunSandboxTestInstance("cache", projectAssistantRunSandboxCacheStateCached, time.Now().UTC())
	changed, err := ensureProjectAssistantRunSandboxOwner(instance, project)
	if err != nil || !changed {
		t.Fatalf("attach owner changed=%t err=%v", changed, err)
	}
	refs := instance.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Name != project.Name || refs[0].UID != project.UID || refs[0].Controller == nil || !*refs[0].Controller {
		t.Fatalf("owner references = %#v", refs)
	}
	if changed, err := ensureProjectAssistantRunSandboxOwner(instance, project); err != nil || changed {
		t.Fatalf("idempotent owner changed=%t err=%v", changed, err)
	}
	other := project.DeepCopy()
	other.UID = "other-project-uid"
	if _, err := ensureProjectAssistantRunSandboxOwner(instance, other); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("owner collision error = %v, want conflict", err)
	}
}

func TestDeleteProjectAssistantRunSandboxCacheDeletesExactLegacyCache(t *testing.T) {
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "shop", UID: "project-uid"}}
	id := identity{orgUUID: "org", workspaceUUID: "ws"}
	scope := projectWorkspaceScope(id, project)
	name := projectAssistantRunSandboxName(scope, project, "legacy-run")
	cache := newRunSandboxTestInstance(name, projectAssistantRunSandboxCacheStateCached, time.Now().UTC())
	client := newRunSandboxTestClient(cache)
	if err := (&Server{}).deleteProjectAssistantRunSandboxCache(context.Background(), client, id, project); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted cache lookup = %v, want NotFound", err)
	}
}

func TestDeleteProjectAssistantRunSandboxCacheRefusesNameCollision(t *testing.T) {
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "shop", UID: "project-uid"}}
	id := identity{orgUUID: "org", workspaceUUID: "ws"}
	name := projectAssistantRunSandboxName(projectWorkspaceScope(id, project), project, "")
	collision := newRunSandboxTestInstance(name, projectAssistantRunSandboxCacheStateCached, time.Now().UTC())
	annotations := collision.GetAnnotations()
	delete(annotations, projectAssistantRunSandboxLabel)
	collision.SetAnnotations(annotations)
	client := newRunSandboxTestClient(collision)
	if err := (&Server{}).deleteProjectAssistantRunSandboxCache(context.Background(), client, id, project); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("collision delete error = %v, want conflict", err)
	}
	if _, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), name, metav1.GetOptions{}); err != nil {
		t.Fatalf("collision instance was deleted: %v", err)
	}
}

func TestProjectAssistantRunSandboxNameReservesInfrastructureChildServiceBudget(t *testing.T) {
	projects := []string{
		"launch-readiness-sandbox-e2e",
		strings.Repeat("a", projectAssistantRunSandboxNameMaxBase),
		strings.Repeat("a", projectAssistantRunSandboxNameMaxBase+1),
		strings.Repeat("project-name-", 32),
	}
	for _, projectName := range projects {
		scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: projectName, ProjectUID: "project-uid"}
		name := projectAssistantRunSandboxName(scope, nil, "run-a")
		if len(name) > projectAssistantRunSandboxNameMaxLength {
			t.Errorf("project %q produced Instance %q (len %d), want <= %d", projectName, name, len(name), projectAssistantRunSandboxNameMaxLength)
		}
		for _, child := range []struct {
			suffix string
			limit  int
		}{
			// PVCs, ConfigMaps, Secrets, ServiceAccounts, Roles, RoleBindings,
			// and Jobs use Kubernetes DNS subdomain names.
			{projectAssistantRunSandboxChildWorkspaceSuffix, projectAssistantRunSandboxDNSSubdomainMaxLength},
			{projectAssistantRunSandboxChildPlatformStateSuffix, projectAssistantRunSandboxDNSSubdomainMaxLength},
			{projectAssistantRunSandboxChildActionsCASuffix, projectAssistantRunSandboxDNSSubdomainMaxLength},
			{projectAssistantRunSandboxChildControlSecretSuffix, projectAssistantRunSandboxDNSSubdomainMaxLength},
			{projectAssistantRunSandboxChildTokenSuffix, projectAssistantRunSandboxDNSSubdomainMaxLength},
			// Services use DNS labels, so this suffix controls the Instance cap.
			{projectAssistantRunSandboxChildControlServiceSuffix, projectAssistantRunSandboxDNSLabelMaxLength},
		} {
			childName := name + child.suffix
			if len(childName) > child.limit {
				t.Errorf("project %q produced child %q (len %d), want <= %d", projectName, childName, len(childName), child.limit)
			}
		}
		if name == "" || strings.Trim(name, "-") != name || strings.ToLower(name) != name {
			t.Errorf("project %q produced invalid DNS-label shape %q", projectName, name)
		}
	}

	// Names that share the visible prefix still retain distinct project identity hashes.
	first := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: strings.Repeat("a", projectAssistantRunSandboxNameMaxBase) + "-first", ProjectUID: "project-uid"}
	second := first
	second.ProjectName = strings.Repeat("a", projectAssistantRunSandboxNameMaxBase) + "-second"
	if one, two := projectAssistantRunSandboxName(first, nil, "run-a"), projectAssistantRunSandboxName(second, nil, "run-a"); one == two {
		t.Fatalf("long project names collided: %q", one)
	}

	boundary := projectAssistantRunSandboxName(workspace.Scope{
		OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: strings.Repeat("b", projectAssistantRunSandboxNameMaxBase), ProjectUID: "project-uid",
	}, nil, "run-a")
	if got := len(boundary + projectAssistantRunSandboxChildServiceSuffix); got != projectAssistantRunSandboxDNSLabelMaxLength {
		t.Fatalf("boundary child Service length = %d, want %d (Instance %q)", got, projectAssistantRunSandboxDNSLabelMaxLength, boundary)
	}
	if got := len(boundary + projectAssistantRunSandboxChildPlatformStateSuffix); got > projectAssistantRunSandboxDNSSubdomainMaxLength {
		t.Fatalf("boundary platform-state PVC length = %d, want <= %d (Instance %q)", got, projectAssistantRunSandboxDNSSubdomainMaxLength, boundary)
	}
}

func TestProjectAssistantSandboxManagerLimitsPerTenant(t *testing.T) {
	m := newProjectAssistantSandboxManager()
	releaseOne, err := m.acquire("org/ws", "cache-1", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOne()
	if _, err := m.acquire("org/ws", "cache-1", "run-other"); err == nil {
		t.Fatal("expected project cache claim conflict")
	}
	releaseTwo, err := m.acquire("org/ws", "cache-2", "run-2")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseTwo()
	if _, err := m.acquire("org/ws", "cache-3", "run-3"); err == nil {
		t.Fatal("expected per-tenant sandbox quota")
	}
	if _, err := m.acquire("other/ws", "cache-3", "run-3"); err != nil {
		t.Fatalf("quota leaked across tenants: %v", err)
	}
	if !m.claimed("cache-1") {
		t.Fatal("manager did not report active cache claim")
	}
	releaseOne()
	if m.claimed("cache-1") {
		t.Fatal("manager retained released cache claim")
	}
}

func TestProjectAssistantSandboxManagerPrunesExpiredLeasesBeforeQuota(t *testing.T) {
	m := newProjectAssistantSandboxManager()
	expired := time.Now().UTC().Add(-time.Second)
	if _, err := m.acquireUntil("org/ws", "cache-expired", "run-expired", expired); err != nil {
		t.Fatal(err)
	}
	if _, err := m.acquireUntil("org/ws", "cache-live", "run-live", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.acquireUntil("org/ws", "cache-new", "run-new", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("expired lease poisoned max-active capacity: %v", err)
	}
	if m.claimed("cache-expired") {
		t.Fatal("expired lease remained locally claimed")
	}
}

func TestProjectAssistantSupervisorStopDeletesSuspendedSandboxAndAllowsFreshRun(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "project-uid"}
	id := identity{orgUUID: scope.OrgUUID, workspaceUUID: scope.WorkspaceUUID}
	now := time.Now().UTC()
	run := store.AssistantRun{
		ID: "run-stop", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusPendingInput,
		ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", RequestID: "input-1",
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "tester", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	name := projectAssistantRunSandboxName(workspace.Scope(scope), nil, run.ID)
	metadata := projectAssistantRunSandboxMetadata{
		RunID: run.ID, OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID,
		ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID,
		Instance: projectAssistantSandboxInstance{Name: name}, CacheGeneration: run.ID,
	}
	checkpoint, err := json.Marshal(projectAssistantCheckpointState{Sandbox: &projectAssistantSandboxCheckpoint{Metadata: metadata}})
	if err != nil {
		t.Fatal(err)
	}
	run.Checkpoint = checkpoint
	if _, err := messages.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	instance := newRunSandboxTestInstance(name, projectAssistantRunSandboxCacheStateActive, now)
	annotations := instance.GetAnnotations()
	annotations[projectAssistantRunSandboxClaimOwner] = run.ID
	annotations[projectAssistantRunSandboxCacheGeneration] = run.ID
	annotations[projectAssistantRunSandboxClaimExpiry] = now.Add(time.Hour).Format(time.RFC3339Nano)
	instance.SetAnnotations(annotations)
	client := newRunSandboxTestClient(instance)
	server.projectClientFor = func(identity) (*asclient.Client, error) { return client, nil }
	release, err := server.projectAssistantSandboxManager().acquire(projectAssistantRunSandboxTenantKey(id, workspace.Scope(scope)), name, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := server.projectAssistantSupervisor().Attach(scope, run, assistant); err != nil {
		t.Fatal(err)
	}
	stopCtx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped, ok, err := server.projectAssistantSupervisor().StopWithIdentity(stopCtx, id, scope, run.ID)
	if err != nil || !ok || stopped.Status != store.AssistantRunStatusInterrupted {
		t.Fatalf("StopWithIdentity = run=%#v stopped=%t err=%v", stopped, ok, err)
	}
	if _, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("stopped sandbox lookup = %v, want NotFound", err)
	}
	nextRelease, err := server.projectAssistantSandboxManager().acquire(projectAssistantRunSandboxTenantKey(id, workspace.Scope(scope)), name, "run-next")
	if err != nil {
		t.Fatalf("fresh run was blocked by stopped sandbox lease: %v", err)
	}
	nextRelease()
}

func TestProjectAssistantInterruptedSandboxCleanupFailsClosedAndReleasesExactRunLease(t *testing.T) {
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "project-uid"}
	id := identity{orgUUID: scope.OrgUUID, workspaceUUID: scope.WorkspaceUUID}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-stop", Status: store.AssistantRunStatusPendingInput}
	name := projectAssistantRunSandboxName(workspace.Scope(scope), nil, run.ID)
	base := projectAssistantRunSandboxMetadata{
		RunID: run.ID, OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID,
		ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID,
		Instance: projectAssistantSandboxInstance{Name: name}, CacheGeneration: run.ID,
	}
	for _, tc := range []struct {
		name        string
		metadata    projectAssistantRunSandboxMetadata
		remoteOwner string
	}{
		{name: "missing run identity", metadata: func() projectAssistantRunSandboxMetadata { value := base; value.RunID = ""; return value }()},
		{name: "foreign remote owner", metadata: base, remoteOwner: "run-foreign"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkpoint, err := json.Marshal(projectAssistantCheckpointState{Sandbox: &projectAssistantSandboxCheckpoint{Metadata: tc.metadata}})
			if err != nil {
				t.Fatal(err)
			}
			run.Checkpoint = checkpoint
			instance := newRunSandboxTestInstance(name, projectAssistantRunSandboxCacheStateActive, now)
			annotations := instance.GetAnnotations()
			owner := run.ID
			if tc.remoteOwner != "" {
				owner = tc.remoteOwner
			}
			annotations[projectAssistantRunSandboxClaimOwner] = owner
			annotations[projectAssistantRunSandboxCacheGeneration] = owner
			instance.SetAnnotations(annotations)
			client := newRunSandboxTestClient(instance)
			server := &Server{projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
			release, err := server.projectAssistantSandboxManager().acquire(projectAssistantRunSandboxTenantKey(id, workspace.Scope(scope)), name, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			otherName := name + "-other"
			otherRelease, err := server.projectAssistantSandboxManager().acquire(projectAssistantRunSandboxTenantKey(id, workspace.Scope(scope)), otherName, "run-other")
			if err != nil {
				t.Fatal(err)
			}
			defer otherRelease()
			cleanupErr := server.cleanupInterruptedProjectAssistantRunSandbox(context.Background(), id, scope, run)
			if !errors.Is(cleanupErr, errProjectAssistantRunSandboxConflict) {
				t.Fatalf("cleanup error = %v, want sandbox conflict", cleanupErr)
			}
			if _, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), name, metav1.GetOptions{}); err != nil {
				t.Fatalf("fail-closed sandbox lookup = %v, want object retained", err)
			}
			if server.projectAssistantSandboxManager().claimed(name) {
				t.Fatal("exact stopped-run lease was not released")
			}
			if !server.projectAssistantSandboxManager().claimed(otherName) {
				t.Fatal("cleanup released a different run's lease")
			}
			release()
		})
	}
}

func TestProjectAssistantSandboxManagerConcurrentClaimsHaveOneLocalWriter(t *testing.T) {
	m := newProjectAssistantSandboxManager()
	const contenders = 32
	start := make(chan struct{})
	results := make(chan struct {
		release func()
		err     error
	}, contenders)
	for i := 0; i < contenders; i++ {
		runID := fmt.Sprintf("run-%d", i)
		go func() {
			<-start
			release, err := m.acquire("org/ws", "project-cache", runID)
			results <- struct {
				release func()
				err     error
			}{release: release, err: err}
		}()
	}
	close(start)

	successes := 0
	var releases []func()
	for i := 0; i < contenders; i++ {
		result := <-results
		if result.err == nil {
			successes++
			releases = append(releases, result.release)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent claims succeeded %d times, want exactly one local writer", successes)
	}
	for _, release := range releases {
		release()
	}
}

func TestProjectAssistantRunSandboxReclaimsOnlyTerminalDurableOwner(t *testing.T) {
	now := time.Now().UTC()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "api", ProjectUID: "project-uid"}
	for _, tc := range []struct {
		name        string
		status      store.AssistantRunStatus
		wantReclaim bool
	}{
		{name: "interrupted owner", status: store.AssistantRunStatusInterrupted, wantReclaim: true},
		{name: "running owner", status: store.AssistantRunStatusRunning, wantReclaim: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := store.NewMemoryStore()
			if err := messages.SaveAssistantRun(context.Background(), scope, store.AssistantRun{
				ID: "run-old", Mode: store.AssistantRunModeDefault, Status: tc.status,
				CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			instance := newRunSandboxTestInstance("cache", projectAssistantRunSandboxCacheStateActive, now)
			annotations := instance.GetAnnotations()
			annotations[projectAssistantRunSandboxClaimOwner] = "run-old"
			annotations[projectAssistantRunSandboxClaimExpiry] = now.Add(time.Hour).Format(time.RFC3339Nano)
			annotations[projectAssistantRunSandboxCacheGeneration] = "run-old"
			instance.SetAnnotations(annotations)
			client := newRunSandboxTestClient(instance)
			server := &Server{store: messages}

			_, err := server.claimProjectAssistantRunSandboxInstance(context.Background(), client, scope, "cache", "run-new")
			if tc.wantReclaim && err != nil {
				t.Fatalf("claim terminal owner's cache: %v", err)
			}
			if !tc.wantReclaim && !errors.Is(err, errProjectAssistantRunSandboxConflict) {
				t.Fatalf("claim live owner's cache error = %v, want sandbox conflict", err)
			}
			updated, getErr := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), "cache", metav1.GetOptions{})
			if getErr != nil {
				t.Fatal(getErr)
			}
			wantOwner := "run-old"
			if tc.wantReclaim {
				wantOwner = "run-new"
			}
			if owner := updated.GetAnnotations()[projectAssistantRunSandboxClaimOwner]; owner != wantOwner {
				t.Fatalf("durable owner = %q, want %q", owner, wantOwner)
			}
		})
	}
}

func TestProjectAssistantRunSandboxSafeTerminalRetainsProjectCache(t *testing.T) {
	now := time.Now().UTC()
	instance := newRunSandboxTestInstance("cache", projectAssistantRunSandboxCacheStateCached, now)
	client := newRunSandboxTestClient(instance)
	server := &Server{projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
	hardExpiry, err := server.claimProjectAssistantRunSandboxInstance(context.Background(), client, store.Scope{}, "cache", "run-2")
	if err != nil {
		t.Fatal(err)
	}
	release, err := server.projectAssistantSandboxManager().acquire("org/ws", "cache", "run-2")
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &projectAssistantRunSandbox{
		server: server,
		id:     identity{orgUUID: "org", workspaceUUID: "ws"},
		instance: projectAssistantSandboxInstance{
			Resource: runSandboxInstancesResource.GVR.Resource,
			Name:     "cache",
		},
		metadata: projectAssistantRunSandboxMetadata{
			Status:          projectAssistantRunSandboxCacheStateActive,
			RunID:           "run-2",
			CacheGeneration: "run-2",
			HardExpiresAt:   hardExpiry,
		},
	}
	modelErr := errors.New("model stream failed")
	if got := finishProjectAssistantRunSandbox(context.Background(), sandbox, release, modelErr, true); !errors.Is(got, modelErr) {
		t.Fatalf("finish error = %v, want original model failure", got)
	}
	retained, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), "cache", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("safe failed turn deleted cache: %v", err)
	}
	annotations := retained.GetAnnotations()
	if owner := annotations[projectAssistantRunSandboxClaimOwner]; owner != "" {
		t.Fatalf("retained cache owner = %q, want released", owner)
	}
	if state := annotations[projectAssistantRunSandboxCacheState]; state != projectAssistantRunSandboxCacheStateCached {
		t.Fatalf("retained cache state = %q", state)
	}
	if server.projectAssistantSandboxManager().claimed("cache") {
		t.Fatal("safe terminal retained process-local claim")
	}
}

func TestProjectAssistantRunSandboxUnsafeTerminalDeletesCache(t *testing.T) {
	now := time.Now().UTC()
	instance := newRunSandboxTestInstance("cache", projectAssistantRunSandboxCacheStateCached, now)
	client := newRunSandboxTestClient(instance)
	server := &Server{projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
	hardExpiry, err := server.claimProjectAssistantRunSandboxInstance(context.Background(), client, store.Scope{}, "cache", "run-3")
	if err != nil {
		t.Fatal(err)
	}
	release, err := server.projectAssistantSandboxManager().acquire("org/ws", "cache", "run-3")
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &projectAssistantRunSandbox{
		server: server,
		id:     identity{orgUUID: "org", workspaceUUID: "ws"},
		instance: projectAssistantSandboxInstance{
			Resource: runSandboxInstancesResource.GVR.Resource,
			Name:     "cache",
		},
		metadata: projectAssistantRunSandboxMetadata{Status: projectAssistantRunSandboxCacheStateActive, RunID: "run-3", CacheGeneration: "run-3", HardExpiresAt: hardExpiry},
	}
	checkpointErr := fmt.Errorf("%w: checkpoint fence failed", errProjectAssistantRunSandboxConflict)
	if got := finishProjectAssistantRunSandbox(context.Background(), sandbox, release, checkpointErr, false); !errors.Is(got, checkpointErr) {
		t.Fatalf("finish error = %v, want checkpoint failure", got)
	}
	if _, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), "cache", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("unsafe terminal cache lookup = %v, want NotFound", err)
	}
}

func TestProjectAssistantRunSandboxSetupFailureDeletesClaimedCache(t *testing.T) {
	now := time.Now().UTC()
	instance := newRunSandboxTestInstance("cache", projectAssistantRunSandboxCacheStateActive, now)
	annotations := instance.GetAnnotations()
	annotations[projectAssistantRunSandboxClaimOwner] = "setup-run"
	annotations[projectAssistantRunSandboxClaimExpiry] = now.Add(time.Hour).Format(time.RFC3339Nano)
	annotations[projectAssistantRunSandboxCacheGeneration] = "setup-run"
	instance.SetAnnotations(annotations)
	client := newRunSandboxTestClient(instance)
	server := &Server{projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
	sandbox := &projectAssistantRunSandbox{
		server: server,
		id:     identity{orgUUID: "org", workspaceUUID: "ws"},
		instance: projectAssistantSandboxInstance{
			Resource: runSandboxInstancesResource.GVR.Resource,
			Name:     "cache",
		},
		metadata: projectAssistantRunSandboxMetadata{Status: projectAssistantRunSandboxCacheStateActive, RunID: "setup-run", CacheGeneration: "setup-run"},
	}
	var releases int
	guard := newProjectAssistantRunSandboxSetupGuard(sandbox, func() { releases++ })
	guard.cleanupSetup()

	if _, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), "cache", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("setup-failure cache lookup = %v, want NotFound", err)
	}
	if releases != 1 || !sandbox.closed {
		t.Fatalf("setup cleanup releases=%d closed=%v, want one release and closed sandbox", releases, sandbox.closed)
	}
}

func TestProjectAssistantRunSandboxSuspensionPreservesDurableClaim(t *testing.T) {
	now := time.Now().UTC()
	instance := newRunSandboxTestInstance("cache", projectAssistantRunSandboxCacheStateActive, now)
	annotations := instance.GetAnnotations()
	annotations[projectAssistantRunSandboxClaimOwner] = "suspended-run"
	annotations[projectAssistantRunSandboxClaimExpiry] = now.Add(time.Hour).Format(time.RFC3339Nano)
	annotations[projectAssistantRunSandboxCacheGeneration] = "suspended-run"
	instance.SetAnnotations(annotations)
	client := newRunSandboxTestClient(instance)
	server := &Server{projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
	sandbox := &projectAssistantRunSandbox{
		server: server,
		id:     identity{orgUUID: "org", workspaceUUID: "ws"},
		instance: projectAssistantSandboxInstance{
			Resource: runSandboxInstancesResource.GVR.Resource,
			Name:     "cache",
		},
		metadata: projectAssistantRunSandboxMetadata{Status: projectAssistantRunSandboxCacheStateActive, RunID: "suspended-run", CacheGeneration: "suspended-run"},
	}
	var releases int
	guard := newProjectAssistantRunSandboxSetupGuard(sandbox, func() { releases++ })
	permission := &projectAssistantPermissionRequiredError{RunID: "suspended-run", ToolName: "edit_file"}
	if got := guard.finish(context.Background(), permission, true); got != permission {
		t.Fatalf("suspension finish error = %v, want original permission error", got)
	}

	retained, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), "cache", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("suspended cache lookup: %v", err)
	}
	got := retained.GetAnnotations()
	if got[projectAssistantRunSandboxClaimOwner] != "suspended-run" || got[projectAssistantRunSandboxCacheGeneration] != "suspended-run" {
		t.Fatalf("suspended durable claim = owner %q generation %q, want suspended-run for both", got[projectAssistantRunSandboxClaimOwner], got[projectAssistantRunSandboxCacheGeneration])
	}
	if got[projectAssistantRunSandboxCacheState] != projectAssistantRunSandboxCacheStateActive {
		t.Fatalf("suspended cache state = %q, want active", got[projectAssistantRunSandboxCacheState])
	}
	if releases != 0 || sandbox.closed {
		t.Fatalf("suspension released/closed sandbox: releases=%d closed=%v", releases, sandbox.closed)
	}
}

func TestProjectAssistantRunSandboxFreshFollowUpClaimsAndRebasesProjectCache(t *testing.T) {
	t.Setenv(projectAssistantRunSandboxModeEnv, string(CodingSandboxModeForce))
	t.Setenv(projectAssistantDevelopmentModeEnv, "true")
	ctx := context.Background()
	now := time.Now().UTC()
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "project-uid"}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "shop", UID: "project-uid"}}
	cacheName := projectAssistantRunSandboxName(scope, project, "run-1")
	cached := newRunSandboxTestInstance(cacheName, projectAssistantRunSandboxCacheStateCached, now.Add(-time.Minute))
	cacheAnnotations := cached.GetAnnotations()
	cacheAnnotations[projectAssistantRunSandboxCacheGeneration] = "run-1"
	delete(cacheAnnotations, projectAssistantRunSandboxClaimOwner)
	delete(cacheAnnotations, projectAssistantRunSandboxClaimExpiry)
	cached.SetGeneration(1)
	_ = unstructured.SetNestedField(cached.Object, readyRunSandboxStatus(1), "status")
	cached.SetAnnotations(cacheAnnotations)
	template := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.faros.sh/v1alpha1",
		"kind":       "Template",
		"metadata":   map[string]any{"name": projectAssistantRunSandboxDefaultTemplate},
		"spec": map[string]any{"development": map[string]any{"components": map[string]any{
			"workspace": map[string]any{"workspacePath": ".", "devImage": "${faros.devImage.universal}"},
		}}},
	}}
	client := newRunSandboxTestClient(cached, template)
	files := workspace.NewFileStore(t.TempDir())
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "main.go", Content: "package main\n"}}); err != nil {
		t.Fatal(err)
	}
	seedDigest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "package main\n"}})
	fake := &sandboxClientFake{response: projectAssistantSandboxWorkspaceResponse{SourceRevision: 9, SourceDigest: seedDigest}}
	server := &Server{
		workspaces:              files,
		hubBase:                 "https://hub.test",
		runSandboxClientFactory: func(*Server) projectAssistantSandboxClient { return fake },
	}
	req := projectAssistantRunRequest{
		Identity:       identity{orgUUID: "org", workspaceUUID: "ws", clusterID: "cluster", token: "token"},
		Client:         client,
		Project:        project,
		Workspace:      files,
		WorkspaceScope: scope,
		AssistantRun:   &store.AssistantRun{ID: "run-2"},
	}
	sandbox, release, err := server.ensureProjectAssistantRunSandbox(ctx, req, newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatalf("fresh follow-up ensure: %v", err)
	}
	defer release()
	if sandbox == nil {
		t.Fatal("fresh follow-up returned nil sandbox")
	}
	metadata := sandbox.metadataSnapshot()
	if metadata.Instance.Name != cacheName {
		t.Fatalf("follow-up cache name = %q, want %q", metadata.Instance.Name, cacheName)
	}
	if metadata.RunID != "run-2" || metadata.CacheGeneration != "run-2" {
		t.Fatalf("follow-up metadata run=%q generation=%q, want run-2 for both", metadata.RunID, metadata.CacheGeneration)
	}
	if metadata.RemoteCheckpointID != "baseline-next" {
		t.Fatalf("follow-up baseline = %q, want fresh baseline-next", metadata.RemoteCheckpointID)
	}
	fake.mu.Lock()
	requests := append([]projectAssistantSandboxWorkspaceRequest(nil), fake.requests...)
	fake.mu.Unlock()
	if len(requests) != 2 || requests[0].Action != "list" || requests[1].Action != "checkpoint" || requests[1].CheckpointAction != "create" {
		t.Fatalf("follow-up baseline requests = %#v, want remote manifest inspection then fresh checkpoint create without reseed", requests)
	}
	if requests[1].SourceRevision != 9 || !sandboxDigestEqual(requests[1].SourceDigest, seedDigest) {
		t.Fatalf("follow-up checkpoint fence = revision %d digest %q, want retained remote revision 9 and matching digest", requests[1].SourceRevision, requests[1].SourceDigest)
	}
	updated, err := client.Resource(runSandboxInstancesResource, "").Get(ctx, cacheName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	annotations := updated.GetAnnotations()
	if annotations[projectAssistantRunSandboxClaimOwner] != "run-2" || annotations[projectAssistantRunSandboxCacheGeneration] != "run-2" || annotations[projectAssistantRunSandboxCacheState] != projectAssistantRunSandboxCacheStateActive {
		t.Fatalf("follow-up durable claim = %#v, want run-2 active generation", annotations)
	}
}

func TestProjectAssistantRunSandboxColdMultiMutationWarmFollowUpKeepsRemoteRevisionDomain(t *testing.T) {
	t.Setenv(projectAssistantRunSandboxModeEnv, string(CodingSandboxModeForce))
	t.Setenv(projectAssistantDevelopmentModeEnv, "true")
	ctx := context.Background()
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "project-uid"}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "shop", UID: "project-uid"}}
	cacheName := projectAssistantRunSandboxName(scope, project, "run-cold")
	cached := newRunSandboxTestInstance(cacheName, projectAssistantRunSandboxCacheStateCached, time.Now().UTC())
	cached.SetGeneration(1)
	_ = unstructured.SetNestedField(cached.Object, readyRunSandboxStatus(1), "status")
	template := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.faros.sh/v1alpha1", "kind": "Template", "metadata": map[string]any{"name": projectAssistantRunSandboxDefaultTemplate},
		"spec": map[string]any{"development": map[string]any{"components": map[string]any{"workspace": map[string]any{"workspacePath": ".", "devImage": "${faros.devImage.universal}"}}}},
	}}
	client := newRunSandboxTestClient(cached, template)
	files := workspace.NewFileStore(t.TempDir())
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "main.go", Content: "package main\n"}}); err != nil {
		t.Fatal(err)
	}
	remote := &sandboxRevisionDomainFake{files: map[string]string{}}
	server := &Server{
		workspaces: files, hubBase: "https://hub.test",
		projectClientFor:        func(identity) (*asclient.Client, error) { return client, nil },
		runSandboxClientFactory: func(*Server) projectAssistantSandboxClient { return remote },
	}
	request := func(runID string) projectAssistantRunRequest {
		return projectAssistantRunRequest{
			Identity: identity{orgUUID: "org", workspaceUUID: "ws", clusterID: "cluster", token: "token"}, Client: client, Project: project,
			Workspace: files, WorkspaceScope: scope, AssistantRun: &store.AssistantRun{ID: runID},
		}
	}

	first, releaseFirst, err := server.ensureProjectAssistantRunSandbox(ctx, request("run-cold"), newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatalf("cold ensure: %v", err)
	}
	if first.metadataSnapshot().RemoteRevision != 1 {
		t.Fatalf("cold remote revision = %d, want worker-owned revision 1", first.metadataSnapshot().RemoteRevision)
	}
	remote.mutateRemote("main.go", "package main\n// one\n")
	remote.mutateRemote("node.js", "console.log('ok')\n")
	remote.mutateRemote("check.py", "print('ok')\n")
	if err := files.ApplyFiles(ctx, scope, []workspace.File{
		{Path: "main.go", Content: "package main\n// one\n"}, {Path: "node.js", Content: "console.log('ok')\n"}, {Path: "check.py", Content: "print('ok')\n"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.retain(ctx); err != nil {
		t.Fatalf("retain cold sandbox: %v", err)
	}
	releaseFirst()

	second, releaseSecond, err := server.ensureProjectAssistantRunSandbox(ctx, request("run-warm"), newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatalf("warm ensure: %v", err)
	}
	defer releaseSecond()
	if second.instance.Name != first.instance.Name || second.metadataSnapshot().RemoteRevision != 4 {
		t.Fatalf("warm cache = %q revision %d, want same %q at remote revision 4", second.instance.Name, second.metadataSnapshot().RemoteRevision, first.instance.Name)
	}
	read, err := second.read(ctx, "node.js")
	if err != nil || read.Content != "console.log('ok')\n" {
		t.Fatalf("warm read = %#v, err=%v", read, err)
	}
	if _, err := second.mutate(ctx, projectAssistantSandboxWorkspaceRequest{Action: "replace", Path: "check.py", Content: "print('warm')\n"}); err != nil {
		t.Fatalf("warm edit: %v", err)
	}
	execResult, err := second.exec(ctx, second.target.dataPlaneRefFor("workspace"), projectSandboxExecRequest{Action: "start", Argv: []string{"python", "check.py"}})
	if err != nil || execResult.Stdout != "WARM_SANDBOX_OK" {
		t.Fatalf("warm exec = %#v, err=%v", execResult, err)
	}
	remote.mu.Lock()
	actions := append([]string(nil), remote.actions...)
	remote.mu.Unlock()
	if strings.Count(strings.Join(actions, ","), "seed") != 1 {
		t.Fatalf("worker actions = %v, want only the cold authoritative seed", actions)
	}
}

func TestProjectAssistantRunSandboxQuotaEvictsOldestUnclaimedCache(t *testing.T) {
	now := time.Now().UTC()
	oldest := newRunSandboxTestInstance("oldest", projectAssistantRunSandboxCacheStateCached, now.Add(-time.Hour))
	newer := newRunSandboxTestInstance("newer", projectAssistantRunSandboxCacheStateCached, now.Add(-time.Minute))
	client := newRunSandboxTestClient(oldest, newer)
	server := &Server{}
	if err := server.enforceProjectAssistantRunSandboxQuota(context.Background(), client, "incoming"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), "oldest", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("oldest cache lookup = %v, want eviction", err)
	}
	if _, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), "newer", metav1.GetOptions{}); err != nil {
		t.Fatalf("newer cache was evicted: %v", err)
	}
}

func TestProjectAssistantRunSandboxQuotaDoesNotEvictLocallyClaimedCache(t *testing.T) {
	now := time.Now().UTC()
	claimed := newRunSandboxTestInstance("claimed", projectAssistantRunSandboxCacheStateCached, now.Add(-time.Hour))
	unclaimed := newRunSandboxTestInstance("unclaimed", projectAssistantRunSandboxCacheStateCached, now.Add(-time.Minute))
	client := newRunSandboxTestClient(claimed, unclaimed)
	server := &Server{}
	release, err := server.projectAssistantSandboxManager().acquire("org/ws", "claimed", "run-active")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := server.enforceProjectAssistantRunSandboxQuota(context.Background(), client, "incoming"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), "claimed", metav1.GetOptions{}); err != nil {
		t.Fatalf("locally claimed cache was evicted: %v", err)
	}
	if _, err := client.Resource(runSandboxInstancesResource, "").Get(context.Background(), "unclaimed", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("unclaimed cache lookup = %v, want eviction", err)
	}
}

func TestProjectAssistantRunSandboxInstanceReadyWaitsForPendingStatus(t *testing.T) {
	components := map[string]projectTemplateComponent{"workspace": {WorkspacePath: "."}}
	pending := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{}}}
	ready := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"generation": int64(1)}}}
	_ = unstructured.SetNestedField(ready.Object, readyRunSandboxStatus(1), "status")
	objects := []*unstructured.Unstructured{pending, ready}
	calls := 0
	err := waitForProjectAssistantRunSandboxInstanceReady(context.Background(), 100*time.Millisecond, time.Millisecond, components, func(context.Context) (*unstructured.Unstructured, error) {
		object := objects[calls]
		calls++
		return object, nil
	})
	if err != nil {
		t.Fatalf("readiness wait = %v", err)
	}
	if calls != 2 {
		t.Fatalf("readiness polls = %d, want pending then ready", calls)
	}
}

func TestProjectAssistantRunSandboxInstanceReadyTimeoutIncludesStatus(t *testing.T) {
	components := map[string]projectTemplateComponent{"workspace": {WorkspacePath: "."}}
	pending := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"generation": int64(1)}}}
	status := readyRunSandboxStatus(1)
	delete(status["components"].(map[string]any)["workspace"].(map[string]any), "controlServiceRef")
	_ = unstructured.SetNestedField(pending.Object, status, "status")
	err := waitForProjectAssistantRunSandboxInstanceReady(context.Background(), 10*time.Millisecond, time.Millisecond, components, func(context.Context) (*unstructured.Unstructured, error) {
		return pending, nil
	})
	if err == nil || !strings.Contains(err.Error(), "did not become ready") || !strings.Contains(err.Error(), "controlServiceRef") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestProjectAssistantRunSandboxInstanceReadinessRequiresCurrentRuntimeState(t *testing.T) {
	components := map[string]projectTemplateComponent{"workspace": {WorkspacePath: "."}}
	tests := []struct {
		name   string
		mutate func(*unstructured.Unstructured)
		want   bool
	}{
		{
			name: "stale observed generation",
			mutate: func(instance *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(instance.Object, int64(1), "status", "observedGeneration")
			},
		},
		{
			name: "missing network phase",
			mutate: func(instance *unstructured.Unstructured) {
				unstructured.RemoveNestedField(instance.Object, "status", "farosNetworkPhase")
			},
		},
		{
			name: "setup network phase",
			mutate: func(instance *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(instance.Object, "setup", "status", "farosNetworkPhase")
			},
		},
		{
			name: "wrong network phase casing",
			mutate: func(instance *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(instance.Object, "Runtime", "status", "farosNetworkPhase")
			},
		},
		{
			name: "current runtime ready with component readiness field",
			want: true,
		},
		{
			name: "current runtime ready without component readiness field",
			mutate: func(instance *unstructured.Unstructured) {
				unstructured.RemoveNestedField(instance.Object, "status", "components", "workspace", "ready")
			},
			want: true,
		},
		{
			name: "current runtime ready with float64 generations and no component readiness field",
			mutate: func(instance *unstructured.Unstructured) {
				metadata := instance.Object["metadata"].(map[string]any)
				metadata["generation"] = float64(2)
				status := instance.Object["status"].(map[string]any)
				status["observedGeneration"] = float64(2)
				status["conditions"].([]any)[0].(map[string]any)["observedGeneration"] = float64(2)
				unstructured.RemoveNestedField(instance.Object, "status", "components", "workspace", "ready")
			},
			want: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"generation": int64(2)}}}
			_ = unstructured.SetNestedField(instance.Object, readyRunSandboxStatus(2), "status")
			if test.mutate != nil {
				test.mutate(instance)
			}
			ready, terminal, reason := projectAssistantRunSandboxInstanceReadiness(instance, components)
			if ready != test.want || terminal {
				t.Fatalf("readiness = %t terminal = %t reason = %q, want ready=%t non-terminal", ready, terminal, reason, test.want)
			}
		})
	}
}

func TestProjectAssistantRunSandboxQuotaIgnoresSameExpiredAndDeletingInstances(t *testing.T) {
	now := time.Now().UTC()
	newInstance := func(name string) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{
				"name":   name,
				"labels": map[string]any{projectAssistantRunSandboxLabel: "true"},
			},
		}}
	}
	activeOne := newInstance("run-one")
	activeTwo := newInstance("run-two")
	sameRun := newInstance("current-run")
	expired := newInstance("expired")
	expired.SetAnnotations(map[string]string{projectAssistantRunSandboxHardExpiry: now.Add(-time.Minute).Format(time.RFC3339Nano)})
	deleting := newInstance("deleting")
	deleting.SetDeletionTimestamp(&metav1.Time{Time: now})
	terminal := newInstance("terminal")
	_ = unstructured.SetNestedField(terminal.Object, "Expired", "status", "phase")
	list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{activeOne, activeTwo, sameRun, expired, deleting, terminal}}
	if got := countProjectAssistantRunSandboxInstances(list, "current-run", now); got != 2 {
		t.Fatalf("active quota count = %d, want 2", got)
	}
	if got := countProjectAssistantRunSandboxInstances(list, "run-one", now); got != 2 {
		t.Fatalf("same-name quota count = %d, want 2", got)
	}
}

func TestProjectAssistantRunSandboxSeedRetries503ThenSucceeds(t *testing.T) {
	calls := 0
	err := retryProjectAssistantRunSandboxSeed(context.Background(), 100*time.Millisecond, time.Millisecond, func(context.Context) error {
		calls++
		if calls == 1 {
			return &projectDevelopmentSyncHTTPError{component: "workspace", status: http.StatusServiceUnavailable, detail: "connection refused"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed retry = %v", err)
	}
	if calls != 2 {
		t.Fatalf("seed attempts = %d, want transient failure followed by success", calls)
	}
}

func TestProjectAssistantRunSandboxSeedPermanentErrorFailsFast(t *testing.T) {
	calls := 0
	err := retryProjectAssistantRunSandboxSeed(context.Background(), 100*time.Millisecond, time.Millisecond, func(context.Context) error {
		calls++
		return &projectDevelopmentSyncHTTPError{component: "workspace", status: http.StatusConflict, detail: "workspace revision conflict"}
	})
	if err == nil || calls != 1 {
		t.Fatalf("permanent seed error = %v, attempts = %d, want one fail-fast attempt", err, calls)
	}
	if projectAssistantRunSandboxSeedRetryable(&projectDevelopmentSyncHTTPError{status: http.StatusConflict, detail: "invalid source"}) {
		t.Fatal("validation conflict was incorrectly marked retryable")
	}
	if !projectAssistantRunSandboxSeedRetryable(&projectDevelopmentSyncHTTPError{status: http.StatusConflict, detail: "runtime namespace is not ready"}) {
		t.Fatal("not-ready conflict was not marked retryable")
	}
}

func TestProjectAssistantRunSandboxCheckpointIsAtomicAndSyncs(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "main.go", Content: "old\n"}}); err != nil {
		t.Fatal(err)
	}
	current, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "main.go", MaxBytes: workspace.MaxReadMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := files.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	synced := make(chan string, 1)
	server := &Server{
		workspaces: files,
		developmentSyncAfterMutation: func(_ identity, _ *aiv1alpha1.Project, name string) error {
			synced <- name
			return nil
		},
	}
	fake := &sandboxClientFake{response: projectAssistantSandboxWorkspaceResponse{
		SourceRevision: revision,
		SourceDigest:   projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "old\n"}}),
		Changes: []projectAssistantSandboxWorkspaceChange{{
			Path: "main.go", Operation: string(workspace.ManagedFileReplace), Content: "new\n", ExpectedVersion: current.Version,
		}},
	}}
	project := &aiv1alpha1.Project{}
	project.Name = "shop"
	// The sandbox captures the project before a template is selected. The
	// request carries the shared, refreshed project after selection.
	sandboxProject := project.DeepCopy()
	project.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}
	sandbox := &projectAssistantRunSandbox{
		server: server, client: fake, project: sandboxProject, id: identity{orgUUID: "org", workspaceUUID: "ws"}, scope: scope,
		target:   projectDevelopmentSyncTargetInfo{Resource: "instances", ResourceName: "run", Components: map[string]projectTemplateComponent{"app": {WorkspacePath: "."}}},
		metadata: projectAssistantRunSandboxMetadata{Status: "active", SourceRevision: revision, SourceDigest: fake.response.SourceDigest, RemoteRevision: revision, RemoteDigest: fake.response.SourceDigest, RemoteCheckpointID: "baseline", RunID: "run", HardExpiresAt: time.Now().Add(time.Hour)},
	}
	if err := sandbox.checkpoint(ctx, projectAssistantRunRequest{Workspace: files, WorkspaceScope: scope, Identity: identity{orgUUID: "org", workspaceUUID: "ws"}, Project: project}); err != nil {
		t.Fatal(err)
	}
	got, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "main.go", MaxBytes: workspace.MaxReadMaxBytes})
	if err != nil || got.Content != "new\n" {
		t.Fatalf("checkpoint content = %#v, err=%v", got, err)
	}
	select {
	case <-synced:
	case <-time.After(time.Second):
		t.Fatal("checkpoint did not trigger preview sync")
	}

	// A source revision drift rejects the complete checkpoint before the fake
	// worker response can be applied, preserving the local bytes atomically.
	if _, err := files.CreateFile(ctx, scope, workspace.CreateOptions{Path: "other.txt", Content: "drift"}); err != nil {
		t.Fatal(err)
	}
	fake.response.Changes = []projectAssistantSandboxWorkspaceChange{{Path: "main.go", Operation: string(workspace.ManagedFileReplace), Content: "bad\n", ExpectedVersion: got.Version}}
	if err := sandbox.checkpoint(ctx, projectAssistantRunRequest{Workspace: files, WorkspaceScope: scope, Identity: identity{orgUUID: "org", workspaceUUID: "ws"}, Project: project}); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("checkpoint drift err = %v, want conflict", err)
	}
}

func TestProjectAssistantRunSandboxTerminalCheckpointDetachesInterruptedContext(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "main.go", Content: "package main\n"}}); err != nil {
		t.Fatal(err)
	}
	revision, err := files.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	digest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "package main\n"}})
	fake := &sandboxClientFake{response: projectAssistantSandboxWorkspaceResponse{SourceRevision: revision, SourceDigest: digest}}
	sandbox := &projectAssistantRunSandbox{
		client: fake,
		id:     identity{orgUUID: "org", workspaceUUID: "ws"},
		scope:  scope,
		target: projectDevelopmentSyncTargetInfo{Resource: "instances", ResourceName: "run", Components: map[string]projectTemplateComponent{
			projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."},
		}},
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: revision, SourceDigest: digest,
			RemoteRevision: revision, RemoteDigest: digest, RemoteCheckpointID: "baseline",
			RunID: "run", HardExpiresAt: time.Now().Add(time.Hour),
		},
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := sandbox.checkpointForTerminalSettlement(canceled, projectAssistantRunRequest{Workspace: files, WorkspaceScope: scope}); err != nil {
		t.Fatalf("interrupted terminal checkpoint: %v", err)
	}
	fake.mu.Lock()
	workspaceCalls := fake.workspaceCalls
	requests := append([]projectAssistantSandboxWorkspaceRequest(nil), fake.requests...)
	fake.mu.Unlock()
	if workspaceCalls != 1 || len(requests) != 1 || requests[0].Action != "checkpoint" {
		t.Fatalf("terminal checkpoint requests = %#v, calls=%d", requests, workspaceCalls)
	}

	expired, expire := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer expire()
	if err := sandbox.checkpointForTerminalSettlement(expired, projectAssistantRunRequest{Workspace: files, WorkspaceScope: scope}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired terminal checkpoint error = %v, want deadline exceeded", err)
	}
	fake.mu.Lock()
	workspaceCalls = fake.workspaceCalls
	fake.mu.Unlock()
	if workspaceCalls != 1 {
		t.Fatalf("expired terminal checkpoint reached remote worker: calls=%d", workspaceCalls)
	}
}

func TestAttachProjectAssistantRunSandboxAllowsLegacyCheckpointWithoutSandbox(t *testing.T) {
	t.Setenv(projectAssistantRunSandboxFlagEnv, "true")
	sandbox, release, err := (&Server{}).attachProjectAssistantRunSandbox(
		context.Background(),
		projectAssistantRunRequest{},
		newProjectEinoAssistantRunState(),
		nil,
	)
	if err != nil {
		t.Fatalf("attach legacy checkpoint: %v", err)
	}
	if sandbox != nil || release == nil {
		t.Fatalf("legacy attach = sandbox %#v, release nil=%t; want no-op", sandbox, release == nil)
	}
	release()
}

func TestProjectAssistantRunSandboxCheckpointPersistsWithoutPreviewTemplate(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "go-todo", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "go.mod", Content: "module example.com/todo\n"}}); err != nil {
		t.Fatal(err)
	}
	current, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "go.mod", MaxBytes: workspace.MaxReadMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := files.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "go.mod", Content: current.Content}})
	syncCalled := make(chan struct{}, 1)
	server := &Server{
		workspaces: files,
		developmentSyncAfterMutation: func(identity, *aiv1alpha1.Project, string) error {
			syncCalled <- struct{}{}
			return nil
		},
	}
	fake := &sandboxClientFake{response: projectAssistantSandboxWorkspaceResponse{
		Changes: []projectAssistantSandboxWorkspaceChange{{
			Path: "main.go", Operation: string(workspace.ManagedFileCreate), Content: "package main\n",
		}},
	}}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "go-todo", UID: "uid"}}
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	sandbox := &projectAssistantRunSandbox{
		server: server, client: fake, project: project, id: identity{orgUUID: "org", workspaceUUID: "ws"}, scope: scope, runState: state,
		target: projectDevelopmentSyncTargetInfo{Resource: "instances", ResourceName: "run", Components: map[string]projectTemplateComponent{
			projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."},
		}},
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", Template: projectAssistantRunSandboxDefaultTemplate,
			SourceRevision: revision, SourceDigest: oldDigest, RemoteRevision: revision + 1, RemoteDigest: "changed",
			RemoteCheckpointID: "baseline", RunID: "run", HardExpiresAt: time.Now().Add(time.Hour),
		},
	}
	state.SetSandbox(sandbox)
	if err := sandbox.checkpoint(ctx, projectAssistantRunRequest{Workspace: files, WorkspaceScope: scope, Identity: sandbox.id, Project: project}); err != nil {
		t.Fatal(err)
	}
	got, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "main.go", MaxBytes: workspace.MaxReadMaxBytes})
	if err != nil || got.Content != "package main\n" {
		t.Fatalf("checkpointed Go source = %#v, err=%v", got, err)
	}
	dirty, err := files.UncommittedPaths(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(dirty, ",") != "main.go" {
		t.Fatalf("dirty paths = %#v, want main.go", dirty)
	}
	select {
	case <-syncCalled:
		t.Fatal("template-less checkpoint scheduled a hosted preview sync")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestProjectAssistantRunSandboxInspectionCheckpointsDirtyMutationBeforeBrowser(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "main.go", Content: "old\n"}}); err != nil {
		t.Fatal(err)
	}
	current, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "main.go", MaxBytes: workspace.MaxReadMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := files.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "old\n"}})
	newDigest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "new\n"}})
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	state.RecordSourceMutation()
	fake := &sandboxClientFake{response: projectAssistantSandboxWorkspaceResponse{
		SourceRevision: revision + 1,
		SourceDigest:   newDigest,
		Changes: []projectAssistantSandboxWorkspaceChange{{
			Path: "main.go", Operation: string(workspace.ManagedFileReplace), Content: "new\n", ExpectedVersion: current.Version,
		}},
	}}
	project := &aiv1alpha1.Project{}
	project.Name = "shop"
	project.UID = "project-uid"
	project.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}
	id := identity{orgUUID: "org", workspaceUUID: "ws"}
	server := NewWithWorkspace(nil, store.NewMemoryStore(), files, "", false)
	var syncCalls int
	server.developmentSyncAfterMutation = func(_ identity, _ *aiv1alpha1.Project, name string) error {
		if name != projectActionWorkspaceSync {
			t.Fatalf("sync action = %q, want %q", name, projectActionWorkspaceSync)
		}
		syncCalls++
		return nil
	}
	inspector := &sandboxSyncObservingPreviewInspector{runState: state}
	server.previewInspector = inspector
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	sandbox := &projectAssistantRunSandbox{
		server: server, client: fake, id: id, project: project, scope: scope,
		target:   projectDevelopmentSyncTargetInfo{Resource: "instances", ResourceName: "run"},
		runState: state,
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: revision, SourceDigest: oldDigest,
			RemoteRevision: revision + 1, RemoteDigest: newDigest, RemoteCheckpointID: "baseline",
			RunID: "run", HardExpiresAt: time.Now().Add(time.Hour),
		},
	}
	state.SetSandbox(sandbox)
	request := projectAssistantToolCallRequest{Identity: id, Project: project, WorkspaceScope: scope, RunState: state}
	if _, err := server.inspectProjectDevelopmentPreview(ctx, request); err != nil {
		t.Fatal(err)
	}
	if inspector.calls != 1 || inspector.status != "succeeded" {
		t.Fatalf("browser inspection evidence = calls %d, status %q; want one call after positive sync", inspector.calls, inspector.status)
	}
	if syncCalls != 1 {
		t.Fatalf("sync calls = %d, want one checkpoint-triggered sync", syncCalls)
	}
	got, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "main.go", MaxBytes: workspace.MaxReadMaxBytes})
	if err != nil || got.Content != "new\n" {
		t.Fatalf("checkpointed source = %#v, err=%v", got, err)
	}
	if fake.workspaceCalls != 2 {
		t.Fatalf("first inspection workspace calls = %d, want diff plus baseline create", fake.workspaceCalls)
	}
	if _, err := server.inspectProjectDevelopmentPreview(ctx, request); err != nil {
		t.Fatal(err)
	}
	if fake.workspaceCalls != 2 || syncCalls != 1 {
		t.Fatalf("clean second inspection repeated checkpoint: workspace calls=%d sync calls=%d", fake.workspaceCalls, syncCalls)
	}
}

func TestProjectAssistantRunSandboxCheckpointIfDirtySkipsReadOnlyAndCleanRuns(t *testing.T) {
	server := &Server{workspaces: workspace.NewFileStore(t.TempDir())}
	id := identity{orgUUID: "org", workspaceUUID: "ws"}
	project := &aiv1alpha1.Project{}
	project.Name = "shop"
	state := newProjectEinoAssistantRunState()
	fake := &sandboxClientFake{response: projectAssistantSandboxWorkspaceResponse{SourceRevision: 2, SourceDigest: "new"}}
	sandbox := &projectAssistantRunSandbox{
		server: server, client: fake, id: id, project: project,
		scope: workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "uid"}, runState: state,
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: 1, SourceDigest: "old", RemoteRevision: 2, RemoteDigest: "new", RemoteCheckpointID: "baseline",
			CheckpointRevision: 2, CheckpointDigest: "new",
		},
	}
	state.SetSandbox(sandbox)
	checkpointed, err := server.checkpointProjectAssistantRunSandboxIfDirty(context.Background(), state)
	if err != nil || checkpointed || fake.workspaceCalls != 0 {
		t.Fatalf("read-only checkpoint = (%v, %v), worker calls=%d; want clean no-op", checkpointed, err, fake.workspaceCalls)
	}

	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	sandbox.mu.Lock()
	sandbox.metadata.RemoteRevision = sandbox.metadata.SourceRevision
	sandbox.metadata.RemoteDigest = sandbox.metadata.SourceDigest
	sandbox.metadata.CheckpointRevision = sandbox.metadata.SourceRevision
	sandbox.metadata.CheckpointDigest = sandbox.metadata.SourceDigest
	sandbox.mu.Unlock()
	checkpointed, err = server.checkpointProjectAssistantRunSandboxIfDirty(context.Background(), state)
	if err != nil || checkpointed || fake.workspaceCalls != 0 {
		t.Fatalf("clean checkpoint = (%v, %v), worker calls=%d; want no-op", checkpointed, err, fake.workspaceCalls)
	}
}

func TestProjectAssistantRunSandboxCheckpointConflictIsFailClosed(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "main.go", Content: "old\n"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := files.CreateFile(ctx, scope, workspace.CreateOptions{Path: "drift.txt", Content: "drift"}); err != nil {
		t.Fatal(err)
	}
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	state.RecordSourceMutation()
	server := &Server{workspaces: files}
	fake := &sandboxClientFake{response: projectAssistantSandboxWorkspaceResponse{SourceRevision: 3, SourceDigest: "new", Changes: []projectAssistantSandboxWorkspaceChange{{Path: "main.go", Operation: string(workspace.ManagedFileReplace), Content: "new\n", ExpectedVersion: "sha256:old"}}}}
	sandbox := &projectAssistantRunSandbox{
		server: server, client: fake, scope: scope, runState: state,
		metadata: projectAssistantRunSandboxMetadata{Status: "active", SourceRevision: 2, SourceDigest: "old", RemoteRevision: 3, RemoteDigest: "new", RemoteCheckpointID: "baseline"},
	}
	state.SetSandbox(sandbox)
	checkpointed, err := server.checkpointProjectAssistantRunSandboxIfDirty(ctx, state)
	if !checkpointed || !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("conflicting checkpoint = (%v, %v), want dirty fail-closed conflict", checkpointed, err)
	}
	if !strings.Contains(projectAssistantRunSandboxCheckpointFailure(err), "not current") {
		t.Fatalf("checkpoint failure = %q, want truthful not-current evidence", projectAssistantRunSandboxCheckpointFailure(err))
	}
}

type sandboxSyncObservingPreviewInspector struct {
	runState *projectEinoAssistantRunState
	status   string
	calls    int
}

func (f *sandboxSyncObservingPreviewInspector) Health(context.Context) error { return nil }

func (f *sandboxSyncObservingPreviewInspector) Inspect(_ context.Context, _ projectAssistantPreviewInspectionRequest) (projectAssistantPreviewInspectionResult, error) {
	f.calls++
	f.status, _ = f.runState.DevelopmentSyncEvidence(1)
	return projectAssistantPreviewInspectionResult{Status: "succeeded"}, nil
}

type sandboxClientFake struct {
	mu             sync.Mutex
	response       projectAssistantSandboxWorkspaceResponse
	execResponse   projectSandboxExecResponse
	execCalls      int
	workspaceCalls int
	requests       []projectAssistantSandboxWorkspaceRequest
}

type sandboxRevisionDomainFake struct {
	mu       sync.Mutex
	revision uint64
	digest   string
	files    map[string]string
	actions  []string
}

func (f *sandboxRevisionDomainFake) Workspace(_ context.Context, _ identity, _ dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, request.Action)
	switch request.Action {
	case "list":
		return projectAssistantSandboxWorkspaceResponse{Status: "ok", SourceRevision: f.revision, SourceDigest: f.digest}, nil
	case "seed":
		f.revision++
		if f.revision == 0 {
			f.revision = 1
		}
		f.files = make(map[string]string, len(request.Files))
		for _, file := range request.Files {
			f.files[file.Path] = file.Content
		}
		f.digest = projectSandboxSyncDigest(request.Files)
		return projectAssistantSandboxWorkspaceResponse{Status: "synced", SourceRevision: f.revision, SourceDigest: f.digest}, nil
	case "checkpoint":
		if request.SourceRevision != f.revision || !sandboxDigestEqual(request.SourceDigest, f.digest) {
			return projectAssistantSandboxWorkspaceResponse{}, errProjectAssistantRunSandboxConflict
		}
		return projectAssistantSandboxWorkspaceResponse{Status: "ok", SourceRevision: f.revision, SourceDigest: f.digest, CheckpointID: fmt.Sprintf("baseline-%d", f.revision)}, nil
	case "read":
		content, ok := f.files[request.Path]
		if !ok {
			return projectAssistantSandboxWorkspaceResponse{}, fs.ErrNotExist
		}
		return projectAssistantSandboxWorkspaceResponse{Status: "ok", SourceRevision: f.revision, SourceDigest: f.digest, File: workspace.FileContent{Path: request.Path, Content: content}}, nil
	case "replace":
		if request.SourceRevision != f.revision || !sandboxDigestEqual(request.SourceDigest, f.digest) {
			return projectAssistantSandboxWorkspaceResponse{}, errProjectAssistantRunSandboxConflict
		}
		f.files[request.Path] = request.Content
		f.revision++
		f.digest = projectSandboxSyncDigest(projectSandboxFilesFromMap(f.files))
		return projectAssistantSandboxWorkspaceResponse{Status: "ok", SourceRevision: f.revision, SourceDigest: f.digest, Mutation: workspace.MutationResult{Path: request.Path}}, nil
	default:
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("unexpected workspace action %q", request.Action)
	}
}

func (f *sandboxRevisionDomainFake) Exec(_ context.Context, _ identity, _ dataPlaneRef, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if request.SourceRevision != f.revision || !sandboxDigestEqual(request.SourceDigest, f.digest) {
		return projectSandboxExecResponse{}, errProjectAssistantRunSandboxConflict
	}
	return projectSandboxExecResponse{State: "succeeded", Stdout: "WARM_SANDBOX_OK"}, nil
}

func (f *sandboxRevisionDomainFake) mutateRemote(path, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = content
	f.revision++
	f.digest = projectSandboxSyncDigest(projectSandboxFilesFromMap(f.files))
}

func projectSandboxFilesFromMap(files map[string]string) []projectSandboxSyncFile {
	out := make([]projectSandboxSyncFile, 0, len(files))
	for path, content := range files {
		out = append(out, projectSandboxSyncFile{Path: path, Content: content})
	}
	return out
}

func (f *sandboxClientFake) Workspace(ctx context.Context, _ identity, _ dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	if err := ctx.Err(); err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workspaceCalls++
	f.requests = append(f.requests, request)
	if request.Action == "seed" {
		response := f.response
		if response.SourceRevision == 0 {
			response.SourceRevision = 1
		}
		if response.SourceDigest == "" {
			response.SourceDigest = projectSandboxSyncDigest(request.Files)
		}
		return response, nil
	}
	if request.CheckpointAction == "create" {
		response := f.response
		response.Changes = nil
		response.CheckpointID = "baseline-next"
		if response.SourceRevision == 0 {
			response.SourceRevision = request.SourceRevision
		}
		if response.SourceDigest == "" {
			response.SourceDigest = request.SourceDigest
		}
		return response, nil
	}
	return f.response, nil
}

func (f *sandboxClientFake) Exec(context.Context, identity, dataPlaneRef, projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCalls++
	if strings.TrimSpace(f.execResponse.State) != "" {
		return f.execResponse, nil
	}
	return projectSandboxExecResponse{SessionID: "session", State: "succeeded"}, nil
}

var _ projectAssistantSandboxClient = (*sandboxClientFake)(nil)

func TestProjectAssistantRunSandboxFeatureFlag(t *testing.T) {
	old := getenv
	defer func() { getenv = old }()
	getenv = func(key string) string {
		if key == projectAssistantRunSandboxFlagEnv {
			return "true"
		}
		return os.Getenv(key)
	}
	if !projectAssistantRunSandboxEnabled() {
		t.Fatal("true run-sandbox flag did not enable feature")
	}
	getenv = func(string) string { return "" }
	if projectAssistantRunSandboxEnabled() {
		t.Fatal("empty run-sandbox flag enabled feature")
	}
}

func TestCodingSandboxPolicyFailsClosedAndMigratesLegacyBoolean(t *testing.T) {
	tests := []struct {
		name     string
		values   map[string]string
		mode     CodingSandboxMode
		eligible bool
		warn     bool
		wantErr  bool
	}{
		{name: "default off", values: map[string]string{}, mode: CodingSandboxModeOff},
		{name: "explicit off", values: map[string]string{projectAssistantRunSandboxModeEnv: "off"}, mode: CodingSandboxModeOff},
		{name: "byo unresolved", values: map[string]string{projectAssistantRunSandboxModeEnv: "byo-only"}, mode: CodingSandboxModeBYOOnly},
		{name: "legacy true", values: map[string]string{projectAssistantRunSandboxFlagEnv: "true"}, mode: CodingSandboxModeBYOOnly, warn: true},
		{name: "legacy false", values: map[string]string{projectAssistantRunSandboxFlagEnv: "false"}, mode: CodingSandboxModeOff, warn: true},
		{name: "force rejected outside dev", values: map[string]string{projectAssistantRunSandboxModeEnv: "force"}, wantErr: true},
		{name: "force rejected with multiple replicas", values: map[string]string{projectAssistantRunSandboxModeEnv: "force", projectAssistantDevelopmentModeEnv: "true", projectAssistantReplicaCountEnv: "2"}, wantErr: true},
		{name: "force dev", values: map[string]string{projectAssistantRunSandboxModeEnv: "force", projectAssistantDevelopmentModeEnv: "true"}, mode: CodingSandboxModeForce, eligible: true},
		{name: "invalid", values: map[string]string{projectAssistantRunSandboxModeEnv: "sometimes"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, warnings, err := ParseCodingSandboxConfig(func(key string) string { return tt.values[key] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseCodingSandboxConfig error = %v, wantErr=%t", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if config.Mode != tt.mode {
				t.Fatalf("mode = %q, want %q", config.Mode, tt.mode)
			}
			if (len(warnings) > 0) != tt.warn {
				t.Fatalf("warnings = %#v, want warning=%t", warnings, tt.warn)
			}
			eligibility := codingSandboxEligibility(config)
			if eligibility.Eligible != tt.eligible {
				t.Fatalf("eligibility = %#v, want eligible=%t", eligibility, tt.eligible)
			}
			if tt.eligible && (eligibility.ProviderExportPath == "" || eligibility.TransportGeneration == "") {
				t.Fatalf("eligible policy lacks provider/transport identity: %#v", eligibility)
			}
		})
	}
}

func TestCodingSandboxOffSkipsInfrastructureBeforeRequestValidation(t *testing.T) {
	server := &Server{runSandboxConfig: CodingSandboxConfig{Mode: CodingSandboxModeOff}, runSandboxConfigured: true}
	sandbox, release, err := server.ensureProjectAssistantRunSandbox(context.Background(), projectAssistantRunRequest{}, nil)
	if err != nil || sandbox != nil || release == nil {
		t.Fatalf("off-mode ensure = sandbox %#v releaseNil=%t err=%v", sandbox, release == nil, err)
	}
	release()
}

func TestCodingSandboxOffPreservesLegacyModelToolSelectionWithoutInfrastructureCalls(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "sandbox-off-tool-baseline")
	h.req.TurnProfile = projectAssistantTurnProfileImplementation
	h.req.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	resolverCalls := 0
	infrastructureCalls := 0
	h.server.codingSandboxResolver = func(context.Context, identity, workspace.Scope) (CodingSandboxEligibility, error) {
		resolverCalls++
		return CodingSandboxEligibility{}, errors.New("off mode must not resolve a provider")
	}
	h.server.runSandboxClientFactory = func(*Server) projectAssistantSandboxClient {
		infrastructureCalls++
		return nil
	}

	modelToolSpecs := func(label string) string {
		t.Helper()
		state := newProjectEinoAssistantRunState()
		state.SetTurnPolicy(h.req.TurnPolicy)
		eligibility := h.server.ResolveCodingSandboxEligibility(context.Background(), h.req.Identity, h.req.WorkspaceScope)
		state.ConfigureSandboxCapability(eligibility, nil)
		tools, err := projectEinoAssistantToolsForDiscovery(context.Background(), h.server, h.req, state, projectEinoAssistantToolDiscovery{})
		if err != nil {
			t.Fatalf("%s tool selection: %v", label, err)
		}
		infos := make([]any, 0, len(tools))
		for _, tool := range tools {
			info, err := tool.Info(context.Background())
			if err != nil {
				t.Fatalf("%s tool info: %v", label, err)
			}
			infos = append(infos, info)
		}
		encoded, err := json.Marshal(infos)
		if err != nil {
			t.Fatalf("%s tool specs: %v", label, err)
		}
		return string(encoded)
	}

	// An unconfigured server is the pre-integration legacy baseline. Explicit
	// off must retain its exact ordered model-facing names and JSON schemas.
	legacy := modelToolSpecs("legacy baseline")
	h.server.ConfigureCodingSandbox(CodingSandboxConfig{Mode: CodingSandboxModeOff, ReplicaCount: 1})
	off := modelToolSpecs("explicit off")
	if off != legacy {
		t.Fatalf("off-mode model tool specs differ from legacy baseline\nlegacy: %s\noff: %s", legacy, off)
	}
	if resolverCalls != 0 || infrastructureCalls != 0 {
		t.Fatalf("off-mode calls: resolver=%d infrastructure=%d, want zero", resolverCalls, infrastructureCalls)
	}
}

func TestCodingSandboxBYOResolverIsScopedAndFailsClosed(t *testing.T) {
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-a"}
	server := &Server{
		runSandboxConfig:     CodingSandboxConfig{Mode: CodingSandboxModeBYOOnly, ReplicaCount: 1},
		runSandboxConfigured: true,
	}
	if eligibility := server.ResolveCodingSandboxEligibility(context.Background(), id, scope); eligibility.Eligible || !strings.Contains(eligibility.Reason, "not available") {
		t.Fatalf("missing resolver eligibility = %#v, want fail-closed", eligibility)
	}
	called := 0
	server.codingSandboxResolver = func(_ context.Context, gotID identity, gotScope workspace.Scope) (CodingSandboxEligibility, error) {
		called++
		if gotID.orgUUID != id.orgUUID || gotScope != scope {
			t.Fatalf("resolver scope = %#v/%#v, want %#v/%#v", gotID, gotScope, id, scope)
		}
		return CodingSandboxEligibility{
			Eligible:            true,
			Reason:              "organization binding resolved",
			ProviderExportPath:  "root:faros:tenants:org-a:providers:infrastructure",
			TransportGeneration: "hub-virtual-workspace-v2",
		}, nil
	}
	eligibility := server.ResolveCodingSandboxEligibility(context.Background(), id, scope)
	if !eligibility.Eligible || called != 1 || eligibility.ProviderExportPath == "" || eligibility.TransportGeneration == "" {
		t.Fatalf("resolved eligibility = %#v calls=%d", eligibility, called)
	}

	server.codingSandboxResolver = func(context.Context, identity, workspace.Scope) (CodingSandboxEligibility, error) {
		return CodingSandboxEligibility{Eligible: true}, nil
	}
	if incomplete := server.ResolveCodingSandboxEligibility(context.Background(), id, scope); incomplete.Eligible || !strings.Contains(incomplete.Reason, "incomplete") {
		t.Fatalf("incomplete resolver eligibility = %#v, want fail-closed", incomplete)
	}
}

func TestCodingSandboxOffAndForceDoNotCallBYOResolver(t *testing.T) {
	for _, config := range []CodingSandboxConfig{
		{Mode: CodingSandboxModeOff, ReplicaCount: 1},
		{Mode: CodingSandboxModeForce, DevelopmentMode: true, ReplicaCount: 1},
	} {
		server := &Server{runSandboxConfig: config, runSandboxConfigured: true}
		server.codingSandboxResolver = func(context.Context, identity, workspace.Scope) (CodingSandboxEligibility, error) {
			t.Fatalf("resolver called for mode %q", config.Mode)
			return CodingSandboxEligibility{}, nil
		}
		eligibility := server.ResolveCodingSandboxEligibility(context.Background(), identity{}, workspace.Scope{})
		if eligibility.Eligible != (config.Mode == CodingSandboxModeForce) {
			t.Fatalf("mode %q eligibility = %#v", config.Mode, eligibility)
		}
	}
}

func TestAttachCodingSandboxRejectsProviderTransportGenerationMismatch(t *testing.T) {
	server := &Server{
		runSandboxConfig:     CodingSandboxConfig{Mode: CodingSandboxModeForce, DevelopmentMode: true, ReplicaCount: 1},
		runSandboxConfigured: true,
	}
	req := projectAssistantRunRequest{
		Identity:       identity{orgUUID: "org", workspaceUUID: "ws"},
		Client:         newRunSandboxTestClient(),
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"}},
		WorkspaceScope: workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid"},
	}
	checkpoint := &projectAssistantSandboxCheckpoint{Metadata: projectAssistantRunSandboxMetadata{
		ProviderExportPath:  projectAssistantPlatformInfrastructureExportPath,
		TransportGeneration: "stale-generation",
	}}
	_, _, err := server.attachProjectAssistantRunSandbox(context.Background(), req, newProjectEinoAssistantRunState(), checkpoint)
	if !errors.Is(err, errProjectAssistantRunSandboxConflict) || !strings.Contains(err.Error(), "transport generation") {
		t.Fatalf("attach mismatch error = %v, want transport conflict", err)
	}
}

func TestProjectAssistantRunSandboxSetupGuardCleansOnceAndRetainsSuspendedRuns(t *testing.T) {
	var releases int
	guard := newProjectAssistantRunSandboxSetupGuard(&projectAssistantRunSandbox{
		metadata: projectAssistantRunSandboxMetadata{Status: "active"},
	}, func() { releases++ })
	guard.cleanupSetup()
	guard.cleanupSetup()
	if releases != 1 {
		t.Fatalf("setup cleanup releases = %d, want 1", releases)
	}
	if err := guard.finish(context.Background(), errors.New("late setup error"), false); err == nil {
		t.Fatal("finished setup guard swallowed the original error")
	}
	if releases != 1 {
		t.Fatalf("finished setup guard released twice: %d", releases)
	}

	var suspendedReleases int
	suspended := &projectAssistantRunSandbox{metadata: projectAssistantRunSandboxMetadata{Status: "active"}}
	suspendedGuard := newProjectAssistantRunSandboxSetupGuard(suspended, func() { suspendedReleases++ })
	permission := &projectAssistantPermissionRequiredError{RunID: "run", ToolName: "edit_file"}
	if got := suspendedGuard.finish(context.Background(), permission, true); got != permission {
		t.Fatalf("suspended finish error = %v, want original permission error", got)
	}
	suspendedGuard.cleanupSetup()
	if suspendedReleases != 0 || suspended.closed {
		t.Fatalf("permission suspension closed/released sandbox: releases=%d closed=%v", suspendedReleases, suspended.closed)
	}
}

func TestProjectAssistantDataPlaneSandboxClientUsesWorkerWorkspaceWire(t *testing.T) {
	oldDigestBytes := sha256.Sum256([]byte("old\n"))
	oldDigest := hex.EncodeToString(oldDigestBytes[:])
	newDigestBytes := sha256.Sum256([]byte("new\n"))
	newDigest := hex.EncodeToString(newDigestBytes[:])
	readRevision := uint64(8)
	readDigest := "changed"
	readFileDigest := newDigest
	var paths []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		paths = append(paths, r.URL.Path)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode %s: %v", r.URL.Path, err)
		}
		write := func(value any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(value)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/workspace/list"):
			if payload["recursive"] != true || int(payload["maxEntries"].(float64)) != workspace.MaxListLimit {
				t.Fatalf("list payload = %#v", payload)
			}
			write(map[string]any{"entries": []any{map[string]any{"path": "main.go", "type": "file", "size": 4}}, "sourceRevision": 7, "sourceDigest": "seed"})
		case strings.HasSuffix(r.URL.Path, "/workspace/seed"):
			if _, ok := payload["sourceRevision"]; ok {
				t.Fatalf("seed payload leaked caller revision: %#v", payload)
			}
			if _, ok := payload["sourceDigest"]; ok {
				t.Fatalf("seed payload leaked caller digest: %#v", payload)
			}
			files, ok := payload["files"].([]any)
			if !ok || len(files) != 1 {
				t.Fatalf("seed payload files = %#v", payload["files"])
			}
			write(map[string]any{"phase": "Synced", "sourceRevision": 10, "sourceDigest": "seeded"})
		case strings.HasSuffix(r.URL.Path, "/workspace/read"):
			write(map[string]any{"files": []any{map[string]any{"path": "main.go", "content": "new\n", "bytes": 4, "digest": readFileDigest}}, "sourceRevision": readRevision, "sourceDigest": readDigest})
		case strings.HasSuffix(r.URL.Path, "/workspace/mutate"):
			if int(payload["expectedRevision"].(float64)) != 7 || payload["expectedDigest"] != "seed" {
				t.Fatalf("mutate payload = %#v", payload)
			}
			write(map[string]any{"phase": "Mutated", "changed": []string{"main.go"}, "sourceRevision": 8, "sourceDigest": "changed"})
		case strings.HasSuffix(r.URL.Path, "/workspace/checkpoint"):
			write(map[string]any{"action": "create", "checkpoint": map[string]any{"id": "base-1", "sourceRevision": 8, "sourceDigest": "changed"}, "sourceRevision": 8, "sourceDigest": "changed"})
		case strings.HasSuffix(r.URL.Path, "/workspace/diff"):
			if payload["checkpointID"] != "base-1" || int(payload["expectedRevision"].(float64)) != 8 {
				t.Fatalf("diff payload = %#v", payload)
			}
			write(map[string]any{"baseRevision": 7, "baseDigest": "seed", "sourceRevision": 8, "sourceDigest": "changed", "changes": []any{map[string]any{"path": "main.go", "kind": "modified", "beforeDigest": oldDigest, "afterDigest": newDigest}}})
		default:
			t.Fatalf("unexpected workspace path %s", r.URL.Path)
		}
	})
	client := projectAssistantDataPlaneSandboxClient{server: &Server{
		hubBase: "http://sandbox.test", mcpInsecureSkipTLSVerify: true,
		sandboxDataPlaneClientFactory: func(time.Duration) *http.Client {
			return &http.Client{Transport: sandboxRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, r)
				return recorder.Result(), nil
			})}
		},
	}}
	id := identity{clusterID: "cluster", token: "token"}
	ref := dataPlaneRef{Resource: "instances", Name: "as-run-shop-123", Component: "workspace"}
	if response, err := client.Workspace(context.Background(), id, ref, projectAssistantSandboxWorkspaceRequest{Action: "list", Limit: workspace.MaxListLimit}); err != nil || len(response.Entries) != 1 {
		t.Fatalf("list response = %#v, err=%v", response, err)
	}
	if response, err := client.Workspace(context.Background(), id, ref, projectAssistantSandboxWorkspaceRequest{Action: "seed", Files: []projectSandboxSyncFile{{Path: "main.go", Content: "new\n"}}, SourceRevision: 1, SourceDigest: "must-not-cross"}); err != nil || response.SourceRevision != 10 || response.SourceDigest != "sha256:seeded" {
		t.Fatalf("seed response = %#v, err=%v", response, err)
	}
	if _, err := client.Workspace(context.Background(), id, ref, projectAssistantSandboxWorkspaceRequest{Action: "replace", Path: "main.go", Content: "new\n", SourceRevision: 7, SourceDigest: "seed"}); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if response, err := client.Workspace(context.Background(), id, ref, projectAssistantSandboxWorkspaceRequest{Action: "checkpoint", CheckpointAction: "create", SourceRevision: 7, SourceDigest: "seed"}); err != nil || response.CheckpointID != "base-1" {
		t.Fatalf("checkpoint create = %#v, err=%v", response, err)
	}
	response, err := client.Workspace(context.Background(), id, ref, projectAssistantSandboxWorkspaceRequest{Action: "checkpoint", CheckpointID: "base-1", SourceRevision: 8, SourceDigest: "changed"})
	if err != nil || len(response.Changes) != 1 || response.Changes[0].Content != "new\n" || response.Changes[0].ExpectedVersion != "sha256:"+oldDigest {
		t.Fatalf("checkpoint diff = %#v, err=%v", response, err)
	}
	readRevision = 9
	if _, err := client.Workspace(context.Background(), id, ref, projectAssistantSandboxWorkspaceRequest{Action: "checkpoint", CheckpointID: "base-1", SourceRevision: 8, SourceDigest: "changed"}); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("checkpoint interleaved revision error = %v, want conflict", err)
	}
	readRevision = 8
	readFileDigest = oldDigest
	if _, err := client.Workspace(context.Background(), id, ref, projectAssistantSandboxWorkspaceRequest{Action: "checkpoint", CheckpointID: "base-1", SourceRevision: 8, SourceDigest: "changed"}); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("checkpoint interleaved content error = %v, want conflict", err)
	}
	for _, suffix := range []string{"/workspace/list", "/workspace/seed", "/workspace/read", "/workspace/mutate", "/workspace/checkpoint", "/workspace/diff", "/workspace/read"} {
		found := false
		for _, got := range paths {
			if strings.HasSuffix(got, suffix) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("wire path %q was not called; paths=%v", suffix, paths)
		}
	}
}

func TestProjectAssistantDataPlaneSandboxClientGrepSupportsFileAndDirectoryPaths(t *testing.T) {
	var operations []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/workspace/list"):
			var request projectAssistantSandboxListWireRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode list request: %v", err)
			}
			operations = append(operations, "list:"+request.Path)
			switch request.Path {
			case "src/style.css":
				http.Error(w, "readdirent /workspace/src/style.css: not a directory", http.StatusBadRequest)
			case "src":
				_ = json.NewEncoder(w).Encode(projectAssistantSandboxListWireResponse{
					Entries: []projectAssistantSandboxListWireEntry{
						{Path: "src/main.js", Type: "file", Size: 12},
						{Path: "src/style.css", Type: "file", Size: 38},
					},
					SourceRevision: 12,
					SourceDigest:   "project",
				})
			default:
				http.Error(w, "no such file or directory", http.StatusNotFound)
			}
		case strings.HasSuffix(r.URL.Path, "/workspace/read"):
			var request projectAssistantSandboxReadWireRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode read request: %v", err)
			}
			operations = append(operations, "read:"+strings.Join(request.Paths, ","))
			if len(request.Paths) != 1 || request.Paths[0] != "src/style.css" {
				http.Error(w, "file not found", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(projectAssistantSandboxReadWireResponse{
				Files: []projectAssistantSandboxReadWireFile{{
					Path: "src/style.css", Content: ".settings-card {\n}\n.settings-card {\n}\n", Bytes: 38, Digest: "style",
				}},
				SourceRevision: 12,
				SourceDigest:   "project",
			})
		default:
			t.Fatalf("unexpected workspace path %s", r.URL.Path)
		}
	})
	client := projectAssistantDataPlaneSandboxClient{server: &Server{
		hubBase: "http://sandbox.test", mcpInsecureSkipTLSVerify: true,
		sandboxDataPlaneClientFactory: func(time.Duration) *http.Client {
			return &http.Client{Transport: sandboxRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, r)
				return recorder.Result(), nil
			})}
		},
	}}
	id := identity{clusterID: "cluster", token: "token"}
	ref := dataPlaneRef{Resource: "instances", Name: "as-run-shop-123", Component: "workspace"}

	for _, searchPath := range []string{"src/style.css", "src"} {
		response, err := client.Workspace(context.Background(), id, ref, projectAssistantSandboxWorkspaceRequest{
			Action: "grep", Path: searchPath, GrepPattern: `\.settings-card \{`, Glob: "*.css", FileType: "css",
		})
		if err != nil {
			t.Fatalf("grep path %q: %v", searchPath, err)
		}
		if response.SourceRevision != 12 || response.SourceDigest != "sha256:project" || len(response.Matches) != 2 {
			t.Fatalf("grep path %q response = %#v", searchPath, response)
		}
		if response.Matches[0].Path != "src/style.css" || response.Matches[0].Line != 1 || response.Matches[1].Line != 3 {
			t.Fatalf("grep path %q matches = %#v", searchPath, response.Matches)
		}
	}

	_, err := client.Workspace(context.Background(), id, ref, projectAssistantSandboxWorkspaceRequest{
		Action: "grep", Path: "src/missing.css", GrepPattern: "settings", Glob: "*.css",
	})
	if err == nil || !strings.Contains(err.Error(), "workspace list endpoint returned 404") {
		t.Fatalf("missing file grep error = %v", err)
	}
	if got, want := strings.Join(operations, ";"), "list:src/style.css;read:src/style.css;list:src;read:src/style.css;list:src/missing.css;read:src/missing.css"; got != want {
		t.Fatalf("grep operations = %q, want %q", got, want)
	}
}

func TestProjectAssistantDataPlaneSandboxCheckpointWithNoChangesUsesDiffFence(t *testing.T) {
	var paths []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if !strings.HasSuffix(r.URL.Path, "/workspace/diff") {
			t.Fatalf("unexpected workspace path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"baseRevision": 4, "baseDigest": "seed", "sourceRevision": 4, "sourceDigest": "seed", "changes": []any{},
		})
	})
	client := projectAssistantDataPlaneSandboxClient{server: &Server{
		hubBase: "http://sandbox.test", mcpInsecureSkipTLSVerify: true,
		sandboxDataPlaneClientFactory: func(time.Duration) *http.Client {
			return &http.Client{Transport: sandboxRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, r)
				return recorder.Result(), nil
			})}
		},
	}}
	response, err := client.Workspace(context.Background(), identity{clusterID: "cluster", token: "token"}, dataPlaneRef{Resource: "instances", Name: "cache", Component: "workspace"}, projectAssistantSandboxWorkspaceRequest{
		Action: "checkpoint", CheckpointID: "base-1", SourceRevision: 4, SourceDigest: "seed",
	})
	if err != nil {
		t.Fatalf("checkpoint diff without changes: %v", err)
	}
	if len(response.Changes) != 0 || response.SourceRevision != 4 || response.SourceDigest != "sha256:seed" {
		t.Fatalf("checkpoint diff = %#v", response)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/workspace/diff") {
		t.Fatalf("wire paths = %v, want diff only", paths)
	}
}

type sandboxRoundTripFunc func(*http.Request) (*http.Response, error)

func (f sandboxRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
