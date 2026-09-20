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

// A container image built from a tenant's repository lives in that repository's
// package registry, and a workload cluster needs a credential to pull it. That
// credential used to be made by App Studio: it read this provider's Connection
// Secret and re-minted the raw token into a dockerconfigjson. Two things were
// wrong with that. The consumer had to hold the Connection's own credential —
// the one that can also push code — and it had to know that "the Code
// provider's Connection keeps a git token under .secretRef" to do it, which is
// a coupling by Secret layout rather than by contract
// (cross-provider-simplification §2.1).
//
// mint_registry_token replaces both. The credential stays inside Code, the
// consumer asks for exactly what it needs, and what comes back is a PULL
// credential: for a GitHub App connection a fresh installation token issued
// with packages:read alone and about an hour to live, which is the narrowest
// thing GitHub will hand out.

// RegistryPullPermissions is what mint_registry_token asks a GitHub App
// installation for. A pull secret sits on a runtime cluster for as long as the
// workload does, so it must be able to read packages and nothing else — not
// contents, not workflows, not metadata writes.
var RegistryPullPermissions = map[string]string{"packages": "read"}

// RegistryToken is one image-pull credential for a Connection's registry.
type RegistryToken struct {
	// Registry is the OCI registry host the credential authenticates to.
	Registry string
	// Username is what the registry expects beside the token. GitHub
	// Packages validates the token and ignores the username, but a
	// dockerconfigjson needs one.
	Username string
	// Token is the credential itself.
	Token string
	// ExpiresAt is when it stops working. Zero means the credential is the
	// Connection's own long-lived token, which is reported rather than
	// hidden: a consumer that writes it into a Secret should know it is not
	// rotating on its own.
	ExpiresAt time.Time
	// Scoped is true when the token was issued with pull-only permissions.
	// A PAT or OAuth connection cannot be narrowed by any GitHub API, so the
	// answer there is false and the caller can decide what to do about it.
	Scoped bool
}

// RegistryFor returns the OCI registry that serves packages for a Connection.
// GitHub Enterprise Server publishes under its own host; github.com publishes
// under ghcr.io.
func RegistryFor(conn *api.Connection) (string, error) {
	if conn == nil {
		return "", errors.New("connection is required")
	}
	if conn.Spec.Provider != "" && conn.Spec.Provider != api.ProviderGitHub {
		return "", errors.New("this connection's provider serves no container registry")
	}
	base := strings.TrimSpace(conn.Spec.BaseURL)
	if base == "" {
		return "ghcr.io", nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", errors.New("connection baseURL is not a usable registry host")
	}
	return u.Host, nil
}

// MintRegistryToken issues an image-pull credential for the Connection's
// registry from the credential held in data.
//
// A GitHub App connection gets a fresh installation token restricted to
// packages:read. A PAT or OAuth connection has no narrowing API, so the
// resolved token is returned as-is with Scoped=false — the point of the action
// is still met, because the credential never leaves this provider's control
// path and the consumer never reads the Connection's Secret.
func (r CredentialResolver) MintRegistryToken(ctx context.Context, conn *api.Connection, login, secretKey string, data map[string][]byte, store SecretStore) (RegistryToken, error) {
	registry, err := RegistryFor(conn)
	if err != nil {
		return RegistryToken{}, err
	}
	username := strings.TrimSpace(login)
	if username == "" {
		username = strings.TrimSpace(conn.Spec.Owner)
	}
	if username == "" {
		// The registry authenticates the token, not the name; a placeholder
		// keeps the dockerconfigjson well-formed.
		username = "railgrid-code"
	}

	if conn.Spec.Type == api.CredentialTypeGitHubApp {
		token, expiresAt, err := r.installationToken(ctx, conn, data, RegistryPullPermissions)
		if err != nil {
			return RegistryToken{}, err
		}
		return RegistryToken{Registry: registry, Username: username, Token: token, ExpiresAt: expiresAt, Scoped: true}, nil
	}

	credential, err := r.ResolveStored(ctx, conn, secretKey, data, store)
	if err != nil {
		return RegistryToken{}, err
	}
	return RegistryToken{Registry: registry, Username: username, Token: credential.Token}, nil
}
