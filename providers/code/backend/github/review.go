// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	gh "github.com/google/go-github/v66/github"
	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
)

var _ backend.ReviewOperations = (*Backend)(nil)

var reviewKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func scopeDigest(scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return hex.EncodeToString(sum[:])
}

func authenticatedActor(ctx context.Context, c *gh.Client, cred backend.Credential) (string, string, error) {
	if cred.ActorLogin != "" && cred.ActorType != "" {
		return cred.ActorLogin, cred.ActorType, nil
	}
	user, response, err := c.Users.Get(ctx, "")
	if err != nil {
		return "", "", classify(response, err)
	}
	if user.GetLogin() == "" || user.GetType() == "" {
		return "", "", errors.New("GitHub authenticated actor is missing")
	}
	return user.GetLogin(), user.GetType(), nil
}

// Only an explicit, definitive rejection can release a claim after a write
// was attempted. A timeout or server error may follow an accepted write.
func classifyReviewWrite(err error, writeAttempted bool) error {
	if err == nil {
		return nil
	}
	var response *gh.ErrorResponse
	var limited *backend.RateLimitError
	permanent := false
	if errors.As(err, &response) && response.Response != nil && !errors.As(err, &limited) {
		switch response.Response.StatusCode {
		case 400, 401, 403, 404, 405, 410, 422:
			permanent = true
		}
	}
	if !writeAttempted || permanent {
		return &backend.ReviewWriteRejected{Err: err, Permanent: permanent}
	}
	return err
}

func hasOperationMarker(body, prefix string) bool {
	body = strings.TrimSpace(body)
	line := strings.TrimSpace(body[strings.LastIndexByte(body, '\n')+1:])
	value, ok := strings.CutPrefix(line, prefix)
	if !ok {
		return false
	}
	fingerprint, ok := strings.CutSuffix(value, " -->")
	return ok && len(fingerprint) == 64 && strings.IndexFunc(fingerprint, func(r rune) bool {
		return !strings.ContainsRune("0123456789abcdef", r)
	}) == -1
}

func (b *Backend) SubmitReview(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, in backend.ReviewInput) (_ *backend.ReviewResult, resultErr error) {
	writeAttempted := false
	defer func() { resultErr = classifyReviewWrite(resultErr, writeAttempted) }()
	if in.Number <= 0 || !objectID.MatchString(in.ExpectedHead) || !reviewKeyPattern.MatchString(in.IdempotencyKey) || len(in.Body) > 32768 {
		return nil, errors.New("invalid review input")
	}
	pr, err := b.ReadPullRequest(ctx, conn, cred, repo, in.Number)
	if err != nil {
		return nil, err
	}
	if pr.Commit != in.ExpectedHead || pr.State != "open" || pr.Merged || !strings.EqualFold(pr.HeadRepository, pr.Repository) {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	markerPrefix := "<!-- railgrid-review-key:" + scopeDigest(in.Scope) + ":" + in.IdempotencyKey + ":"
	marker := markerPrefix + backend.ReviewFingerprint(in) + " -->"
	actorLogin, actorType, err := authenticatedActor(ctx, c, cred)
	if err != nil {
		return nil, err
	}
	// GitHub has no idempotency token for review creation. Search first and
	// recover a lost response by the stable marker before attempting a write.
	existing, err := b.findReviewByMarker(ctx, c, conn, repo, in.Number, markerPrefix, marker, in.ExpectedHead, actorLogin)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		_, unanchored, err := b.validReviewNotes(ctx, c, conn, repo, in.Number, in.Comments)
		if err != nil {
			return nil, err
		}
		return &backend.ReviewResult{Review: *existing, Unanchored: unanchored}, nil
	}
	if in.RecoverOnly {
		return nil, errors.New("review outcome is unconfirmed; marker not yet observable")
	}
	valid, unanchored, err := b.validReviewNotes(ctx, c, conn, repo, in.Number, in.Comments)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(in.Body)
	if body != "" {
		body += "\n\n"
	}
	body += marker
	req := &gh.PullRequestReviewRequest{CommitID: &in.ExpectedHead, Body: &body, Event: gh.String("COMMENT"), Comments: valid}
	writeAttempted = true
	created, response, err := c.PullRequests.CreateReview(ctx, owner(conn, repo), repo.Spec.Name, in.Number, req)
	if err != nil {
		// A second lookup resolves the common accepted-write/lost-response case.
		found, lookupErr := b.findReviewByMarker(ctx, c, conn, repo, in.Number, markerPrefix, marker, in.ExpectedHead, actorLogin)
		if lookupErr == nil && found != nil {
			return &backend.ReviewResult{Review: *found, Unanchored: unanchored}, nil
		}
		return nil, classify(response, err)
	}
	if created == nil || created.GetCommitID() != in.ExpectedHead || created.GetState() != "COMMENTED" || created.GetUser().GetLogin() != actorLogin || created.GetUser().GetType() != actorType {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	return &backend.ReviewResult{Review: backend.Review{ID: created.GetID(), Login: created.GetUser().GetLogin(), ActorType: created.GetUser().GetType(), Commit: created.GetCommitID(), State: created.GetState(), Body: created.GetBody()}, Unanchored: unanchored}, nil
}

func (b *Backend) findReviewByMarker(ctx context.Context, c *gh.Client, conn *api.Connection, repo *api.Repository, number int, markerPrefix, marker, head, actor string) (*backend.Review, error) {
	for page := 1; page <= 20; page++ {
		items, response, err := c.PullRequests.ListReviews(ctx, owner(conn, repo), repo.Spec.Name, number, &gh.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return nil, classify(response, err)
		}
		for _, item := range items {
			if hasOperationMarker(item.GetBody(), markerPrefix) && item.GetUser().GetLogin() == actor {
				if !hasMarker(item.GetBody(), marker) {
					return nil, backend.ErrRepositoryIdentityConflict
				}
				if item.GetCommitID() != head {
					return nil, backend.ErrRepositoryIdentityConflict
				}
				return &backend.Review{ID: item.GetID(), Login: item.GetUser().GetLogin(), ActorType: item.GetUser().GetType(), Commit: item.GetCommitID(), State: item.GetState(), Body: item.GetBody()}, nil
			}
		}
		if response == nil || response.NextPage == 0 {
			return nil, nil
		}
	}
	return nil, errors.New("review listing exceeds pagination limit")
}

type diffLocation struct {
	side string
	line int
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:.*)$`)

func validReviewPath(value string) bool {
	return value != "" && len(value) <= 1024 && !path.IsAbs(value) && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../") && !strings.Contains(value, "\\") && !strings.ContainsFunc(value, unicode.IsControl)
}

func patchLocations(patch string) (map[diffLocation]bool, error) {
	locations := map[diffLocation]bool{}
	if patch == "" || len(patch) > 1<<20 {
		return nil, errors.New("missing or oversized patch")
	}
	lines := strings.Split(patch, "\n")
	oldLine, newLine, oldLeft, newLeft, active := 0, 0, 0, 0, false
	for i, line := range lines {
		if strings.HasPrefix(line, "@@") {
			if active && (oldLeft != 0 || newLeft != 0) {
				return nil, errors.New("incomplete patch hunk")
			}
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, errors.New("invalid patch hunk")
			}
			values := []int{0, 1, 0, 1}
			for index, raw := range m[1:5] {
				if raw == "" {
					continue
				}
				value, err := strconv.ParseInt(raw, 10, 32)
				if err != nil {
					return nil, errors.New("patch coordinate exceeds bounds")
				}
				values[index] = int(value)
			}
			oldLine, oldLeft, newLine, newLeft = values[0], values[1], values[2], values[3]
			active = true
			continue
		}
		if line == "" && i == len(lines)-1 {
			continue
		}
		if strings.HasPrefix(line, "\\ No newline at end of file") {
			continue
		}
		if !active || line == "" {
			return nil, errors.New("invalid patch line")
		}
		switch line[0] {
		case ' ':
			locations[diffLocation{"RIGHT", newLine}] = true
			oldLine++
			newLine++
			oldLeft--
			newLeft--
		case '-':
			locations[diffLocation{"LEFT", oldLine}] = true
			oldLine++
			oldLeft--
		case '+':
			locations[diffLocation{"RIGHT", newLine}] = true
			newLine++
			newLeft--
		default:
			return nil, errors.New("invalid patch line")
		}
		if oldLeft < 0 || newLeft < 0 {
			return nil, errors.New("patch exceeds hunk bounds")
		}
	}
	if !active || oldLeft != 0 || newLeft != 0 {
		return nil, errors.New("incomplete patch")
	}
	return locations, nil
}

func (b *Backend) validReviewNotes(ctx context.Context, c *gh.Client, conn *api.Connection, repo *api.Repository, number int, notes []backend.ReviewNote) ([]*gh.DraftReviewComment, []backend.ReviewNote, error) {
	valid := make([]*gh.DraftReviewComment, 0, len(notes))
	unanchored := []backend.ReviewNote{}
	remaining := map[string][]backend.ReviewNote{}
	for _, note := range notes {
		if !validReviewPath(note.Path) || note.Line <= 0 || strings.TrimSpace(note.Body) == "" || len(note.Body) > 8192 || (note.Side != "" && note.Side != "RIGHT" && note.Side != "LEFT") {
			unanchored = append(unanchored, note)
			continue
		}
		remaining[note.Path] = append(remaining[note.Path], note)
	}
	matched := map[string]bool{}
	for page := 1; page <= 30 && len(remaining) > 0; page++ {
		files, response, err := c.PullRequests.ListFiles(ctx, owner(conn, repo), repo.Spec.Name, number, &gh.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return nil, nil, classify(response, err)
		}
		if len(files) > 100 {
			return nil, nil, errors.New("invalid pull request file page")
		}
		for _, file := range files {
			findings, needed := remaining[file.GetFilename()]
			if !needed {
				continue
			}
			locations, parseErr := patchLocations(file.GetPatch())
			if parseErr != nil {
				unanchored = append(unanchored, findings...)
				delete(remaining, file.GetFilename())
				continue
			}
			for _, finding := range findings {
				side := finding.Side
				if side == "" {
					side = "RIGHT"
				}
				key := finding.Path + "\x00" + strconv.Itoa(finding.Line) + "\x00" + side
				if locations[diffLocation{side, finding.Line}] {
					matched[key] = true
				} else {
					unanchored = append(unanchored, finding)
				}
			}
			delete(remaining, file.GetFilename())
		}
		if response == nil || response.NextPage == 0 {
			break
		}
	}
	for _, findings := range remaining {
		unanchored = append(unanchored, findings...)
	}
	for _, note := range notes {
		side := note.Side
		if side == "" {
			side = "RIGHT"
		}
		if matched[note.Path+"\x00"+strconv.Itoa(note.Line)+"\x00"+side] {
			valid = append(valid, &gh.DraftReviewComment{Path: &note.Path, Line: gh.Int(note.Line), Side: &side, Body: &note.Body})
		}
	}
	return valid, unanchored, nil
}

func hasMarker(body, marker string) bool {
	return strings.HasSuffix(strings.TrimSpace(body), marker)
}

func (b *Backend) UpsertManagedComment(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, in backend.ManagedCommentInput) (_ *backend.Comment, resultErr error) {
	writeAttempted := false
	defer func() { resultErr = classifyReviewWrite(resultErr, writeAttempted) }()
	if in.Number <= 0 || !reviewKeyPattern.MatchString(in.Key) || strings.TrimSpace(in.Body) == "" || len(in.Body) > 32768 {
		return nil, errors.New("invalid managed comment")
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	markerPrefix := "<!-- railgrid-managed-comment:" + scopeDigest(in.Scope) + ":" + in.Key + ":"
	marker := markerPrefix + backend.CommentFingerprint(in) + " -->"
	actorLogin, _, err := authenticatedActor(ctx, c, cred)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(in.Body) + "\n\n" + marker
	var known *gh.IssueComment
	for page := 1; page <= 20; page++ {
		comments, response, err := c.Issues.ListComments(ctx, owner(conn, repo), repo.Spec.Name, in.Number, &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{Page: page, PerPage: 100}})
		if err != nil {
			return nil, classify(response, err)
		}
		for _, existing := range comments {
			if existing.GetID() == in.CommentID && in.CommentID > 0 {
				if existing.GetUser().GetLogin() != actorLogin {
					return nil, backend.ErrRepositoryIdentityConflict
				}
				known = existing
			}
			if existing.GetUser().GetLogin() != actorLogin || !hasOperationMarker(existing.GetBody(), markerPrefix) {
				continue
			}
			if hasMarker(existing.GetBody(), marker) {
				return &backend.Comment{ID: existing.GetID(), URL: existing.GetHTMLURL(), Body: existing.GetBody(), Author: existing.GetUser().GetLogin(), AuthorType: existing.GetUser().GetType(), UpdatedAt: existing.GetUpdatedAt().Time}, nil
			}
			if known != nil && known.GetID() != existing.GetID() {
				return nil, backend.ErrRepositoryIdentityConflict
			}
			known = existing
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		if page == 20 {
			return nil, errors.New("comment listing exceeds pagination limit")
		}
	}
	if in.CommentID > 0 && (known == nil || known.GetID() != in.CommentID) {
		// The issue comment listing can lag immediately after a create or edit.
		// Confirm deletion by ID before allowing the action layer to forget its
		// receipt and issue a replacement create.
		comment, response, getErr := c.Issues.GetComment(ctx, owner(conn, repo), repo.Spec.Name, in.CommentID)
		if getErr != nil {
			if response != nil && response.StatusCode == http.StatusNotFound {
				if known == nil {
					return nil, backend.ErrManagedCommentMissing
				}
				// The pinned ID disappeared, but the listing found another comment
				// carrying this operation's marker. Adopt it instead of creating a
				// duplicate or forgetting the receipt.
			} else {
				return nil, classify(response, getErr)
			}
		} else {
			if comment.GetUser().GetLogin() != actorLogin {
				return nil, backend.ErrRepositoryIdentityConflict
			}
			known = comment
		}
	}
	if known != nil && (!in.RecoverOnly || in.CommentID > 0) {
		if known.GetUser().GetLogin() != actorLogin {
			return nil, errors.New("managed comment is owned by another author")
		}
		writeAttempted = true
		updated, response, err := c.Issues.EditComment(ctx, owner(conn, repo), repo.Spec.Name, known.GetID(), &gh.IssueComment{Body: &body})
		if err != nil {
			return nil, classify(response, err)
		}
		return &backend.Comment{ID: updated.GetID(), URL: updated.GetHTMLURL(), Body: updated.GetBody(), Author: updated.GetUser().GetLogin(), AuthorType: updated.GetUser().GetType(), UpdatedAt: updated.GetUpdatedAt().Time}, nil
	}
	if in.RecoverOnly {
		return nil, errors.New("managed comment outcome is unconfirmed; marker not yet observable")
	}
	writeAttempted = true
	created, response, err := c.Issues.CreateComment(ctx, owner(conn, repo), repo.Spec.Name, in.Number, &gh.IssueComment{Body: &body})
	if err != nil {
		// Recover an accepted create by re-listing and matching our ownership marker.
		for page := 1; page <= 20; page++ {
			comments, _, listErr := c.Issues.ListComments(ctx, owner(conn, repo), repo.Spec.Name, in.Number, &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{Page: page, PerPage: 100}})
			if listErr != nil {
				break
			}
			for _, item := range comments {
				if hasMarker(item.GetBody(), marker) && item.GetUser().GetLogin() == actorLogin {
					return &backend.Comment{ID: item.GetID(), URL: item.GetHTMLURL(), Body: item.GetBody(), Author: item.GetUser().GetLogin(), AuthorType: item.GetUser().GetType(), UpdatedAt: item.GetUpdatedAt().Time}, nil
				}
			}
		}
		return nil, classify(response, err)
	}
	return &backend.Comment{ID: created.GetID(), URL: created.GetHTMLURL(), Body: created.GetBody(), Author: created.GetUser().GetLogin(), AuthorType: created.GetUser().GetType(), UpdatedAt: created.GetUpdatedAt().Time}, nil
}

func (b *Backend) MarkPullRequestReady(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, in backend.ReadyInput) (*backend.ReadyResult, error) {
	if in.Number <= 0 || !objectID.MatchString(in.ExpectedHead) {
		return nil, errors.New("invalid ready input")
	}
	current, err := b.ReadPullRequest(ctx, conn, cred, repo, in.Number)
	if err != nil {
		return nil, err
	}
	if current.Commit != in.ExpectedHead || current.State != "open" || current.Merged {
		return nil, backend.ErrRepositoryIdentityConflict
	}
	if !current.Draft {
		return &backend.ReadyResult{PullRequest: current, ExpectedHead: in.ExpectedHead, HeadMatched: current.Commit == in.ExpectedHead, Ready: true}, nil
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return nil, err
	}
	if current.NodeID == "" {
		return nil, errors.New("pull request has no forge node id")
	}
	endpoint := *c.BaseURL
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/")
	if strings.HasSuffix(endpoint.Path, "/api/v3") {
		endpoint.Path = strings.TrimSuffix(endpoint.Path, "/api/v3") + "/api/graphql"
	} else {
		endpoint.Path += "/graphql"
	}
	query := `mutation MarkReady($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { id number isDraft } } }`
	request, err := c.NewRequest("POST", endpoint.String(), map[string]any{"query": query, "variables": map[string]string{"id": current.NodeID}})
	if err != nil {
		return nil, err
	}
	var graph struct {
		Data struct {
			Mark struct {
				PullRequest struct {
					ID      string `json:"id"`
					Number  int    `json:"number"`
					IsDraft bool   `json:"isDraft"`
				} `json:"pullRequest"`
			} `json:"markPullRequestReadyForReview"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	response, err := c.Do(ctx, request, &graph)
	if err != nil {
		return nil, classify(response, err)
	}
	if len(graph.Errors) > 0 || graph.Data.Mark.PullRequest.ID != current.NodeID || graph.Data.Mark.PullRequest.Number != in.Number || graph.Data.Mark.PullRequest.IsDraft {
		return nil, errors.New("forge rejected ready transition")
	}
	// GitHub's ready mutation has no expected-head precondition. Read back
	// immediately and report the actual state; callers must re-evaluate handoff.
	observed, err := b.ReadPullRequest(ctx, conn, cred, repo, in.Number)
	if err != nil {
		return nil, err
	}
	return &backend.ReadyResult{PullRequest: observed, ExpectedHead: in.ExpectedHead, HeadMatched: observed.Commit == in.ExpectedHead, Ready: observed.State == "open" && !observed.Draft}, nil
}
