/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package backend defines the contract every code-provider sub-provider
// (git host) implements. The controller layer (Connection / Repository /
// DeployKey / Collaborator reconcilers) never depends on a specific host —
// it dispatches through this interface.
//
// PR A ships the interface + a stub used by the controllers' smoke tests.
// PR B ships the real github backend. Future PRs add gitlab, etc.; nothing
// in this file or in the reconcilers changes when they land.
//
// Unlike the infrastructure provider's Backend, GitBackend has NO Run method:
// the code provider's controllers own the watch loop (one multicluster
// manager across all tenant workspaces). A GitBackend is a pure remote-API
// dispatcher — every method is a synchronous, idempotent call against the
// host, given the already-resolved credential.
package backend

import (
	"context"
	"fmt"
	"sort"
	"sync"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
)

// Credential is the resolved secret material a GitBackend authenticates with.
// For type=pat this carries the token; future credential types (github-app,
// oauth) add fields without breaking existing backends.
type Credential struct {
	// Token is the PAT (or, later, a minted installation/oauth token).
	Token string
}

// RepositoryResult is what EnsureRepository reports back; the reconciler
// mirrors it onto Repository.status.
type RepositoryResult struct {
	RepoID   string
	HTMLURL  string
	CloneURL string
	SSHURL   string
}

// RepositoryCommitFile is one text file to write in a repository commit.
type RepositoryCommitFile struct {
	Path    string
	Content string
}

// RepositoryCommitInput describes a single commit authored through a backend.
type RepositoryCommitInput struct {
	Message        string
	Branch         string
	IdempotencyKey string
	Files          []RepositoryCommitFile
}

// RepositoryCommitResult is what a backend returns after moving the branch ref.
type RepositoryCommitResult struct {
	CommitSHA string
	CommitURL string
	Branch    string
	Files     []string
}

// DeployKeyResult is what EnsureDeployKey reports back. PublicKey echoes the
// key actually registered (useful when the backend, not the caller, supplied
// it — though key generation happens in the controller, not the backend).
type DeployKeyResult struct {
	KeyID string
}

// CollaboratorResult is what EnsureCollaborator reports back. Pending is true
// when the host created an invitation the user must still accept.
type CollaboratorResult struct {
	Pending      bool
	InvitationID string
}

// PackageInfo describes one package (artifact) published under a repository on
// the host — e.g. a container image, npm or maven package. It is read-only
// OBSERVED state: packages appear when artifacts are pushed (docker push, npm
// publish, …), never via an API "create", so there is no Ensure/Delete here.
type PackageInfo struct {
	// Name is the package's name on the host.
	Name string
	// Type is the ecosystem: container | docker | npm | maven | rubygems | nuget.
	Type string
	// Visibility is "public", "internal", or "private" (host-reported, may be empty).
	Visibility string
	// HTMLURL links to the package's browser page.
	HTMLURL string
	// ImageRepository is the pullable registry path (no tag/digest) for image
	// packages, e.g. "ghcr.io/owner/repo/component" — combine with a version's
	// Digest to form a deployable reference. Empty for non-image packages.
	ImageRepository string
	// VersionCount is how many versions the host reports (0 when unknown).
	VersionCount int64
	// UpdatedAt is the last-updated time in RFC3339, or "" when unknown.
	UpdatedAt string
	// Versions is a bounded, most-recent-first list of the package's published
	// versions with their tags and digest. Populated for container/docker
	// packages (empty for ecosystems without a digest/tag model, or when the
	// backend does not resolve versions). Lets consumers map a build tag (e.g.
	// "sha-<commit>") to an immutable image digest without any other datastore.
	Versions []PackageVersion
}

// PackageVersion is one published version of a package: an immutable digest and
// the tags currently pointing at it (a container image push creates one).
type PackageVersion struct {
	// Digest is the version's immutable content digest, e.g. "sha256:…".
	Digest string
	// Tags are the tags pointing at this digest (e.g. "sha-<commit>", "latest").
	Tags []string
	// CreatedAt is the version's creation time in RFC3339, or "" when unknown.
	CreatedAt string
}

// PackageLister is an OPTIONAL capability a backend may implement to expose the
// packages published under a repository. It is intentionally NOT part of
// GitBackend: it is read-only and consumed only by the portal's packages view,
// not by the reconcilers. Callers type-assert for it and report "unsupported"
// for backends (e.g. the test stub) that don't implement it.
type PackageLister interface {
	// ListPackages returns the packages linked to repo. Idempotent, read-only.
	ListPackages(ctx context.Context, conn *codev1alpha1.Connection, cred Credential, repo *codev1alpha1.Repository) ([]PackageInfo, error)
}

// WorkflowRunQuery identifies the CI run to inspect.
type WorkflowRunQuery struct {
	// WorkflowFileName is the workflow file (e.g. "kedge-app-studio-build.yml").
	WorkflowFileName string
	// HeadSHA optionally pins the commit; empty means the most recent run.
	HeadSHA string
	// MaxLogLines caps the failure-log tail returned per failed job.
	MaxLogLines int
}

// WorkflowJobStatus is one job of a workflow run.
type WorkflowJobStatus struct {
	Name       string
	Status     string // queued | in_progress | completed
	Conclusion string // success | failure | cancelled | ... | "" while running
	// FailureLog is a bounded tail of the job's logs, populated only for a
	// failed job so the caller can see why it broke.
	FailureLog string
}

// WorkflowRunStatus is the observed state of a CI run for a commit.
type WorkflowRunStatus struct {
	// Found is false when no run exists for the query (e.g. the workflow has
	// never run for that commit) — not an error.
	Found      bool
	RunID      int64
	HTMLURL    string
	HeadSHA    string
	Status     string // queued | in_progress | completed
	Conclusion string // success | failure | ... | "" while running
	Jobs       []WorkflowJobStatus
}

// WorkflowRunReader is an OPTIONAL capability: read a workflow's latest run for
// a commit, including failed jobs' log tails. Consumed by the build-doctor so a
// failing build can be diagnosed, not just detected.
type WorkflowRunReader interface {
	LatestWorkflowRun(ctx context.Context, conn *codev1alpha1.Connection, cred Credential, repo *codev1alpha1.Repository, query WorkflowRunQuery) (WorkflowRunStatus, error)
}

// WorkflowDispatcher is an OPTIONAL capability: re-run a workflow without a code
// change (the workflow must declare workflow_dispatch), so a flaky/failed build
// can be retried.
type WorkflowDispatcher interface {
	DispatchWorkflow(ctx context.Context, conn *codev1alpha1.Connection, cred Credential, repo *codev1alpha1.Repository, workflowFileName, ref string) error
}

// RepositoryCommitter is an OPTIONAL capability for backends that can write
// text files to a repository without requiring a local git clone.
type RepositoryCommitter interface {
	CommitFiles(ctx context.Context, conn *codev1alpha1.Connection, cred Credential, repo *codev1alpha1.Repository, input RepositoryCommitInput) (RepositoryCommitResult, error)
}

// RepositoryCheckoutInput bounds a text-tree read. Zero limits apply the
// backend's defaults; the caller (the RepositoryCheckout controller) sets
// them from the platform's workspace bounds.
type RepositoryCheckoutInput struct {
	// Ref is the branch, tag, or commit SHA to read; empty means the
	// repository's default branch.
	Ref string
	// MaxFiles caps how many files the checkout returns.
	MaxFiles int
	// MaxFileBytes caps one file's size; larger files are skipped.
	MaxFileBytes int64
	// MaxTotalBytes caps the checkout's total content size; files beyond it
	// are skipped.
	MaxTotalBytes int64
}

// RepositoryCheckoutResult is the text tree a backend read: UTF-8 files plus
// the paths it skipped (binary, oversized, over the caps).
type RepositoryCheckoutResult struct {
	Ref       string
	CommitSHA string
	Files     []RepositoryCommitFile
	Skipped   []string
}

// RepositoryReader is an OPTIONAL capability for backends that can read a
// repository's text tree without a local git clone — the CommitFiles flow in
// reverse, consumed by App Studio workspace hydration and repo import.
type RepositoryReader interface {
	CheckoutFiles(ctx context.Context, conn *codev1alpha1.Connection, cred Credential, repo *codev1alpha1.Repository, input RepositoryCheckoutInput) (RepositoryCheckoutResult, error)
}

// GitBackend is the seam between the controllers and a concrete git host.
// Every method is idempotent: the reconciler calls it on every pass for a
// given generation, so a backend MUST treat "already in the desired state"
// as success, and "already gone" (on delete) as success too.
type GitBackend interface {
	// Name MUST match codev1alpha1.GitProvider used in Connection.spec.provider
	// (lower-case: "github"). Registered at process startup via Registry.Register.
	Name() string

	// ValidateConnection authenticates cred against the host and returns the
	// authenticated login plus any discoverable token scopes. An error means
	// the credential is bad or the host is unreachable; the ConnectionController
	// surfaces it on the Validated condition.
	ValidateConnection(ctx context.Context, conn *codev1alpha1.Connection, cred Credential) (login string, scopes []string, err error)

	// EnsureRepository creates the repository if absent and returns its host
	// identifiers. Idempotent: an existing repo returns its current identifiers.
	EnsureRepository(ctx context.Context, conn *codev1alpha1.Connection, cred Credential, repo *codev1alpha1.Repository) (RepositoryResult, error)
	// DeleteRepository removes the repository. Idempotent: a missing repo is success.
	DeleteRepository(ctx context.Context, conn *codev1alpha1.Connection, cred Credential, repo *codev1alpha1.Repository) error

	// EnsureDeployKey registers publicKey on the repo and returns its host id.
	// Idempotent on the (repo, key) pair.
	EnsureDeployKey(ctx context.Context, conn *codev1alpha1.Connection, cred Credential, repo *codev1alpha1.Repository, key *codev1alpha1.DeployKey, publicKey string) (DeployKeyResult, error)
	// DeleteDeployKey removes the key identified by keyID. Idempotent.
	DeleteDeployKey(ctx context.Context, conn *codev1alpha1.Connection, cred Credential, repo *codev1alpha1.Repository, keyID string) error

	// EnsureCollaborator grants the user the requested permission. Idempotent.
	EnsureCollaborator(ctx context.Context, conn *codev1alpha1.Connection, cred Credential, repo *codev1alpha1.Repository, collab *codev1alpha1.Collaborator) (CollaboratorResult, error)
	// RemoveCollaborator revokes the grant (or cancels a pending invitation). Idempotent.
	RemoveCollaborator(ctx context.Context, conn *codev1alpha1.Connection, cred Credential, repo *codev1alpha1.Repository, collab *codev1alpha1.Collaborator) error
}

// Registry holds the backends a process registered, indexed by Name(). The
// reconcilers look here when dispatching, keyed by Connection.spec.provider.
// Concurrency-safe; registration happens during single-threaded startup.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]GitBackend
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]GitBackend{}}
}

// Register adds a backend by Name(). Errors on nil/unnamed/duplicate so main()
// can fail fast rather than silently overwrite.
func (r *Registry) Register(b GitBackend) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b == nil || b.Name() == "" {
		return fmt.Errorf("backend: cannot register nil or unnamed backend")
	}
	if _, ok := r.byName[b.Name()]; ok {
		return fmt.Errorf("backend: %q already registered", b.Name())
	}
	r.byName[b.Name()] = b
	return nil
}

// Get returns the backend registered under name, or ok=false when unknown so
// the reconciler can set a ProviderNotFound condition without crashing.
func (r *Registry) Get(name string) (GitBackend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.byName[name]
	return b, ok
}

// Names returns every registered backend's Name() in deterministic order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
