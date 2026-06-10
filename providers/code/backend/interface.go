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

	codev1alpha1 "github.com/faroshq/faros-kedge/providers/code/apis/v1alpha1"
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
