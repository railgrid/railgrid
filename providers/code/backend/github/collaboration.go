// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v66/github"
	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
)

var _ backend.Collaboration = (*Backend)(nil)
var objectID = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (b *Backend) collaborationClient(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository) (*gh.Client, error) {
	if repo.Status.RepoID == "" || repo.DeletionTimestamp != nil || conn.DeletionTimestamp != nil {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return nil, err
	}
	remote, response, err := c.Repositories.Get(ctx, owner(conn, repo), repo.Spec.Name)
	if err != nil {
		return nil, classify(response, err)
	}
	if remote.GetID() <= 0 || strconv.FormatInt(remote.GetID(), 10) != repo.Status.RepoID || !strings.EqualFold(remote.GetFullName(), owner(conn, repo)+"/"+repo.Spec.Name) {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	return c, nil
}
func validBranch(branch string) bool {
	if branch == "" || len(branch) > 255 || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.ContainsAny(branch, " ~^:?*[\\\t\r\n\x00") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") || branch == "@" {
		return false
	}
	for _, part := range strings.Split(branch, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
func (b *Backend) BranchHead(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, branch string) (string, error) {
	if !validBranch(branch) {
		return "", errors.New("invalid branch")
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return "", err
	}
	ref, response, err := c.Git.GetRef(ctx, owner(conn, repo), repo.Spec.Name, "heads/"+branch)
	if response != nil && response.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if err != nil {
		return "", classify(response, err)
	}
	if ref.GetRef() != "refs/heads/"+branch || !objectID.MatchString(ref.GetObject().GetSHA()) {
		return "", backend.ErrRepositoryIdentityConflict
	}
	return ref.GetObject().GetSHA(), nil
}
func pullResult(pr *gh.PullRequest) *backend.PullRequest {
	if pr == nil {
		return nil
	}
	result := &backend.PullRequest{Number: pr.GetNumber(), NodeID: pr.GetNodeID(), URL: pr.GetHTMLURL(), Repository: pr.GetBase().GetRepo().GetFullName(), HeadRepository: pr.GetHead().GetRepo().GetFullName(), Head: pr.GetHead().GetRef(), Base: pr.GetBase().GetRef(), Commit: pr.GetHead().GetSHA(), State: pr.GetState(), Merged: pr.GetMerged(), Draft: pr.GetDraft(), BaseHead: pr.GetBase().GetSHA(), MergeableState: pr.GetMergeableState()}
	// GitHub fills merge_commit_sha for OPEN pull requests too: it is the sha
	// of a trial merge it computes for mergeability, not evidence of a merge.
	// Only a merged pull request carries merge proof.
	if pr.GetMerged() {
		result.MergeCommit = pr.GetMergeCommitSHA()
		result.Merger = pr.GetMergedBy().GetLogin()
		result.MergerType = pr.GetMergedBy().GetType()
	}
	if !pr.GetMergedAt().IsZero() {
		value := pr.GetMergedAt().Time
		result.MergedAt = &value
	}
	return result
}
func validPullInput(in backend.PullRequestInput) bool {
	return validBranch(in.Head) && validBranch(in.Base) && in.Head != in.Base && objectID.MatchString(in.Commit)
}
func pullMatches(pr *backend.PullRequest, conn *api.Connection, repo *api.Repository, in backend.PullRequestInput) bool {
	canonical := owner(conn, repo) + "/" + repo.Spec.Name
	return pr != nil && pr.Number > 0 && strings.EqualFold(pr.Repository, canonical) && strings.EqualFold(pr.HeadRepository, canonical) && pr.Head == in.Head && pr.Base == in.Base && pr.Commit == in.Commit
}
func (b *Backend) FindPullRequest(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, in backend.PullRequestInput) (*backend.PullRequest, error) {
	if !validPullInput(in) {
		return nil, errors.New("invalid pull request binding")
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	var found *backend.PullRequest
	for page := 1; page <= 20; page++ {
		list, response, err := c.PullRequests.List(ctx, owner(conn, repo), repo.Spec.Name, &gh.PullRequestListOptions{State: "all", Head: owner(conn, repo) + ":" + in.Head, Base: in.Base, ListOptions: gh.ListOptions{PerPage: 50, Page: page}})
		if err != nil {
			return nil, classify(response, err)
		}
		for _, raw := range list {
			result := pullResult(raw)
			if !pullMatches(result, conn, repo, in) {
				continue
			}
			if found != nil {
				return nil, errors.New("ambiguous pull request identity")
			}
			found = result
		}
		if response == nil || response.NextPage == 0 {
			return found, nil
		}
	}
	return nil, errors.New("pull request search exceeds limit")
}
func (b *Backend) ReadPullRequest(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, number int) (*backend.PullRequest, error) {
	if number <= 0 {
		return nil, errors.New("pull request number required")
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	raw, response, err := c.PullRequests.Get(ctx, owner(conn, repo), repo.Spec.Name, number)
	if err != nil {
		return nil, classify(response, err)
	}
	result := pullResult(raw)
	if result.Number != number || !strings.EqualFold(result.Repository, owner(conn, repo)+"/"+repo.Spec.Name) {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	return result, nil
}

// ContainsCommit reports whether head's history contains ancestor: the
// comparison from ancestor to head is "ahead" or "identical".
func (b *Backend) ContainsCommit(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, ancestor, head string) (bool, error) {
	if !objectID.MatchString(ancestor) || !objectID.MatchString(head) {
		return false, errors.New("commit identity is invalid")
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return false, err
	}
	comparison, response, err := c.Repositories.CompareCommits(ctx, owner(conn, repo), repo.Spec.Name, ancestor, head, &gh.ListOptions{PerPage: 1})
	if err != nil {
		return false, classify(response, err)
	}
	if comparison == nil || comparison.GetBaseCommit().GetSHA() != ancestor || comparison.GetStatus() == "" {
		return false, errors.New("forge returned an incomplete commit comparison")
	}
	return comparison.GetStatus() == "ahead" || comparison.GetStatus() == "identical", nil
}

var _ backend.CommitAncestry = (*Backend)(nil)

// ObservePullRequestBase compares immutable commit IDs after resolving the actual
// current base branch tip. The PR payload's base SHA can lag the branch itself.
func (b *Backend) ObservePullRequestBase(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, pr *backend.PullRequest) (string, *bool, string, error) {
	if !validBranch(pr.Base) || !objectID.MatchString(pr.Commit) {
		return "", nil, "", errors.New("pull request base or head identity is invalid")
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return "", nil, "", err
	}
	branch, response, err := c.Repositories.GetBranch(ctx, owner(conn, repo), repo.Spec.Name, pr.Base, 0)
	if err != nil {
		return "", nil, "reading current base branch: " + classify(response, err).Error(), nil
	}
	baseSHA := branch.GetCommit().GetSHA()
	if branch.GetName() != pr.Base || !objectID.MatchString(baseSHA) {
		return "", nil, "current base branch returned an invalid identity", nil
	}
	comparison, response, err := c.Repositories.CompareCommits(ctx, owner(conn, repo), repo.Spec.Name, baseSHA, pr.Commit, &gh.ListOptions{PerPage: 1})
	if err != nil {
		return baseSHA, nil, "comparing current base with pull request head: " + classify(response, err).Error(), nil
	}
	if comparison == nil || comparison.GetBaseCommit().GetSHA() != baseSHA || comparison.GetStatus() == "" || comparison.BehindBy == nil {
		return baseSHA, nil, "forge returned an incomplete base comparison", nil
	}
	current := (comparison.GetStatus() == "ahead" || comparison.GetStatus() == "identical") && comparison.GetBehindBy() == 0
	return baseSHA, &current, "", nil
}
func (b *Backend) CreatePullRequest(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, in backend.PullRequestInput) (*backend.PullRequest, error) {
	if !validPullInput(in) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 256 || len(in.Body) > 32768 {
		return nil, errors.New("invalid pull request input")
	}
	head, err := b.BranchHead(ctx, conn, cred, repo, in.Head)
	if err != nil {
		return nil, err
	}
	if head != in.Commit {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	raw, response, err := c.PullRequests.Create(ctx, owner(conn, repo), repo.Spec.Name, &gh.NewPullRequest{Title: &in.Title, Body: &in.Body, Head: &in.Head, Base: &in.Base, Draft: gh.Bool(in.Draft)})
	if err != nil {
		return nil, classify(response, err)
	}
	result := pullResult(raw)
	if !pullMatches(result, conn, repo, in) {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	return result, nil
}
func (b *Backend) UpdatePullRequest(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, number int, in backend.PullRequestInput) (*backend.PullRequest, error) {
	if !validPullInput(in) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 256 || len(in.Body) > 32768 {
		return nil, errors.New("invalid pull request input")
	}
	current, err := b.ReadPullRequest(ctx, conn, cred, repo, number)
	if err != nil {
		return nil, err
	}
	if !pullMatches(current, conn, repo, in) {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	raw, response, err := c.PullRequests.Edit(ctx, owner(conn, repo), repo.Spec.Name, number, &gh.PullRequest{Title: &in.Title, Body: &in.Body})
	if err != nil {
		return nil, classify(response, err)
	}
	result := pullResult(raw)
	if !pullMatches(result, conn, repo, in) {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	return result, nil
}
func (b *Backend) PullRequestFeedback(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, number int, commit string) (*backend.Feedback, error) {
	if !objectID.MatchString(commit) {
		return nil, errors.New("feedback commit required")
	}
	before, err := b.ReadPullRequest(ctx, conn, cred, repo, number)
	if err != nil {
		return nil, err
	}
	if before.Commit != commit || !strings.EqualFold(before.HeadRepository, before.Repository) {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	result := &backend.Feedback{Checks: []backend.Check{}, Reviews: []backend.Review{}}
	expectedChecks := -1
	seenChecks := map[int64]bool{}
	seenReviews := map[int64]bool{}
	permissions := reviewerPermissions{client: c, owner: owner(conn, repo), repo: repo.Spec.Name, levels: map[string]string{}}
	for page := 1; page <= 20; page++ {
		checks, response, err := c.Checks.ListCheckRunsForRef(ctx, owner(conn, repo), repo.Spec.Name, commit, &gh.ListCheckRunsOptions{ListOptions: gh.ListOptions{Page: page, PerPage: 50}})
		if err != nil {
			return nil, classify(response, err)
		}
		if checks == nil || len(checks.CheckRuns) > 50 || checks.GetTotal() < 0 {
			return nil, errors.New("invalid check page")
		}
		if expectedChecks < 0 {
			expectedChecks = checks.GetTotal()
		} else if expectedChecks != checks.GetTotal() {
			return nil, errors.New("check count changed")
		}
		for _, check := range checks.CheckRuns {
			if check.GetID() <= 0 || check.GetApp().GetID() <= 0 || check.GetName() == "" || seenChecks[check.GetID()] {
				return nil, errors.New("invalid or duplicate check identity")
			}
			seenChecks[check.GetID()] = true
			if check.GetHeadSHA() != commit {
				return nil, backend.ErrRepositoryIdentityConflict
			}
			output := check.GetOutput().GetTitle() + "\n" + check.GetOutput().GetSummary() + "\n" + check.GetOutput().GetText()
			output, err = checkDiagnostic(ctx, c, owner(conn, repo), repo.Spec.Name, check, output)
			if err != nil {
				return nil, err
			}
			result.Checks = append(result.Checks, backend.Check{ID: check.GetID(), Name: check.GetName(), AppID: check.GetApp().GetID(), Head: commit, Status: check.GetStatus(), Conclusion: check.GetConclusion(), DetailsURL: check.GetDetailsURL(), Output: output})
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		if page == 20 {
			return nil, errors.New("check pagination exceeds limit")
		}
	}
	if len(result.Checks) != expectedChecks {
		return nil, errors.New("incomplete check listing")
	}
	for page := 1; page <= 20; page++ {
		reviews, response, err := c.PullRequests.ListReviews(ctx, owner(conn, repo), repo.Spec.Name, number, &gh.ListOptions{Page: page, PerPage: 50})
		if err != nil {
			return nil, classify(response, err)
		}
		if len(reviews) > 50 {
			return nil, errors.New("invalid review page")
		}
		for _, review := range reviews {
			if review.GetID() <= 0 || seenReviews[review.GetID()] || review.GetUser().GetLogin() == "" || review.GetUser().GetType() == "" || !objectID.MatchString(review.GetCommitID()) {
				return nil, errors.New("invalid or duplicate review identity")
			}
			seenReviews[review.GetID()] = true
			if review.GetCommitID() != commit || review.GetState() == "PENDING" {
				continue
			}
			if review.GetSubmittedAt().IsZero() {
				return nil, errors.New("review timestamp missing")
			}
			body, err := reviewDiagnostic(ctx, c, owner(conn, repo), repo.Spec.Name, number, review, commit)
			if err != nil {
				return nil, err
			}
			permission := ""
			if review.GetState() == "CHANGES_REQUESTED" && review.GetUser().GetType() == "User" {
				permission, err = permissions.of(ctx, review.GetUser().GetLogin())
				if err != nil {
					return nil, err
				}
			}
			result.Reviews = append(result.Reviews, backend.Review{ID: review.GetID(), Login: review.GetUser().GetLogin(), ActorType: review.GetUser().GetType(), Commit: review.GetCommitID(), State: review.GetState(), Body: body, SubmittedAt: review.GetSubmittedAt().Time, AuthorPermission: permission})
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		if page == 20 {
			return nil, errors.New("review pagination exceeds limit")
		}
	}
	after, err := b.ReadPullRequest(ctx, conn, cred, repo, number)
	if err != nil {
		return nil, err
	}
	if after.Commit != before.Commit || after.Head != before.Head || after.HeadRepository != before.HeadRepository || after.Base != before.Base || after.State != before.State || after.Merged != before.Merged {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 480<<10 {
		return nil, errors.New("feedback exceeds bounded response")
	}
	return result, nil
}
func issueComment(raw *gh.IssueComment) *backend.Comment {
	return &backend.Comment{ID: raw.GetID(), URL: raw.GetHTMLURL(), Body: raw.GetBody(), Author: raw.GetUser().GetLogin(), AuthorType: raw.GetUser().GetType(), UpdatedAt: raw.GetUpdatedAt().Time}
}
func (b *Backend) ListComments(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, number, page int) (*backend.CommentPage, error) {
	if number <= 0 || page < 1 || page > 20 {
		return nil, errors.New("invalid comment page")
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	list, response, err := c.Issues.ListComments(ctx, owner(conn, repo), repo.Spec.Name, number, &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{Page: page, PerPage: 50}})
	if err != nil {
		return nil, classify(response, err)
	}
	result := &backend.CommentPage{Comments: []backend.Comment{}}
	for _, raw := range list {
		result.Comments = append(result.Comments, *issueComment(raw))
	}
	if response != nil {
		result.NextPage = response.NextPage
	}
	return result, nil
}
func (b *Backend) AddComment(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, number int, body string) (*backend.Comment, error) {
	if number <= 0 || strings.TrimSpace(body) == "" || len(body) > 32768 {
		return nil, errors.New("invalid comment input")
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	raw, response, err := c.Issues.CreateComment(ctx, owner(conn, repo), repo.Spec.Name, number, &gh.IssueComment{Body: &body})
	if err != nil {
		return nil, classify(response, err)
	}
	return issueComment(raw), nil
}
func (b *Backend) ReplyToReview(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, number int, parent int64, body string) (*backend.Comment, error) {
	if number <= 0 || parent <= 0 || strings.TrimSpace(body) == "" || len(body) > 32768 {
		return nil, errors.New("invalid review reply input")
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	raw, response, err := c.PullRequests.CreateCommentInReplyTo(ctx, owner(conn, repo), repo.Spec.Name, number, body, parent)
	if err != nil {
		return nil, classify(response, err)
	}
	return &backend.Comment{ID: raw.GetID(), URL: raw.GetHTMLURL(), Body: raw.GetBody(), Author: raw.GetUser().GetLogin(), AuthorType: raw.GetUser().GetType(), UpdatedAt: raw.GetUpdatedAt().Time}, nil
}

func (b *Backend) ListBranches(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, page int) (*backend.BranchPage, error) {
	if page == 0 {
		page = 1
	}
	if page < 1 || page > 10000 {
		return nil, errors.New("invalid branch page")
	}
	client, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	branches, response, err := client.Repositories.ListBranches(ctx, owner(conn, repo), repo.Spec.Name, &gh.BranchListOptions{ListOptions: gh.ListOptions{Page: page, PerPage: 50}})
	if err != nil {
		return nil, classify(response, err)
	}
	if len(branches) > 50 {
		return nil, errors.New("branch page exceeds limit")
	}
	result := &backend.BranchPage{Branches: []string{}}
	for _, branch := range branches {
		if branch == nil || !validBranch(branch.GetName()) {
			return nil, errors.New("invalid branch observation")
		}
		result.Branches = append(result.Branches, branch.GetName())
	}
	if response != nil {
		result.NextPage = response.NextPage
	}
	if result.NextPage != 0 && (result.NextPage <= page || result.NextPage > 10000) {
		return nil, errors.New("invalid next branch page")
	}
	return result, nil
}

// reviewerPermissions reads, once per login, the permission a reviewer holds
// on the repository now. A coordinator acts on a request for changes only
// from someone who could push the change themselves.
type reviewerPermissions struct {
	client      *gh.Client
	owner, repo string
	levels      map[string]string
}

func (p reviewerPermissions) of(ctx context.Context, login string) (string, error) {
	if level, ok := p.levels[login]; ok {
		return level, nil
	}
	level, response, err := p.client.Repositories.GetPermissionLevel(ctx, p.owner, p.repo, login)
	if err == nil {
		p.levels[login] = normalizePermission(level.GetPermission())
		return p.levels[login], nil
	}
	// A rate limit is not an answer. GitHub sends secondary limits as 403,
	// and recording one as "none" would demote a maintainer's command or
	// blocking review; it is retried instead.
	classified := classify(response, err)
	var limited *backend.RateLimitError
	if errors.As(classified, &limited) {
		return "", classified
	}
	if response != nil && (response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusForbidden) {
		// Not a collaborator, or the credential cannot read collaborators:
		// either way the review carries no write authority.
		p.levels[login] = "none"
		return "none", nil
	}
	return "", classified
}

func normalizePermission(permission string) string {
	switch permission {
	case "admin", "write", "read":
		return permission
	default:
		return "none"
	}
}

// IssueComment reads one conversation comment back by its identifier. The
// issue number comes from the comment's own issue URL.
func (b *Backend) IssueComment(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, id int64) (*backend.IssueComment, error) {
	if id <= 0 {
		return nil, errors.New("comment id required")
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	raw, response, err := c.Issues.GetComment(ctx, owner(conn, repo), repo.Spec.Name, id)
	if err != nil {
		if response != nil && response.StatusCode == http.StatusNotFound {
			return nil, backend.ErrCommentNotFound
		}
		return nil, classify(response, err)
	}
	if raw.GetID() != id || raw.GetUser().GetLogin() == "" {
		return nil, errors.New("invalid comment identity")
	}
	number, err := issueNumberFromURL(raw.GetIssueURL())
	if err != nil {
		return nil, err
	}
	return &backend.IssueComment{ID: raw.GetID(), IssueNumber: number, URL: raw.GetHTMLURL(), Body: raw.GetBody(), Author: raw.GetUser().GetLogin(), AuthorType: raw.GetUser().GetType(), CreatedAt: raw.GetCreatedAt().Time}, nil
}

func issueNumberFromURL(raw string) (int, error) {
	index := strings.LastIndex(raw, "/issues/")
	if index < 0 {
		return 0, errors.New("comment has no issue")
	}
	number, err := strconv.Atoi(raw[index+len("/issues/"):])
	if err != nil || number <= 0 {
		return 0, errors.New("comment has an invalid issue number")
	}
	return number, nil
}

// RepositoryPermission is login's current permission on the repository:
// admin, write, read or none.
func (b *Backend) RepositoryPermission(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, login string) (string, error) {
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return "", err
	}
	return reviewerPermissions{client: c, owner: owner(conn, repo), repo: repo.Spec.Name, levels: map[string]string{}}.of(ctx, login)
}
