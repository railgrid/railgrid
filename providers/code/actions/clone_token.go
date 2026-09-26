// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"encoding/json"
	"time"

	"github.com/railgrid/provider-sdk/actionwire"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// MintCloneToken is mint-registry-token's repository-bound sibling: it hands a
// consumer a short-lived, read-only git clone credential for ONE repository,
// so the consumer never holds the Connection's own push-capable credential.
//
// It is bound to a Repository, not to a Connection, because that is the unit
// being granted: kcp authorizes `create` on repositories/mint-clone-token, so
// a grant to clone one repository says nothing about any other repository the
// same Connection reaches. Factory calls it when it dispatches an attempt and
// passes the result to the runner, which clones with it and nothing else.
//
// See tenant/clone_token.go for what is minted and why.
const MintCloneToken = "mint-clone-token"

// cloneTokenInput is the repositories/mint-clone-token/v1 input, the same
// shape every other repository action takes. The UIDs pin what the caller saw
// to what this provider reads with its own identity, so an object deleted and
// recreated under the same name between the two reads fails rather than being
// served, and the owner/name slug pins which upstream repository the caller
// believes it is asking about.
type cloneTokenInput struct {
	Repository    string `json:"repository"`
	RepositoryUID string `json:"repositoryUID"`
	ConnectionUID string `json:"connectionUID"`
}

// CloneTokenOutput is what mint-clone-token returns: a clone credential and
// its metadata — never the Connection's own credential, and never anything
// about the Secret it came from.
type CloneTokenOutput struct {
	// RemoteURL is the repository's HTTPS clone URL, with no credential in
	// it. The token goes in the request, not in the remote.
	RemoteURL string `json:"remoteURL"`
	// Username accompanies the token in an HTTPS git exchange.
	Username string `json:"username"`
	// Token is the clone credential. Empty means the connection holds none:
	// a public repository still clones, and the caller decides whether to try.
	Token string `json:"token"`
	// ExpiresAt is RFC 3339, or absent when the credential does not expire on
	// its own.
	ExpiresAt string `json:"expiresAt,omitempty"`
	// Scoped reports whether the token was issued read-only for this
	// repository's contents alone. A consumer that must not run an attempt on
	// a push-capable credential can refuse an unscoped one.
	Scoped bool `json:"scoped"`
}

// mintCloneToken runs the action. The gate has already passed; visible is the
// provider's read of the Repository and provider acts as this provider in the
// request's cluster.
//
// It takes its own input and never reaches a git host through the backend
// registry: what it needs is the Connection's credential material, not a
// resolved credential, so it pins the binding itself rather than going through
// resolve() — which would mint the Connection's FULL installation token on the
// way to minting a narrowed one.
func (s *Server) mintCloneToken(ctx context.Context, provider dynamic.Interface, visible *unstructured.Unstructured, raw json.RawMessage) (any, *actionwire.Error) {
	var in cloneTokenInput
	if err := decodeStrict(raw, &in); err != nil {
		return nil, wireError("invalid_action_input")
	}
	binding, err := s.pinRepositoryBinding(ctx, provider, visible, in.RepositoryUID, in.ConnectionUID, in.Repository)
	if err != nil {
		return nil, wireError("action_forbidden")
	}
	token, err := s.Credentials.MintCloneToken(ctx, binding.conn, binding.repo, binding.secretKey, binding.data, binding.store)
	if err != nil {
		return nil, wireError("clone_token_unavailable")
	}
	out := CloneTokenOutput{RemoteURL: token.RemoteURL, Username: token.Username, Token: token.Token, Scoped: token.Scoped}
	if !token.ExpiresAt.IsZero() {
		out.ExpiresAt = token.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out, nil
}
