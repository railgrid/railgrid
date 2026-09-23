// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package actions serves caller-authorized, repository-bound provider actions.
//
// The route grammar, the two gates and the response envelope all come from
// provider-sdk/dataplane: ParseRequest parses
// /actions/clusters/{id}/repositories/{name}/{action}/v1, Gate proves the
// caller can see the Repository and holds `create` on
// repositories/{action}, and Serve bounds the body, the deadline and the
// result. What stays here is what only this provider knows: pinning the
// caller-visible Repository against the provider's own export read
// (authority.go), resolving the Connection credential, and dispatching to a
// git backend.
package actions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/commitbundle"
	"github.com/railgrid/provider-code/tenant"
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

// StageSnapshot is the one served verb that is not in the CatalogEntry: its
// body carries a base64 git bundle far past the 1 MiB ceiling
// CatalogEntry.spec.actions[].limits.maxInputBytes allows
// (apis/providers/v1alpha1/actions.go). It is documented as the single
// catalogue exception in docs/provider-actions.md, "Uncatalogued large-upload
// verbs", and is gated exactly like every catalogued action.
const StageSnapshot = "stage_snapshot"

// MaxInputBytes bounds the stage_snapshot body: a 25 MiB decoded bundle plus
// its base64 expansion and the surrounding JSON.
const MaxInputBytes = 36 << 20

// MaxCatalogInputBytes is the largest input a CatalogEntry may declare
// (apis/providers/v1alpha1/actions.go caps limits.maxInputBytes at 1 MiB).
const MaxCatalogInputBytes = 1 << 20

// MaxOutputBytes is the declared output bound shared by every action.
const MaxOutputBytes = 512 << 10

// actionTimeout is the declared timeoutSeconds of every catalogued action; it
// bounds the upload, the backend call and the encode together.
const actionTimeout = 180 * time.Second

// served maps each action name to the limits its CatalogEntry declaration
// states (manifest.yaml). An action missing from this map is not served, so
// adding a catalogue entry and serving it are one edit.
var served = map[string]dataplane.Limits{
	"branches":            jsonAction(50),
	"branch_head":         jsonAction(50),
	"find_pull_request":   jsonAction(50),
	"pull_request":        jsonAction(50),
	"create_pull_request": jsonAction(50),
	"update_pull_request": jsonAction(50),
	"feedback":            jsonAction(2000),
	"comments":            jsonAction(50),
	"add_comment":         jsonAction(50),
	"reply_to_review":     jsonAction(50),
	"prepare_snapshot":    jsonAction(50),
	"publish_snapshot":    jsonAction(50),
	// commit takes the catalogue's whole 1 MiB input ceiling: a small
	// generated app fits inline, and anything larger arrives as a bundleRef
	// staged through StageCommitBundle.
	Commit: {
		Timeout:        actionTimeout,
		MaxInputBytes:  MaxCatalogInputBytes,
		MaxOutputBytes: MaxOutputBytes,
		MaxResultItems: 50,
	},
	StageCommitBundle: {
		Timeout:        actionTimeout,
		MaxInputBytes:  MaxCommitBundleInputBytes,
		MaxOutputBytes: MaxOutputBytes,
	},
	StageSnapshot: {
		Timeout:        actionTimeout,
		MaxInputBytes:  MaxInputBytes,
		MaxOutputBytes: MaxOutputBytes,
	},
	// mint_clone_token is bound to a Repository, so it is served here rather
	// than in connectionActions: the grant to clone one repository must not
	// follow from a grant on the Connection every repository shares.
	MintCloneToken: {
		Timeout:        actionTimeout,
		MaxInputBytes:  64 << 10,
		MaxOutputBytes: MaxOutputBytes,
	},
}

func jsonAction(items int64) dataplane.Limits {
	return dataplane.Limits{
		Timeout:        actionTimeout,
		MaxInputBytes:  64 << 10,
		MaxOutputBytes: MaxOutputBytes,
		MaxResultItems: items,
	}
}

// MintRegistryToken is the one action bound to a Connection rather than a
// Repository: it hands a consumer a pull credential for that Connection's
// container registry, so the credential itself never leaves this provider.
// See tenant/registry_token.go for why it exists.
const MintRegistryToken = "mint_registry_token"

// connectionActions are the actions bound to connections/{name} instead of
// repositories/{name}. Gate 2 therefore asks about connections/{action}, and
// a grant on a repository action never reaches one of these.
var connectionActions = map[string]dataplane.Limits{
	MintRegistryToken: {
		Timeout:        actionTimeout,
		MaxInputBytes:  64 << 10,
		MaxOutputBytes: MaxOutputBytes,
	},
}

// snapshotActions share one memory admission slot: decoding, staging and
// loading a bundle all hold it, including any git subprocess using it.
var snapshotActions = map[string]bool{
	StageSnapshot:      true,
	StageCommitBundle:  true,
	"prepare_snapshot": true,
	"publish_snapshot": true,
}

// statusFor maps a typed action failure to its HTTP status. Every code here
// is one this package produces; anything else is treated as a backend outcome
// that did not settle.
var statusFor = map[string]int{
	"invalid_action_input":         http.StatusBadRequest,
	"action_forbidden":             http.StatusForbidden,
	"identity_conflict":            http.StatusConflict,
	"unsupported_provider":         http.StatusUnprocessableEntity,
	"unsupported_action":           http.StatusUnprocessableEntity,
	"invalid_snapshot":             http.StatusUnprocessableEntity,
	"snapshot_unavailable":         http.StatusUnprocessableEntity,
	"bundle_unavailable":           http.StatusUnprocessableEntity,
	"clone_token_unavailable":      http.StatusBadGateway,
	"commit_not_created":           http.StatusBadGateway,
	"upstream_outcome_unconfirmed": http.StatusBadGateway,
}

// Server serves the repository-bound action routes.
type Server struct {
	// Caller builds the caller-scoped client both gates run through. It never
	// carries the provider's own credential.
	Caller dataplane.CallerFactory
	// Authority resolves the provider's own view of one of its own objects
	// through its accepted APIExport, for pinning what gate 1 returned. It
	// takes the resource being pinned because an action may be bound to a
	// Repository or to a Connection, and the endpoint is found by proving the
	// named object is readable through it.
	Authority func(context.Context, string, schema.GroupVersionResource, string) (dynamic.Interface, error)
	Backends  *backend.Registry
	// Bundles is this provider's own source-bundle store, which the commit
	// verbs write into. A RepositoryCommit is only a pointer at it.
	Bundles       commitbundle.Store
	Credentials   tenant.CredentialResolver
	SnapshotDir   string
	slots         chan struct{}
	snapshotSlots chan struct{}
}

func New(caller dataplane.CallerFactory, authority func(context.Context, string, schema.GroupVersionResource, string) (dynamic.Interface, error), backends *backend.Registry) *Server {
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

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logger := klog.FromContext(r.Context()).WithName("code-actions")
	req, ok := dataplane.ParseRequest(dataplane.ActionsRoot, r)
	if !ok {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	// Which resource an action is bound to decides which object gate 1 reads
	// and which subresource gate 2 asks about, so it is resolved from the
	// route before anything else happens.
	gvr, kind := repositories, "Repository"
	limits, isServed := served[req.Verb]
	if req.Resource == connections.Resource {
		gvr, kind = connections, "Connection"
		limits, isServed = connectionActions[req.Verb]
	}
	if (req.Resource != repositories.Resource && req.Resource != connections.Resource) ||
		req.Component != "" || req.Version != "v1" || req.Tail != "" || !isServed {
		http.NotFound(w, r)
		return
	}
	envelope := actionwire.New(r, "code", req.Verb, actionwire.ResourceRef{APIVersion: "code.railgrid.ai/v1alpha1", Kind: kind, Resource: gvr.Resource, Name: req.Name})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", envelope.RequestID)
	fail := func(status int, code string) {
		envelope.Failure(w, status, code, strings.ReplaceAll(code, "_", " "), false)
	}

	// Admission before either gate: a saturated process refuses work without
	// reading a body or touching kcp.
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		fail(http.StatusServiceUnavailable, "action_capacity_unavailable")
		return
	}

	// The two gates, as the caller. The returned Repository is what resolve
	// pins the provider's own read against.
	visible, _, err := dataplane.Gate(r.Context(), r, s.Caller, gvr, req)
	if err != nil {
		// The detail stays provider-side; the caller learns only the status.
		logger.V(3).Info("repository action refused", "action", req.Verb, "repository", req.Name, "err", err)
		status := dataplane.StatusForAs(err, http.StatusForbidden)
		fail(status, gateFailureCode(status))
		return
	}

	if snapshotActions[req.Verb] {
		select {
		case s.snapshotSlots <- struct{}{}:
			defer func() { <-s.snapshotSlots }()
		default:
			fail(http.StatusServiceUnavailable, "action_capacity_unavailable")
			return
		}
	}

	dataplane.Serve(w, r, envelope, limits, func(ctx context.Context, raw json.RawMessage) (any, *actionwire.Error) {
		if req.Resource == connections.Resource {
			return s.runConnection(ctx, req, visible, raw)
		}
		return s.run(ctx, r, req, visible, raw)
	}, dataplane.WithErrorStatus(func(e *actionwire.Error) int {
		if status, ok := statusFor[e.Code]; ok {
			return status
		}
		return http.StatusBadGateway
	}))
}

// gateFailureCode names a gate failure by the status it maps to, so the
// envelope says as little as the body does.
func gateFailureCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "action_unauthenticated"
	case http.StatusBadRequest:
		return "invalid_action_route"
	case http.StatusForbidden:
		return "action_forbidden"
	default:
		return "action_unavailable"
	}
}

func wireError(code string) *actionwire.Error {
	return &actionwire.Error{Code: code, Message: strings.ReplaceAll(code, "_", " "), Retryable: false}
}

// run is the executor dataplane.Serve calls once the gates have passed and the
// body has been decoded to its "input" member.
func (s *Server) run(ctx context.Context, r *http.Request, req dataplane.Request, visible *unstructured.Unstructured, raw json.RawMessage) (any, *actionwire.Error) {
	// The commit verbs take their own input and never reach a git host: they
	// write this provider's bundle store and a RepositoryCommit, which the
	// commit controller applies. They therefore skip resolve() — there is no
	// credential to load — and pin the Repository themselves (commit.go).
	switch req.Verb {
	case Commit:
		return s.commit(ctx, req, visible, raw)
	case StageCommitBundle:
		return s.stageCommitBundle(ctx, req, visible, raw)
	case MintCloneToken:
		// A clone credential is minted from the Connection's own credential
		// material, not fetched from a git host, so this verb takes its own
		// input and never reaches the backend registry either.
		return s.mintCloneToken(ctx, req, visible, raw)
	}
	in, err := decodeInput(raw)
	if err != nil {
		return nil, wireError("invalid_action_input")
	}
	conn, repo, credential, err := s.resolve(ctx, req.ClusterID, req.Name, visible, in)
	if err != nil {
		return nil, wireError("action_forbidden")
	}
	implementation, ok := s.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		return nil, wireError("unsupported_provider")
	}
	collaboration, ok := implementation.(backend.Collaboration)
	if !ok {
		return nil, wireError("unsupported_action")
	}
	var output any
	switch req.Verb {
	case StageSnapshot:
		output, err = s.stage(r, req.ClusterID, in)
	case "branches":
		lister, ok := implementation.(backend.BranchLister)
		if !ok {
			return nil, wireError("unsupported_action")
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
			return nil, wireError("invalid_snapshot")
		}
		snapshot, loadErr := s.loadSnapshot(r, req.ClusterID, in)
		if loadErr != nil {
			return nil, wireError("snapshot_unavailable")
		}
		if req.Verb == "prepare_snapshot" {
			err = publisher.VerifySnapshot(ctx, conn, credential, repo, snapshot)
		} else {
			err = publisher.PublishSnapshot(ctx, conn, credential, repo, snapshot, in.Branch, in.ExpectedHead)
		}
		output = map[string]any{"commit": snapshot.Commit, "tree": snapshot.Tree}
	}
	if err != nil {
		if errors.Is(err, backend.ErrRepositoryIdentityConflict) {
			return nil, wireError("identity_conflict")
		}
		return nil, wireError("upstream_outcome_unconfirmed")
	}
	return output, nil
}

// decodeInput reads the action's own input strictly: an unknown member is a
// caller bug and is refused rather than ignored, the same way the envelope
// around it is.
func decodeInput(raw json.RawMessage) (Input, error) {
	var in Input
	if len(raw) == 0 || string(raw) == "null" {
		return in, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return Input{}, err
	}
	if err := decoder.Decode(new(any)); err == nil {
		return Input{}, errors.New("unexpected trailing action input")
	}
	return in, nil
}

// resolve pins what gate 1 returned against the provider's own read of the
// Repository and its Connection, then loads the Connection credential. The
// caller never reads a Secret; a replaced object, a changed spec or a
// mismatched identity fails closed.
func (s *Server) resolve(ctx context.Context, cluster, name string, visible *unstructured.Unstructured, input Input) (*api.Connection, *api.Repository, backend.Credential, error) {
	// Every action taking this input names the repository it means as
	// owner/name; an input that omits it is refused here rather than reaching
	// the binding with nothing to check.
	if input.Repository == "" {
		return nil, nil, backend.Credential{}, errors.New("repository action denied")
	}
	binding, err := s.pinRepositoryBinding(ctx, cluster, name, visible, input.RepositoryUID, input.ConnectionUID, input.Repository)
	if err != nil {
		return nil, nil, backend.Credential{}, err
	}
	credential, err := s.Credentials.ResolveStored(ctx, binding.conn, binding.secretKey, binding.data, binding.store)
	if err != nil {
		return nil, nil, backend.Credential{}, errors.New("repository action denied")
	}
	return binding.conn, binding.repo, credential, nil
}

// repositoryBinding is one pinned Repository, its Connection and the credential
// Secret's contents — everything an action needs to act on that repository as
// the tenant, and nothing about where the Secret lives.
type repositoryBinding struct {
	conn      *api.Connection
	repo      *api.Repository
	secretKey string
	data      map[string][]byte
	store     tenant.SecretStore
}

// pinRepositoryBinding is resolve() stopping one step short of a resolved
// credential: it proves the binding and opens the Secret, leaving what to mint
// from it to the caller. An action that issues its own narrowed token
// (mint_clone_token) must reach the GitHub App key itself, and must not mint
// the Connection's full credential on the way past.
//
// repository, when non-empty, is the owner/name the caller claims; it is
// checked before the Secret is ever read, so a caller that names the wrong
// repository never reaches a credential lookup.
func (s *Server) pinRepositoryBinding(ctx context.Context, cluster, name string, visible *unstructured.Unstructured, repositoryUID, connectionUID, repository string) (repositoryBinding, error) {
	fail := func() (repositoryBinding, error) {
		return repositoryBinding{}, errors.New("repository action denied")
	}
	if s.Authority == nil || repositoryUID == "" || connectionUID == "" || string(visible.GetUID()) != repositoryUID {
		return fail()
	}
	provider, err := s.Authority(ctx, cluster, repositories, name)
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
	if err != nil || string(connection.GetUID()) != connectionUID || connection.GetDeletionTimestamp() != nil {
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
	if repository != "" && !strings.EqualFold(repository, owner+"/"+repo.Spec.Name) {
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
	return repositoryBinding{conn: &conn, repo: &repo, secretKey: tenant.CredentialSecretID(&conn, ns), data: data, store: store}, nil
}
