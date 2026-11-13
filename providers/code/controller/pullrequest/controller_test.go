/*
Copyright 2026 The Railgrid Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0
*/

package pullrequest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/snapshotstore"
)

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	headC = "dddddddddddddddddddddddddddddddddddddddd"
	tree  = "cccccccccccccccccccccccccccccccccccccccc"
)

// fakeForge is a forge with one branch and, optionally, one pull request.
type fakeForge struct {
	backend.Collaboration
	backend.SnapshotPublisher
	branch      string
	published   []backend.Snapshot
	expected    []string
	pr          *backend.PullRequest
	created     int
	reviews     []backend.Review
	comments    []backend.Comment
	checks      []backend.Check
	feedbackErr error
}

func (f *fakeForge) BranchHead(context.Context, *codev1alpha1.Connection, backend.Credential, *codev1alpha1.Repository, string) (string, error) {
	return f.branch, nil
}

func (f *fakeForge) PublishSnapshot(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, in backend.Snapshot, _, expected string) error {
	if expected != f.branch {
		return errors.New("lease failed")
	}
	f.published = append(f.published, in)
	f.expected = append(f.expected, expected)
	f.branch = in.Commit
	return nil
}

func (f *fakeForge) FindPullRequest(context.Context, *codev1alpha1.Connection, backend.Credential, *codev1alpha1.Repository, backend.PullRequestInput) (*backend.PullRequest, error) {
	return f.pr, nil
}

func (f *fakeForge) ReadPullRequest(context.Context, *codev1alpha1.Connection, backend.Credential, *codev1alpha1.Repository, int) (*backend.PullRequest, error) {
	return f.pr, nil
}

func (f *fakeForge) CreatePullRequest(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, in backend.PullRequestInput) (*backend.PullRequest, error) {
	f.created++
	f.pr = &backend.PullRequest{Number: 17, URL: "https://example.invalid/pull/17", Repository: "acme/product", HeadRepository: "acme/product", Head: in.Head, Base: in.Base, Commit: in.Commit, State: "open"}
	return f.pr, nil
}

func (f *fakeForge) PullRequestFeedback(context.Context, *codev1alpha1.Connection, backend.Credential, *codev1alpha1.Repository, int, string) (*backend.Feedback, error) {
	if f.feedbackErr != nil {
		return nil, f.feedbackErr
	}
	return &backend.Feedback{Checks: f.checks, Reviews: f.reviews}, nil
}

func (f *fakeForge) ListComments(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, _ int, page int) (*backend.CommentPage, error) {
	if page != 1 {
		return &backend.CommentPage{}, nil
	}
	return &backend.CommentPage{Comments: f.comments}, nil
}

func stage(t *testing.T, store snapshotstore.Store, cluster string, snapshot backend.Snapshot) string {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Stage(context.Background(), cluster, "some-caller", data)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func resource(head *codev1alpha1.PullRequestHead) *codev1alpha1.PullRequest {
	pr := &codev1alpha1.PullRequest{Spec: codev1alpha1.PullRequestSpec{RepositoryRef: "product", Branch: "work/task-1", Base: "main", Title: "ENG-4: test"}}
	pr.Spec.DesiredHead = head
	return pr
}

// The branch is moved only from the head this resource last observed: the
// first push needs an absent branch, a later one the previous head, and a
// branch that is somewhere else is never overwritten. The staged snapshot
// must be the desired head, and it is published with the named subject.
func TestAdvanceMovesTheBranchUnderAnExpectedHeadLease(t *testing.T) {
	ctx := context.Background()
	store, err := snapshotstore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Snapshots: store}
	snapshot := backend.Snapshot{BaseCommit: headB, Commit: headA, Tree: tree, Bundle: []byte("bundle")}
	ref := stage(t, store, "cluster-1", snapshot)
	head := &codev1alpha1.PullRequestHead{Commit: headA, BaseCommit: headB, Tree: tree, BundleRef: ref, Message: "ENG-4: test"}

	forge := &fakeForge{}
	if err := r.advance(ctx, forge, nil, backend.Credential{}, nil, "cluster-1", resource(head), head); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if forge.branch != headA || forge.expected[0] != "" || forge.published[0].Message != "ENG-4: test" {
		t.Fatalf("first push: branch=%s expected=%q message=%q", forge.branch, forge.expected[0], forge.published[0].Message)
	}
	// Already there: nothing to do.
	if err := r.advance(ctx, forge, nil, backend.Credential{}, nil, "cluster-1", resource(head), head); err != nil || len(forge.published) != 1 {
		t.Fatalf("second pass pushed again: %v %d", err, len(forge.published))
	}
	// A branch that already exists before the first push is not ours to move.
	taken := &fakeForge{branch: headB}
	err = r.advance(ctx, taken, nil, backend.Credential{}, nil, "cluster-1", resource(head), head)
	var permanent permanentError
	if !errors.As(err, &permanent) {
		t.Fatalf("an existing branch was moved: %v", err)
	}
	// A branch that moved away from the observed head is not overwritten.
	moved := &fakeForge{branch: headB}
	pr := resource(head)
	pr.Status.Head = "dddddddddddddddddddddddddddddddddddddddd"
	pr.Status.Number = 17
	if err := r.advance(ctx, moved, nil, backend.Credential{}, nil, "cluster-1", pr, head); !errors.As(err, &permanent) {
		t.Fatalf("a moved branch was overwritten: %v", err)
	}
	// A staged snapshot that is not the desired head is refused.
	wrong := &codev1alpha1.PullRequestHead{Commit: headB, BaseCommit: headA, Tree: tree, BundleRef: ref}
	if err := r.advance(ctx, &fakeForge{}, nil, backend.Credential{}, nil, "cluster-1", resource(wrong), wrong); !errors.As(err, &permanent) {
		t.Fatalf("a mismatched snapshot was published: %v", err)
	}
	// An expired or missing stage is a wait, not a failure.
	missing := &codev1alpha1.PullRequestHead{Commit: headA, BaseCommit: headB, Tree: tree, BundleRef: strings.Repeat("0", 64)}
	if err := r.advance(ctx, &fakeForge{}, nil, backend.Credential{}, nil, "cluster-1", resource(missing), missing); err == nil || errors.As(err, &permanent) {
		t.Fatalf("a missing stage was treated as permanent: %v", err)
	}
}

func TestAcceptedPublicationUsesImmutableExpectedHeadAndRecoversCandidate(t *testing.T) {
	store, err := snapshotstore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Snapshots: store}
	ref := stage(t, store, "tenant", backend.Snapshot{BaseCommit: headC, Commit: headA, Tree: tree, Bundle: []byte("bundle")})
	intent := &codev1alpha1.PullRequestPublication{Sequence: 1, OperationID: "op-1", ExpectedHead: headB, Candidate: codev1alpha1.PullRequestHead{Commit: headA, BaseCommit: headC, Tree: tree, BundleRef: ref}}
	pr := resource(nil)
	forge := &fakeForge{branch: headB}
	if err := r.advancePublication(context.Background(), forge, nil, backend.Credential{}, nil, "tenant", pr, intent); err != nil {
		t.Fatal(err)
	}
	if forge.branch != headA || len(forge.published) != 1 || forge.expected[0] != headB {
		t.Fatalf("publication did not use expected-head lease: branch=%s expected=%v", forge.branch, forge.expected)
	}
	// A retry after an accepted push recognizes the candidate already on the
	// branch and does not create another forge write.
	if err := r.advancePublication(context.Background(), forge, nil, backend.Credential{}, nil, "tenant", pr, intent); err != nil || len(forge.published) != 1 {
		t.Fatalf("recovery repeated publication: err=%v writes=%d", err, len(forge.published))
	}
	// An external push after acceptance still fails the original lease.
	forge.branch = headC
	if err := r.advancePublication(context.Background(), forge, nil, backend.Credential{}, nil, "tenant", pr, intent); err == nil || len(forge.published) != 1 {
		t.Fatalf("stale accepted lease wrote over an external head: err=%v writes=%d", err, len(forge.published))
	}
}

func TestPublicationAllowsCandidateParentIndependentOfBranchLease(t *testing.T) {
	intent := &codev1alpha1.PullRequestPublication{Sequence: 1, OperationID: "op", ExpectedHead: headB, Candidate: codev1alpha1.PullRequestHead{Commit: headA, BaseCommit: headA, Tree: tree, BundleRef: strings.Repeat("d", 64)}}
	if err := validatePublication(intent); err != nil {
		t.Fatalf("valid rebuilt candidate rejected: %v", err)
	}
}

func TestReconcilePersistsAcceptanceBeforeForgeWriteAndTerminalReceiptStillObserves(t *testing.T) {
	r, c, host, req, pr := publicationFixture(t)
	pr.Spec.DesiredHead = nil
	pr.Spec.Body = ""
	pr.Spec.AttachmentRef = ""
	ref := stage(t, r.Snapshots, "tenant", backend.Snapshot{BaseCommit: headB, Commit: headA, Tree: tree, Bundle: []byte("candidate")})
	pr.Spec.Publication = &codev1alpha1.PullRequestPublication{Sequence: 1, OperationID: "publish-1", ExpectedAbsent: true, Candidate: codev1alpha1.PullRequestHead{Commit: headA, BaseCommit: headB, Tree: tree, BundleRef: ref}}
	pr.Status.Head = ""
	pr.Generation++
	if err := c.Update(t.Context(), pr); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if len(host.published) != 0 {
		t.Fatal("forge write happened before accepted receipt persisted")
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(pr), pr); err != nil {
		t.Fatal(err)
	}
	if pr.Status.Publication == nil || pr.Status.Publication.Phase != "Accepted" {
		t.Fatalf("receipt = %+v", pr.Status.Publication)
	}
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if len(host.published) != 1 {
		t.Fatalf("accepted operation did not publish once: %d", len(host.published))
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(pr), pr); err != nil {
		t.Fatal(err)
	}
	if pr.Status.Publication == nil || pr.Status.Publication.Phase != "Published" || pr.Status.Publication.ObservedHead != headA {
		t.Fatalf("publication outcome = %+v", pr.Status.Publication)
	}
	reads := host.reads
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if len(host.published) != 1 {
		t.Fatal("terminal receipt replayed publication")
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(pr), pr); err != nil {
		t.Fatal(err)
	}
	if host.reads != reads+1 {
		t.Fatal("terminal receipt stopped forge observation")
	}
}

func TestReconcileAcknowledgesCancellationBeforeAcceptanceWithoutForgeWrite(t *testing.T) {
	r, c, host, req, pr := publicationFixture(t)
	pr.Spec.DesiredHead = nil
	ref := stage(t, r.Snapshots, "tenant", backend.Snapshot{BaseCommit: headB, Commit: headA, Tree: tree, Bundle: []byte("candidate")})
	pr.Spec.Publication = &codev1alpha1.PullRequestPublication{Sequence: 1, OperationID: "cancel-1", ExpectedAbsent: true, Candidate: codev1alpha1.PullRequestHead{Commit: headA, BaseCommit: headB, Tree: tree, BundleRef: ref}, Cancel: true}
	pr.Generation++
	if err := c.Update(t.Context(), pr); err != nil {
		t.Fatal(err)
	}
	// A pre-accept cancellation must be durably acknowledged even while forge
	// credentials are unavailable.
	if err := c.Delete(t.Context(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("Reconcile without forge credentials: %v", err)
	}
	if len(host.published) != 0 || host.created != 0 {
		t.Fatal("cancelled pre-acceptance intent reached forge")
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(pr), pr); err != nil {
		t.Fatal(err)
	}
	if pr.Status.Publication == nil || pr.Status.Publication.Phase != "Cancelled" || pr.Status.Publication.FinishedAt == nil {
		t.Fatalf("cancellation receipt = %+v", pr.Status.Publication)
	}
}

// The pull request is opened once: a create whose answer was lost is found
// again rather than opened twice.
func TestOpenAdoptsAnExistingPullRequest(t *testing.T) {
	ctx := context.Background()
	r := &Reconciler{}
	head := &codev1alpha1.PullRequestHead{Commit: headA, BaseCommit: headB, Tree: tree}
	forge := &fakeForge{}
	first, err := r.open(ctx, forge, nil, backend.Credential{}, nil, resource(head), "")
	if err != nil || first.Number != 17 || forge.created != 1 {
		t.Fatalf("open: %+v %v created=%d", first, err, forge.created)
	}
	again, err := r.open(ctx, forge, nil, backend.Credential{}, nil, resource(head), "")
	if err != nil || again.Number != 17 || forge.created != 1 {
		t.Fatalf("reopen: %+v %v created=%d", again, err, forge.created)
	}
}

// The conversation is read into the status as the forge reports it — who
// wrote it, what kind of actor they are, against which head — bounded, and
// never interpreted.
func TestObserveConversationCarriesReviewsAndComments(t *testing.T) {
	r := &Reconciler{}
	at := time.Date(2026, 10, 3, 6, 0, 0, 0, time.UTC)
	forge := &fakeForge{
		reviews:  []backend.Review{{ID: 5, Login: "alice", ActorType: "User", Commit: headA, State: "COMMENTED", Body: "make it part of readme", SubmittedAt: at}},
		comments: []backend.Comment{{ID: 9, Author: "factory[bot]", AuthorType: "Bot", Body: "Factory implementation", UpdatedAt: at}, {ID: 10, Author: "alice", AuthorType: "User", Body: strings.Repeat("x", maxBody+10), UpdatedAt: at}},
	}
	pr := resource(nil)
	pr.Status.Number = 17
	pr.Status.Head = headA
	if err := r.observeConversation(context.Background(), forge, nil, backend.Credential{}, nil, pr); err != nil {
		t.Fatal(err)
	}
	if len(pr.Status.Reviews) != 1 || pr.Status.Reviews[0].Author != "alice" || pr.Status.Reviews[0].State != "COMMENTED" || pr.Status.Reviews[0].Commit != headA {
		t.Fatalf("reviews = %+v", pr.Status.Reviews)
	}
	if len(pr.Status.Comments) != 2 || pr.Status.Comments[0].AuthorType != "Bot" || len(pr.Status.Comments[1].Body) != maxBody || pr.Status.CommentsTruncated {
		t.Fatalf("comments = %+v truncated=%t", pr.Status.Comments, pr.Status.CommentsTruncated)
	}
}

func TestAdoptionRequiresMatchingOpenSameRepositoryPullRequest(t *testing.T) {
	base := &backend.PullRequest{Number: 12, Repository: "acme/product", HeadRepository: "acme/product", Head: "work/task-1", Base: "main", State: "open"}
	cases := []struct {
		name   string
		mutate func(*backend.PullRequest)
		want   bool
	}{
		{"matching", func(*backend.PullRequest) {}, true},
		{"wrong repository", func(p *backend.PullRequest) { p.Repository = "other/product" }, false},
		{"fork head", func(p *backend.PullRequest) { p.HeadRepository = "fork/product" }, false},
		{"wrong branch", func(p *backend.PullRequest) { p.Head = "other" }, false},
		{"wrong base", func(p *backend.PullRequest) { p.Base = "release" }, false},
		{"closed", func(p *backend.PullRequest) { p.State = "closed" }, false},
		{"merged", func(p *backend.PullRequest) { p.Merged = true }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := *base
			tc.mutate(&got)
			if validAdoption(&got, "acme/product", "work/task-1", "main") != tc.want {
				t.Fatalf("validAdoption = %t, want %t", !tc.want, tc.want)
			}
		})
	}
}

func TestCheckObservationsAreCommitPinnedAndFreshnessIsNotAdvancedOnError(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	r := &Reconciler{Now: func() time.Time { return now }}
	forge := &fakeForge{checks: []backend.Check{{ID: 5, Name: "build", AppID: 8, Head: headA, Status: "completed", Conclusion: "success"}}}
	pr := resource(nil)
	pr.Status.Number = 17
	pr.Status.Head = headA
	if err := r.observeConversation(context.Background(), forge, nil, backend.Credential{}, nil, pr); err != nil {
		t.Fatal(err)
	}
	if pr.Status.ChecksHead != headA || pr.Status.ChecksObservedAt == nil || !pr.Status.ChecksObservedAt.Time.Equal(now) || len(pr.Status.Checks) != 1 {
		t.Fatalf("check observation = %+v", pr.Status)
	}
	oldObservedAt := pr.Status.ChecksObservedAt.DeepCopy()
	pr.Status.Head = headB
	forge.feedbackErr = errors.New("forge unavailable")
	if err := r.observeConversation(context.Background(), forge, nil, backend.Credential{}, nil, pr); err == nil {
		t.Fatal("expected unavailable observation")
	}
	if pr.Status.ChecksHead != headA || !pr.Status.ChecksObservedAt.Equal(oldObservedAt) || pr.Status.ChecksError == "" {
		t.Fatalf("unavailable checks were refreshed or not marked: %+v", pr.Status)
	}
}

// An open pull request is re-read on the interval the coordinator set, five
// minutes when unset, and never more often than the floor.
func TestObserveIntervalDefaultsAndFloors(t *testing.T) {
	cases := map[string]struct {
		spec *metav1.Duration
		want time.Duration
	}{
		"unset":      {nil, defaultObserveInterval},
		"zero":       {&metav1.Duration{}, defaultObserveInterval},
		"configured": {&metav1.Duration{Duration: 2 * time.Minute}, 2 * time.Minute},
		"too short":  {&metav1.Duration{Duration: time.Second}, minObserveInterval},
	}
	for name, tc := range cases {
		pr := resource(nil)
		pr.Spec.ObserveInterval = tc.spec
		if got := observeInterval(pr); got != tc.want {
			t.Errorf("%s: observeInterval = %s, want %s", name, got, tc.want)
		}
	}
}

func TestForgeObservationPreservesExactActionClaim(t *testing.T) {
	const claimedAt = "2026-11-12T09:08:07.123456789Z"
	// Keep raw JSON outside the typed fake client so it cannot normalize the
	// timestamp before the simulated API server applies the merge patch.
	raw := []byte(`{"metadata":{"resourceVersion":"1"},"status":{"reviewOperations":[{"key":"review","phase":"Accepted","updatedAt":"` + claimedAt + `"}]}}`)
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		SubResourcePatch: func(_ context.Context, _ client.Client, sub string, obj client.Object, patch client.Patch, _ ...client.SubResourcePatchOption) error {
			if sub != "status" {
				t.Fatalf("patched subresource %q", sub)
			}
			data, err := patch.Data(obj)
			if err != nil {
				return err
			}
			raw, err = jsonpatch.MergePatch(raw, data)
			return err
		},
	}).Build()
	var current codev1alpha1.PullRequest
	if err := json.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	next := current.DeepCopy()
	next.Status.Head = headA
	if err := updateStatusIfChanged(t.Context(), c, &current, next); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), claimedAt) || !strings.Contains(string(raw), headA) {
		t.Fatalf("observation rewrote claim or lost new head: %s", raw)
	}
}
