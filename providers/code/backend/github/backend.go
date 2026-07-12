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
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	gogithub "github.com/google/go-github/v66/github"
	"golang.org/x/oauth2"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
)

// Backend is the GitHub implementation of backend.GitBackend.
type Backend struct{}

// New returns a GitHub backend registered under "github".
func New() *Backend { return &Backend{} }

func (b *Backend) Name() string { return string(codev1alpha1.ProviderGitHub) }

const repositoryCommitIdempotencyTrailer = "Kedge-RepositoryCommit:"

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

// CommitFiles creates one commit containing all supplied text files and moves
// the target branch. It uses GitHub's Git data API, so the provider never needs
// a local clone or working tree.
func (b *Backend) CommitFiles(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository, input backend.RepositoryCommitInput) (backend.RepositoryCommitResult, error) {
	if len(input.Files) == 0 {
		return backend.RepositoryCommitResult{}, errors.New("github: at least one file is required")
	}
	message := strings.TrimSpace(input.Message)
	if message == "" {
		message = "Update generated application files"
	}
	message = commitMessageWithIdempotencyKey(message, input.IdempotencyKey)
	branch := strings.TrimSpace(input.Branch)
	if branch == "" {
		branch = strings.TrimSpace(repo.Spec.DefaultBranch)
	}
	if branch == "" {
		branch = "main"
	}

	entries, files, err := gitTreeEntries(input.Files)
	if err != nil {
		return backend.RepositoryCommitResult{}, err
	}

	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return backend.RepositoryCommitResult{}, err
	}
	org := owner(conn, repo)
	refName := "heads/" + branch
	ref, resp, err := c.Git.GetRef(ctx, org, repo.Spec.Name, refName)
	if err != nil && (resp == nil || resp.StatusCode != http.StatusNotFound) {
		return backend.RepositoryCommitResult{}, classify(resp, err)
	}
	headSHA := ""
	if ref != nil && ref.GetObject() != nil {
		headSHA = ref.GetObject().GetSHA()
	}
	if headSHA != "" && strings.TrimSpace(input.IdempotencyKey) != "" {
		prior, found, err := findRepositoryCommitByIdempotencyKey(ctx, c, org, repo.Spec.Name, branch, input.IdempotencyKey)
		if err != nil {
			return backend.RepositoryCommitResult{}, err
		}
		if found {
			return backend.RepositoryCommitResult{
				CommitSHA: prior.GetSHA(),
				CommitURL: prior.GetHTMLURL(),
				Branch:    branch,
				Files:     files,
			}, nil
		}
	}
	var parent *gogithub.Commit
	baseTree := ""
	if headSHA != "" {
		parent, resp, err = c.Git.GetCommit(ctx, org, repo.Spec.Name, headSHA)
		if err != nil {
			return backend.RepositoryCommitResult{}, classify(resp, err)
		}
		if parent.GetTree() != nil {
			baseTree = parent.GetTree().GetSHA()
		}
	}
	tree, resp, err := c.Git.CreateTree(ctx, org, repo.Spec.Name, baseTree, entries)
	if err != nil {
		return backend.RepositoryCommitResult{}, classify(resp, err)
	}
	if shouldReuseHeadCommit(parent, tree, baseTree) {
		return backend.RepositoryCommitResult{
			CommitSHA: parent.GetSHA(),
			CommitURL: commitURL(org, repo, parent),
			Branch:    branch,
			Files:     files,
		}, nil
	}
	parents := []*gogithub.Commit(nil)
	if headSHA != "" {
		parents = []*gogithub.Commit{{SHA: gogithub.String(headSHA)}}
	}
	commit, resp, err := c.Git.CreateCommit(ctx, org, repo.Spec.Name, &gogithub.Commit{
		Message: gogithub.String(message),
		Tree:    tree,
		Parents: parents,
	}, nil)
	if err != nil {
		return backend.RepositoryCommitResult{}, classify(resp, err)
	}
	nextRef := &gogithub.Reference{
		Ref: gogithub.String("refs/" + refName),
		Object: &gogithub.GitObject{
			SHA: commit.SHA,
		},
	}
	if headSHA == "" {
		_, resp, err = c.Git.CreateRef(ctx, org, repo.Spec.Name, nextRef)
	} else {
		_, resp, err = c.Git.UpdateRef(ctx, org, repo.Spec.Name, nextRef, false)
	}
	if err != nil {
		if isRecoverableRefRace(resp, err) {
			if res, ok, retryErr := recoverOrRetryConcurrentCommit(ctx, c, org, repo, branch, commit.GetSHA(), input.IdempotencyKey, message, entries, files); retryErr != nil {
				return backend.RepositoryCommitResult{}, retryErr
			} else if ok {
				return res, nil
			}
		}
		return backend.RepositoryCommitResult{}, classify(resp, err)
	}
	return backend.RepositoryCommitResult{
		CommitSHA: commit.GetSHA(),
		CommitURL: commitURL(org, repo, commit),
		Branch:    branch,
		Files:     files,
	}, nil
}

func shouldReuseHeadCommit(parent *gogithub.Commit, tree *gogithub.Tree, baseTree string) bool {
	return parent != nil && tree != nil && tree.GetSHA() != "" && tree.GetSHA() == baseTree
}

func recoverOrRetryConcurrentCommit(ctx context.Context, c *gogithub.Client, org string, repo *codev1alpha1.Repository, branch, attemptedCommitSHA, idempotencyKey, message string, entries []*gogithub.TreeEntry, files []string) (backend.RepositoryCommitResult, bool, error) {
	head, ok, err := currentBranchHead(ctx, c, org, repo, branch)
	if err != nil {
		return backend.RepositoryCommitResult{}, false, err
	}
	if !ok {
		return backend.RepositoryCommitResult{}, false, nil
	}
	baseTree := ""
	if head.GetTree() != nil {
		baseTree = head.GetTree().GetSHA()
	}
	if concurrentHeadMatchesCommitRequest(head, attemptedCommitSHA, idempotencyKey) {
		return backend.RepositoryCommitResult{
			CommitSHA: head.GetSHA(),
			CommitURL: commitURL(org, repo, head),
			Branch:    branch,
			Files:     files,
		}, true, nil
	}
	tree, resp, err := c.Git.CreateTree(ctx, org, repo.Spec.Name, baseTree, entries)
	if err != nil {
		return backend.RepositoryCommitResult{}, false, classify(resp, err)
	}
	commit, resp, err := c.Git.CreateCommit(ctx, org, repo.Spec.Name, &gogithub.Commit{
		Message: gogithub.String(message),
		Tree:    tree,
		Parents: []*gogithub.Commit{{SHA: gogithub.String(head.GetSHA())}},
	}, nil)
	if err != nil {
		return backend.RepositoryCommitResult{}, false, classify(resp, err)
	}
	nextRef := &gogithub.Reference{
		Ref: gogithub.String("refs/heads/" + branch),
		Object: &gogithub.GitObject{
			SHA: commit.SHA,
		},
	}
	if _, resp, err = c.Git.UpdateRef(ctx, org, repo.Spec.Name, nextRef, false); err != nil {
		return backend.RepositoryCommitResult{}, false, classify(resp, err)
	}
	return backend.RepositoryCommitResult{
		CommitSHA: commit.GetSHA(),
		CommitURL: commitURL(org, repo, commit),
		Branch:    branch,
		Files:     files,
	}, true, nil
}

func concurrentHeadMatchesCommitRequest(head *gogithub.Commit, attemptedCommitSHA, idempotencyKey string) bool {
	if head == nil {
		return false
	}
	if attemptedCommitSHA != "" && head.GetSHA() == attemptedCommitSHA {
		return true
	}
	return commitMessageHasIdempotencyKey(head.GetMessage(), idempotencyKey)
}

func currentBranchHead(ctx context.Context, c *gogithub.Client, org string, repo *codev1alpha1.Repository, branch string) (*gogithub.Commit, bool, error) {
	ref, resp, err := c.Git.GetRef(ctx, org, repo.Spec.Name, "heads/"+branch)
	if err != nil {
		return nil, false, classify(resp, err)
	}
	if ref == nil || ref.GetObject() == nil || ref.GetObject().GetSHA() == "" {
		return nil, false, nil
	}
	head, resp, err := c.Git.GetCommit(ctx, org, repo.Spec.Name, ref.GetObject().GetSHA())
	if err != nil {
		return nil, false, classify(resp, err)
	}
	if head == nil {
		return nil, false, nil
	}
	return head, true, nil
}

func isRecoverableRefRace(resp *gogithub.Response, err error) bool {
	return isNotFastForward(resp, err) || isReferenceAlreadyExists(resp, err)
}

func isNotFastForward(resp *gogithub.Response, err error) bool {
	if resp == nil || resp.StatusCode != http.StatusUnprocessableEntity || err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not a fast forward")
}

func isReferenceAlreadyExists(resp *gogithub.Response, err error) bool {
	if resp == nil || resp.StatusCode != http.StatusUnprocessableEntity || err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "reference already exists")
}

func commitMessageWithIdempotencyKey(message, key string) string {
	key = strings.TrimSpace(key)
	if key == "" || strings.Contains(message, repositoryCommitIdempotencyTrailer) {
		return message
	}
	return message + "\n\n" + repositoryCommitIdempotencyTrailer + " " + key
}

func findRepositoryCommitByIdempotencyKey(ctx context.Context, c *gogithub.Client, org, repo, branch, key string) (*gogithub.RepositoryCommit, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, false, nil
	}
	commits, resp, err := c.Repositories.ListCommits(ctx, org, repo, &gogithub.CommitsListOptions{
		SHA: branch,
		ListOptions: gogithub.ListOptions{
			PerPage: 50,
		},
	})
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusConflict) {
			return nil, false, nil
		}
		return nil, false, classify(resp, err)
	}
	for _, commit := range commits {
		if commit == nil || commit.GetCommit() == nil {
			continue
		}
		if commitMessageHasIdempotencyKey(commit.GetCommit().GetMessage(), key) {
			return commit, true, nil
		}
	}
	return nil, false, nil
}

func commitMessageHasIdempotencyKey(message, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	for _, line := range strings.Split(message, "\n") {
		if strings.TrimSpace(line) == repositoryCommitIdempotencyTrailer+" "+key {
			return true
		}
	}
	return false
}

func commitURL(org string, repo *codev1alpha1.Repository, commit *gogithub.Commit) string {
	if commit == nil {
		return ""
	}
	if url := commit.GetHTMLURL(); url != "" {
		return url
	}
	if commit.GetSHA() == "" {
		return ""
	}
	repoURL := strings.TrimRight(repo.Status.HTMLURL, "/")
	if repoURL == "" {
		repoURL = "https://github.com/" + org + "/" + repo.Spec.Name
	}
	return repoURL + "/commit/" + commit.GetSHA()
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
			info := packageInfo(p)
			// Resolve versions (tags + digest) and the pullable image path for
			// image packages so callers can map a build tag to a deployable
			// reference. Best-effort: a versions failure must not drop the
			// package from the crawl.
			if pt == "container" || pt == "docker" {
				info.ImageRepository = "ghcr.io/" + strings.ToLower(org) + "/" + strings.ToLower(p.GetName())
				if versions, err := listPackageVersions(ctx, c, org, pt, p.GetName()); err == nil {
					info.Versions = versions
				}
			}
			out = append(out, info)
		}
	}
	return out, nil
}

// packageVersionsMax bounds how many recent versions we resolve per package —
// enough to cover recent build tags without unbounded API paging.
const packageVersionsMax = 100

// listPackageVersions returns a package's versions (most recent first, bounded)
// with their tags and digest. Tries the organization endpoint, falling back to
// the user endpoint on the "owner is a user" signal, like listPackagesOfType.
func listPackageVersions(ctx context.Context, c *gogithub.Client, org, pkgType, pkgName string) ([]backend.PackageVersion, error) {
	// Package names can contain "/" (e.g. "repo/component"); the API path
	// segment must be escaped.
	name := url.PathEscape(pkgName)
	opt := &gogithub.PackageListOptions{ListOptions: gogithub.ListOptions{PerPage: packageVersionsMax}}
	asUser := false
	var versions []*gogithub.PackageVersion
	for {
		var (
			page []*gogithub.PackageVersion
			resp *gogithub.Response
			err  error
		)
		if asUser {
			page, resp, err = c.Users.PackageGetAllVersions(ctx, org, pkgType, name, opt)
		} else {
			page, resp, err = c.Organizations.PackageGetAllVersions(ctx, org, pkgType, name, opt)
			if err != nil && isNotOrg(resp) {
				asUser = true
				continue
			}
		}
		if err != nil {
			return nil, classify(resp, err)
		}
		versions = append(versions, page...)
		if resp == nil || resp.NextPage == 0 || len(versions) >= packageVersionsMax {
			break
		}
		opt.Page = resp.NextPage
	}

	out := make([]backend.PackageVersion, 0, len(versions))
	for _, v := range versions {
		created := ""
		if t := v.GetCreatedAt(); !t.IsZero() {
			created = t.UTC().Format(time.RFC3339)
		}
		pv := backend.PackageVersion{Digest: v.GetName(), CreatedAt: created}
		if meta := v.GetMetadata(); meta != nil {
			if container := meta.GetContainer(); container != nil {
				pv.Tags = container.Tags
			}
		}
		out = append(out, pv)
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

func gitTreeEntries(files []backend.RepositoryCommitFile) ([]*gogithub.TreeEntry, []string, error) {
	byPath := map[string]string{}
	for _, f := range files {
		clean, err := cleanRepositoryPath(f.Path)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := byPath[clean]; exists {
			return nil, nil, fmt.Errorf("github: duplicate file path %q", clean)
		}
		byPath[clean] = f.Content
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	entries := make([]*gogithub.TreeEntry, 0, len(paths))
	for _, p := range paths {
		entries = append(entries, &gogithub.TreeEntry{
			Path:    gogithub.String(p),
			Mode:    gogithub.String("100644"),
			Type:    gogithub.String("blob"),
			Content: gogithub.String(byPath[p]),
		})
	}
	return entries, paths, nil
}

func cleanRepositoryPath(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" {
		return "", errors.New("github: file path cannot be empty")
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("github: file path %q must be relative", raw)
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", fmt.Errorf("github: file path %q cannot contain ..", raw)
		}
		if strings.ContainsRune(part, 0) {
			return "", fmt.Errorf("github: file path %q cannot contain NUL", raw)
		}
	}
	clean := strings.TrimPrefix(path.Clean("/"+raw), "/")
	if clean == "." || clean == "" {
		return "", errors.New("github: file path cannot be empty")
	}
	return clean, nil
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
