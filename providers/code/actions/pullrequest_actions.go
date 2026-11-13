// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
)

func (s *Server) runPullRequest(ctx context.Context, provider dynamic.Interface, req dataplane.Request, visible *unstructured.Unstructured, raw json.RawMessage) (any, *actionwire.Error) {
	in, err := decodeInput(raw)
	if err != nil {
		return nil, wireError("invalid_action_input")
	}
	var pr api.PullRequest
	if runtime.DefaultUnstructuredConverter.FromUnstructured(visible.Object, &pr) != nil || pr.DeletionTimestamp != nil || pr.Status.Number <= 0 {
		return nil, wireError("action_not_found")
	}
	if in.Number != 0 && in.Number != pr.Status.Number {
		return nil, wireError("identity_conflict")
	}
	repoRef := pr.Spec.RepositoryRef
	if repoRef == "" {
		return nil, wireError("identity_conflict")
	}
	repoObject, err := provider.Resource(repositories).Get(ctx, repoRef, metav1.GetOptions{})
	if err != nil {
		return nil, wireError("action_not_found")
	}
	var repo api.Repository
	if runtime.DefaultUnstructuredConverter.FromUnstructured(repoObject.Object, &repo) != nil || repo.DeletionTimestamp != nil {
		return nil, wireError("action_not_found")
	}
	connObject, err := provider.Resource(connections).Get(ctx, repo.Spec.ConnectionRef, metav1.GetOptions{})
	if err != nil {
		return nil, wireError("action_not_found")
	}
	var connection api.Connection
	if runtime.DefaultUnstructuredConverter.FromUnstructured(connObject.Object, &connection) != nil || connection.DeletionTimestamp != nil {
		return nil, wireError("action_not_found")
	}
	repositoryOwner := repo.Spec.Owner
	if repositoryOwner == "" {
		repositoryOwner = connection.Spec.Owner
	}
	in.Repository = repositoryOwner + "/" + repo.Spec.Name
	in.RepositoryUID = string(repoObject.GetUID())
	in.ConnectionUID = string(connObject.GetUID())
	binding, err := s.pinRepositoryBinding(ctx, provider, repoObject, in.RepositoryUID, in.ConnectionUID, in.Repository)
	if err != nil {
		return nil, wireError("action_forbidden")
	}
	credential, err := s.Credentials.ResolveStored(ctx, binding.conn, binding.secretKey, binding.data, binding.store)
	if err != nil {
		return nil, wireError("action_forbidden")
	}
	conn, resolvedRepo := binding.conn, binding.repo
	implementation, ok := s.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		return nil, wireError("unsupported_provider")
	}
	ops, ok := implementation.(backend.ReviewOperations)
	if !ok {
		return nil, wireError("unsupported_action")
	}
	collab, ok := implementation.(backend.Collaboration)
	if !ok {
		return nil, wireError("unsupported_action")
	}
	observed, err := collab.ReadPullRequest(ctx, conn, credential, resolvedRepo, pr.Status.Number)
	if err != nil {
		return nil, wireError("upstream_outcome_unconfirmed")
	}
	if observed.Number != pr.Status.Number || observed.Head != pr.Spec.Branch || observed.Base != pr.Spec.Base || observed.HeadRepository != observed.Repository {
		return nil, wireError("identity_conflict")
	}
	if (req.Verb == "submit-review" || req.Verb == "upsert-comment") && conn.Spec.Type == api.CredentialTypeGitHubApp {
		actor, actorType, actorErr := s.Credentials.ResolveActor(ctx, conn, binding.data)
		if actorErr != nil {
			return nil, wireError("action_unavailable")
		}
		credential.ActorLogin, credential.ActorType = actor, actorType
	}
	switch req.Verb {
	case "submit-review":
		reviewInput := backend.ReviewInput{Number: pr.Status.Number, ExpectedHead: in.ExpectedHead, IdempotencyKey: in.IdempotencyKey, Body: in.Body, Comments: in.Comments, Scope: string(visible.GetUID())}
		if backend.ValidateReviewInput(reviewInput) != nil {
			return nil, wireError("invalid_action_input")
		}
		if observed.Commit != in.ExpectedHead || observed.State != "open" || observed.Merged {
			return nil, wireError("identity_conflict")
		}
		fingerprint := backend.ReviewFingerprint(reviewInput)
		claimedAt := time.Now().UTC().Format(time.RFC3339Nano)
		claimed, claimErr := claimReviewOperation(ctx, provider, req.Name, visible.GetUID(), pr.Spec.RepositoryRef, pr.Spec.Branch, pr.Spec.Base, pr.Status.Number, in.IdempotencyKey, in.ExpectedHead, fingerprint, claimedAt)
		if claimErr != nil {
			return nil, wireError("review_operation_conflict")
		}
		reviewInput.RecoverOnly = !claimed
		result, callErr := ops.SubmitReview(ctx, conn, credential, resolvedRepo, reviewInput)
		if callErr != nil {
			var rejected *backend.ReviewWriteRejected
			if errors.As(callErr, &rejected) {
				if claimed {
					releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					_, releaseErr := updateReviewOperation(releaseCtx, provider, req.Name, visible.GetUID(), pr.Spec.RepositoryRef, pr.Spec.Branch, pr.Spec.Base, pr.Status.Number, in.IdempotencyKey, in.ExpectedHead, fingerprint, "Release", claimedAt, 0)
					cancel()
					if releaseErr != nil {
						return nil, wireErrorRetryable("upstream_outcome_unconfirmed", true)
					}
				}
				if rejected.Permanent {
					return nil, wireErrorRetryable("upstream_outcome_unconfirmed", false)
				}
			}
			if errors.Is(callErr, backend.ErrRepositoryIdentityConflict) {
				return nil, wireError("identity_conflict")
			}
			return nil, wireErrorRetryable("upstream_outcome_unconfirmed", true)
		}
		if result == nil || result.Review.ID <= 0 || result.Review.Commit != in.ExpectedHead {
			return nil, wireError("upstream_outcome_unconfirmed")
		}
		if err := completeReviewOperation(ctx, provider, req.Name, visible.GetUID(), pr.Spec.RepositoryRef, pr.Spec.Branch, pr.Spec.Base, pr.Status.Number, in.IdempotencyKey, in.ExpectedHead, fingerprint, result.Review.ID); err != nil {
			return nil, wireErrorRetryable("upstream_outcome_unconfirmed", true)
		}
		return result, nil
	case "upsert-comment":
		commentInput := backend.ManagedCommentInput{Number: pr.Status.Number, Key: in.CommentKey, Body: in.Body, Scope: string(visible.GetUID())}
		if backend.ValidateManagedCommentInput(commentInput) != nil {
			return nil, wireError("invalid_action_input")
		}
		fingerprint := backend.CommentFingerprint(commentInput)
		claimedAt := time.Now().UTC().Format(time.RFC3339Nano)
		claimed, commentID, claimErr := claimCommentOperation(ctx, provider, req.Name, visible.GetUID(), pr.Spec.RepositoryRef, pr.Spec.Branch, pr.Spec.Base, pr.Status.Number, in.CommentKey, fingerprint, claimedAt)
		if claimErr != nil {
			return nil, wireError("review_operation_conflict")
		}
		commentInput.CommentID = commentID
		commentInput.RecoverOnly = !claimed
		comment, callErr := ops.UpsertManagedComment(ctx, conn, credential, resolvedRepo, commentInput)
		if callErr != nil {
			if !claimed && errors.Is(callErr, backend.ErrManagedCommentMissing) {
				// The backend reports missing only after confirming the pinned
				// comment ID is absent and no owned marker can be adopted. The
				// receipt can then be discarded so a later request may create again.
				_, _, err := updateCommentOperation(ctx, provider, req.Name, visible.GetUID(), pr.Spec.RepositoryRef, pr.Spec.Branch, pr.Spec.Base, pr.Status.Number, in.CommentKey, fingerprint, "ForgetMissing", "", commentID)
				if err != nil {
					return nil, wireErrorRetryable("upstream_outcome_unconfirmed", true)
				}
			}
			var rejected *backend.ReviewWriteRejected
			if errors.As(callErr, &rejected) {
				if claimed {
					releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					_, _, releaseErr := updateCommentOperation(releaseCtx, provider, req.Name, visible.GetUID(), pr.Spec.RepositoryRef, pr.Spec.Branch, pr.Spec.Base, pr.Status.Number, in.CommentKey, fingerprint, "Release", claimedAt, 0)
					cancel()
					if releaseErr != nil {
						return nil, wireErrorRetryable("upstream_outcome_unconfirmed", true)
					}
				}
				if rejected.Permanent {
					return nil, wireErrorRetryable("upstream_outcome_unconfirmed", false)
				}
			}
			if errors.Is(callErr, backend.ErrRepositoryIdentityConflict) {
				return nil, wireError("identity_conflict")
			}
			return nil, wireErrorRetryable("upstream_outcome_unconfirmed", true)
		}
		if comment == nil || comment.ID <= 0 {
			return nil, wireError("upstream_outcome_unconfirmed")
		}
		if err := completeCommentOperation(ctx, provider, req.Name, visible.GetUID(), pr.Spec.RepositoryRef, pr.Spec.Branch, pr.Spec.Base, pr.Status.Number, in.CommentKey, fingerprint, comment.ID); err != nil {
			return nil, wireErrorRetryable("upstream_outcome_unconfirmed", true)
		}
		return comment, nil
	case "ready":
		if observed.Commit != in.ExpectedHead {
			return nil, wireError("identity_conflict")
		}
		ready, callErr := ops.MarkPullRequestReady(ctx, conn, credential, resolvedRepo, backend.ReadyInput{Number: pr.Status.Number, ExpectedHead: in.ExpectedHead})
		if callErr != nil {
			if errors.Is(callErr, backend.ErrRepositoryIdentityConflict) {
				return nil, wireError("identity_conflict")
			}
			return nil, wireErrorRetryable("upstream_outcome_unconfirmed", true)
		}
		return ready, nil
	default:
		return nil, wireError("unsupported_action")
	}
}
