// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package backend

import (
	"context"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
)

// Collaboration is the optional host capability behind repository actions.
// It exposes observations and explicit mutations, never engineering policy.
type Collaboration interface {
	BranchHead(context.Context, *api.Connection, Credential, *api.Repository, string) (string, error)
	FindPullRequest(context.Context, *api.Connection, Credential, *api.Repository, PullRequestInput) (*PullRequest, error)
	ReadPullRequest(context.Context, *api.Connection, Credential, *api.Repository, int) (*PullRequest, error)
	CreatePullRequest(context.Context, *api.Connection, Credential, *api.Repository, PullRequestInput) (*PullRequest, error)
	UpdatePullRequest(context.Context, *api.Connection, Credential, *api.Repository, int, PullRequestInput) (*PullRequest, error)
	PullRequestFeedback(context.Context, *api.Connection, Credential, *api.Repository, int, string) (*Feedback, error)
	ListComments(context.Context, *api.Connection, Credential, *api.Repository, int, int) (*CommentPage, error)
	AddComment(context.Context, *api.Connection, Credential, *api.Repository, int, string) (*Comment, error)
	ReplyToReview(context.Context, *api.Connection, Credential, *api.Repository, int, int64, string) (*Comment, error)
}

type PullRequestInput struct {
	Head   string `json:"head"`
	Base   string `json:"base"`
	Commit string `json:"commit"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	Draft  bool   `json:"draft,omitempty"`
}

type PullRequest struct {
	Number           int        `json:"number"`
	NodeID           string     `json:"nodeID,omitempty"`
	URL              string     `json:"url"`
	Repository       string     `json:"repository"`
	HeadRepository   string     `json:"headRepository"`
	Head             string     `json:"head"`
	Base             string     `json:"base"`
	Commit           string     `json:"commit"`
	State            string     `json:"state"`
	Merged           bool       `json:"merged"`
	MergeCommit      string     `json:"mergeCommit,omitempty"`
	MergedAt         *time.Time `json:"mergedAt,omitempty"`
	Merger           string     `json:"merger,omitempty"`
	MergerType       string     `json:"mergerType,omitempty"`
	Draft            bool       `json:"draft,omitempty"`
	BaseHead         string     `json:"baseHead,omitempty"`
	BaseCurrent      *bool      `json:"baseCurrent,omitempty"`
	BaseCurrentError string     `json:"baseCurrentError,omitempty"`
	MergeableState   string     `json:"mergeableState,omitempty"`
}

type Check struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	AppID      int64  `json:"appID"`
	Head       string `json:"head"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"detailsURL"`
	Output     string `json:"output"`
}
type Review struct {
	ID          int64     `json:"id"`
	Login       string    `json:"login"`
	ActorType   string    `json:"actorType"`
	Commit      string    `json:"commit"`
	State       string    `json:"state"`
	Body        string    `json:"body"`
	SubmittedAt time.Time `json:"submittedAt"`
}
type Feedback struct {
	Checks  []Check  `json:"checks"`
	Reviews []Review `json:"reviews"`
}
type Comment struct {
	ID         int64     `json:"id"`
	URL        string    `json:"url"`
	Body       string    `json:"body"`
	Author     string    `json:"author"`
	AuthorType string    `json:"authorType"`
	UpdatedAt  time.Time `json:"updatedAt"`
}
type CommentPage struct {
	Comments []Comment `json:"comments"`
	NextPage int       `json:"nextPage"`
}

// BranchLister lists a bounded page using the registered repository identity.
type BranchLister interface {
	ListBranches(context.Context, *api.Connection, Credential, *api.Repository, int) (*BranchPage, error)
}

// PullRequestBaseObserver optionally reads the current base branch tip and
// compares it with the PR head. It is separate from ordinary PR reads because
// ancestry checks are extra forge calls.
type PullRequestBaseObserver interface {
	ObservePullRequestBase(context.Context, *api.Connection, Credential, *api.Repository, *PullRequest) (string, *bool, string, error)
}
type BranchPage struct {
	Branches []string `json:"branches"`
	NextPage int      `json:"nextPage"`
}
