// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-sdk/dataplane"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// Operation is a forge effect, not an engineering workflow transition.
// Snapshot.Bundle is base64 JSON, with the canonical Runner snapshot format.
var operationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)

type ReviewDelegate struct {
	InvocationKey string `json:"invocationKey"`
	Agent         string `json:"agent"`
	AgentUID      string `json:"agentUID"`
}

type ReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

type Operation struct {
	Comments           []ReviewComment   `json:"comments,omitempty"`
	ReviewOnly         bool              `json:"reviewOnly,omitempty"`
	Delegate           *ReviewDelegate   `json:"delegate,omitempty"`
	InvocationAgent    string            `json:"invocationAgent"`
	InvocationAgentUID string            `json:"invocationAgentUID"`
	ScopeProof         string            `json:"scopeProof"`
	InvocationKey      string            `json:"invocationKey"`
	EventUID           string            `json:"eventUID"`
	SourceUID          string            `json:"sourceUID,omitempty"`
	SourceDigest       string            `json:"sourceDigest,omitempty"`
	GrantRef           string            `json:"grantRef,omitempty"`
	SourceProof        string            `json:"sourceProof,omitempty"`
	Op                 string            `json:"op"`
	PullRequestUID     string            `json:"pullRequestUID"`
	ExpectedHead       string            `json:"expectedHead,omitempty"`
	Key                string            `json:"key,omitempty"`
	Body               string            `json:"body,omitempty"`
	FindingsCount      *int              `json:"findingsCount,omitempty"`
	Snapshot           *backend.Snapshot `json:"snapshot,omitempty"`
}

func validPublication(prRecord *api.PullRequest, e *api.GitHubEvent, pr PullRequest, in Operation) error {
	if mode, _ := Parse(e.Spec.Comment); mode != "review-fix" {
		return errors.New("prRecord has no code-write authority")
	}
	if pr.State != "open" || pr.Head.Repo.ID != e.Spec.RepositoryID || pr.Base.Repo.ID != e.Spec.RepositoryID || pr.Head.Ref != e.Spec.Branch || pr.Base.Ref != e.Spec.Base || pr.Head.Ref == "main" || pr.Head.Ref == pr.Base.Ref {
		return errors.New("publication target is not the admitted PR branch")
	}
	if in.Snapshot != nil && prRecord.Spec.DesiredHead != nil && prRecord.Status.Head != prRecord.Spec.DesiredHead.Commit && prRecord.Spec.DesiredHead.Commit != in.Snapshot.Commit {
		return errors.New("previous desired publication has not been observed yet")
	}
	if in.Snapshot == nil || in.Snapshot.BaseCommit != in.ExpectedHead || in.ExpectedHead == "" {
		return errors.New("snapshot must extend the exact expected head")
	}
	if pr.Head.SHA != in.ExpectedHead && pr.Head.SHA != in.Snapshot.Commit {
		return errors.New("PR head changed; publication refused")
	}
	return nil
}
func (s *Server) Operate(ctx context.Context, dyn dynamic.Interface, cluster string, obj *unstructured.Unstructured, raw json.RawMessage, identity dataplane.ProxiedIdentity) (any, error) {
	prRecord, err := decode[api.PullRequest](obj)
	if err != nil {
		return nil, err
	}
	var in Operation
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&in); err != nil {
		return nil, errors.New("invalid operation input")
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing operation input")
	}
	if prRecord.Spec.Command != nil && prRecord.Spec.Command.Task != nil {
		return s.operateTask(ctx, dyn, cluster, prRecord, in, identity)
	}
	if prRecord.Spec.Command == nil || in.EventUID != prRecord.Spec.Command.EventUID || in.PullRequestUID != string(prRecord.UID) || time.Now().After(prRecord.Spec.Command.Deadline.Time) {
		return nil, errors.New("prRecord identity changed or deadline expired")
	}
	e, b, allowed, err := s.Context(ctx, dyn, cluster, prRecord.Spec.Command.EventRef)
	if err != nil {
		return nil, err
	}
	if !allowed || string(e.UID) != prRecord.Spec.Command.EventUID {
		return nil, errors.New("requester is not a current maintainer/admin, or event identity changed")
	}
	if !hmac.Equal([]byte(in.ScopeProof), []byte(roleProof(publicationProof(cluster, prRecord, in.InvocationKey, in.InvocationAgent, in.InvocationAgentUID, b.data["webhookSecret"]), in.ReviewOnly, b.data["webhookSecret"]))) || !operationKeyPattern.MatchString(in.InvocationKey) {
		return nil, errors.New("publication scope is not bound to this admitted invocation")
	}
	if mode, _ := Parse(e.Spec.Comment); mode == "" {
		return nil, errors.New("not an authorized PR command")
	}
	if in.ReviewOnly && in.Op != "observe" && in.Op != "review" {
		return nil, errors.New("delegated review authority permits only observe and review")
	}
	if in.Op == "delegate-review" {
		if err := validReviewDelegate(in); err != nil {
			return nil, err
		}
		child := in.Delegate
		proof := publicationProof(cluster, prRecord, child.InvocationKey, child.Agent, child.AgentUID, b.data["webhookSecret"])
		return map[string]any{"scopeProof": roleProof(proof, true, b.data["webhookSecret"])}, nil
	}
	var pr PullRequest
	if err = b.client.Do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", e.Spec.Repository, e.Spec.PullRequest), nil, &pr); err != nil {
		return nil, err
	}
	if pr.Head.Repo.ID != e.Spec.RepositoryID || pr.Head.Ref != e.Spec.Branch || pr.Base.Ref != e.Spec.Base {
		return nil, errors.New("PR identity changed")
	}
	if in.Op == "observe" {
		observation, err := observe(ctx, b.client, e, pr)
		if err != nil {
			return nil, err
		}
		return map[string]any{"observation": observation, "repository": e.Spec.Repository, "branch": e.Spec.Branch, "base": e.Spec.Base, "event": e.Spec}, nil
	}
	if !operationKeyPattern.MatchString(in.Key) || len(in.Body) > 16384 {
		return nil, errors.New("a bounded idempotency key and body are required")
	}
	marker := fmt.Sprintf("<!-- railgrid-pr-command:%s:%d:%s:%s:%s -->", prRecord.UID, e.Spec.CommentID, in.Op, in.ExpectedHead, in.Key)
	switch in.Op {
	case "comment":
		// A PR has one status surface across commands and continuations. Do not
		// include the delivery, invocation or head in its identity.
		return publishTextUpdating(ctx, b.client, e, 0, in.Body, fmt.Sprintf("<!-- railgrid-pr-status:%s -->", prRecord.UID), true)
	case "review":
		if pr.State != "open" || in.ExpectedHead == "" || pr.Head.SHA != in.ExpectedHead {
			return nil, errors.New("review requires the exact current PR head")
		}
		if in.FindingsCount == nil || *in.FindingsCount < 0 {
			return nil, errors.New("review requires a nonnegative findingsCount; use zero for a clean review")
		}
		if err := validateReviewComments(in.Comments, *in.FindingsCount); err != nil {
			return nil, err
		}
		if *in.FindingsCount == 0 {
			return map[string]any{"suppressed": true, "reason": "clean review belongs in the PR status comment"}, nil
		}
		// COMMENT is fixed here. Agents cannot approve or request a merge.
		return publishReview(ctx, b.client, e, in.ExpectedHead, in.Body, marker, in.Comments)
	case "publish":
		if err = validPublication(prRecord, e, pr, in); err != nil {
			return nil, err
		}
		if s.Snapshots == nil {
			return nil, errors.New("snapshot store unavailable")
		}
		implementation, ok := s.Backends.Get("github")
		if !ok {
			return nil, errors.New("GitHub backend unavailable")
		}
		publisher, ok := implementation.(backend.SnapshotPublisher)
		if !ok {
			return nil, errors.New("snapshot validation unavailable")
		}
		if err = publisher.VerifySnapshot(ctx, b.conn, b.credential, b.repo, *in.Snapshot); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(in.Snapshot)
		if err != nil {
			return nil, err
		}
		ref, err := s.Snapshots.Stage(ctx, cluster, "pr-command:"+string(prRecord.UID), raw)
		if err != nil {
			return nil, err
		}
		// Publication intent is the existing PullRequest desiredHead. The existing
		// controller owns the atomic Git lease and recovery of an unconfirmed push.
		prRecord.Spec.DesiredHead = &api.PullRequestHead{Commit: in.Snapshot.Commit, BaseCommit: in.ExpectedHead, Tree: in.Snapshot.Tree, BundleRef: ref, Message: in.Snapshot.Message}
		u, err := asU(prRecord)
		if err != nil {
			return nil, err
		}
		_, err = dyn.Resource(PullRequests).Update(ctx, u, metav1.UpdateOptions{})
		return map[string]any{"commit": in.Snapshot.Commit, "desiredHeadRecorded": err == nil}, err
	default:
		return nil, errors.New("unknown forge operation")
	}
}

func publishText(ctx context.Context, c Client, e *api.GitHubEvent, head any, body, marker string) (any, error) {
	return publishTextUpdating(ctx, c, e, head, body, marker, false)
}

func publishTextUpdating(ctx context.Context, c Client, e *api.GitHubEvent, head any, body, marker string, update bool) (any, error) {
	return publishTextWithComments(ctx, c, e, head, body, marker, update, nil)
}

func publishTextWithComments(ctx context.Context, c Client, e *api.GitHubEvent, head any, body, marker string, update bool, comments []ReviewComment) (any, error) {
	path := fmt.Sprintf("/repos/%s/issues/%d/comments", e.Spec.Repository, e.Spec.PullRequest)
	reviewHead, _ := head.(string)
	if reviewHead != "" {
		path = fmt.Sprintf("/repos/%s/pulls/%d/reviews", e.Spec.Repository, e.Spec.PullRequest)
	}
	for page := 1; page <= 20; page++ {
		var items []Comment
		if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", path, page), nil, &items); err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.User.Type == "Bot" && item.User.Login == c.BotLogin && strings.HasSuffix(item.Body, "\n\n"+marker) {
				if len(comments) > 0 && item.Body != body+"\n\n"+marker {
					return nil, errors.New("review key already used with different content")
				}
				if update && item.Body != body+"\n\n"+marker {
					var result Comment
					err := c.Do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", e.Spec.Repository, item.ID), map[string]any{"body": body + "\n\n" + marker}, &result)
					return map[string]any{"id": item.ID, "updated": true}, err
				}
				return map[string]any{"id": item.ID, "reused": true}, nil
			}
		}
		if len(items) < 100 {
			input := map[string]any{"body": body + "\n\n" + marker}
			if reviewHead != "" {
				input["event"] = "COMMENT"
				input["commit_id"] = reviewHead
				if len(comments) > 0 {
					// GitHub has no atomic head lease for reviews. Recheck directly
					// before posting, and always bind comments to commit_id.
					var current PullRequest
					if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", e.Spec.Repository, e.Spec.PullRequest), nil, &current); err != nil {
						return nil, err
					}
					if current.State != "open" || current.Head.SHA != reviewHead {
						return nil, errors.New("PR head changed; inline review refused")
					}
					input["comments"] = comments
				}
			}
			var result Comment
			err := c.Do(ctx, http.MethodPost, path, input, &result)
			return map[string]any{"id": result.ID}, err
		}
	}
	return nil, errors.New("publication history exceeds recovery limit")
}

// Observation is an ephemeral response, not a lifecycle record.
type Observation struct {
	BaseCurrent    *bool  `json:"baseCurrent,omitempty"`
	Head           string `json:"head"`
	BaseHead       string `json:"baseHead"`
	CIState        string `json:"ciState"`
	MergeableState string `json:"mergeableState"`
	Summary        string `json:"summary"`
	Closed         bool   `json:"closed"`
}

func observe(ctx context.Context, c Client, e *api.GitHubEvent, pr PullRequest) (Observation, error) {
	summary, state, err := observeCI(ctx, c, e.Spec.Repository, pr.Head.SHA)
	if err != nil {
		return Observation{}, err
	}
	var comparison struct {
		Status string `json:"status"`
		Behind int    `json:"behind_by"`
	}
	if err = c.Do(ctx, "GET", fmt.Sprintf("/repos/%s/compare/%s...%s", e.Spec.Repository, pr.Base.SHA, pr.Head.SHA), nil, &comparison); err != nil {
		return Observation{}, err
	}
	baseCurrent := comparison.Behind == 0 && (comparison.Status == "ahead" || comparison.Status == "identical")

	return Observation{BaseCurrent: &baseCurrent, Head: pr.Head.SHA, BaseHead: pr.Base.SHA, CIState: state, MergeableState: pr.MergeableState, Summary: summary, Closed: pr.State != "open"}, err
}

// Proof is deterministic for recovery and bound to the existing command lease.
// It is issued only through the verified event/context capability, which
// Agents does not hold. A copied proof cannot authorize a new invocation key.
func publicationProof(cluster string, pr *api.PullRequest, key, agent, agentUID string, secret []byte) string {
	if pr.Spec.Command == nil {
		return ""
	}
	raw, _ := json.Marshal([]string{cluster, pr.Name, string(pr.UID), pr.Spec.Command.EventUID, pr.Spec.Command.Deadline.UTC().Format(time.RFC3339Nano), key, agent, agentUID})
	return Signature(secret, raw)
}

func (s *Server) issuePublicationScope(ctx context.Context, dyn dynamic.Interface, cluster string, e *api.GitHubEvent, b binding, allowed bool, raw json.RawMessage) (string, error) {
	var in struct {
		PublicationScope *struct {
			PullRequest    string `json:"pullRequest"`
			PullRequestUID string `json:"pullRequestUID"`
			InvocationKey  string `json:"invocationKey"`
			Agent          string `json:"agent"`
			AgentUID       string `json:"agentUID"`
		} `json:"publicationScope"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return "", errors.New("invalid publication scope request")
		}
	}
	if in.PublicationScope == nil {
		return "", nil
	}
	scope := in.PublicationScope
	if !allowed || !operationKeyPattern.MatchString(scope.InvocationKey) || scope.Agent == "" || scope.AgentUID == "" || len(scope.Agent) > 253 || len(scope.AgentUID) > 128 {
		return "", errors.New("publication scope is not authorized")
	}
	u, err := dyn.Resource(PullRequests).Get(ctx, scope.PullRequest, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	pr, err := decode[api.PullRequest](u)
	if err != nil {
		return "", err
	}
	mode, _ := Parse(e.Spec.Comment)
	if mode == "" || string(pr.UID) != scope.PullRequestUID || pr.Spec.Command == nil || pr.Spec.Command.EventUID != string(e.UID) || pr.Spec.RepositoryRef != e.Spec.RepositoryRef || pr.Spec.Branch != e.Spec.Branch || pr.Spec.Base != e.Spec.Base || time.Now().After(pr.Spec.Command.Deadline.Time) {
		return "", errors.New("publication scope or lease changed")
	}
	return publicationProof(cluster, pr, scope.InvocationKey, scope.Agent, scope.AgentUID, b.data["webhookSecret"]), nil
}

// Role attenuation is signed independently: removing reviewOnly cannot turn a
// child proof into publication or further delegation authority.
func roleProof(proof string, reviewOnly bool, key []byte) string {
	if !reviewOnly {
		return proof
	}
	return Signature(key, []byte("review-only\x00"+proof))
}
func validReviewDelegate(in Operation) error {
	d := in.Delegate
	if in.ReviewOnly || d == nil || !operationKeyPattern.MatchString(d.InvocationKey) || d.Agent == "" || len(d.Agent) > 253 || d.AgentUID == "" || len(d.AgentUID) > 128 || d.InvocationKey == in.InvocationKey {
		return errors.New("invalid delegated review scope")
	}
	return nil
}
