// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/railgrid/provider-code/commitbundle"
	"github.com/railgrid/provider-code/commitexec"
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// Checkout is the third uncatalogued large-transfer verb, and the commit flow
// in reverse: it reads a managed Repository's tree at a ref and returns the
// files inline. It is the grammar's answer to the same question the
// checkout_repository MCP tool answers, and both run the one executor in
// commitexec: create a RepositoryCheckout, let the checkout controller read
// the tree through the git backend into a provider-owned bundle, hand the
// bundle's files back and reclaim both.
//
// What the verb adds is the contract's authorization. kcp proves the caller
// holds `create` on repositories/checkout and the gate that it can see the
// Repository; the RepositoryCheckout is then created AS THE PROVIDER. That is
// the difference that matters to a consumer: the MCP tool creates the CR as
// the bearer it was handed, so a caller needs `create` on a foreign kind in
// the tenant workspace — which a hub-minted scoped identity is never given
// (pkg/hub/identity/policy.go mints only `get` on another provider's kinds)
// and a foreign provider's claim does not carry either. App Studio hydrates
// and restores a project workspace through this verb, as itself, under its
// `repositories/checkout` claim.
//
// It is a verb rather than a catalogued action for the same reason
// stage-commit-bundle is: the body it carries is past the catalogue's bounds.
// A checkout returns up to 48 MiB of decoded files (64 MiB as base64, plus
// the envelope), which is past the 64 MiB `limits.maxOutputBytes` ceiling the
// CatalogEntry API allows an action to declare, so no honest action
// declaration exists. See docs/provider-actions.md, "Uncatalogued
// large-transfer verbs".
const Checkout = "checkout"

// MaxCheckoutOutputBytes bounds a checkout envelope: the bundle store's
// 48 MiB of decoded content, expanded by base64 (4/3) and wrapped in JSON.
const MaxCheckoutOutputBytes = 72 << 20

// checkoutInput is the repositories/checkout input.
//
// The repository is addressed by the route, so the input carries no name —
// only the UID, which pins what the caller saw against the provider's own
// read, the same pinning every other verb here does.
type checkoutInput struct {
	RepositoryUID string `json:"repositoryUID"`
	// Ref is a branch, tag or commit SHA; empty reads the default branch.
	Ref string `json:"ref,omitempty"`
	// BinaryEncoding is omitted to skip binary files (they are listed in
	// skipped) or "base64" to receive them encoded.
	BinaryEncoding string `json:"binaryEncoding,omitempty"`
}

// checkout runs the checkout verb. The gate has already passed; visible is
// the provider's read of the Repository and provider acts as this provider in
// the request's cluster, which is what creates and reclaims the
// RepositoryCheckout.
func (s *Server) checkout(ctx context.Context, provider dynamic.Interface, req dataplane.Request, visible *unstructured.Unstructured, raw json.RawMessage) (any, *actionwire.Error) {
	var in checkoutInput
	if err := decodeStrict(raw, &in); err != nil {
		return nil, wireError("invalid_action_input")
	}
	if aerr := pinRepository(visible, in.RepositoryUID); aerr != nil {
		return nil, aerr
	}
	if provider == nil {
		return nil, wireError("action_forbidden")
	}
	request := commitexec.CheckoutRequest{
		RepositoryRef:  req.Name,
		Ref:            strings.TrimSpace(in.Ref),
		BinaryEncoding: strings.TrimSpace(in.BinaryEncoding),
	}
	if err := request.Validate(); err != nil {
		return nil, wireError("invalid_action_input")
	}
	out, err := commitexec.Checkout(ctx, provider, s.Bundles, request)
	if err != nil {
		switch {
		case errors.Is(err, commitexec.ErrCheckoutTimeout):
			return nil, wireError("checkout_not_completed")
		case errors.Is(err, commitexec.ErrCheckoutFailed):
			// The controller's Ready message says why the git host refused
			// the read (a missing ref, a tree past the limits); it is the
			// one detail a consumer can act on, so it travels.
			return nil, &actionwire.Error{Code: "checkout_failed", Message: err.Error(), Retryable: false}
		case commitbundle.IsNotFound(err):
			return nil, wireError("bundle_unavailable")
		}
		return nil, wireError("checkout_not_created")
	}
	return out, nil
}
