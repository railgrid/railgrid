/*
Copyright 2026 The Railgrid Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0
*/

package pullrequest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
)

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tree  = "cccccccccccccccccccccccccccccccccccccccc"
)

// fakeForge is a forge with one branch and, optionally, one pull request.
type fakeForge struct {
	backend.Collaboration
	backend.SnapshotPublisher
	branch    string
	published []backend.Snapshot
	expected  []string
	pr        *backend.PullRequest
	created   int
	reviews   []backend.Review
	comments  []backend.Comment
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

func (f *fakeForge) CreatePullRequest(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, in backend.PullRequestInput) (*backend.PullRequest, error) {
	f.created++
	f.pr = &backend.PullRequest{Number: 17, URL: "https://example.invalid/pull/17", Repository: "acme/product", HeadRepository: "acme/product", Head: in.Head, Base: in.Base, Commit: in.Commit, State: "open"}
	return f.pr, nil
}

func (f *fakeForge) PullRequestFeedback(context.Context, *codev1alpha1.Connection, backend.Credential, *codev1alpha1.Repository, int, string) (*backend.Feedback, error) {
	return &backend.Feedback{Reviews: f.reviews}, nil
}

func (f *fakeForge) ListComments(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, _ int, page int) (*backend.CommentPage, error) {
	if page != 1 {
		return &backend.CommentPage{}, nil
	}
	return &backend.CommentPage{Comments: f.comments}, nil
}

func stage(t *testing.T, dir, cluster string, snapshot backend.Snapshot) string {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	ref := hex.EncodeToString(digest[:])
	tenant := sha256.Sum256([]byte(cluster))
	scope := filepath.Join(dir, hex.EncodeToString(tenant[:]), "some-caller")
	if err := os.MkdirAll(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, ref+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return ref
}

func resource(head *codev1alpha1.PullRequestHead) *codev1alpha1.PullRequest {
	pr := &codev1alpha1.PullRequest{Spec: codev1alpha1.PullRequestSpec{RepositoryRef: "product", Branch: "factory/task-1", Base: "main", Title: "ENG-4: test"}}
	pr.Spec.DesiredHead = head
	return pr
}

// The branch is moved only from the head this resource last observed: the
// first push needs an absent branch, a later one the previous head, and a
// branch that is somewhere else is never overwritten. The staged snapshot
// must be the desired head, and it is published with the named subject.
func TestAdvanceMovesTheBranchUnderAnExpectedHeadLease(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r := &Reconciler{SnapshotDir: dir}
	snapshot := backend.Snapshot{BaseCommit: headB, Commit: headA, Tree: tree, Bundle: []byte("bundle")}
	ref := stage(t, dir, "cluster-1", snapshot)
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
	err := r.advance(ctx, taken, nil, backend.Credential{}, nil, "cluster-1", resource(head), head)
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

// The pull request is opened once: a create whose answer was lost is found
// again rather than opened twice.
func TestOpenAdoptsAnExistingPullRequest(t *testing.T) {
	ctx := context.Background()
	r := &Reconciler{}
	head := &codev1alpha1.PullRequestHead{Commit: headA, BaseCommit: headB, Tree: tree}
	forge := &fakeForge{}
	first, err := r.open(ctx, forge, nil, backend.Credential{}, nil, resource(head))
	if err != nil || first.Number != 17 || forge.created != 1 {
		t.Fatalf("open: %+v %v created=%d", first, err, forge.created)
	}
	again, err := r.open(ctx, forge, nil, backend.Credential{}, nil, resource(head))
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
