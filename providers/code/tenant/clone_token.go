// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
)

// A Factory runner has to clone the tenant's repository before it can work in
// it. It used to be handed a checkout that already existed on the edge host,
// which only works while every attempt lands on a host that happens to hold
// one. The alternative it must not have is the Connection's own credential:
// that one can push to every repository the connection reaches, and it would
// sit in a runner's environment for as long as the attempt runs.
//
// mint_clone_token is mint_registry_token's repository-bound sibling. The
// credential stays inside Code, the consumer asks for one repository, and what
// comes back is a CLONE credential: for a GitHub App connection a fresh
// installation token issued with contents:read alone and about an hour to
// live, which is the narrowest thing GitHub will hand out for a git fetch.

// ClonePermissions is what mint_clone_token asks a GitHub App installation
// for. A runner holds the token for the length of an attempt, so it must be
// able to read a repository's contents and nothing else — not write them, not
// packages, not workflows.
var ClonePermissions = map[string]string{"contents": "read"}

// CloneUsername is the username GitHub expects beside a token in an HTTPS git
// exchange. The token is what authenticates; the username is a constant.
const CloneUsername = "x-access-token"

// CloneToken is one read-only clone credential for a single repository.
type CloneToken struct {
	// RemoteURL is the repository's HTTPS clone URL. It never carries the
	// credential inline: a URL with userinfo in it ends up in a runner's
	// `git remote -v`, its reflog and any error it prints.
	RemoteURL string
	// Username is what the git transport sends beside the token.
	Username string
	// Token is the credential itself. Empty means the connection holds no
	// usable credential — a public repository still clones without one, so
	// the remote is reported rather than refused.
	Token string
	// ExpiresAt is when it stops working. Zero means the credential does not
	// expire on its own, which a consumer that caches it should know.
	ExpiresAt time.Time
	// Scoped is true when the token was issued with read-only, single-purpose
	// permissions. A PAT or OAuth connection cannot be narrowed by any GitHub
	// API, so the answer there is false and the caller can decide what to do
	// about it.
	Scoped bool
}

// CloneRemoteFor returns the HTTPS clone URL of one repository.
//
// The Repository's observed status.cloneURL is the host's own answer and is
// preferred; a Repository whose controller has not reported one yet (or whose
// host reported something that is not an HTTPS URL) falls back to the same
// derivation the github backend uses for its git transport — the connection's
// origin plus owner/name.
func CloneRemoteFor(conn *api.Connection, repo *api.Repository) (string, error) {
	if conn == nil || repo == nil {
		return "", errors.New("connection and repository are required")
	}
	if conn.Spec.Provider != "" && conn.Spec.Provider != api.ProviderGitHub {
		return "", errors.New("this connection's provider serves no HTTPS clone credential")
	}
	if remote, ok := httpsRemote(repo.Status.CloneURL); ok {
		return remote, nil
	}
	origin := "https://github.com"
	if base := strings.TrimSpace(conn.Spec.BaseURL); base != "" {
		u, err := url.Parse(base)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return "", errors.New("connection baseURL is not a usable git host")
		}
		// The git transport uses the host's origin, not its REST /api/v3
		// prefix (backend/github/snapshot.go does the same).
		origin = u.Scheme + "://" + u.Host
	}
	owner := strings.TrimSpace(repo.Spec.Owner)
	if owner == "" {
		owner = strings.TrimSpace(conn.Spec.Owner)
	}
	name := strings.TrimSpace(repo.Spec.Name)
	if owner == "" || name == "" {
		return "", errors.New("repository has no owner/name to clone")
	}
	return origin + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + ".git", nil
}

// httpsRemote accepts a host-reported clone URL only when it is an HTTPS URL,
// and returns it with anything that is not scheme, host and path removed —
// userinfo above all, so a credential the host echoed back cannot be handed
// on inside the remote.
func httpsRemote(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path == "" {
		return "", false
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath(), true
}

// MintCloneToken issues a read-only clone credential for one repository from
// the credential held in data.
//
// A GitHub App connection gets a fresh installation token restricted to
// contents:read. A PAT or OAuth connection has no narrowing API, so the
// resolved token is returned as-is with Scoped=false — the point of the action
// is still met, because the credential never leaves this provider's control
// path and the consumer never reads the Connection's Secret.
//
// A connection holding no token at all is not a failure: a public repository
// clones anonymously, so the remote comes back with an empty Token and the
// caller decides whether that is enough.
func (r CredentialResolver) MintCloneToken(ctx context.Context, conn *api.Connection, repo *api.Repository, secretKey string, data map[string][]byte, store SecretStore) (CloneToken, error) {
	remote, err := CloneRemoteFor(conn, repo)
	if err != nil {
		return CloneToken{}, err
	}

	if conn.Spec.Type == api.CredentialTypeGitHubApp {
		token, expiresAt, err := r.installationToken(ctx, conn, data, ClonePermissions)
		if err != nil {
			return CloneToken{}, err
		}
		return CloneToken{RemoteURL: remote, Username: CloneUsername, Token: token, ExpiresAt: expiresAt, Scoped: true}, nil
	}
	if conn.Spec.Type != api.CredentialTypePAT && conn.Spec.Type != api.CredentialTypeOAuth && conn.Spec.Type != "" {
		return CloneToken{}, errors.New("unsupported Code credential type")
	}

	// No stored token: report the remote and let the caller try an anonymous
	// clone rather than failing a public repository over a credential it does
	// not need.
	if strings.TrimSpace(string(data[CredentialTokenKey(conn)])) == "" {
		return CloneToken{RemoteURL: remote, Username: CloneUsername}, nil
	}
	fresh, err := r.refreshed(ctx, conn, secretKey, data, store)
	if err != nil {
		return CloneToken{}, err
	}
	credential, err := r.Resolve(ctx, conn, fresh)
	if err != nil {
		return CloneToken{}, err
	}
	return CloneToken{RemoteURL: remote, Username: CloneUsername, Token: credential.Token, ExpiresAt: storedExpiry(fresh)}, nil
}

// storedExpiry reads the expiry the OAuth flow writes next to an expiring user
// token. Absent or unparseable means the credential does not expire on its own
// as far as this provider knows, which is what a zero time says.
func storedExpiry(data map[string][]byte) time.Time {
	raw := strings.TrimSpace(string(data[OAuthExpiryKey]))
	if raw == "" {
		return time.Time{}
	}
	expiry, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return expiry
}
