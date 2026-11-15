// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package pullrequestcommand

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	codescheme "github.com/railgrid/provider-code/scheme"
)

const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var now = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

type fakeForge struct {
	backend.GitBackend
	backend.Collaboration
	comment     *backend.IssueComment
	commentErr  error
	pr          *backend.PullRequest
	permissions map[string]string
	asked       []string
}

func (f *fakeForge) Name() string { return "github" }
func (f *fakeForge) IssueComment(context.Context, *api.Connection, backend.Credential, *api.Repository, int64) (*backend.IssueComment, error) {
	return f.comment, f.commentErr
}
func (f *fakeForge) RepositoryPermission(_ context.Context, _ *api.Connection, _ backend.Credential, _ *api.Repository, login string) (string, error) {
	f.asked = append(f.asked, login)
	return f.permissions[login], nil
}
func (f *fakeForge) ReadPullRequest(context.Context, *api.Connection, backend.Credential, *api.Repository, int) (*backend.PullRequest, error) {
	return f.pr, nil
}

func fixture(t *testing.T, forge *fakeForge) (*Reconciler, client.Client, *api.PullRequestCommand) {
	t.Helper()
	command := &api.PullRequestCommand{ObjectMeta: metav1.ObjectMeta{Name: "product-comment-77"}, Spec: api.PullRequestCommandSpec{RepositoryRef: "product", CommentID: 77}}
	repo := &api.Repository{ObjectMeta: metav1.ObjectMeta{Name: "product", UID: "repo"}, Spec: api.RepositorySpec{ConnectionRef: "conn", Name: "product"}}
	conn := &api.Connection{ObjectMeta: metav1.ObjectMeta{Name: "conn"}, Spec: api.ConnectionSpec{Type: api.CredentialTypePAT, Provider: api.ProviderGitHub, Owner: "acme", SecretRef: api.LocalSecretReference{Name: "credential", Namespace: "default", Key: "token"}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}}
	c := fake.NewClientBuilder().WithScheme(codescheme.NewScheme()).WithStatusSubresource(command).WithObjects(command, repo, conn, secret).Build()
	registry := backend.NewRegistry()
	if err := registry.Register(forge); err != nil {
		t.Fatal(err)
	}
	return &Reconciler{Backends: registry, Now: func() time.Time { return now }}, c, command
}

func validForge() *fakeForge {
	return &fakeForge{
		comment:     &backend.IssueComment{ID: 77, IssueNumber: 5, URL: "https://github.com/acme/product/pull/5#issuecomment-77", Body: "/railgrid review-fix line-a\nthanks", Author: "maintainer", AuthorType: "User", CreatedAt: now.Add(-time.Minute)},
		pr:          &backend.PullRequest{Number: 5, Repository: "acme/product", HeadRepository: "acme/product", Head: "feature", Base: "main", Commit: head, State: "open"},
		permissions: map[string]string{"maintainer": "write"},
	}
}

func verifyOnce(t *testing.T, r *Reconciler, c client.Client, command *api.PullRequestCommand) *api.PullRequestCommand {
	t.Helper()
	if _, err := r.verify(t.Context(), c, command); err != nil {
		t.Fatal(err)
	}
	var got api.PullRequestCommand
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(command), &got); err != nil {
		t.Fatal(err)
	}
	return &got
}

// What the forge confirms is all that is recorded: the comment's command,
// its author and their permission, and the pull request's head and branches.
func TestVerifiedCommandRecordsWhatTheForgeConfirmed(t *testing.T) {
	forge := validForge()
	r, c, command := fixture(t, forge)
	got := verifyOnce(t, r, c, command)
	s := got.Status
	if s.Phase != api.PullRequestCommandVerified || s.Command != "review-fix" || len(s.Args) != 1 || s.Args[0] != "line-a" || s.Number != 5 || s.Author != "maintainer" || s.AuthorPermission != "write" || s.Head != head || s.HeadBranch != "feature" || s.Base != "main" || s.Fork || s.State != "open" {
		t.Fatalf("verified status = %+v", s)
	}
	// Verification is once: a later reconcile reads nothing.
	forge.comment = nil
	forge.asked = nil
	verifyOnce(t, r, c, got)
	if len(forge.asked) != 0 {
		t.Fatal("a verified command was verified again")
	}
}

func TestCommandsAreRejectedWithAReason(t *testing.T) {
	cases := map[string]func(*fakeForge){
		"not a pull request": func(f *fakeForge) { f.comment.URL = "https://github.com/acme/product/issues/5#issuecomment-77" },
		"stale":              func(f *fakeForge) { f.comment.CreatedAt = now.Add(-2 * time.Hour) },
		"not a command":      func(f *fakeForge) { f.comment.Body = "please review" },
		"no head":            func(f *fakeForge) { f.pr.Commit = "" },
		"head moved":         func(f *fakeForge) { f.comment.Body = "/railgrid review bbbbbbb" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			forge := validForge()
			mutate(forge)
			r, c, command := fixture(t, forge)
			got := verifyOnce(t, r, c, command)
			if got.Status.Phase != api.PullRequestCommandRejected || got.Status.Reason == "" || got.Status.Command != "" {
				t.Fatalf("status = %+v", got.Status)
			}
		})
	}
}

// A bot's command and a fork's pull request are recorded as they are; the
// coordinator decides. A bot's permission is never looked up.
func TestBotsAndForksAreRecordedForTheCoordinatorToRefuse(t *testing.T) {
	forge := validForge()
	forge.comment.AuthorType = "Bot"
	forge.pr.HeadRepository = "someone/product"
	r, c, command := fixture(t, forge)
	got := verifyOnce(t, r, c, command)
	if got.Status.Phase != api.PullRequestCommandVerified || got.Status.AuthorPermission != "none" || !got.Status.Fork || len(forge.asked) != 0 {
		t.Fatalf("status = %+v, permission lookups %v", got.Status, forge.asked)
	}
}

// A comment GitHub does not have is a forged or mistaken delivery: nothing of
// it is kept.
func TestForgedCommentIsDropped(t *testing.T) {
	forge := validForge()
	forge.commentErr = backend.ErrCommentNotFound
	r, c, command := fixture(t, forge)
	if _, err := r.verify(t.Context(), c, command); err != nil {
		t.Fatal(err)
	}
	var got api.PullRequestCommand
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(command), &got); !apierrors.IsNotFound(err) {
		t.Fatalf("forged command still present: %v %+v", err, got.Status)
	}
}

func TestTransientForgeErrorsRetry(t *testing.T) {
	forge := validForge()
	forge.commentErr = errors.New("github unavailable")
	r, c, command := fixture(t, forge)
	if _, err := r.verify(t.Context(), c, command); err == nil {
		t.Fatal("a transient error must be retried")
	}
	var got api.PullRequestCommand
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(command), &got); err != nil || got.Status.Phase != "" {
		t.Fatalf("status after a transient error = %+v (%v)", got.Status, err)
	}
}

func TestTerminalCommandsExpireFromVerificationTime(t *testing.T) {
	for _, phase := range []string{api.PullRequestCommandVerified, api.PullRequestCommandRejected} {
		t.Run(phase, func(t *testing.T) {
			r, c, command := fixture(t, validForge())
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(command), command); err != nil {
				t.Fatal(err)
			}
			verifiedAt := metav1.NewTime(now.Add(-terminalRetention + time.Minute))
			command.Status = api.PullRequestCommandStatus{Phase: phase, VerifiedAt: &verifiedAt}
			if err := c.Status().Update(t.Context(), command); err != nil {
				t.Fatal(err)
			}
			result, err := r.verify(t.Context(), c, command)
			if err != nil || result.RequeueAfter != time.Minute {
				t.Fatalf("retention result = %v, %v", result, err)
			}
			r.Now = func() time.Time { return now.Add(time.Minute) }
			if _, err := r.verify(t.Context(), c, command); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(command), command); !apierrors.IsNotFound(err) {
				t.Fatalf("expired command still present: %v", err)
			}
		})
	}
}

// A command that names the head commit is recorded as pinned; one that names
// nothing is not, so a consumer can tell a commit the commenter chose from a
// head read after the comment.
func TestCommitPinIsRecorded(t *testing.T) {
	for body, want := range map[string]bool{
		"/railgrid review " + head[:7]: true,
		"/railgrid review " + head:     true,
		"/railgrid review":             false,
		"/railgrid review fast":        false,
	} {
		t.Run(body, func(t *testing.T) {
			forge := validForge()
			forge.comment.Body = body
			r, c, command := fixture(t, forge)
			got := verifyOnce(t, r, c, command)
			if got.Status.Phase != api.PullRequestCommandVerified || got.Status.HeadPinned != want {
				t.Fatalf("status = %+v, want headPinned=%v", got.Status, want)
			}
		})
	}
}

// A command Code cannot verify is rejected once no comment that old would be
// accepted, instead of being retried for ever.
func TestUnverifiableCommandIsRejectedAfterTheCommandAge(t *testing.T) {
	forge := validForge()
	forge.commentErr = errors.New("github unavailable")
	r, c, command := fixture(t, forge)
	command.CreationTimestamp = metav1.NewTime(now.Add(-2 * time.Hour))
	got := verifyOnce(t, r, c, command)
	if got.Status.Phase != api.PullRequestCommandRejected || !strings.Contains(got.Status.Reason, "could not be verified") {
		t.Fatalf("status = %+v", got.Status)
	}
}
