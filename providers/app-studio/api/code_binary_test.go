/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"testing"
	"time"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/internal/codecommit"
	"github.com/railgrid/provider-app-studio/workspace"
)

// A hydrate asks the checkout verb for binaries as base64 unconditionally —
// the verb's input declares the encoding, so there is no tool schema to
// probe first — and writes the decoded bytes into the workspace.
func TestHydrateRequestsAndWritesBase64Binaries(t *testing.T) {
	image := testPNG(2048)
	upstream, checkouts := checkoutVerbServer(t, codecommit.Checkout{Ref: "main", CommitSHA: "sha", Files: []codecommit.CheckoutFile{
		{Path: "public/logo.png", Content: base64.StdEncoding.EncodeToString(image), Encoding: "base64"},
		{Path: "index.html", Content: "<html></html>\n"},
	}}, nil)
	f := newProjectFilesFixture(t)
	f.server.callers = newTestCallers(nil, upstream.URL)
	f.server.developmentSyncAfterMutation = func(identity, *aiv1alpha1.Project, string) error { return nil }
	resp, err := f.server.hydrateWorkspaceFromRepository(context.Background(), identity{orgUUID: "org-a", workspaceUUID: "workspace-a", clusterID: "cluster-a"}, f.project, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Written) != 2 || len(resp.Skipped) != 0 {
		t.Fatalf("hydrate = %#v", resp)
	}
	call := checkouts.last(t)
	if call.Input["binaryEncoding"] != "base64" {
		t.Fatalf("checkout input = %v, want binaryEncoding base64", call.Input)
	}
	if _, set := call.Input["ref"]; set {
		t.Fatalf("checkout input = %v, want no ref for the default branch", call.Input)
	}
	got, err := f.workspaces.ReadFileBytes(context.Background(), f.scope, "public/logo.png", 0)
	if err != nil || !bytes.Equal(got, image) {
		t.Fatalf("hydrated binary mismatch: %v", err)
	}
}

// Checkout runs outside the workspace mutation lock. If another thread edits
// the shared source while Code is returning the repository tree, hydration
// must reject its stale snapshot rather than overwrite that edit.
func TestHydrateRejectsWorkspaceChangeDuringCheckout(t *testing.T) {
	ctx := context.Background()
	f := newProjectFilesFixture(t)
	if _, err := f.workspaces.PutFile(ctx, f.scope, workspace.PutOptions{Path: "index.ts", Data: []byte("before checkout")}); err != nil {
		t.Fatal(err)
	}
	mutationErr := make(chan error, 1)
	runtimeLockErr := make(chan error, 1)
	upstream, _ := checkoutVerbServer(t, codecommit.Checkout{Ref: "main", CommitSHA: "git-sha", Files: []codecommit.CheckoutFile{
		{Path: "index.ts", Content: "repository snapshot"},
		{Path: "repository-only.ts", Content: "must not be installed"},
	}}, func() {
		lockCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
		release, err := f.server.acquireProjectRuntimeOperation(lockCtx, f.scope)
		if release != nil {
			release()
		}
		cancel()
		runtimeLockErr <- err
		_, writeErr := f.workspaces.PutFile(ctx, f.scope, workspace.PutOptions{Path: "index.ts", Data: []byte("concurrent edit")})
		mutationErr <- writeErr
	})
	f.server.callers = newTestCallers(nil, upstream.URL)
	f.server.developmentSyncAfterMutation = func(identity, *aiv1alpha1.Project, string) error {
		f.syncs.Add(1)
		return nil
	}

	_, err := f.server.hydrateWorkspaceFromRepository(ctx,
		identity{orgUUID: "org-a", workspaceUUID: "workspace-a", clusterID: "cluster-a"}, f.project, "")
	if !errors.Is(err, workspace.ErrSourceRevisionConflict) {
		t.Fatalf("hydrate error = %v, want source revision conflict", err)
	}
	if err := <-runtimeLockErr; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runtime operation during checkout = %v, want it held until checkout completes", err)
	}
	if err := <-mutationErr; err != nil {
		t.Fatalf("concurrent source edit: %v", err)
	}
	got, err := f.workspaces.ReadFileBytes(ctx, f.scope, "index.ts", 0)
	if err != nil || string(got) != "concurrent edit" {
		t.Fatalf("concurrent edit after rejected hydrate = %q, %v", got, err)
	}
	if _, err := f.workspaces.ReadFileBytes(ctx, f.scope, "repository-only.ts", 0); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale repository file read error = %v, want not-exist", err)
	}
	if got := f.syncs.Load(); got != 0 {
		t.Fatalf("development syncs = %d, want none for rejected hydrate", got)
	}
}

// A file the verb returns with an encoding App Studio cannot decode is
// skipped with its reason, never written as text.
func TestHydrateSkipsFilesWithUnknownEncoding(t *testing.T) {
	upstream, _ := checkoutVerbServer(t, codecommit.Checkout{Ref: "main", CommitSHA: "sha", Files: []codecommit.CheckoutFile{
		{Path: "weird.bin", Content: "0x00", Encoding: "hex"},
		{Path: "index.html", Content: "<html></html>\n"},
	}}, nil)
	f := newProjectFilesFixture(t)
	f.server.callers = newTestCallers(nil, upstream.URL)
	f.server.developmentSyncAfterMutation = func(identity, *aiv1alpha1.Project, string) error { return nil }
	resp, err := f.server.hydrateWorkspaceFromRepository(context.Background(), identity{orgUUID: "org-a", workspaceUUID: "workspace-a", clusterID: "cluster-a"}, f.project, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Written) != 1 || resp.Written[0] != "index.html" || len(resp.Skipped) != 1 || resp.Skipped[0] != `weird.bin (checkout: unsupported file encoding "hex")` {
		t.Fatalf("hydrate = %#v", resp)
	}
}

// Without a provider credential the hydrate fails before any call: there is
// no fallback to a user or project token for a cross-provider verb.
func TestHydrateRequiresProviderCredential(t *testing.T) {
	f := newProjectFilesFixture(t)
	f.server.callers = nil
	_, err := f.server.hydrateWorkspaceFromRepository(context.Background(), identity{orgUUID: "org-a", workspaceUUID: "workspace-a", clusterID: "cluster-a"}, f.project, "")
	if err == nil || err.Error() != "no provider credential configured; cannot reach the Code provider" {
		t.Fatalf("err = %v", err)
	}
}
