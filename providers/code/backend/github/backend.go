/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package github implements the GitBackend interface against GitHub (and
// GitHub Enterprise Server via Connection.spec.baseURL) using a Personal
// Access Token. The token is supplied per call as backend.Credential — the
// backend holds no global credential; every Connection authenticates as its
// own account.
//
// PR B implements connection validation + repository lifecycle. Deploy keys
// and collaborators (the remaining GitBackend methods) land in PR C.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	gogithub "github.com/google/go-github/v66/github"
	"golang.org/x/oauth2"

	codev1alpha1 "github.com/faroshq/faros-kedge/providers/code/apis/v1alpha1"
	"github.com/faroshq/faros-kedge/providers/code/backend"
)

// Backend is the GitHub implementation of backend.GitBackend.
type Backend struct{}

// New returns a GitHub backend registered under "github".
func New() *Backend { return &Backend{} }

func (b *Backend) Name() string { return string(codev1alpha1.ProviderGitHub) }

// client builds a token-authenticated go-github client for one call. baseURL
// (Connection.spec.baseURL) targets GitHub Enterprise Server when set; empty
// uses the public github.com API.
func (b *Backend) client(ctx context.Context, cred backend.Credential, baseURL string) (*gogithub.Client, error) {
	if cred.Token == "" {
		return nil, errors.New("github: empty credential token")
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cred.Token})
	httpClient := oauth2.NewClient(ctx, ts)
	if baseURL == "" {
		return gogithub.NewClient(httpClient), nil
	}
	// NewEnterpriseClient normalizes the /api/v3 + upload URL suffixes for GHES.
	c, err := gogithub.NewClient(httpClient).WithEnterpriseURLs(baseURL, baseURL)
	if err != nil {
		return nil, fmt.Errorf("github: invalid baseURL %q: %w", baseURL, err)
	}
	return c, nil
}

// owner resolves the account a repository is created/looked-up under: the
// per-Repository override if set, else the Connection's owner.
func owner(conn *codev1alpha1.Connection, repo *codev1alpha1.Repository) string {
	if repo != nil && repo.Spec.Owner != "" {
		return repo.Spec.Owner
	}
	return conn.Spec.Owner
}

// ValidateConnection authenticates the token and returns the login + granted
// scopes. GitHub reports token scopes on the X-OAuth-Scopes response header.
func (b *Backend) ValidateConnection(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential) (string, []string, error) {
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return "", nil, err
	}
	// Empty user => the authenticated user.
	user, resp, err := c.Users.Get(ctx, "")
	if err != nil {
		return "", nil, classify(resp, err)
	}
	login := user.GetLogin()
	if login == "" {
		return "", nil, errors.New("github: authenticated but no login returned")
	}
	return login, parseScopes(resp), nil
}

// EnsureRepository creates the repository if absent and returns its host
// identifiers. Idempotent: an existing repo returns its current identifiers.
func (b *Backend) EnsureRepository(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository) (backend.RepositoryResult, error) {
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return backend.RepositoryResult{}, err
	}
	org := owner(conn, repo)

	// Look up first so the call is idempotent and we don't 422 on re-reconcile.
	existing, resp, err := c.Repositories.Get(ctx, org, repo.Spec.Name)
	if err == nil {
		return repoResult(existing), nil
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		return backend.RepositoryResult{}, classify(resp, err)
	}

	// Create. When org == the authenticated user, GitHub requires the org
	// argument to Create be "" (it creates under the caller); for a real org
	// it must be the org login. We can't cheaply tell which here, so try the
	// org form first and fall back to the user form on the "not an org" 404.
	newRepo := &gogithub.Repository{
		Name:        gogithub.String(repo.Spec.Name),
		Private:     gogithub.Bool(repo.Spec.Visibility != codev1alpha1.VisibilityPublic),
		Description: gogithub.String(repo.Spec.Description),
		AutoInit:    gogithub.Bool(repo.Spec.AutoInit),
	}
	if repo.Spec.DefaultBranch != "" {
		newRepo.DefaultBranch = gogithub.String(repo.Spec.DefaultBranch)
	}
	if repo.Spec.Visibility != "" {
		newRepo.Visibility = gogithub.String(string(repo.Spec.Visibility))
	}

	created, resp, err := c.Repositories.Create(ctx, org, newRepo)
	if err != nil {
		// org is actually a user account → retry with org="".
		if isNotOrg(resp) {
			created, resp, err = c.Repositories.Create(ctx, "", newRepo)
		}
		if err != nil {
			return backend.RepositoryResult{}, classify(resp, err)
		}
	}
	return repoResult(created), nil
}

// DeleteRepository removes the repository. Idempotent: a missing repo is success.
func (b *Backend) DeleteRepository(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository) error {
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return err
	}
	resp, err := c.Repositories.Delete(ctx, owner(conn, repo), repo.Spec.Name)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil
		}
		return classify(resp, err)
	}
	return nil
}

// errNotImplemented is returned by the deploy-key/collaborator methods until
// PR C wires them; the controllers for those kinds are skeletons in PR A/B, so
// these are never called on the hot path yet.
var errNotImplemented = errors.New("github: not implemented until PR C")

func (b *Backend) EnsureDeployKey(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, _ *codev1alpha1.DeployKey, _ string) (backend.DeployKeyResult, error) {
	return backend.DeployKeyResult{}, errNotImplemented
}

func (b *Backend) DeleteDeployKey(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, _ string) error {
	return errNotImplemented
}

func (b *Backend) EnsureCollaborator(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, _ *codev1alpha1.Collaborator) (backend.CollaboratorResult, error) {
	return backend.CollaboratorResult{}, errNotImplemented
}

func (b *Backend) RemoveCollaborator(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, _ *codev1alpha1.Collaborator) error {
	return errNotImplemented
}

// repoResult maps a go-github Repository to the backend result shape.
func repoResult(r *gogithub.Repository) backend.RepositoryResult {
	return backend.RepositoryResult{
		RepoID:   strconv.FormatInt(r.GetID(), 10),
		HTMLURL:  r.GetHTMLURL(),
		CloneURL: r.GetCloneURL(),
		SSHURL:   r.GetSSHURL(),
	}
}

// parseScopes reads GitHub's X-OAuth-Scopes header into a slice.
func parseScopes(resp *gogithub.Response) []string {
	if resp == nil || resp.Response == nil {
		return nil
	}
	raw := resp.Header.Get("X-OAuth-Scopes")
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	scopes := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			scopes = append(scopes, s)
		}
	}
	return scopes
}

// isNotOrg reports whether a Create error is GitHub's "owner is a user, not an
// org" signal (a 404 on the org create endpoint).
func isNotOrg(resp *gogithub.Response) bool {
	return resp != nil && resp.StatusCode == http.StatusNotFound
}

// classify turns a go-github error into a clearer message, surfacing auth
// failures distinctly so the controller's condition is actionable.
func classify(resp *gogithub.Response, err error) error {
	if resp != nil && resp.Response != nil {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("github: credential rejected (401): %w", err)
		case http.StatusForbidden:
			return fmt.Errorf("github: forbidden — token lacks scope or rate-limited (403): %w", err)
		}
	}
	return err
}
