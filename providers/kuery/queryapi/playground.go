// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

// An ad-hoc query still runs as a verb on a named object — there is no second,
// un-named route — so an ad-hoc caller needs a name. Both ad-hoc callers (the
// portal's playground and the MCP tools) use the same one: a SavedView named
// after the authenticated user, which they create in their own workspace with
// their own credential the first time they run something.
//
// That is what keeps "ad-hoc" inside the contract rather than beside it. The
// view is an ordinary object in the tenant's workspace: their RBAC decides who
// may create it, the two gates apply to it like any other, and an
// administrator who wants to stop one user querying the fleet removes their
// grant on that name.

// PlaygroundNamePrefix is the fixed part of a per-user scratch view's name.
const PlaygroundNamePrefix = "playground-"

// playgroundDigestLength is how much of the user digest the name carries.
// Twelve hex characters is 48 bits: ample against accidental collision among
// the users of one workspace, and short enough that the name stays readable in
// a kubectl listing.
const playgroundDigestLength = 12

// PlaygroundViewName is the scratch SavedView for one user. The user string is
// the authenticated identity the hub injects as X-Railgrid-User (an email, in
// practice).
//
// It is a digest rather than the address itself because a SavedView name is a
// path segment and an object name: an email contains characters that are
// neither, and a workspace's object names are readable by anyone who can list
// them, which is a wider audience than the people entitled to know who has an
// account. An empty user yields the shared "playground-anonymous" view, which
// is what a host that does not inject the header gets — one scratch view for
// the workspace instead of a silent per-request one.
func PlaygroundViewName(user string) string {
	user = strings.TrimSpace(strings.ToLower(user))
	if user == "" {
		return PlaygroundNamePrefix + "anonymous"
	}
	digest := sha256.Sum256([]byte(user))
	return PlaygroundNamePrefix + hex.EncodeToString(digest[:])[:playgroundDigestLength]
}

// EnsurePlaygroundView creates the caller's scratch SavedView if it is absent,
// AS THE CALLER — never with the provider's identity. A caller who may not
// create SavedViews in their workspace gets the failure they should get, and
// no query runs.
//
// The created view carries no query: every run of it supplies one as the
// request's input override, so the object is a name and a grant surface rather
// than a store.
func EnsurePlaygroundView(ctx context.Context, caller dynamic.Interface, name, user string) error {
	views := caller.Resource(kueryv1alpha1.SavedViewsResource)
	if _, err := views.Get(ctx, name, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("reading the scratch view: %w", err)
	}

	display := "Query playground"
	if user != "" {
		display = "Query playground — " + user
	}
	view := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kueryv1alpha1.SchemeGroupVersion.String(),
		"kind":       "SavedView",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"displayName": display,
			"description": "Scratch view for ad-hoc queries from the portal playground and the kuery MCP tools. Each run supplies its own query.",
		},
	}}
	if _, err := views.Create(ctx, view, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating the scratch view: %w", err)
	}
	return nil
}
