// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/railgrid/provider-code/commitbundle"
	"github.com/railgrid/provider-code/commitexec"
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// Commit is the catalogued action that writes files to a managed Repository.
//
// It is the grammar's answer to the same question commit_files answers over
// MCP, and both run the one executor in commitexec: store the source bundle
// in this provider's own store, then create the RepositoryCommit that points
// at it. What the action adds is the contract's authorization — kcp proves the
// caller holds `create` on repositories/commit, the gate that it can see the
// Repository — and a result a consumer can follow: the commit's name
// and UID. It does NOT wait for the commit to land; the CR is the thing to
// watch, and a consumer that needs the outcome watches it (App Studio's
// project reconciler does exactly that).
const Commit = "commit"

// StageCommitBundle is the second uncatalogued large-upload verb, alongside
// StageSnapshot and for the same reason: a repository's worth of generated
// files does not fit in the 1 MiB CatalogEntry.spec.actions[].limits
// .maxInputBytes ceiling (apis/providers/v1alpha1/actions.go), and inventing
// a catalogue that lies about its own bounds would be worse than declaring
// the exception. A caller with more than one mebibyte of files uploads them
// here, gets an opaque bundle handle back, and passes that handle to Commit
// as `bundleRef`. Both verbs are gated exactly like a catalogued action, and
// kcp authorizes this one as `create` on repositories/stage-commit-bundle, so
// the grant to upload is separate from the grant to commit.
//
// See docs/provider-actions.md, "Uncatalogued large-upload verbs".
const StageCommitBundle = "stage-commit-bundle"

// MaxCommitBundleInputBytes bounds a stage-commit-bundle body: the bundle
// store's 48 MiB of decoded content, expanded by base64 (4/3) and wrapped in
// JSON.
const MaxCommitBundleInputBytes = 68 << 20

// commitInput is the repositories/commit/v1 input.
//
// The commit is addressed by the route, so the input carries no repository
// name — only the UID, which pins what the caller saw against the provider's
// own read, the same pinning every other action here does.
//
// One of files or bundleRef is given. What is NOT here is as deliberate: a
// RepositoryCommit carries a repositoryRef, a branch, a message and a bundle
// pointer and nothing else (apis/v1alpha1/types_repositorycommit.go), so an
// input member the CR cannot carry would be accepted and then silently
// dropped. Per-file modes, a commit author and a base-commit fence are
// therefore not in this contract; adding any of them starts at the CR and the
// backend's RepositoryCommitInput, not here.
type commitInput struct {
	RepositoryUID string       `json:"repositoryUID"`
	Message       string       `json:"message,omitempty"`
	Branch        string       `json:"branch,omitempty"`
	Files         []commitFile `json:"files,omitempty"`
	BundleRef     string       `json:"bundleRef,omitempty"`
	BundleDigest  string       `json:"bundleDigest,omitempty"`
}

// commitFile is one write or deletion. Content carries the file's bytes in
// Encoding: omitted or "utf-8" for text, "base64" for anything else.
type commitFile struct {
	Path     string `json:"path"`
	Content  string `json:"content,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Delete   bool   `json:"delete,omitempty"`
}

// stageCommitBundleInput is the large-upload input: the same file list, with
// no commit metadata, because staging decides nothing about the commit.
type stageCommitBundleInput struct {
	RepositoryUID string       `json:"repositoryUID"`
	Files         []commitFile `json:"files"`
}

// commitOutput is what a caller follows the commit by.
type commitOutput struct {
	Commit commitRef `json:"commit"`
}

type commitRef struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// stageCommitBundleOutput hands back the opaque handle Commit takes.
type stageCommitBundleOutput struct {
	BundleRef    string `json:"bundleRef"`
	BundleDigest string `json:"bundleDigest"`
	FileCount    int    `json:"fileCount"`
	Size         int64  `json:"size"`
}

// commit runs the commit action. The gate has already passed; visible is the
// provider's read of the Repository and provider acts as this provider in the
// request's cluster, which is what writes the RepositoryCommit: the caller
// authorized the commit through kcp and the gate, and does not need `create`
// on a foreign provider's kind to have one made for it.
func (s *Server) commit(ctx context.Context, provider dynamic.Interface, req dataplane.Request, visible *unstructured.Unstructured, raw json.RawMessage) (any, *actionwire.Error) {
	var in commitInput
	if err := decodeStrict(raw, &in); err != nil {
		return nil, wireError("invalid_action_input")
	}
	if aerr := pinRepository(visible, in.RepositoryUID); aerr != nil {
		return nil, aerr
	}
	if provider == nil {
		return nil, wireError("action_forbidden")
	}
	request := commitexec.Request{
		RepositoryRef: req.Name,
		Message:       in.Message,
		Branch:        in.Branch,
		Files:         commitFiles(in.Files),
	}
	if ref := strings.TrimSpace(in.BundleRef); ref != "" {
		request.Bundle = &commitbundle.BundleRef{Name: ref, Digest: strings.TrimSpace(in.BundleDigest)}
	}
	if err := request.Validate(); err != nil {
		return nil, wireError("invalid_action_input")
	}
	created, err := commitexec.Create(ctx, provider, s.Bundles, req.ClusterID, request)
	if err != nil {
		if commitbundle.IsNotFound(err) {
			return nil, wireError("bundle_unavailable")
		}
		return nil, wireError("commit_not_created")
	}
	return commitOutput{Commit: commitRef{Name: created.Name, UID: created.UID}}, nil
}

// stageCommitBundle stores an oversized file payload and returns its handle.
// The bundle lands under the request's cluster scope — the same scope Commit
// reads it back from and the RepositoryCommit controller reconciles under —
// and the orphan sweeper reclaims it if no commit ever names it.
func (s *Server) stageCommitBundle(ctx context.Context, req dataplane.Request, visible *unstructured.Unstructured, raw json.RawMessage) (any, *actionwire.Error) {
	var in stageCommitBundleInput
	if err := decodeStrict(raw, &in); err != nil {
		return nil, wireError("invalid_action_input")
	}
	if aerr := pinRepository(visible, in.RepositoryUID); aerr != nil {
		return nil, aerr
	}
	if len(in.Files) == 0 {
		return nil, wireError("invalid_action_input")
	}
	if s.Bundles == nil {
		return nil, wireError("bundle_unavailable")
	}
	ref, err := s.Bundles.Put(ctx, req.ClusterID, commitFiles(in.Files))
	if err != nil {
		return nil, wireError("invalid_action_input")
	}
	return stageCommitBundleOutput{
		BundleRef:    ref.Name,
		BundleDigest: ref.Digest,
		FileCount:    ref.FileCount,
		Size:         ref.Size,
	}, nil
}

// pinRepository is resolve() without the Connection and the credential: the
// commit verbs never talk to a git host, so there is nothing to authenticate
// with, but the caller's input still has to name the Repository the gate
// returned. The UID is what the caller saw; the object is what the provider
// read, so a Repository deleted and recreated under the same name in between
// fails rather than being committed to.
func pinRepository(visible *unstructured.Unstructured, repositoryUID string) *actionwire.Error {
	if visible == nil || strings.TrimSpace(repositoryUID) == "" || string(visible.GetUID()) != repositoryUID || visible.GetDeletionTimestamp() != nil {
		return wireError("action_forbidden")
	}
	return nil
}

func commitFiles(files []commitFile) []commitbundle.File {
	out := make([]commitbundle.File, 0, len(files))
	for _, f := range files {
		out = append(out, commitbundle.File{Path: f.Path, Content: f.Content, Encoding: f.Encoding, Delete: f.Delete})
	}
	return out
}
