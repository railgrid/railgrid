// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package pullrequestcommand verifies the "/railgrid" commands the commands
// webhook recorded. Verification reads everything back from the forge with
// the Repository's own credential, once: the comment, its author's current
// repository permission, and the pull request's head, branches and state.
package pullrequestcommand

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/commandhook"
	"github.com/railgrid/provider-code/controller/shared"
)

// MaxCommandAge is how old a comment may be when Code verifies it. GitHub
// delivers within seconds; an older comment is a replay or a long outage, and
// the person can leave the command again.
const MaxCommandAge = time.Hour

var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

// host is the backend as this reconciler needs it.
type host interface {
	backend.Collaboration
	backend.CommandReader
}

// Reconciler verifies PullRequestCommands.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
	Now      func() time.Time
}

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-pullrequestcommand").
		For(&codev1alpha1.PullRequestCommand{}).
		Complete(r)
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile verifies one command.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	var command codev1alpha1.PullRequestCommand
	if err := c.Get(ctx, req.NamespacedName, &command); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	return r.verify(ctx, c, &command)
}

func (r *Reconciler) verify(ctx context.Context, c client.Client, command *codev1alpha1.PullRequestCommand) (ctrl.Result, error) {
	if !command.DeletionTimestamp.IsZero() || command.Status.Phase == codev1alpha1.PullRequestCommandVerified || command.Status.Phase == codev1alpha1.PullRequestCommandRejected {
		return ctrl.Result{}, nil
	}
	repo, err := shared.ResolveRepository(ctx, c, command.Spec.RepositoryRef)
	if err != nil {
		return ctrl.Result{}, r.reject(ctx, c, command, err.Error())
	}
	conn, err := shared.ResolveConnection(ctx, c, repo.Spec.ConnectionRef)
	if err != nil {
		return ctrl.Result{}, err
	}
	if r.Backends == nil {
		return ctrl.Result{}, errors.New("git backends are unavailable")
	}
	gitBackend, ok := r.Backends.Get(string(conn.Spec.Provider))
	forge, supported := gitBackend.(host)
	if !ok || !supported {
		return ctrl.Result{}, r.reject(ctx, c, command, fmt.Sprintf("git provider %q does not support pull request commands", conn.Spec.Provider))
	}
	cred, err := shared.ResolveCredential(ctx, c, conn)
	if err != nil {
		return ctrl.Result{}, err
	}
	comment, err := forge.IssueComment(ctx, conn, cred, repo, command.Spec.CommentID)
	if errors.Is(err, backend.ErrCommentNotFound) {
		// The repository has no such comment: a forged or mistaken delivery.
		// Nothing about it is worth keeping.
		klog.FromContext(ctx).Info("dropping pull request command for a comment GitHub does not have", "command", command.Name)
		return ctrl.Result{}, client.IgnoreNotFound(c.Delete(ctx, command))
	}
	if err != nil {
		return r.retry(err)
	}
	if !strings.Contains(comment.URL, fmt.Sprintf("/pull/%d", comment.IssueNumber)) {
		return ctrl.Result{}, r.reject(ctx, c, command, "the comment is not on a pull request")
	}
	if age := r.now().Sub(comment.CreatedAt); age > MaxCommandAge {
		return ctrl.Result{}, r.reject(ctx, c, command, "the comment is more than an hour old; leave the command again")
	}
	name, args, reason, ok := commandhook.Parse(comment.Body)
	if !ok {
		return ctrl.Result{}, r.reject(ctx, c, command, reason)
	}
	pr, err := forge.ReadPullRequest(ctx, conn, cred, repo, comment.IssueNumber)
	if err != nil {
		return r.retry(err)
	}
	if pr == nil || !commitPattern.MatchString(pr.Commit) {
		return ctrl.Result{}, r.reject(ctx, c, command, "the pull request has no readable head commit")
	}
	permission := "none"
	if comment.AuthorType == "User" {
		if permission, err = forge.RepositoryPermission(ctx, conn, cred, repo, comment.Author); err != nil {
			return r.retry(err)
		}
	}
	commented, verified := metav1.NewTime(comment.CreatedAt), metav1.NewTime(r.now())
	command.Status = codev1alpha1.PullRequestCommandStatus{
		Phase: codev1alpha1.PullRequestCommandVerified, Number: comment.IssueNumber, URL: comment.URL,
		Author: comment.Author, AuthorType: comment.AuthorType, AuthorPermission: permission,
		Command: name, Args: args,
		Head: pr.Commit, HeadBranch: pr.Head, Base: pr.Base, Fork: !strings.EqualFold(pr.HeadRepository, pr.Repository),
		State: pr.State, Merged: pr.Merged, Draft: pr.Draft,
		CommentedAt: &commented, VerifiedAt: &verified,
	}
	return ctrl.Result{}, c.Status().Update(ctx, command)
}

func (r *Reconciler) retry(err error) (ctrl.Result, error) {
	if wait, _, ok := shared.RateLimitWait(err, r.now()); ok {
		return ctrl.Result{RequeueAfter: wait}, nil
	}
	return ctrl.Result{}, err
}

func (r *Reconciler) reject(ctx context.Context, c client.Client, command *codev1alpha1.PullRequestCommand, reason string) error {
	if len(reason) > 512 {
		reason = reason[:512]
	}
	verified := metav1.NewTime(r.now())
	command.Status = codev1alpha1.PullRequestCommandStatus{Phase: codev1alpha1.PullRequestCommandRejected, Reason: reason, VerifiedAt: &verified}
	return c.Status().Update(ctx, command)
}
