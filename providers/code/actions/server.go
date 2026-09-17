// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package actions serves caller-authorized, repository-bound provider actions.
package actions

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/tenant"
	"github.com/railgrid/provider-sdk/actionwire"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var segment = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,252}$`)
var verbs = map[string]bool{"branches": true, "branch_head": true, "find_pull_request": true, "pull_request": true, "create_pull_request": true, "update_pull_request": true, "feedback": true, "comments": true, "add_comment": true, "reply_to_review": true, "prepare_snapshot": true, "publish_snapshot": true}

const MaxInputBytes = 36 << 20
const MaxOutputBytes = 512 << 10

type CallerFactory interface {
	For(string, string) (dynamic.Interface, error)
}
type Server struct {
	Caller        CallerFactory
	Authority     func(context.Context, string, string) (dynamic.Interface, error)
	Backends      *backend.Registry
	Credentials   tenant.CredentialResolver
	SnapshotDir   string
	slots         chan struct{}
	snapshotSlots chan struct{}
}

func New(caller CallerFactory, authority func(context.Context, string, string) (dynamic.Interface, error), backends *backend.Registry) *Server {
	return &Server{Caller: caller, Authority: authority, Backends: backends, slots: make(chan struct{}, 8), snapshotSlots: make(chan struct{}, 1)}
}

type Input struct {
	Repository      string `json:"repository"`
	RepositoryUID   string `json:"repositoryUID"`
	ConnectionUID   string `json:"connectionUID"`
	Branch          string `json:"branch,omitempty"`
	ExpectedHead    string `json:"expectedHead,omitempty"`
	Number          int    `json:"number,omitempty"`
	Page            int    `json:"page,omitempty"`
	ParentCommentID int64  `json:"parentCommentID,omitempty"`
	backend.PullRequestInput
	Snapshot  *backend.Snapshot `json:"snapshot,omitempty"`
	BundleRef string            `json:"bundleRef,omitempty"`
}
type Request struct {
	RequestID string `json:"requestId,omitempty"`
	Input     Input  `json:"input"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method != http.MethodPost || r.URL.RawPath != "" || len(parts) != 7 || parts[0] != "actions" || parts[1] != "clusters" || parts[3] != "repositories" || parts[6] != "v1" || (!verbs[parts[5]] && parts[5] != "stage_snapshot") {
		http.NotFound(w, r)
		return
	}
	cluster, name, action := parts[2], parts[4], parts[5]
	if !segment.MatchString(cluster) || !segment.MatchString(name) || name == "." || name == ".." || cluster != r.Header.Get("X-Railgrid-Cluster") {
		http.Error(w, "invalid repository action scope", http.StatusForbidden)
		return
	}
	envelope := actionwire.New(r, "code", action, actionwire.ResourceRef{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Repository", Resource: "repositories", Name: name})
	w.Header().Set("X-Request-ID", envelope.RequestID)
	fail := func(status int, code string, retryable bool) {
		envelope.Failure(w, status, code, strings.ReplaceAll(code, "_", " "), retryable)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		fail(http.StatusServiceUnavailable, "action_capacity_unavailable", false)
		return
	}
	visible, err := s.authorize(ctx, r, cluster, name, action)
	if err != nil {
		fail(403, "action_forbidden", false)
		return
	}
	// Snapshot decoding, staging and loading share one memory admission slot.
	// Keep it until completion, including any Git subprocess using the bundle.
	if action == "stage_snapshot" || action == "prepare_snapshot" || action == "publish_snapshot" {
		select {
		case s.snapshotSlots <- struct{}{}:
			defer func() { <-s.snapshotSlots }()
		default:
			fail(http.StatusServiceUnavailable, "action_capacity_unavailable", false)
			return
		}
	}
	limit := int64(65536)
	if action == "stage_snapshot" {
		limit = MaxInputBytes
	}
	request, err := readActionRequest(ctx, w, r, limit, 30*time.Second)
	if err != nil {
		fail(400, "invalid_action_input", false)
		return
	}
	conn, repo, credential, err := s.resolve(ctx, cluster, name, visible, request.Input)
	if err != nil {
		fail(403, "action_forbidden", false)
		return
	}
	implementation, ok := s.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		fail(422, "unsupported_provider", false)
		return
	}
	collaboration, ok := implementation.(backend.Collaboration)
	if !ok {
		fail(422, "unsupported_action", false)
		return
	}
	in := request.Input
	var output any
	switch action {
	case "stage_snapshot":
		output, err = s.stage(r, cluster, in)
	case "branches":
		lister, ok := implementation.(backend.BranchLister)
		if !ok {
			fail(422, "unsupported_action", false)
			return
		}
		output, err = lister.ListBranches(ctx, conn, credential, repo, in.Page)
	case "branch_head":
		var head string
		head, err = collaboration.BranchHead(ctx, conn, credential, repo, in.Branch)
		output = map[string]any{"head": head}
	case "find_pull_request":
		output, err = collaboration.FindPullRequest(ctx, conn, credential, repo, in.PullRequestInput)
	case "pull_request":
		output, err = collaboration.ReadPullRequest(ctx, conn, credential, repo, in.Number)
	case "create_pull_request":
		output, err = collaboration.CreatePullRequest(ctx, conn, credential, repo, in.PullRequestInput)
	case "update_pull_request":
		output, err = collaboration.UpdatePullRequest(ctx, conn, credential, repo, in.Number, in.PullRequestInput)
	case "feedback":
		output, err = collaboration.PullRequestFeedback(ctx, conn, credential, repo, in.Number, in.Commit)
	case "comments":
		output, err = collaboration.ListComments(ctx, conn, credential, repo, in.Number, in.Page)
	case "add_comment":
		output, err = collaboration.AddComment(ctx, conn, credential, repo, in.Number, in.Body)
	case "reply_to_review":
		output, err = collaboration.ReplyToReview(ctx, conn, credential, repo, in.Number, in.ParentCommentID, in.Body)
	case "prepare_snapshot", "publish_snapshot":
		publisher, ok := implementation.(backend.SnapshotPublisher)
		if !ok || in.Snapshot != nil || in.BundleRef == "" {
			fail(422, "invalid_snapshot", false)
			return
		}
		snapshot, loadErr := s.loadSnapshot(r, cluster, in)
		if loadErr != nil {
			fail(422, "snapshot_unavailable", false)
			return
		}
		if action == "prepare_snapshot" {
			err = publisher.VerifySnapshot(ctx, conn, credential, repo, snapshot)
		} else {
			err = publisher.PublishSnapshot(ctx, conn, credential, repo, snapshot, in.Branch, in.ExpectedHead)
		}
		output = map[string]any{"commit": snapshot.Commit, "tree": snapshot.Tree}
	}
	if err != nil {
		if errors.Is(err, backend.ErrRepositoryIdentityConflict) {
			fail(409, "identity_conflict", false)
		} else {
			fail(502, "upstream_outcome_unconfirmed", false)
		}
		return
	}
	encoded, err := envelope.Success(output)
	if err != nil || len(encoded) > MaxOutputBytes {
		fail(502, "result_limit", false)
		return
	}
	_, _ = w.Write(encoded)
}
func (s *Server) authorize(ctx context.Context, r *http.Request, cluster, name, action string) (*unstructured.Unstructured, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.Caller == nil || s.Authority == nil || token == "" || token == r.Header.Get("Authorization") {
		return nil, errors.New("repository action denied")
	}
	caller, err := s.Caller.For(cluster, token)
	if err != nil {
		return nil, errors.New("repository action denied")
	}
	visible, err := caller.Resource(repositories).Get(ctx, name, metav1.GetOptions{})
	if err != nil || visible.GetDeletionTimestamp() != nil {
		return nil, errors.New("repository action denied")
	}
	review, err := caller.Resource(schema.GroupVersionResource{Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews"}).Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview", "spec": map[string]any{"resourceAttributes": map[string]any{"group": "code.railgrid.ai", "resource": "repositories", "name": name, "verb": "invoke", "subresource": action}}}}, metav1.CreateOptions{})
	if err != nil {
		return nil, errors.New("repository action denied")
	}
	allowed, _, _ := unstructured.NestedBool(review.Object, "status", "allowed")
	if !allowed {
		return nil, errors.New("repository action denied")
	}
	return visible, nil
}

func (s *Server) resolve(ctx context.Context, cluster, name string, visible *unstructured.Unstructured, input Input) (*api.Connection, *api.Repository, backend.Credential, error) {
	fail := func() (*api.Connection, *api.Repository, backend.Credential, error) {
		return nil, nil, backend.Credential{}, errors.New("repository action denied")
	}
	if input.RepositoryUID == "" || input.ConnectionUID == "" || string(visible.GetUID()) != input.RepositoryUID {
		return fail()
	}
	provider, err := s.Authority(ctx, cluster, name)
	if err != nil {
		return fail()
	}
	authoritative, err := provider.Resource(repositories).Get(ctx, name, metav1.GetOptions{})
	if err != nil || authoritative.GetUID() != visible.GetUID() || authoritative.GetDeletionTimestamp() != nil || !reflect.DeepEqual(authoritative.Object["spec"], visible.Object["spec"]) {
		return fail()
	}
	var repo api.Repository
	if runtime.DefaultUnstructuredConverter.FromUnstructured(authoritative.Object, &repo) != nil || repo.Status.RepoID == "" {
		return fail()
	}
	connection, err := provider.Resource(connections).Get(ctx, repo.Spec.ConnectionRef, metav1.GetOptions{})
	if err != nil || string(connection.GetUID()) != input.ConnectionUID || connection.GetDeletionTimestamp() != nil {
		return fail()
	}
	var conn api.Connection
	if runtime.DefaultUnstructuredConverter.FromUnstructured(connection.Object, &conn) != nil {
		return fail()
	}
	owner := repo.Spec.Owner
	if owner == "" {
		owner = conn.Spec.Owner
	}
	if !strings.EqualFold(input.Repository, owner+"/"+repo.Spec.Name) {
		return fail()
	}
	ns := conn.Spec.SecretRef.Namespace
	if ns == "" {
		ns = tenant.DefaultCredentialsNamespace()
	}
	store := &tenant.DynamicSecretStore{Client: provider, Namespace: ns, Name: conn.Spec.SecretRef.Name}
	data, _, err := store.Load(ctx)
	if err != nil {
		return fail()
	}
	credential, err := s.Credentials.ResolveStored(ctx, &conn, tenant.CredentialSecretID(&conn, ns), data, store)
	if err != nil {
		return fail()
	}
	return &conn, &repo, credential, nil
}
