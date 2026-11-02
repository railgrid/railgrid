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

package softdelete

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/pkg/hub/kcp"
)

// WorkspaceWatcher is the half of Provisioner that lets the Workspace branch
// run on watch events: one watch per organization on its child Workspaces,
// instead of listing every organization's children on a timer. The
// Bootstrapper implements it; a provisioner that cannot (tests) keeps the
// sweep.
type WorkspaceWatcher interface {
	// WatchChildWorkspaces opens a watch on the child Workspaces of
	// root:railgrid:tenants:{orgUUID}. The watch replays every existing
	// Workspace as ADDED when it opens, which is how a restart catches up.
	WatchChildWorkspaces(ctx context.Context, orgUUID string) (watch.Interface, error)
}

// childWorkspaceKey identifies one child Workspace of one organization.
type childWorkspaceKey struct{ org, ws string }

// childWorkspaceState is what the branch needs from a Workspace: the two
// soft-delete annotations, taken from the watch event so no GET follows.
type childWorkspaceState struct {
	requestedAt *time.Time
	forced      bool
}

// workspaceWatch drives the Workspace branch from watches. Every event
// enqueues the Workspace; a worker runs the same undelete / within-grace /
// cascade decision the sweep made, and a Workspace inside its grace period
// is re-queued for the moment the grace ends instead of being re-read every
// minute until then.
type workspaceWatch struct {
	r       *Reconciler
	watcher WorkspaceWatcher
	queue   workqueue.TypedRateLimitingInterface[childWorkspaceKey]

	mu   sync.Mutex
	orgs map[string]context.CancelFunc
	seen map[childWorkspaceKey]childWorkspaceState
}

const (
	workspaceWatchWorkers = 2
	workspaceWatchBackoff = 30 * time.Second
)

func newWorkspaceWatch(r *Reconciler, w WorkspaceWatcher) *workspaceWatch {
	return &workspaceWatch{
		r:       r,
		watcher: w,
		queue:   workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[childWorkspaceKey]()),
		orgs:    map[string]context.CancelFunc{},
		seen:    map[childWorkspaceKey]childWorkspaceState{},
	}
}

// Run is the manager Runnable: seeds one watch per known organization (the
// Organization reconciler adds and removes the rest), runs the workers, and
// returns when ctx is cancelled.
func (w *workspaceWatch) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName(workspaceControllerName)
	logger.Info("Starting soft-delete workspace watch")
	defer w.queue.ShutDown()

	if orgs, err := w.r.provisioner.ListOrgWorkspaces(ctx); err != nil {
		logger.Error(err, "Listing organizations to seed workspace watches failed; organizations will be picked up as they reconcile")
	} else {
		for _, org := range orgs {
			w.ensureOrg(ctx, org)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < workspaceWatchWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w.processNext(ctx) {
			}
		}()
	}
	<-ctx.Done()
	w.mu.Lock()
	for _, cancel := range w.orgs {
		cancel()
	}
	w.mu.Unlock()
	w.queue.ShutDown()
	wg.Wait()
	return nil
}

// ensureOrg opens the organization's Workspace watch if it is not open.
func (w *workspaceWatch) ensureOrg(ctx context.Context, orgUUID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.orgs[orgUUID]; ok {
		return
	}
	orgCtx, cancel := context.WithCancel(ctx)
	w.orgs[orgUUID] = cancel
	go w.watchOrg(orgCtx, orgUUID)
}

// forgetOrg closes the organization's watch and drops its Workspaces. The
// cascade of a deleted organization is the Organization branch's job.
func (w *workspaceWatch) forgetOrg(orgUUID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cancel, ok := w.orgs[orgUUID]; ok {
		cancel()
		delete(w.orgs, orgUUID)
	}
	for k := range w.seen {
		if k.org == orgUUID {
			delete(w.seen, k)
		}
	}
}

func (w *workspaceWatch) watchOrg(ctx context.Context, orgUUID string) {
	logger := klog.FromContext(ctx).WithName(workspaceControllerName).WithValues("org", orgUUID)
	delay := time.Second
	for {
		wi, err := w.watcher.WatchChildWorkspaces(ctx, orgUUID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.V(1).Info("Workspace watch unavailable; retrying", "retryIn", delay.String(), "error", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay *= 2; delay > workspaceWatchBackoff {
				delay = workspaceWatchBackoff
			}
			continue
		}
		delay = time.Second
		for ev := range wi.ResultChan() {
			w.observe(ctx, orgUUID, ev, logger)
		}
		wi.Stop()
		if ctx.Err() != nil {
			return
		}
	}
}

// observe folds one watch event into the seen set and queues the Workspace.
func (w *workspaceWatch) observe(ctx context.Context, orgUUID string, ev watch.Event, logger klog.Logger) {
	switch ev.Type {
	case watch.Bookmark:
		return
	case watch.Error:
		logger.V(1).Info("Workspace watch returned an error event; reopening")
		return
	}
	obj, ok := ev.Object.(*unstructured.Unstructured)
	if !ok {
		return
	}
	key := childWorkspaceKey{org: orgUUID, ws: obj.GetName()}
	if ev.Type == watch.Deleted {
		w.mu.Lock()
		delete(w.seen, key)
		w.mu.Unlock()
		return
	}
	state, err := childWorkspaceStateOf(obj)
	if err != nil {
		logger.Error(err, "Reading soft-delete annotations failed; the Workspace is skipped until it changes", "workspace", key.ws)
		return
	}
	w.mu.Lock()
	w.seen[key] = state
	w.mu.Unlock()
	w.queue.Add(key)
}

// childWorkspaceStateOf reads the two soft-delete annotations the sweep used
// to GET: the request timestamp (RFC3339) and the forced marker.
func childWorkspaceStateOf(obj *unstructured.Unstructured) (childWorkspaceState, error) {
	annos := obj.GetAnnotations()
	st := childWorkspaceState{forced: annos[kcp.WorkspaceDeletionForcedAnnotation] == "true"}
	if raw := annos[kcp.WorkspaceDeletionAnnotation]; raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return st, err
		}
		st.requestedAt = &t
	}
	return st, nil
}

func (w *workspaceWatch) processNext(ctx context.Context) bool {
	key, shutdown := w.queue.Get()
	if shutdown {
		return false
	}
	defer w.queue.Done(key)

	w.mu.Lock()
	state, known := w.seen[key]
	w.mu.Unlock()
	if !known {
		w.queue.Forget(key) // deleted since it was queued
		return true
	}
	logger := klog.FromContext(ctx).WithName(workspaceControllerName).WithValues("org", key.org, "workspace", key.ws)
	again, err := w.r.reconcileChildWorkspace(ctx, key, state)
	switch {
	case err != nil:
		logger.Error(err, "Workspace soft-delete reconcile failed; retrying with backoff")
		w.queue.AddRateLimited(key)
	case again > 0:
		w.queue.Forget(key)
		w.queue.AddAfter(key, again)
	default:
		w.queue.Forget(key)
	}
	return true
}

// reconcileChildWorkspace is the sweep's per-workspace decision, fed by a
// watch event instead of two GETs. It returns how long to wait before
// looking again: the remaining grace period, or zero when nothing is
// pending.
func (r *Reconciler) reconcileChildWorkspace(ctx context.Context, key childWorkspaceKey, st childWorkspaceState) (time.Duration, error) {
	state := &workspaceCascadeState{}
	if st.requestedAt == nil {
		// Never soft-deleted, or undeleted: idempotently clear UMI markers.
		return 0, r.handleWorkspaceUndelete(ctx, state, key.org, key.ws)
	}
	if !st.forced {
		if expiry := r.gracePeriodFor(*st.requestedAt); r.now().Before(expiry) {
			if err := r.handleWorkspaceWithinGrace(ctx, state, key.org, key.ws, *st.requestedAt); err != nil {
				return 0, err
			}
			return expiry.Sub(r.now()), nil
		}
	}
	return 0, r.handleWorkspaceCascade(ctx, state, key.org, key.ws)
}
