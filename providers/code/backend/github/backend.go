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
	"time"

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

// EnsureDeployKey registers publicKey on the repo and returns its host id.
// Idempotent on the key material: an already-registered identical key returns
// its existing id rather than 422-ing.
func (b *Backend) EnsureDeployKey(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository, key *codev1alpha1.DeployKey, publicKey string) (backend.DeployKeyResult, error) {
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return backend.DeployKeyResult{}, err
	}
	org := owner(conn, repo)

	// Idempotency: a re-reconcile must not create a duplicate. GitHub rejects
	// duplicate key material with a 422, so match an existing key first.
	keys, resp, err := c.Repositories.ListKeys(ctx, org, repo.Spec.Name, &gogithub.ListOptions{PerPage: 100})
	if err != nil {
		return backend.DeployKeyResult{}, classify(resp, err)
	}
	for _, k := range keys {
		if sameKeyMaterial(k.GetKey(), publicKey) {
			return backend.DeployKeyResult{KeyID: strconv.FormatInt(k.GetID(), 10)}, nil
		}
	}

	title := key.Spec.Title
	if title == "" {
		title = key.Name
	}
	created, resp, err := c.Repositories.CreateKey(ctx, org, repo.Spec.Name, &gogithub.Key{
		Title:    gogithub.String(title),
		Key:      gogithub.String(publicKey),
		ReadOnly: gogithub.Bool(key.Spec.ReadOnly),
	})
	if err != nil {
		return backend.DeployKeyResult{}, classify(resp, err)
	}
	return backend.DeployKeyResult{KeyID: strconv.FormatInt(created.GetID(), 10)}, nil
}

// DeleteDeployKey removes the key identified by keyID. Idempotent: an empty or
// missing key is success.
func (b *Backend) DeleteDeployKey(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository, keyID string) error {
	if keyID == "" {
		return nil
	}
	id, err := strconv.ParseInt(keyID, 10, 64)
	if err != nil {
		return fmt.Errorf("github: invalid deploy key id %q: %w", keyID, err)
	}
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return err
	}
	resp, err := c.Repositories.DeleteKey(ctx, owner(conn, repo), repo.Spec.Name, id)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil
		}
		return classify(resp, err)
	}
	return nil
}

// EnsureCollaborator grants the user the requested permission. Idempotent.
// Pending is true when GitHub created an invitation the user must still accept
// (the usual case for someone who is not already a member/collaborator).
func (b *Backend) EnsureCollaborator(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository, collab *codev1alpha1.Collaborator) (backend.CollaboratorResult, error) {
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return backend.CollaboratorResult{}, err
	}
	perm := string(collab.Spec.Permission)
	if perm == "" {
		perm = string(codev1alpha1.PermissionPull)
	}
	inv, resp, err := c.Repositories.AddCollaborator(ctx, owner(conn, repo), repo.Spec.Name, collab.Spec.Username, &gogithub.RepositoryAddCollaboratorOptions{Permission: perm})
	if err != nil {
		return backend.CollaboratorResult{}, classify(resp, err)
	}
	// 204 No Content => the user was already a collaborator (no invitation).
	if resp != nil && resp.StatusCode == http.StatusNoContent {
		return backend.CollaboratorResult{Pending: false}, nil
	}
	if inv != nil && inv.GetID() != 0 {
		return backend.CollaboratorResult{Pending: true, InvitationID: strconv.FormatInt(inv.GetID(), 10)}, nil
	}
	return backend.CollaboratorResult{Pending: false}, nil
}

// RemoveCollaborator revokes the grant and cancels any pending invitation.
// Idempotent.
func (b *Backend) RemoveCollaborator(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository, collab *codev1alpha1.Collaborator) error {
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return err
	}
	org := owner(conn, repo)

	// Cancel a still-pending invitation (RemoveCollaborator only affects
	// accepted collaborators, so an outstanding invite would otherwise linger).
	invites, resp, err := c.Repositories.ListInvitations(ctx, org, repo.Spec.Name, &gogithub.ListOptions{PerPage: 100})
	if err == nil {
		for _, inv := range invites {
			if inv.GetInvitee() != nil && strings.EqualFold(inv.GetInvitee().GetLogin(), collab.Spec.Username) {
				if _, derr := c.Repositories.DeleteInvitation(ctx, org, repo.Spec.Name, inv.GetID()); derr != nil {
					return fmt.Errorf("github: cancel invitation for %q: %w", collab.Spec.Username, derr)
				}
			}
		}
	} else if resp == nil || resp.StatusCode != http.StatusNotFound {
		return classify(resp, err)
	}

	resp, err = c.Repositories.RemoveCollaborator(ctx, org, repo.Spec.Name, collab.Spec.Username)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil
		}
		return classify(resp, err)
	}
	return nil
}

// packageTypes are GitHub's package ecosystems. The list-packages API REQUIRES
// a package_type filter, so we query each ecosystem and merge the results.
var packageTypes = []string{"container", "docker", "npm", "maven", "rubygems", "nuget"}

// ListPackages returns the packages published under repo's owner that are linked
// to repo. GitHub has no per-repository packages endpoint, so we list the
// owner's packages per ecosystem (org endpoint, falling back to the user
// endpoint when the owner is a user account) and filter by repository. Read-only
// — packages are created by pushing artifacts, not through this call.
func (b *Backend) ListPackages(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository) ([]backend.PackageInfo, error) {
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return nil, err
	}
	org := owner(conn, repo)

	var out []backend.PackageInfo
	for _, pt := range packageTypes {
		pkgs, err := listPackagesOfType(ctx, c, org, pt)
		if err != nil {
			return nil, err
		}
		for _, p := range pkgs {
			if p.GetRepository() == nil || !strings.EqualFold(p.GetRepository().GetName(), repo.Spec.Name) {
				continue
			}
			out = append(out, packageInfo(p))
		}
	}
	return out, nil
}

// listPackagesOfType pages through one ecosystem's packages for org. It tries
// the organization endpoint first and falls back to the user endpoint on the
// "owner is a user, not an org" 404 (same signal EnsureRepository handles).
func listPackagesOfType(ctx context.Context, c *gogithub.Client, org, pkgType string) ([]*gogithub.Package, error) {
	opt := &gogithub.PackageListOptions{
		PackageType: gogithub.String(pkgType),
		ListOptions: gogithub.ListOptions{PerPage: 100},
	}
	var all []*gogithub.Package
	asUser := false
	for {
		var (
			page []*gogithub.Package
			resp *gogithub.Response
			err  error
		)
		if asUser {
			page, resp, err = c.Users.ListPackages(ctx, org, opt)
		} else {
			page, resp, err = c.Organizations.ListPackages(ctx, org, opt)
			if err != nil && isNotOrg(resp) {
				asUser = true
				continue // re-issue the same page against the user endpoint
			}
		}
		if err != nil {
			return nil, classify(resp, err)
		}
		all = append(all, page...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return all, nil
}

// packageInfo maps a go-github Package to the backend result shape.
func packageInfo(p *gogithub.Package) backend.PackageInfo {
	updated := ""
	if t := p.GetUpdatedAt(); !t.IsZero() {
		updated = t.UTC().Format(time.RFC3339)
	}
	return backend.PackageInfo{
		Name:         p.GetName(),
		Type:         p.GetPackageType(),
		Visibility:   p.GetVisibility(),
		HTMLURL:      p.GetHTMLURL(),
		VersionCount: p.GetVersionCount(),
		UpdatedAt:    updated,
	}
}

// sameKeyMaterial compares two OpenSSH public keys by type + base64 body,
// ignoring the trailing comment GitHub may add or strip.
func sameKeyMaterial(a, b string) bool {
	fa := strings.Fields(a)
	fb := strings.Fields(b)
	if len(fa) < 2 || len(fb) < 2 {
		return false
	}
	return fa[0] == fb[0] && fa[1] == fb[1]
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
