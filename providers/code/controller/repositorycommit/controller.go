/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package repositorycommit reconciles RepositoryCommit requests by materializing
// provider-owned source bundles into git commits on the referenced Repository.
package repositorycommit

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/source"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/commitbundle"
	"github.com/railgrid/provider-code/controller/shared"
)

// Reconciler applies RepositoryCommit requests to the git host.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
	Bundles  commitbundle.Store

	waiters bundleWaiters
}

// bundleArrivalTimeout bounds how long a commit waits for its bundle. The wait
// itself is event-driven — commitbundle.Notifier wakes the commit the moment
// the bundle lands — so this is only the backstop for a bundle that never
// arrives at all (a crashed writer, or a writer in another replica whose
// in-process notification this replica never sees).
const bundleArrivalTimeout = 30 * time.Second

// SetupWithManager wires the reconciler into the multicluster manager. When the
// bundle store can announce arrivals, a channel source turns "the bundle
// landed" into an enqueue instead of a poll.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	b := mcbuilder.ControllerManagedBy(mgr).
		Named("code-repositorycommits").
		For(&codev1alpha1.RepositoryCommit{})
	if notifier, ok := r.Bundles.(commitbundle.Notifier); ok {
		b = b.WatchesRawSource(bundleArrivals(notifier, &r.waiters))
	}
	return b.Complete(r)
}

// bundleArrivals enqueues the RepositoryCommits that are waiting for each
// bundle as it lands.
func bundleArrivals(notifier commitbundle.Notifier, waiters *bundleWaiters) source.TypedSource[mcreconcile.Request] {
	return source.TypedFunc[mcreconcile.Request](func(ctx context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
		arrivals := notifier.Notify(ctx)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case arrival, ok := <-arrivals:
					if !ok {
						return
					}
					for _, request := range waiters.wake(arrival.Scope, arrival.Name) {
						q.Add(request)
					}
				}
			}
		}()
		return nil
	})
}

// bundleWaiters records which commits are blocked on which bundle. A commit
// registers before it looks for its bundle, so an arrival between the lookup
// and the registration cannot be missed.
type bundleWaiters struct {
	mu      sync.Mutex
	waiting map[string]map[mcreconcile.Request]struct{}
}

func bundleKey(scope, name string) string { return scope + "\x00" + name }

func (w *bundleWaiters) wait(scope, name string, request mcreconcile.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.waiting == nil {
		w.waiting = map[string]map[mcreconcile.Request]struct{}{}
	}
	key := bundleKey(scope, name)
	if w.waiting[key] == nil {
		w.waiting[key] = map[mcreconcile.Request]struct{}{}
	}
	w.waiting[key][request] = struct{}{}
}

func (w *bundleWaiters) forget(scope, name string, request mcreconcile.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := bundleKey(scope, name)
	delete(w.waiting[key], request)
	if len(w.waiting[key]) == 0 {
		delete(w.waiting, key)
	}
}

// wake removes and returns everything waiting for one bundle.
func (w *bundleWaiters) wake(scope, name string) []mcreconcile.Request {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := bundleKey(scope, name)
	requests := make([]mcreconcile.Request, 0, len(w.waiting[key]))
	for request := range w.waiting[key] {
		requests = append(requests, request)
	}
	delete(w.waiting, key)
	return requests
}

// Reconcile commits the referenced bundle once and records the terminal result.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("repositorycommit", req.Name, "cluster", req.ClusterName)

	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var commit codev1alpha1.RepositoryCommit
	if err := c.Get(ctx, req.NamespacedName, &commit); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !commit.DeletionTimestamp.IsZero() || isTerminal(commit.Status.Phase) {
		return ctrl.Result{}, nil
	}
	bundleRef := commit.Spec.Source.BundleRef
	bundleScope := string(req.ClusterName)
	fail := func(message string) error {
		return r.failAndDeleteBundle(ctx, c, &commit, message, bundleScope, bundleRef)
	}
	if r.Bundles == nil {
		return ctrl.Result{}, fail("bundle store is unavailable")
	}
	if r.Backends == nil {
		return ctrl.Result{}, fail("git backends are unavailable")
	}

	now := metav1.Now()
	if commit.Status.Phase == "" {
		commit.Status.Phase = codev1alpha1.RepositoryCommitPhasePending
	}
	if commit.Status.StartedAt == nil {
		commit.Status.StartedAt = &now
	}
	commit.Status.Phase = codev1alpha1.RepositoryCommitPhaseRunning
	commit.Status.ObservedGeneration = commit.Generation
	shared.SetCondition(&commit.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, "Commit is running.", commit.Generation)
	if err := updateStatusIfChanged(ctx, c, &commit); err != nil {
		return ctrl.Result{}, err
	}

	repo, err := shared.ResolveRepository(ctx, c, commit.Spec.RepositoryRef)
	if err != nil {
		return ctrl.Result{}, fail(err.Error())
	}
	conn, err := shared.ResolveConnection(ctx, c, repo.Spec.ConnectionRef)
	if err != nil {
		return ctrl.Result{}, fail(err.Error())
	}
	gitBackend, ok := r.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		return ctrl.Result{}, fail(fmt.Sprintf("git provider %q is not registered", conn.Spec.Provider))
	}
	committer, ok := gitBackend.(backend.RepositoryCommitter)
	if !ok {
		return ctrl.Result{}, fail(fmt.Sprintf("git provider %q does not support committing files", conn.Spec.Provider))
	}
	cred, err := shared.ResolveCredential(ctx, c, conn)
	if err != nil {
		return ctrl.Result{}, fail(err.Error())
	}
	if _, err := gitBackend.EnsureRepository(ctx, conn, cred, repo); err != nil {
		if retryAt, ok := rateLimitRetry(err, commit.Status.StartedAt, time.Now()); ok {
			return r.waitForRateLimit(ctx, c, &commit, retryAt)
		}
		return ctrl.Result{}, fail(fmt.Sprintf("ensure repository: %v", err))
	}

	// Register before the lookup: a bundle that lands between the two wakes
	// this commit through the arrival source instead of being missed.
	r.waiters.wait(bundleScope, bundleRef.Name, req)
	bundle, err := r.Bundles.Get(ctx, bundleScope, bundleRef.Name, bundleRef.Digest)
	if err != nil {
		if wait, waiting := bundleArrivalBackoff(commit.Status.StartedAt, time.Now()); commitbundle.IsNotFound(err) && waiting {
			// The arrival notification is the wake-up; this requeue only
			// bounds a bundle that never arrives.
			logger.V(4).Info("source bundle not visible yet, waiting for arrival", "bundle", bundleRef.Name, "deadline", wait)
			return ctrl.Result{RequeueAfter: wait}, nil
		}
		r.waiters.forget(bundleScope, bundleRef.Name, req)
		return ctrl.Result{}, fail(err.Error())
	}
	r.waiters.forget(bundleScope, bundleRef.Name, req)
	files := make([]backend.RepositoryCommitFile, 0, len(bundle.Files))
	fileStatus := make([]codev1alpha1.RepositoryCommitFileStatus, 0, len(bundle.Files))
	for _, f := range bundle.Files {
		files = append(files, backend.RepositoryCommitFile{Path: f.Path, Content: f.Content, Encoding: f.Encoding, Delete: f.Delete})
		fileStatus = append(fileStatus, codev1alpha1.RepositoryCommitFileStatus{
			Path:   f.Path,
			Size:   f.Size,
			Digest: f.Digest,
			Delete: f.Delete,
		})
	}
	res, err := committer.CommitFiles(ctx, conn, cred, repo, backend.RepositoryCommitInput{
		Message:        commit.Spec.Message,
		Branch:         commit.Spec.Branch,
		IdempotencyKey: repositoryCommitIdempotencyKey(&commit),
		Files:          files,
	})
	if err != nil {
		// Retrying is safe: the idempotency trailer lets the backend find a
		// commit that landed before the limit hit instead of writing it twice.
		if retryAt, ok := rateLimitRetry(err, commit.Status.StartedAt, time.Now()); ok {
			return r.waitForRateLimit(ctx, c, &commit, retryAt)
		}
		return ctrl.Result{}, fail(err.Error())
	}

	completed := metav1.Now()
	next := commit.DeepCopy()
	next.Status.Phase = codev1alpha1.RepositoryCommitPhaseSucceeded
	next.Status.ObservedGeneration = commit.Generation
	next.Status.CompletedAt = &completed
	next.Status.Branch = res.Branch
	next.Status.CommitSHA = res.CommitSHA
	next.Status.CommitURL = res.CommitURL
	next.Status.Source = &codev1alpha1.RepositoryCommitSourceStatus{
		Digest:    bundle.Digest,
		Size:      bundle.Size,
		FileCount: len(bundle.Files),
	}
	next.Status.Files = fileStatus
	shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionTrue, codev1alpha1.ReasonReady, "Commit succeeded.", commit.Generation)
	if err := updateStatusIfChanged(ctx, c, next); err != nil {
		return ctrl.Result{}, fmt.Errorf("update repositorycommit %q status: %w", commit.Name, err)
	}
	if err := r.Bundles.Delete(ctx, bundleScope, bundleRef.Name, bundleRef.Digest); err != nil {
		logger.Error(err, "delete committed source bundle", "bundle", bundleRef.Name)
	}
	logger.V(3).Info("repository commit succeeded", "repository", commit.Spec.RepositoryRef, "commitSHA", res.CommitSHA)
	return ctrl.Result{}, nil
}

// rateLimitRetry returns when to retry a commit that failed on a host rate
// limit. ok is false for other errors, and once the retry would land outside
// RepositoryCommitRateLimitWindow after startedAt; the commit then fails.
func rateLimitRetry(err error, startedAt *metav1.Time, now time.Time) (time.Time, bool) {
	var limited *backend.RateLimitError
	if !errors.As(err, &limited) || startedAt == nil {
		return time.Time{}, false
	}
	retryAt := limited.RetryAt
	if earliest := now.Add(time.Second); retryAt.Before(earliest) {
		retryAt = earliest
	}
	if retryAt.After(startedAt.Add(codev1alpha1.RepositoryCommitRateLimitWindow)) {
		return time.Time{}, false
	}
	return retryAt, true
}

// waitForRateLimit keeps the commit Running with its bundle and schedules the
// retry, reporting the wait on the Ready condition.
func (r *Reconciler) waitForRateLimit(ctx context.Context, c client.Client, commit *codev1alpha1.RepositoryCommit, retryAt time.Time) (ctrl.Result, error) {
	wait := max(time.Until(retryAt), time.Second)
	next := commit.DeepCopy()
	message := fmt.Sprintf("GitHub rate limit; retrying in %ds", int(wait.Round(time.Second)/time.Second))
	shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonRateLimited, message, commit.Generation)
	if err := updateStatusIfChanged(ctx, c, next); err != nil {
		return ctrl.Result{}, err
	}
	klog.FromContext(ctx).V(3).Info("repository commit rate limited, requeuing", "repositorycommit", commit.Name, "retryAt", retryAt)
	return ctrl.Result{RequeueAfter: wait}, nil
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, commit *codev1alpha1.RepositoryCommit, message string) error {
	next := commit.DeepCopy()
	now := metav1.Now()
	if next.Status.StartedAt == nil {
		next.Status.StartedAt = &now
	}
	next.Status.CompletedAt = &now
	next.Status.ObservedGeneration = commit.Generation
	next.Status.Phase = codev1alpha1.RepositoryCommitPhaseFailed
	shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonError, message, commit.Generation)
	if err := updateStatusIfChanged(ctx, c, next); err != nil {
		return fmt.Errorf("update repositorycommit %q failure status: %w", commit.Name, err)
	}
	return nil
}

func (r *Reconciler) failAndDeleteBundle(ctx context.Context, c client.Client, commit *codev1alpha1.RepositoryCommit, message, bundleScope string, bundleRef codev1alpha1.RepositoryCommitBundleReference) error {
	if err := r.fail(ctx, c, commit, message); err != nil {
		return err
	}
	if r.Bundles == nil || bundleRef.Name == "" {
		return nil
	}
	if err := r.Bundles.Delete(ctx, bundleScope, bundleRef.Name, bundleRef.Digest); err != nil {
		klog.FromContext(ctx).Error(err, "delete failed source bundle", "bundle", bundleRef.Name)
	}
	return nil
}

func updateStatusIfChanged(ctx context.Context, c client.Client, commit *codev1alpha1.RepositoryCommit) error {
	current := &codev1alpha1.RepositoryCommit{}
	if err := c.Get(ctx, client.ObjectKey{Name: commit.Name}, current); err != nil {
		return err
	}
	if reflect.DeepEqual(current.Status, commit.Status) {
		return nil
	}
	current.Status = commit.Status
	if err := c.Status().Update(ctx, current); err != nil {
		return fmt.Errorf("update repositorycommit %q status: %w", commit.Name, err)
	}
	return nil
}

func isTerminal(phase codev1alpha1.RepositoryCommitPhase) bool {
	return phase == codev1alpha1.RepositoryCommitPhaseSucceeded || phase == codev1alpha1.RepositoryCommitPhaseFailed
}

func repositoryCommitIdempotencyKey(commit *codev1alpha1.RepositoryCommit) string {
	if commit == nil {
		return ""
	}
	if commit.UID != "" {
		return string(commit.UID)
	}
	return commit.Name
}

// bundleArrivalBackoff reports whether a commit may still wait for its bundle
// and, if so, how long is left of bundleArrivalTimeout. The remainder is the
// backoff for the case where no arrival is ever announced; it is never zero,
// so the commit cannot spin.
func bundleArrivalBackoff(startedAt *metav1.Time, now time.Time) (time.Duration, bool) {
	if startedAt == nil {
		return bundleArrivalTimeout, true
	}
	remaining := bundleArrivalTimeout - now.Sub(startedAt.Time)
	if remaining <= 0 {
		return 0, false
	}
	return max(remaining, time.Second), true
}
