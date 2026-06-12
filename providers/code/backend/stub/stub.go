/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package stub is a no-op GitBackend that returns canned success. It lets the
// code provider's controllers be wired and smoke-tested end-to-end before the
// real github backend (PR B) lands: a Connection flips to Validated, a
// Repository gets synthesized URLs, etc. It performs NO network calls.
package stub

import (
	"context"
	"fmt"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
)

// Backend is the canned-success stub. The Seen* fields let tests assert which
// calls the reconcilers made.
type Backend struct {
	// name is the provider key this stub answers to. Defaults to "github" so
	// it can stand in for the real backend during PR A.
	name string
}

// New returns a stub registered under "github" (so it transparently stands in
// for the real backend until PR B replaces it).
func New() *Backend {
	return &Backend{name: string(codev1alpha1.ProviderGitHub)}
}

// NewNamed returns a stub registered under an arbitrary provider key — handy
// for tests that register more than one.
func NewNamed(name string) *Backend {
	return &Backend{name: name}
}

func (b *Backend) Name() string { return b.name }

func (b *Backend) ValidateConnection(_ context.Context, conn *codev1alpha1.Connection, _ backend.Credential) (string, []string, error) {
	login := conn.Spec.Owner
	if login == "" {
		login = "stub-user"
	}
	return login, []string{"repo"}, nil
}

func (b *Backend) EnsureRepository(_ context.Context, conn *codev1alpha1.Connection, _ backend.Credential, repo *codev1alpha1.Repository) (backend.RepositoryResult, error) {
	owner := repo.Spec.Owner
	if owner == "" {
		owner = conn.Spec.Owner
	}
	full := owner + "/" + repo.Spec.Name
	return backend.RepositoryResult{
		RepoID:   "stub-" + repo.Spec.Name,
		HTMLURL:  "https://stub.example/" + full,
		CloneURL: "https://stub.example/" + full + ".git",
		SSHURL:   "git@stub.example:" + full + ".git",
	}, nil
}

func (b *Backend) DeleteRepository(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository) error {
	return nil
}

func (b *Backend) EnsureDeployKey(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, repo *codev1alpha1.Repository, key *codev1alpha1.DeployKey, publicKey string) (backend.DeployKeyResult, error) {
	if publicKey == "" {
		return backend.DeployKeyResult{}, fmt.Errorf("stub: no public key supplied for deploy key %q", key.Name)
	}
	return backend.DeployKeyResult{KeyID: "stub-key-" + key.Name}, nil
}

func (b *Backend) DeleteDeployKey(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, _ string) error {
	return nil
}

func (b *Backend) EnsureCollaborator(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, collab *codev1alpha1.Collaborator) (backend.CollaboratorResult, error) {
	return backend.CollaboratorResult{Pending: false, InvitationID: "stub-invite-" + collab.Name}, nil
}

func (b *Backend) RemoveCollaborator(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, _ *codev1alpha1.Collaborator) error {
	return nil
}
