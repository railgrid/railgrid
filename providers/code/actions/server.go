// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package actions serves caller-authorized, repository-bound provider actions.
//
// Every action is a kcp custom subresource on this provider's APIExport,
// reached as
//
//	/clusters/{id}/apis/code.railgrid.ai/v1alpha1/{repositories|connections}/{name}/{action}
//
// kcp authenticates the caller, authorizes `create` on {resource}/{action}
// with ordinary RBAC and reverse-proxies the request here with the caller's
// identity stamped in requestheader headers. provider-sdk/serve's adapter
// parses the path, checks the coordinate against the manifest and hands this
// handler the parsed route (dataplane.RouteFrom) and the caller
// (dataplane.ProxiedIdentityFrom). There is no bearer on an action.
//
// The gate and the response envelope come from provider-sdk/dataplane: Gate
// proves the caller can see the bound object (a SubjectAccessReview run on the
// caller's behalf) and returns the provider's own read of it together with a
// client acting AS THE PROVIDER through its export virtual workspace; Serve
// bounds the body, the deadline and the result. What stays here is what only
// this provider knows: pinning the caller's input against that read, resolving
// the Connection credential as the provider, and dispatching to a git backend.
package actions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

// StageSnapshot is the one served verb that is not in the CatalogEntry: its
// body carries a base64 git bundle far past the 1 MiB ceiling
// an action's limits.maxInputBytes allows
// (apis/providers/v1alpha1/actions.go). It is documented as the single
// catalogue exception in docs/provider-actions.md, "Uncatalogued large-upload
// verbs", declared as a plain verb on repositories so its coordinate stays
// grantable, and
// gated exactly like every catalogued action.
const StageSnapshot = "stage-snapshot"

// MaxInputBytes bounds the stage-snapshot body: a 25 MiB decoded bundle plus
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

// ContractVersion is the contract version every action here is declared at
// (every spec.export.resources[].actions[] entry declares version: v1, and an
// action's id is that name/version pair). The path does not carry it; serve's
// adapter restores it from the declaration onto the route.
const ContractVersion = "v1"

// served maps each action name to the limits its CatalogEntry declaration
// states (manifest.yaml). An action missing from this map is not served, so
// adding a catalogue entry and serving it are one edit.
var served = map[string]dataplane.Limits{
	"branches":            jsonAction(50),
	"branch-head":         jsonAction(50),
	"find-pull-request":   jsonAction(50),
	"pull-request":        jsonAction(50),
	"create-pull-request": jsonAction(50),
	"update-pull-request": jsonAction(50),
	"feedback":            jsonAction(2000),
	"comments":            jsonAction(50),
	"add-comment":         jsonAction(50),
	"reply-to-review":     jsonAction(50),
	"prepare-snapshot":    jsonAction(50),
	"publish-snapshot":    jsonAction(50),
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
	// mint-clone-token is bound to a Repository, so it is served here rather
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
const MintRegistryToken = "mint-registry-token"

// connectionActions are the actions bound to connections/{name} instead of
// repositories/{name}. kcp therefore authorizes connections/{action}, and a
// grant on a repository action never reaches one of these.
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
	"prepare-snapshot": true,
	"publish-snapshot": true,
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

// Server serves the repository- and connection-bound action routes.
type Server struct {
	// Callers builds the client the gate and every action run through: one
	// acting as the provider in the addressed cluster, through its export
	// virtual workspace. There is no caller credential on an action; anything
	// asked about the caller is a SubjectAccessReview run on their behalf.
	Callers  dataplane.ProviderCallerFactory
	Backends *backend.Registry
	// Bundles is this provider's own source-bundle store, which the commit
	// verbs write into. A RepositoryCommit is only a pointer at it.
	Bundles       commitbundle.Store
	Credentials   tenant.CredentialResolver
	SnapshotDir   string
	slots         chan struct{}
	snapshotSlots chan struct{}
}

func New(callers dataplane.ProviderCallerFactory, backends *backend.Registry) *Server {
	return &Server{Callers: callers, Backends: backends, slots: make(chan struct{}, 8), snapshotSlots: make(chan struct{}, 1)}
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
	// The route was parsed once, by serve's adapter, which also checked the
	// coordinate against the manifest and restored the contract version. A
	// request that did not come through it addresses nothing.
	route, ok := dataplane.RouteFrom(r.Context())
	if !ok {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	req := route.Request
	// Which resource an action is bound to decides which object the gate
	// reads, so it is resolved from the route before anything else happens.
	gvr, kind := repositories, "Repository"
	limits, isServed := served[req.Verb]
	if req.Resource == connections.Resource {
		gvr, kind = connections, "Connection"
		limits, isServed = connectionActions[req.Verb]
	}
	if route.Group != gvr.Group || route.APIVersion != gvr.Version ||
		(req.Resource != repositories.Resource && req.Resource != connections.Resource) ||
		req.Component != "" || req.Version != ContractVersion || req.Tail != "" || !isServed {
		http.NotFound(w, r)
		return
	}
	envelope := actionwire.New(r, "code", req.Verb, actionwire.ResourceRef{APIVersion: gvr.GroupVersion().String(), Kind: kind, Resource: gvr.Resource, Name: req.Name})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", envelope.RequestID)
	fail := func(status int, code string) {
		envelope.Failure(w, status, code, strings.ReplaceAll(code, "_", " "), false)
	}

	// Admission before the gate: a saturated process refuses work without
	// reading a body or touching kcp.
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		fail(http.StatusServiceUnavailable, "action_capacity_unavailable")
		return
	}

	// The gate: the caller can see the bound object (kcp already authorized
	// the verb itself before forwarding). What comes back is the provider's
	// own read of that object and a client acting as the provider, which is
	// what every action from here on acts through.
	visible, provider, err := dataplane.Gate(r.Context(), s.Callers, gvr, req)
	if err != nil {
		// The detail stays provider-side; the caller learns only the status.
		logger.V(3).Info("action refused", "action", req.Verb, "resource", gvr.Resource, "name", req.Name, "err", err)
		status := dataplane.StatusFor(err)
		fail(status, gateFailureCode(status))
		return
	}
	identity, _ := dataplane.ProxiedIdentityFrom(r.Context())

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
			return s.runConnection(ctx, provider, req, visible, raw)
		}
		return s.run(ctx, provider, identity, req, visible, raw)
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
	case http.StatusNotFound:
		// The contract's non-disclosing answer: the object is not there, or
		// the caller cannot see it, and the two are indistinguishable.
		return "action_not_found"
	default:
		return "action_unavailable"
	}
}

func wireError(code string) *actionwire.Error {
	return &actionwire.Error{Code: code, Message: strings.ReplaceAll(code, "_", " "), Retryable: false}
}

// run is the executor dataplane.Serve calls once the gate has passed and the
// body has been decoded to its "input" member. provider acts as this
// provider in the request's cluster; visible is the provider's read of the
// Repository the gate returned.
func (s *Server) run(ctx context.Context, provider dynamic.Interface, identity dataplane.ProxiedIdentity, req dataplane.Request, visible *unstructured.Unstructured, raw json.RawMessage) (any, *actionwire.Error) {
	// The commit verbs take their own input and never reach a git host: they
	// write this provider's bundle store and a RepositoryCommit, which the
	// commit controller applies. They therefore skip resolve() — there is no
	// credential to load — and pin the Repository themselves (commit.go).
	switch req.Verb {
	case Commit:
		return s.commit(ctx, provider, req, visible, raw)
	case StageCommitBundle:
		return s.stageCommitBundle(ctx, req, visible, raw)
	case MintCloneToken:
		// A clone credential is minted from the Connection's own credential
		// material, not fetched from a git host, so this verb takes its own
		// input and never reaches the backend registry either.
		return s.mintCloneToken(ctx, provider, visible, raw)
	}
	in, err := decodeInput(raw)
	if err != nil {
		return nil, wireError("invalid_action_input")
	}
	conn, repo, credential, err := s.resolve(ctx, provider, visible, in)
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
		output, err = s.stage(identity, req.ClusterID, in)
	case "branches":
		lister, ok := implementation.(backend.BranchLister)
		if !ok {
			return nil, wireError("unsupported_action")
		}
		output, err = lister.ListBranches(ctx, conn, credential, repo, in.Page)
	case "branch-head":
		var head string
		head, err = collaboration.BranchHead(ctx, conn, credential, repo, in.Branch)
		output = map[string]any{"head": head}
	case "find-pull-request":
		output, err = collaboration.FindPullRequest(ctx, conn, credential, repo, in.PullRequestInput)
	case "pull-request":
		output, err = collaboration.ReadPullRequest(ctx, conn, credential, repo, in.Number)
	case "create-pull-request":
		output, err = collaboration.CreatePullRequest(ctx, conn, credential, repo, in.PullRequestInput)
	case "update-pull-request":
		output, err = collaboration.UpdatePullRequest(ctx, conn, credential, repo, in.Number, in.PullRequestInput)
	case "feedback":
		output, err = collaboration.PullRequestFeedback(ctx, conn, credential, repo, in.Number, in.Commit)
	case "comments":
		output, err = collaboration.ListComments(ctx, conn, credential, repo, in.Number, in.Page)
	case "add-comment":
		output, err = collaboration.AddComment(ctx, conn, credential, repo, in.Number, in.Body)
	case "reply-to-review":
		output, err = collaboration.ReplyToReview(ctx, conn, credential, repo, in.Number, in.ParentCommentID, in.Body)
	case "prepare-snapshot", "publish-snapshot":
		publisher, ok := implementation.(backend.SnapshotPublisher)
		if !ok || in.Snapshot != nil || in.BundleRef == "" {
			return nil, wireError("invalid_snapshot")
		}
		snapshot, loadErr := s.loadSnapshot(identity, req.ClusterID, in)
		if loadErr != nil {
			return nil, wireError("snapshot_unavailable")
		}
		if req.Verb == "prepare-snapshot" {
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

// resolve pins the caller's input against the provider's read of the
// Repository and its Connection, then loads the Connection credential as the
// provider. The caller never reads a Secret; a replaced object or a
// mismatched identity fails closed.
func (s *Server) resolve(ctx context.Context, provider dynamic.Interface, visible *unstructured.Unstructured, input Input) (*api.Connection, *api.Repository, backend.Credential, error) {
	// Every action taking this input names the repository it means as
	// owner/name; an input that omits it is refused here rather than reaching
	// the binding with nothing to check.
	if input.Repository == "" {
		return nil, nil, backend.Credential{}, errors.New("repository action denied")
	}
	binding, err := s.pinRepositoryBinding(ctx, provider, visible, input.RepositoryUID, input.ConnectionUID, input.Repository)
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
// (mint-clone-token) must reach the GitHub App key itself, and must not mint
// the Connection's full credential on the way past.
//
// visible is the Repository as the gate read it — as the provider, through its
// export virtual workspace, so it is already the authoritative view. What the
// input pins is the CALLER's view: the UIDs say which Repository and which
// Connection the caller believes it is asking about, so an object deleted and
// recreated under the same name since the caller looked fails rather than
// being served. repository, when non-empty, is the owner/name the caller
// claims; it is checked before the Secret is ever read, so a caller that names
// the wrong repository never reaches a credential lookup.
func (s *Server) pinRepositoryBinding(ctx context.Context, provider dynamic.Interface, visible *unstructured.Unstructured, repositoryUID, connectionUID, repository string) (repositoryBinding, error) {
	fail := func() (repositoryBinding, error) {
		return repositoryBinding{}, errors.New("repository action denied")
	}
	if provider == nil || visible == nil || repositoryUID == "" || connectionUID == "" || string(visible.GetUID()) != repositoryUID || visible.GetDeletionTimestamp() != nil {
		return fail()
	}
	var repo api.Repository
	if runtime.DefaultUnstructuredConverter.FromUnstructured(visible.Object, &repo) != nil || repo.Status.RepoID == "" {
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
