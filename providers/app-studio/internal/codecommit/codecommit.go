/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package codecommit asks the Code provider to commit files, as App Studio,
// through App Studio's own APIExport virtual workspace.
//
// There is exactly one way to commit a project's files and this is it. Two
// callers use it:
//
//   - the Project reconciler (controller/project), converging the workspace's
//     dirty set when the project is idle;
//   - the assistant's commit_project_files tool (api), when the human asked
//     for the commit.
//
// Both make the call AS THIS PROVIDER. `repositories/commit` and
// `repositories/stage-commit-bundle` are kcp custom subresources the Code
// provider publishes on its export; App Studio CLAIMS them (manifest.yaml
// spec.requires[].resources[]), the tenant accepts the claim at Enable,
// and kcp serves each on App Studio's virtual workspace at
//
//	<export VW>/clusters/{tenant}/apis/code.railgrid.ai/v1alpha1/repositories/{name}/{verb}
//
// authorizing the call against the claim, resolving it to whichever Code copy
// the workspace bound, and forwarding it there impersonating App Studio. The
// Code gate sees a foreign provider whose claim is the authorization; the
// human's identity does not travel. Who may ask App Studio to commit is
// decided on App Studio's side — the caller passed the gate on the Project's
// own verb — and "may App Studio commit here" is the claim the tenant
// accepted.
//
// What used to be here instead, on the assistant's side, was the
// `code__commit_files` MCP tool: a tools/call through the tenant's MCP
// aggregate, which meant holding `use` on an MCPServer to commit a file, a
// base64 capability probe against the tool's advertised input schema, and a
// result whose shape was whatever prose the tool returned. None of that was
// the contract. The action's schema declares the encoding, so there is nothing
// to probe; its result is `{commit: {name, uid}}`, the object to watch.
//
// Nothing here waits for the commit to land. The RepositoryCommit the action
// creates is the durable thing, and the Project reconciler's watch on it is
// what settles the workspace ledger — for both callers, because both record
// the same pending commit.
//
// Files larger than the catalogue's 1 MiB input ceiling go up first through
// `stage-commit-bundle` (the Code provider's uncatalogued large-upload verb,
// docs/provider-actions.md §"Uncatalogued large-upload verbs") and the commit
// then names the handle. A small commit skips the extra round trip.
package codecommit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"

	"github.com/railgrid/provider-app-studio/internal/crossprovider"
)

const (
	// Action and StageBundleAction are the two verbs this package invokes on a
	// Repository. They are named here, in manifest.yaml's spec.requires entries
	// (repositories/commit, repositories/stage-commit-bundle) and in the
	// project identity's clause-C rules (controller/project/identity.go), and
	// nowhere else.
	Action            = "commit"
	StageBundleAction = "stage-commit-bundle"
	// ActionVersion is the commit action's contract version. It is not part of
	// the kube path — the Code provider restores it from its own declaration —
	// and is kept for the identity rules that still name it.
	ActionVersion = "v1"

	// InlineMaxBytes is the catalogued input ceiling for commit/v1
	// (CatalogEntry limits.maxInputBytes, itself capped at 1 MiB by
	// apis/providers/v1alpha1/actions.go). A payload over it is staged.
	InlineMaxBytes = 1 << 20

	// Timeout outlasts the provider's own declared 180 s action timeout, so a
	// deadline here is always the provider's answer and never a client that
	// gave up first.
	Timeout = 200 * time.Second

	// maxResponseBytes bounds an action envelope. Both verbs return a handful
	// of identifiers; anything larger is a wrong endpoint.
	maxResponseBytes = 1 << 20
)

// File is one write or deletion in a commit, in the action's own wire shape.
// Content carries the file's bytes in Encoding: omitted or "utf-8" for text,
// "base64" for anything else. One list, because the action takes one — the
// MCP tool's separate deletePaths member was a second way to say the same
// thing.
type File struct {
	Path     string `json:"path"`
	Content  string `json:"content,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Delete   bool   `json:"delete,omitempty"`
}

// Ref is what the commit action hands back: the RepositoryCommit to follow.
// There is deliberately no phase and no SHA — the CR carries those, and
// reading them anywhere else is how the two copies drift.
type Ref struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// Request is one commit, addressed at the Repository the route names.
//
// RepositoryUID is not redundant with RepositoryRef: the provider pins what
// its own read returns against it, so a name that has been recycled onto a
// different object fails closed instead of committing into a stranger's
// repository.
type Request struct {
	// Cluster is the tenant workspace's logical-cluster ID.
	Cluster string

	RepositoryRef string
	RepositoryUID string

	Message string
	Branch  string
	Files   []File
}

// Caller is what this package needs of the provider's caller factory
// (*dataplane.Callers): the URL of a claimed verb through this provider's
// export virtual workspace, and an HTTP client authenticating as the provider.
type Caller interface {
	ExportVerbURL(ctx context.Context, gvr schema.GroupVersionResource, r dataplane.Request) (string, error)
	ProviderHTTPClient() (*http.Client, error)
}

// RepositoriesGVR is the Code kind the two verbs hang off.
var RepositoriesGVR = schema.GroupVersionResource{Group: crossprovider.CodeAPIGroup, Version: "v1alpha1", Resource: crossprovider.RepositoriesResource}

// Client reaches the Code provider's verbs as this provider.
type Client struct {
	// Callers addresses and authenticates the call. Required.
	Callers Caller
}

// Commit asks the Code provider for one commit and returns the
// RepositoryCommit it created. It does not wait for that commit to land.
func (c *Client) Commit(ctx context.Context, req Request) (Ref, error) {
	if c == nil || c.Callers == nil {
		return Ref{}, fmt.Errorf("no provider credential configured to reach the Code provider with")
	}
	if len(req.Files) == 0 {
		return Ref{}, fmt.Errorf("a commit needs at least one file")
	}
	input := map[string]any{
		"repositoryUID": req.RepositoryUID,
		"files":         req.Files,
	}
	if message := strings.TrimSpace(req.Message); message != "" {
		input["message"] = message
	}
	if branch := strings.TrimSpace(req.Branch); branch != "" {
		input["branch"] = branch
	}
	encoded, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return Ref{}, err
	}
	if len(encoded) > InlineMaxBytes {
		staged, err := c.stage(ctx, req)
		if err != nil {
			return Ref{}, err
		}
		delete(input, "files")
		input["bundleRef"] = staged.BundleRef
		input["bundleDigest"] = staged.BundleDigest
		if encoded, err = json.Marshal(map[string]any{"input": input}); err != nil {
			return Ref{}, err
		}
	}
	var out struct {
		Commit Ref `json:"commit"`
	}
	if err := c.invoke(ctx, req, Action, encoded, &out); err != nil {
		return Ref{}, err
	}
	if strings.TrimSpace(out.Commit.Name) == "" {
		return Ref{}, fmt.Errorf("commit action returned no RepositoryCommit to follow")
	}
	return out.Commit, nil
}

// StagedBundle is the handle stage-commit-bundle returns.
type StagedBundle struct {
	BundleRef    string `json:"bundleRef"`
	BundleDigest string `json:"bundleDigest"`
	FileCount    int    `json:"fileCount"`
	Size         int64  `json:"size"`
}

func (c *Client) stage(ctx context.Context, req Request) (StagedBundle, error) {
	encoded, err := json.Marshal(map[string]any{"input": map[string]any{
		"repositoryUID": req.RepositoryUID,
		"files":         req.Files,
	}})
	if err != nil {
		return StagedBundle{}, err
	}
	var staged StagedBundle
	if err := c.invoke(ctx, req, StageBundleAction, encoded, &staged); err != nil {
		return StagedBundle{}, err
	}
	if strings.TrimSpace(staged.BundleRef) == "" || strings.TrimSpace(staged.BundleDigest) == "" {
		return StagedBundle{}, fmt.Errorf("staging returned no bundle handle")
	}
	return staged, nil
}

// invoke POSTs one action envelope and decodes its result.
func (c *Client) invoke(ctx context.Context, req Request, action string, body []byte, out any) error {
	endpoint, err := c.Callers.ExportVerbURL(ctx, RepositoriesGVR, dataplane.Request{
		ClusterID: req.Cluster,
		Resource:  crossprovider.RepositoriesResource,
		Name:      req.RepositoryRef,
		Verb:      action,
	})
	if err != nil {
		return fmt.Errorf("addressing %s on the Code provider: %w", action, err)
	}
	client, err := c.Callers.ProviderHTTPClient()
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	// A non-2xx answer still carries an envelope; the typed code in it says
	// more than the status, so it is preferred when present.
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("%s: HTTP %d with a malformed response", action, resp.StatusCode)
	}
	if envelope.Error != nil && envelope.Error.Code != "" {
		return fmt.Errorf("%s: %s", action, envelope.Error.Code)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: HTTP %d", action, resp.StatusCode)
	}
	if len(envelope.Result) == 0 {
		return fmt.Errorf("%s: response carried no result", action)
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return nil
}
