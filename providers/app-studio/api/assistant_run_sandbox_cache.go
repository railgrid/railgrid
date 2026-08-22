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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/workspace"
)

var (
	errProjectAssistantRunSandboxConflict = errors.New("assistant run sandbox workspace conflict")
	errProjectAssistantRunSandboxClosed   = errors.New("assistant run sandbox is closed")
)

var runSandboxInstancesResource = tenant.Resource{
	GVR:  schema.GroupVersionResource{Group: "infrastructure.faros.sh", Version: "v1alpha1", Resource: projectAssistantRunSandboxResource},
	Kind: projectAssistantRunSandboxKind,
}

type projectAssistantSandboxManager struct {
	mu     sync.Mutex
	active map[string]map[string]projectAssistantSandboxLease
}

type projectAssistantSandboxLease struct {
	runID     string
	expiresAt time.Time
}

func newProjectAssistantSandboxManager() *projectAssistantSandboxManager {
	return &projectAssistantSandboxManager{active: map[string]map[string]projectAssistantSandboxLease{}}
}

// acquire serializes use of a cached project environment inside App Studio's
// enforced single-writer deployment. The Instance annotations are a durable
// recovery/eviction fence, but the production GraphQL apply path is not a
// compare-and-swap primitive; multi-replica cache ownership must move to the
// provider's durable execution-claim store before the deployment is scaled.
func (m *projectAssistantSandboxManager) acquire(tenantKey, cacheKey, runID string) (func(), error) {
	return m.acquireUntil(tenantKey, cacheKey, runID, time.Now().UTC().Add(projectAssistantRunSandboxHardTTL))
}

func (m *projectAssistantSandboxManager) acquireUntil(tenantKey, cacheKey, runID string, expiresAt time.Time) (func(), error) {
	if m == nil {
		return func() {}, nil
	}
	tenantKey = strings.TrimSpace(tenantKey)
	cacheKey = strings.TrimSpace(cacheKey)
	runID = strings.TrimSpace(runID)
	if tenantKey == "" || cacheKey == "" || runID == "" {
		return nil, errors.New("assistant run sandbox tenant, cache, and run IDs are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		m.active = map[string]map[string]projectAssistantSandboxLease{}
	}
	m.pruneExpiredLocked(time.Now().UTC())
	owned := m.active[tenantKey]
	if owned == nil {
		owned = map[string]projectAssistantSandboxLease{}
		m.active[tenantKey] = owned
	}
	if owner := owned[cacheKey]; owner.runID != "" && owner.runID != runID {
		return nil, fmt.Errorf("project coding environment is already claimed by run %q", owner.runID)
	}
	if _, ok := owned[cacheKey]; !ok && len(owned) >= projectAssistantRunSandboxMaxActive {
		return nil, fmt.Errorf("tenant already has %d active assistant run sandboxes", projectAssistantRunSandboxMaxActive)
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(projectAssistantRunSandboxHardTTL)
	}
	owned[cacheKey] = projectAssistantSandboxLease{runID: runID, expiresAt: expiresAt}
	released := false
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if released {
			return
		}
		released = true
		owned := m.active[tenantKey]
		if lease := owned[cacheKey]; lease.runID == runID {
			delete(owned, cacheKey)
		}
		if len(owned) == 0 {
			delete(m.active, tenantKey)
		}
	}, nil
}

func (m *projectAssistantSandboxManager) setExpiry(tenantKey, cacheKey, runID string, expiresAt time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	owned := m.active[strings.TrimSpace(tenantKey)]
	if lease := owned[strings.TrimSpace(cacheKey)]; lease.runID == strings.TrimSpace(runID) {
		lease.expiresAt = expiresAt
		owned[strings.TrimSpace(cacheKey)] = lease
	}
}

func (m *projectAssistantSandboxManager) releaseExact(tenantKey, cacheKey, runID string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tenantKey = strings.TrimSpace(tenantKey)
	cacheKey = strings.TrimSpace(cacheKey)
	runID = strings.TrimSpace(runID)
	owned := m.active[tenantKey]
	lease, ok := owned[cacheKey]
	if !ok || lease.runID != runID {
		return false
	}
	delete(owned, cacheKey)
	if len(owned) == 0 {
		delete(m.active, tenantKey)
	}
	return true
}

func (m *projectAssistantSandboxManager) releaseRun(tenantKey, runID string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tenantKey = strings.TrimSpace(tenantKey)
	runID = strings.TrimSpace(runID)
	owned := m.active[tenantKey]
	released := false
	for cacheKey, lease := range owned {
		if lease.runID == runID {
			delete(owned, cacheKey)
			released = true
		}
	}
	if len(owned) == 0 {
		delete(m.active, tenantKey)
	}
	return released
}

func (m *projectAssistantSandboxManager) pruneExpiredLocked(now time.Time) {
	for tenantKey, owned := range m.active {
		for cacheKey, lease := range owned {
			if !lease.expiresAt.IsZero() && !now.Before(lease.expiresAt) {
				delete(owned, cacheKey)
			}
		}
		if len(owned) == 0 {
			delete(m.active, tenantKey)
		}
	}
}

func (m *projectAssistantSandboxManager) claimed(cacheKey string) bool {
	if m == nil || strings.TrimSpace(cacheKey) == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredLocked(time.Now().UTC())
	for _, owned := range m.active {
		if owned[cacheKey].runID != "" {
			return true
		}
	}
	return false
}

func projectAssistantRunSandboxTenantKey(id identity, scope workspace.Scope) string {
	org := strings.TrimSpace(id.orgUUID)
	if org == "" {
		org = strings.TrimSpace(scope.OrgUUID)
	}
	ws := strings.TrimSpace(id.workspaceUUID)
	if ws == "" {
		ws = strings.TrimSpace(scope.WorkspaceUUID)
	}
	return org + "/" + ws
}

func projectAssistantRunSandboxName(scope workspace.Scope, project *aiv1alpha1.Project, runID string) string {
	// runID is intentionally ignored. The Instance is a project-scoped cache;
	// run ownership is a durable annotation claim, while this name keeps every
	// new chat/follow-up on the same workspace volume for up to the hard TTL.
	projectName := strings.TrimSpace(scope.ProjectName)
	projectUID := strings.TrimSpace(scope.ProjectUID)
	if project != nil {
		if projectName == "" {
			projectName = strings.TrimSpace(project.Name)
		}
		if projectUID == "" {
			projectUID = string(project.UID)
		}
	}
	material := strings.Join([]string{scope.OrgUUID, scope.WorkspaceUUID, projectName, projectUID}, "\x00")
	sum := sha256.Sum256([]byte(material))
	base := dnsSafeSandboxName(projectName)
	name := projectAssistantRunSandboxNamePrefix + base + "-" + hex.EncodeToString(sum[:projectAssistantRunSandboxHashBytes])
	// dnsSafeSandboxName already applies the downstream suffix budget. Keep a
	// defensive bound here so future edits to the name components cannot
	// silently reintroduce an invalid Instance name.
	if len(name) > projectAssistantRunSandboxNameMaxLength {
		name = name[:projectAssistantRunSandboxNameMaxLength]
		name = strings.TrimRight(name, "-")
	}
	return name
}

// ensureProjectAssistantRunSandboxOwner makes the cached coding environment a
// real child of its Project. Terminal turns deliberately retain this Instance,
// so run-lifecycle cleanup is no longer sufficient; the owner reference is the
// durable deletion contract for API, kubectl, and controller-driven Project
// deletion alike.
func ensureProjectAssistantRunSandboxOwner(instance *unstructured.Unstructured, project *aiv1alpha1.Project) (bool, error) {
	if instance == nil {
		return false, errors.New("run sandbox instance is nil")
	}
	owner := bindings.OwnerRef(project)
	if owner == nil {
		return false, errors.New("run sandbox Project owner identity is incomplete")
	}
	refs := instance.GetOwnerReferences()
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller && ref.UID != owner.UID {
			return false, fmt.Errorf("%w: run sandbox instance already has another controller owner", errProjectAssistantRunSandboxConflict)
		}
		if ref.APIVersion == owner.APIVersion && ref.Kind == owner.Kind {
			if ref.Name != owner.Name || ref.UID != owner.UID {
				return false, fmt.Errorf("%w: run sandbox instance belongs to another Project", errProjectAssistantRunSandboxConflict)
			}
			return false, nil
		}
	}
	instance.SetOwnerReferences(append(refs, *owner))
	return true, nil
}

func dnsSafeSandboxName(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	value := strings.Trim(b.String(), "-")
	if value == "" {
		return "project"
	}
	if len(value) > projectAssistantRunSandboxNameMaxBase {
		value = strings.TrimRight(value[:projectAssistantRunSandboxNameMaxBase], "-")
	}
	return value
}

func projectAssistantRunSandboxAnnotationTime(instance *unstructured.Unstructured, key string) (time.Time, bool) {
	if instance == nil {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(instance.GetAnnotations()[key])
	if raw == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// projectAssistantRunSandboxInstanceClaimed treats a claim without a valid
// expiry as held. A malformed/missing expiry must fail closed: evicting such
// an Instance could terminate a suspended run owned by another replica.
func projectAssistantRunSandboxInstanceClaimed(instance *unstructured.Unstructured, now time.Time) bool {
	if instance == nil {
		return false
	}
	owner := strings.TrimSpace(instance.GetAnnotations()[projectAssistantRunSandboxClaimOwner])
	if owner == "" {
		return false
	}
	expiresAt, ok := projectAssistantRunSandboxAnnotationTime(instance, projectAssistantRunSandboxClaimExpiry)
	return !ok || now.Before(expiresAt)
}

func projectAssistantRunSandboxInstanceLastActivity(instance *unstructured.Unstructured) time.Time {
	if last, ok := projectAssistantRunSandboxAnnotationTime(instance, projectAssistantRunSandboxLastActivity); ok {
		return last
	}
	if instance != nil {
		created := instance.GetCreationTimestamp()
		if !created.IsZero() {
			return created.Time
		}
	}
	return time.Time{}
}

func minProjectAssistantRunSandboxExpiry(left, right time.Time) time.Time {
	if left.IsZero() {
		return right
	}
	if right.IsZero() || left.Before(right) {
		return left
	}
	return right
}

func projectAssistantRunSandboxInstanceCached(instance *unstructured.Unstructured, now time.Time) bool {
	if instance == nil || instance.GetDeletionTimestamp() != nil || projectAssistantRunSandboxInstanceExpired(instance, now) {
		return false
	}
	if projectAssistantRunSandboxInstanceClaimed(instance, now) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(instance.GetAnnotations()[projectAssistantRunSandboxCacheState]), projectAssistantRunSandboxCacheStateCached)
}

// claimProjectAssistantRunSandboxInstance persists the active run identity for
// crash recovery, suspended-run reattachment, and safe quota eviction. The
// in-process sandbox manager provides exclusivity in the currently enforced
// single-writer deployment; GraphQL applyYaml is create-or-update rather than
// a compare-and-swap operation, so these annotations must not be described as
// a distributed lock.
func (s *Server) claimProjectAssistantRunSandboxInstance(ctx context.Context, c *asclient.Client, scope store.Scope, name, runID string) (time.Time, error) {
	if c == nil {
		return time.Time{}, errors.New("project client is not configured")
	}
	name = strings.TrimSpace(name)
	runID = strings.TrimSpace(runID)
	if name == "" || runID == "" {
		return time.Time{}, errors.New("run sandbox instance name and run ID are required")
	}
	resource := c.Resource(runSandboxInstancesResource, "")
	for attempt := 0; attempt < 3; attempt++ {
		instance, err := resource.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return time.Time{}, fmt.Errorf("get run sandbox instance %q for claim: %w", name, err)
		}
		now := time.Now().UTC()
		annotations := instance.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		} else {
			copy := make(map[string]string, len(annotations)+6)
			for key, value := range annotations {
				copy[key] = value
			}
			annotations = copy
		}
		if strings.EqualFold(strings.TrimSpace(annotations[projectAssistantRunSandboxCacheState]), projectAssistantRunSandboxCacheStateEvicting) {
			return time.Time{}, fmt.Errorf("%w: run sandbox instance %q is being evicted", errProjectAssistantRunSandboxConflict, name)
		}
		owner := strings.TrimSpace(annotations[projectAssistantRunSandboxClaimOwner])
		claimExpiry, claimExpiryOK := projectAssistantRunSandboxAnnotationTime(instance, projectAssistantRunSandboxClaimExpiry)
		if owner != "" && owner != runID && (!claimExpiryOK || now.Before(claimExpiry)) {
			reclaimable, reclaimErr := s.projectAssistantRunSandboxClaimOwnerReclaimable(ctx, scope, owner)
			if reclaimErr != nil {
				return time.Time{}, fmt.Errorf("verify run sandbox instance %q claim owner %q: %w", name, owner, reclaimErr)
			}
			if !reclaimable {
				return time.Time{}, fmt.Errorf("%w: run sandbox instance %q is claimed by another run", errProjectAssistantRunSandboxConflict, name)
			}
		}
		hardExpiry, hardExpiryOK := projectAssistantRunSandboxAnnotationTime(instance, projectAssistantRunSandboxHardExpiry)
		if !hardExpiryOK {
			hardExpiry = now.Add(projectAssistantRunSandboxHardTTL)
		}
		if !now.Before(hardExpiry) {
			return time.Time{}, fmt.Errorf("%w: sandbox hard lifetime has expired", errProjectAssistantRunSandboxConflict)
		}
		idleExpiry := now.Add(projectAssistantRunSandboxIdleTTL)
		if idleExpiry.After(hardExpiry) {
			idleExpiry = hardExpiry
		}
		annotations[projectAssistantRunSandboxClaimOwner] = runID
		annotations[projectAssistantRunSandboxClaimExpiry] = hardExpiry.Format(time.RFC3339Nano)
		annotations[projectAssistantRunSandboxCacheGeneration] = runID
		annotations[projectAssistantRunSandboxCacheState] = projectAssistantRunSandboxCacheStateActive
		annotations[projectAssistantRunSandboxLastActivity] = now.Format(time.RFC3339Nano)
		annotations[projectAssistantRunSandboxIdleExpiry] = idleExpiry.Format(time.RFC3339Nano)
		if !hardExpiryOK {
			annotations[projectAssistantRunSandboxHardExpiry] = hardExpiry.Format(time.RFC3339Nano)
		}
		instance.SetAnnotations(annotations)
		updated, err := resource.Update(ctx, instance, metav1.UpdateOptions{})
		if err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return time.Time{}, fmt.Errorf("claim run sandbox instance %q: %w", name, err)
		}
		if strings.TrimSpace(updated.GetAnnotations()[projectAssistantRunSandboxClaimOwner]) != runID {
			return time.Time{}, fmt.Errorf("%w: run sandbox instance %q did not retain the requested claim", errProjectAssistantRunSandboxConflict, name)
		}
		return hardExpiry, nil
	}
	return time.Time{}, fmt.Errorf("%w: run sandbox instance %q claim raced with another writer", errProjectAssistantRunSandboxConflict, name)
}

// projectAssistantRunSandboxClaimOwnerReclaimable distinguishes an orphaned
// durable annotation from a live or suspended owner. A coordinator can exit
// after terminalizing a run but before it clears the Instance claim. On the
// next process, the in-memory lease map is empty, so the durable run row is the
// authoritative recovery fence: terminal (or already-retained-away) owners no
// longer have execution authority, while every non-terminal owner remains
// protected for resume.
func (s *Server) projectAssistantRunSandboxClaimOwnerReclaimable(ctx context.Context, scope store.Scope, owner string) (bool, error) {
	if s == nil || s.store == nil || strings.TrimSpace(owner) == "" {
		return false, nil
	}
	run, err := s.store.GetAssistantRun(ctx, scope, owner)
	if err == nil {
		return assistantRunTerminal(run.Status), nil
	}
	if errors.Is(err, store.ErrAssistantRunNotFound) {
		return true, nil
	}
	return false, err
}
func (s *Server) ensureProjectAssistantRunSandboxInstance(ctx context.Context, c *asclient.Client, project *aiv1alpha1.Project, name, template string) (bool, error) {
	if c == nil {
		return false, errors.New("project client is not configured")
	}
	obj, err := c.Resource(runSandboxInstancesResource, "").Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if obj.GetAnnotations()["faros.sh/app-studio-run-sandbox"] != "true" {
			return false, fmt.Errorf("run sandbox instance %q is not App Studio-owned", name)
		}
		observed, _, _ := unstructured.NestedString(obj.Object, "spec", "template")
		if strings.TrimSpace(observed) != template {
			return false, fmt.Errorf("run sandbox instance %q uses template %q, want %q", name, observed, template)
		}
		if projectAssistantRunSandboxInstanceExpired(obj, time.Now().UTC()) {
			if projectAssistantRunSandboxInstanceClaimed(obj, time.Now().UTC()) {
				return false, fmt.Errorf("run sandbox instance %q is expired but still claimed", name)
			}
			if deleteErr := c.Resource(runSandboxInstancesResource, "").Delete(ctx, name, metav1.DeleteOptions{}); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
				return false, fmt.Errorf("delete expired run sandbox instance %q: %w", name, deleteErr)
			}
			if err := waitForProjectAssistantRunSandboxInstanceDeleted(ctx, c, name); err != nil {
				return false, err
			}
			return s.ensureProjectAssistantRunSandboxInstance(ctx, c, project, name, template)
		}
		ownerChanged, ownerErr := ensureProjectAssistantRunSandboxOwner(obj, project)
		if ownerErr != nil {
			return false, ownerErr
		}
		if ownerChanged {
			if _, updateErr := c.Resource(runSandboxInstancesResource, "").Update(ctx, obj, metav1.UpdateOptions{}); updateErr != nil {
				return false, fmt.Errorf("attach Project owner to run sandbox instance %q: %w", name, updateErr)
			}
		}
		return false, nil
	}
	if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("get run sandbox instance %q: %w", name, err)
	}
	now := time.Now().UTC()
	labels := map[string]string{projectAssistantRunSandboxLabel: "true"}
	if project != nil {
		labels["faros.sh/project"] = dnsSafeSandboxName(project.Name)
	}
	obj = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": projectAssistantRunSandboxAPIVersion,
		"kind":       projectAssistantRunSandboxKind,
		"metadata": map[string]any{
			"name":   name,
			"labels": labels,
			"annotations": map[string]any{
				projectAssistantRunSandboxLabel:        "true",
				"faros.sh/app-studio-run-sandbox-idle": projectAssistantRunSandboxIdleTTL.String(),
				"faros.sh/app-studio-run-sandbox-hard": projectAssistantRunSandboxHardTTL.String(),
				projectAssistantRunSandboxIdleExpiry:   now.Add(projectAssistantRunSandboxIdleTTL).Format(time.RFC3339Nano),
				projectAssistantRunSandboxHardExpiry:   now.Add(projectAssistantRunSandboxHardTTL).Format(time.RFC3339Nano),
				projectAssistantRunSandboxCacheState:   projectAssistantRunSandboxCacheStateNew,
				projectAssistantRunSandboxLastActivity: now.Format(time.RFC3339Nano),
			},
		},
		"spec": map[string]any{
			"template": template,
			"values": map[string]any{
				"name":      name,
				"farosMode": "development",
			},
		},
	}}
	if _, err := ensureProjectAssistantRunSandboxOwner(obj, project); err != nil {
		return false, err
	}
	if _, err := c.Resource(runSandboxInstancesResource, "").Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		return false, fmt.Errorf("create run sandbox instance %q: %w", name, err)
	}
	return true, nil
}

// deleteProjectAssistantRunSandboxCache removes the project-scoped coding
// environment before deleting the Project. The exact deterministic name also
// covers caches created before owner references were introduced.
func (s *Server) deleteProjectAssistantRunSandboxCache(ctx context.Context, c *asclient.Client, id identity, project *aiv1alpha1.Project) error {
	if c == nil || project == nil {
		return nil
	}
	name := projectAssistantRunSandboxName(projectWorkspaceScope(id, project), project, "")
	resource := c.Resource(runSandboxInstancesResource, "")
	instance, err := resource.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get project coding environment %q: %w", name, err)
	}
	if instance.GetAnnotations()[projectAssistantRunSandboxLabel] != "true" {
		return fmt.Errorf("%w: instance %q is not an App Studio coding environment", errProjectAssistantRunSandboxConflict, name)
	}
	if err := resource.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete project coding environment %q: %w", name, err)
	}
	return nil
}

func waitForProjectAssistantRunSandboxInstanceDeleted(ctx context.Context, c *asclient.Client, name string) error {
	if c == nil {
		return errors.New("project client is not configured")
	}
	waitCtx, cancel := context.WithTimeout(ctx, projectAssistantRunSandboxReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(projectAssistantRunSandboxReadyPoll)
	defer ticker.Stop()
	resource := c.Resource(runSandboxInstancesResource, "")
	for {
		_, err := resource.Get(waitCtx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get deleting run sandbox instance %q: %w", name, err)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for expired run sandbox instance %q deletion: %w", name, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Server) enforceProjectAssistantRunSandboxQuota(ctx context.Context, c *asclient.Client, currentName string) error {
	if c == nil {
		return errors.New("project client is not configured")
	}
	list, err := c.ListInfrastructureInstances(ctx, metav1.ListOptions{LabelSelector: projectAssistantRunSandboxLabel + "=true"})
	if err != nil {
		return fmt.Errorf("list tenant run sandboxes for quota: %w", err)
	}
	if list == nil {
		return nil
	}
	now := time.Now().UTC()
	active := countProjectAssistantRunSandboxInstances(list, currentName, now)
	if active < projectAssistantRunSandboxMaxActive {
		return nil
	}
	// Successful terminals retain an unclaimed project cache. Make room for a
	// new project by evicting the least-recently-used cached Instance, but only
	// after a visible state transition to "evicting". The single-writer manager
	// excludes locally claimed caches; durable annotations exclude a suspended
	// cache recovered after coordinator restart.
	candidates := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		instance := &list.Items[i]
		if instance.GetName() == strings.TrimSpace(currentName) || s.projectAssistantSandboxManager().claimed(instance.GetName()) || !projectAssistantRunSandboxInstanceCached(instance, now) {
			continue
		}
		candidates = append(candidates, instance)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return projectAssistantRunSandboxInstanceLastActivity(candidates[i]).Before(projectAssistantRunSandboxInstanceLastActivity(candidates[j]))
	})
	var lastErr error
	for _, candidate := range candidates {
		evicted, err := s.evictProjectAssistantRunSandboxCache(ctx, c, candidate.GetName(), now)
		if err != nil {
			lastErr = err
			continue
		}
		if evicted {
			active--
			if active < projectAssistantRunSandboxMaxActive {
				return nil
			}
		}
	}
	if lastErr != nil {
		return fmt.Errorf("tenant already has %d active assistant run sandboxes; cache eviction failed: %w", active, lastErr)
	}
	return fmt.Errorf("tenant already has %d active assistant run sandboxes; no unclaimed cached sandbox is available for eviction", active)
}

func (s *Server) evictProjectAssistantRunSandboxCache(ctx context.Context, c *asclient.Client, name string, now time.Time) (bool, error) {
	if c == nil {
		return false, errors.New("project client is not configured")
	}
	resource := c.Resource(runSandboxInstancesResource, "")
	if s.projectAssistantSandboxManager().claimed(name) {
		return false, nil
	}
	instance, err := resource.Get(ctx, strings.TrimSpace(name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get cached run sandbox %q for eviction: %w", name, err)
	}
	if !projectAssistantRunSandboxInstanceCached(instance, now) {
		return false, nil
	}
	annotations := instance.GetAnnotations()
	copy := make(map[string]string, len(annotations)+1)
	for key, value := range annotations {
		copy[key] = value
	}
	copy[projectAssistantRunSandboxCacheState] = projectAssistantRunSandboxCacheStateEvicting
	instance.SetAnnotations(copy)
	_, err = resource.Update(ctx, instance, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mark cached run sandbox %q for eviction: %w", name, err)
	}
	if s.projectAssistantSandboxManager().claimed(name) {
		return false, nil
	}
	if err := resource.Delete(ctx, strings.TrimSpace(name), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("delete cached run sandbox %q: %w", name, err)
	}
	return true, nil
}

func countProjectAssistantRunSandboxInstances(list *unstructured.UnstructuredList, currentName string, now time.Time) int {
	if list == nil {
		return 0
	}
	active := 0
	for i := range list.Items {
		instance := &list.Items[i]
		if instance.GetName() == strings.TrimSpace(currentName) || instance.GetDeletionTimestamp() != nil || projectAssistantRunSandboxInstanceExpired(instance, now) {
			continue
		}
		active++
	}
	return active
}

func projectAssistantRunSandboxInstanceExpired(instance *unstructured.Unstructured, now time.Time) bool {
	if instance == nil {
		return true
	}
	annotations := instance.GetAnnotations()
	for _, key := range []string{projectAssistantRunSandboxIdleExpiry, projectAssistantRunSandboxHardExpiry} {
		if raw := strings.TrimSpace(annotations[key]); raw != "" {
			expiresAt, err := time.Parse(time.RFC3339Nano, raw)
			if err == nil && !now.Before(expiresAt) {
				return true
			}
		}
	}
	status, _, _ := unstructured.NestedString(instance.Object, "status", "status")
	if strings.EqualFold(strings.TrimSpace(status), "expired") {
		return true
	}
	phase, _, _ := unstructured.NestedString(instance.Object, "status", "phase")
	return strings.EqualFold(strings.TrimSpace(phase), "expired")
}
