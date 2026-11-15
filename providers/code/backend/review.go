// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
)

// ReviewOperations contains explicit, idempotent review lifecycle operations.
// Kept separate from Collaboration so host adapters can add them independently.
type ReviewOperations interface {
	SubmitReview(context.Context, *api.Connection, Credential, *api.Repository, ReviewInput) (*ReviewResult, error)
	UpsertManagedComment(context.Context, *api.Connection, Credential, *api.Repository, ManagedCommentInput) (*Comment, error)
	MarkPullRequestReady(context.Context, *api.Connection, Credential, *api.Repository, ReadyInput) (*ReadyResult, error)
}

// ErrManagedCommentMissing means the pinned comment is absent from a complete listing.
var ErrManagedCommentMissing = errors.New("the managed comment is missing")

// ReviewWriteRejected indicates the attempt can safely release its forge claim.
type ReviewWriteRejected struct {
	Err       error
	Permanent bool
}

func (e *ReviewWriteRejected) Error() string { return e.Err.Error() }
func (e *ReviewWriteRejected) Unwrap() error { return e.Err }

type DiffAnchor struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side,omitempty"`
}

type ReviewInput struct {
	Number         int          `json:"number"`
	ExpectedHead   string       `json:"expectedHead"`
	IdempotencyKey string       `json:"idempotencyKey"`
	Body           string       `json:"body"`
	Comments       []ReviewNote `json:"comments,omitempty"`
	RecoverOnly    bool         `json:"-"`
	Scope          string       `json:"-"`
}

var reviewKeyRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func ValidateReviewInput(in ReviewInput) error {
	if in.Number <= 0 || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(in.ExpectedHead) || !reviewKeyRE.MatchString(in.IdempotencyKey) || len(in.Body) > 32768 || len(in.Comments) > 50 {
		return errors.New("invalid review input")
	}
	for _, note := range in.Comments {
		if note.Path == "" || len(note.Path) > 1024 || note.Line <= 0 || strings.TrimSpace(note.Body) == "" || len(note.Body) > 8192 || (note.Side != "" && note.Side != "RIGHT" && note.Side != "LEFT") {
			return errors.New("invalid review note")
		}
	}
	return nil
}

type ReviewNote struct {
	DiffAnchor
	Body string `json:"body"`
}

type ReviewResult struct {
	Review     Review       `json:"review"`
	Unanchored []ReviewNote `json:"unanchored,omitempty"`
}

type ManagedCommentInput struct {
	Number      int    `json:"number"`
	Key         string `json:"key"`
	Body        string `json:"body"`
	CommentID   int64  `json:"-"`
	RecoverOnly bool   `json:"-"`
	Scope       string `json:"-"`
}

type ReadyInput struct {
	Number       int    `json:"number"`
	ExpectedHead string `json:"expectedHead"`
}

type ReadyResult struct {
	PullRequest  *PullRequest `json:"pullRequest"`
	ExpectedHead string       `json:"expectedHead"`
	HeadMatched  bool         `json:"headMatched"`
	Ready        bool         `json:"ready"`
}

// ReviewFingerprint binds an idempotency key to the exact review payload.
func ReviewFingerprint(in ReviewInput) string {
	payload, _ := json.Marshal(struct {
		Body     string
		Comments []ReviewNote
	}{in.Body, in.Comments})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func CommentFingerprint(in ManagedCommentInput) string {
	digest := sha256.Sum256([]byte(in.Body))
	return hex.EncodeToString(digest[:])
}

func ValidateManagedCommentInput(in ManagedCommentInput) error {
	if in.Number <= 0 || !reviewKeyRE.MatchString(in.Key) || strings.TrimSpace(in.Body) == "" || len(in.Body) > 32768 {
		return errors.New("invalid managed comment")
	}
	return nil
}
