/*
Copyright 2026 The Railgrid Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0
*/

// Package pullrequest makes the forge agree with a PullRequest resource and
// keeps the resource agreeing with the forge.
//
// Intent is the spec: a head branch, a base, public text, and the exact
// snapshot commit the branch should be at. Each pass does the least that
// closes the gap — advance the branch under an expected-head lease, open the
// pull request once — and then reads the forge back into the status: head,
// state, merge, and the review conversation. The forge has no watch, so an
// open pull request is re-read on a timer; a merged or closed one is read
// once more and left alone.
package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/railgrid/provider-code/actions"
	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/controller/shared"
)

const (
	// defaultObserveInterval is how often an open pull request is re-read
	// when spec.observeInterval is unset; minObserveInterval is the floor.
	// Every read is a dozen or more forge requests (the pull request, the
	// checks, the reviews and their comments, the conversation), so the
	// interval is the one knob that bounds this controller's rate budget.
	defaultObserveInterval = 5 * time.Minute
	minObserveInterval     = 30 * time.Second
	// pushSettleInterval is how soon a pull request is re-read after its
	// branch was advanced and the forge did not report the new head yet.
	pushSettleInterval = 15 * time.Second
	// Bounds on what the status carries of the conversation.
	maxReviews  = 64
	maxComments = 128
	maxBody     = 8192
	// commentPages bounds the conversation read (Code's own page limit).
	commentPages = 20
)

// Reconciler drives PullRequest resources.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
	// SnapshotDir is where the stage-snapshot verb keeps staged bundles.
	SnapshotDir string
	Now         func() time.Time
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	// Only a spec change re-triggers a reconcile. Every pass writes the
	// status (at least lastObserved), and without this filter that write
	// would re-enqueue the object at once: a hot loop against the forge
	// that burns the whole rate budget, with the timer below never reached.
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-pullrequests").
		For(&codev1alpha1.PullRequest{}, mcbuilder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

// observeInterval is how long to wait before re-reading pr from the forge.
func observeInterval(pr *codev1alpha1.PullRequest) time.Duration {
	if pr.Spec.ObserveInterval == nil || pr.Spec.ObserveInterval.Duration <= 0 {
		return defaultObserveInterval
	}
	return max(pr.Spec.ObserveInterval.Duration, minObserveInterval)
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// forge is the backend as this reconciler needs it.
type forge interface {
	backend.Collaboration
	backend.SnapshotPublisher
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("pullrequest", req.Name, "cluster", req.ClusterName)
	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	var pr codev1alpha1.PullRequest
	if err := c.Get(ctx, req.NamespacedName, &pr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !pr.DeletionTimestamp.IsZero() {
		// Deleting the resource forgets the pull request; it does not close it.
		return ctrl.Result{}, nil
	}
	if r.Backends == nil {
		return ctrl.Result{}, r.fail(ctx, c, &pr, "git backends are unavailable")
	}
	repo, err := shared.ResolveRepository(ctx, c, pr.Spec.RepositoryRef)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, c, &pr, err.Error())
	}
	conn, err := shared.ResolveConnection(ctx, c, repo.Spec.ConnectionRef)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, c, &pr, err.Error())
	}
	gitBackend, ok := r.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		return ctrl.Result{}, r.fail(ctx, c, &pr, fmt.Sprintf("git provider %q is not registered", conn.Spec.Provider))
	}
	host, ok := gitBackend.(forge)
	if !ok {
		return ctrl.Result{}, r.fail(ctx, c, &pr, fmt.Sprintf("git provider %q does not support pull requests", conn.Spec.Provider))
	}
	cred, err := shared.ResolveCredential(ctx, c, conn)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, c, &pr, err.Error())
	}

	next := pr.DeepCopy()
	next.Status.ObservedGeneration = pr.Generation

	// 1. The branch: advance it to the desired head when it is not there.
	pushed := false
	if head := pr.Spec.DesiredHead; head != nil && pr.Status.Head != head.Commit {
		pushed = true
		if err := r.advance(ctx, host, conn, cred, repo, string(req.ClusterName), &pr, head); err != nil {
			var permanent permanentError
			if errors.As(err, &permanent) {
				return ctrl.Result{}, r.fail(ctx, c, &pr, err.Error())
			}
			shared.SetCondition(&next.Status.Conditions, codev1alpha1.PullRequestConditionHeadApplied, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, err.Error(), pr.Generation)
			shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, err.Error(), pr.Generation)
			if updateErr := updateStatusIfChanged(ctx, c, &pr, next); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, err
		}
	}

	// 2. The pull request: open it once there is something to review.
	if pr.Status.Number == 0 && pr.Spec.DesiredHead != nil {
		opened, err := r.open(ctx, host, conn, cred, repo, &pr)
		if err != nil {
			shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, err.Error(), pr.Generation)
			if updateErr := updateStatusIfChanged(ctx, c, &pr, next); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, err
		}
		next.Status.Number = opened.Number
		next.Status.URL = opened.URL
	}
	if next.Status.Number == 0 {
		next.Status.Phase = codev1alpha1.PullRequestPhasePending
		shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, "No head to review yet.", pr.Generation)
		return ctrl.Result{}, updateStatusIfChanged(ctx, c, &pr, next)
	}

	// 3. The forge's view, read back.
	observed, err := host.ReadPullRequest(ctx, conn, cred, repo, next.Status.Number)
	if err != nil {
		shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonError, "pull request unavailable: "+err.Error(), pr.Generation)
		if updateErr := updateStatusIfChanged(ctx, c, &pr, next); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{RequeueAfter: observeInterval(&pr)}, nil
	}
	if observed.Head != pr.Spec.Branch || observed.Base != pr.Spec.Base || !strings.EqualFold(observed.HeadRepository, observed.Repository) {
		return ctrl.Result{}, r.fail(ctx, c, &pr, "the pull request on the forge is not the one this resource describes")
	}
	// The branch was just advanced and the forge does not report it yet: a
	// read right after a push can lag. Reading the conversation against the
	// stale head would be refused, and waiting the whole observe interval
	// would leave the coordinator a stale head for minutes. Come back soon.
	if pushed && pr.Spec.DesiredHead != nil && observed.Commit != pr.Spec.DesiredHead.Commit {
		shared.SetCondition(&next.Status.Conditions, codev1alpha1.PullRequestConditionHeadApplied, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, "The head branch was advanced; waiting for the forge to report it.", pr.Generation)
		shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, "The head branch was advanced; waiting for the forge to report it.", pr.Generation)
		if err := updateStatusIfChanged(ctx, c, &pr, next); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: pushSettleInterval}, nil
	}
	next.Status.URL = observed.URL
	next.Status.Head = observed.Commit
	next.Status.State = strings.ToLower(observed.State)
	next.Status.Merged = observed.Merged
	next.Status.MergeCommit = observed.MergeCommit
	next.Status.Merger = observed.Merger
	next.Status.MergerType = observed.MergerType
	next.Status.MergedAt = nil
	if observed.MergedAt != nil {
		at := metav1.NewTime(*observed.MergedAt)
		next.Status.MergedAt = &at
	}
	switch {
	case observed.Merged:
		next.Status.Phase = codev1alpha1.PullRequestPhaseMerged
	case next.Status.State == "open":
		next.Status.Phase = codev1alpha1.PullRequestPhaseOpen
	default:
		next.Status.Phase = codev1alpha1.PullRequestPhaseClosed
	}
	// The conversation: reviews against the current head, and the comments.
	// A read that fails leaves the previous view in place and says so.
	conversationErr := r.observeConversation(ctx, host, conn, cred, repo, next)
	stamp := metav1.NewTime(r.now())
	next.Status.LastObserved = &stamp
	applied := pr.Spec.DesiredHead == nil || observed.Commit == pr.Spec.DesiredHead.Commit
	if applied {
		shared.SetCondition(&next.Status.Conditions, codev1alpha1.PullRequestConditionHeadApplied, metav1.ConditionTrue, codev1alpha1.ReasonReady, "The head branch is at the desired commit.", pr.Generation)
	} else {
		shared.SetCondition(&next.Status.Conditions, codev1alpha1.PullRequestConditionHeadApplied, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, "The head branch is at "+observed.Commit+", not the desired commit.", pr.Generation)
	}
	switch {
	case conversationErr != nil:
		shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonError, "conversation unavailable: "+conversationErr.Error(), pr.Generation)
	case applied:
		shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionTrue, codev1alpha1.ReasonReady, "The forge agrees with this resource.", pr.Generation)
	default:
		shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, "The head branch is behind the desired commit.", pr.Generation)
	}
	if err := updateStatusIfChanged(ctx, c, &pr, next); err != nil {
		return ctrl.Result{}, err
	}
	if next.Status.Phase == codev1alpha1.PullRequestPhaseOpen || !applied {
		return ctrl.Result{RequeueAfter: observeInterval(&pr)}, nil
	}
	logger.V(3).Info("pull request settled", "phase", next.Status.Phase)
	return ctrl.Result{}, nil
}

// permanentError is a desired head the forge will never take: the branch is
// somewhere this resource did not put it, or the snapshot is not what it
// claims. Retrying cannot change the answer; a person has to.
type permanentError struct{ error }

// advance moves the head branch to the desired snapshot under an
// expected-head lease: the head last observed, or an absent branch for the
// first push. A branch that is already there is left alone.
func (r *Reconciler) advance(ctx context.Context, host forge, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository, cluster string, pr *codev1alpha1.PullRequest, head *codev1alpha1.PullRequestHead) error {
	current, err := host.BranchHead(ctx, conn, cred, repo, pr.Spec.Branch)
	if err != nil {
		return fmt.Errorf("reading the head branch: %w", err)
	}
	if current == head.Commit {
		return nil
	}
	expected := pr.Status.Head
	if current != expected {
		if expected == "" && pr.Status.Number == 0 && current != "" {
			return permanentError{fmt.Errorf("the head branch %q already exists at %s; it is not this pull request's to move", pr.Spec.Branch, current)}
		}
		if expected != "" {
			return permanentError{fmt.Errorf("the head branch moved to %s outside this resource; refusing to overwrite it", current)}
		}
	}
	snapshot, err := actions.FindStaged(r.SnapshotDir, cluster, head.BundleRef)
	if err != nil {
		return fmt.Errorf("staged snapshot %s: %w", head.BundleRef, err)
	}
	if snapshot.Commit != head.Commit || snapshot.Tree != head.Tree || snapshot.BaseCommit != head.BaseCommit {
		return permanentError{errors.New("the staged snapshot is not the desired head")}
	}
	snapshot.Message = head.Message
	if err := host.PublishSnapshot(ctx, conn, cred, repo, snapshot, pr.Spec.Branch, expected); err != nil {
		return fmt.Errorf("advancing the head branch: %w", err)
	}
	return nil
}

// open creates the pull request, or adopts the one the forge already has for
// this head branch (a create whose answer was lost).
func (r *Reconciler) open(ctx context.Context, host forge, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository, pr *codev1alpha1.PullRequest) (*backend.PullRequest, error) {
	input := backend.PullRequestInput{Head: pr.Spec.Branch, Base: pr.Spec.Base, Commit: pr.Spec.DesiredHead.Commit, Title: pr.Spec.Title, Body: pr.Spec.Body}
	existing, err := host.FindPullRequest(ctx, conn, cred, repo, input)
	if err != nil {
		return nil, fmt.Errorf("finding the pull request: %w", err)
	}
	if existing != nil {
		return existing, nil
	}
	created, err := host.CreatePullRequest(ctx, conn, cred, repo, input)
	if err != nil {
		return nil, fmt.Errorf("opening the pull request: %w", err)
	}
	return created, nil
}

func (r *Reconciler) observeConversation(ctx context.Context, host forge, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository, pr *codev1alpha1.PullRequest) error {
	feedback, err := host.PullRequestFeedback(ctx, conn, cred, repo, pr.Status.Number, pr.Status.Head)
	if err != nil {
		return err
	}
	reviews := make([]codev1alpha1.PullRequestReview, 0, len(feedback.Reviews))
	for _, review := range feedback.Reviews {
		at := metav1.NewTime(review.SubmittedAt)
		reviews = append(reviews, codev1alpha1.PullRequestReview{ID: review.ID, Author: review.Login, AuthorType: review.ActorType, State: review.State, Commit: review.Commit, Body: bounded(review.Body), SubmittedAt: &at})
	}
	if len(reviews) > maxReviews {
		reviews = reviews[len(reviews)-maxReviews:]
	}
	comments := make([]codev1alpha1.PullRequestComment, 0, maxComments)
	truncated := false
	for page, count := 1, 0; page > 0 && count < commentPages; count++ {
		result, err := host.ListComments(ctx, conn, cred, repo, pr.Status.Number, page)
		if err != nil {
			return err
		}
		for _, comment := range result.Comments {
			at := metav1.NewTime(comment.UpdatedAt)
			comments = append(comments, codev1alpha1.PullRequestComment{ID: comment.ID, Author: comment.Author, AuthorType: comment.AuthorType, Body: bounded(comment.Body), URL: comment.URL, UpdatedAt: &at})
			if len(comments) > maxComments {
				comments = comments[1:]
				truncated = true
			}
		}
		page = result.NextPage
		if page > commentPages {
			truncated = true
			break
		}
	}
	pr.Status.Reviews = reviews
	pr.Status.Comments = comments
	pr.Status.CommentsTruncated = truncated
	return nil
}

func bounded(value string) string {
	if len(value) <= maxBody {
		return value
	}
	return value[:maxBody]
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, pr *codev1alpha1.PullRequest, message string) error {
	next := pr.DeepCopy()
	next.Status.ObservedGeneration = pr.Generation
	next.Status.Phase = codev1alpha1.PullRequestPhaseFailed
	shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonError, message, pr.Generation)
	return updateStatusIfChanged(ctx, c, pr, next)
}

func updateStatusIfChanged(ctx context.Context, c client.Client, current, next *codev1alpha1.PullRequest) error {
	if reflect.DeepEqual(current.Status, next.Status) {
		return nil
	}
	return c.Status().Update(ctx, next)
}
