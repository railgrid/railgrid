/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package github

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"unicode/utf8"

	gogithub "github.com/google/go-github/v66/github"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
)

// Default checkout bounds when the caller passes zero values. They mirror the
// App Studio workspace bounds the checked-out tree ultimately lands in.
const (
	defaultCheckoutMaxFiles      = 500
	defaultCheckoutMaxFileBytes  = 256 << 10 // 256 KiB per file
	defaultCheckoutMaxTotalBytes = 16 << 20  // 16 MiB per checkout
)

// CheckoutFiles reads the repository's text tree at input.Ref without a local
// clone: resolve the ref to a commit, walk the git tree recursively, and fetch
// each blob. Binary blobs (NUL byte / invalid UTF-8), oversized files, and
// anything beyond the caps are skipped and reported, never errors — a partial
// text checkout is the contract (backend.RepositoryReader).
func (b *Backend) CheckoutFiles(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository, input backend.RepositoryCheckoutInput) (backend.RepositoryCheckoutResult, error) {
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return backend.RepositoryCheckoutResult{}, err
	}
	org := owner(conn, repo)

	maxFiles := input.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultCheckoutMaxFiles
	}
	maxFileBytes := input.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = defaultCheckoutMaxFileBytes
	}
	maxTotalBytes := input.MaxTotalBytes
	if maxTotalBytes <= 0 {
		maxTotalBytes = defaultCheckoutMaxTotalBytes
	}

	ref := input.Ref
	if ref == "" {
		ref = repo.Spec.DefaultBranch
	}
	if ref == "" {
		current, resp, err := c.Repositories.Get(ctx, org, repo.Spec.Name)
		if err != nil {
			return backend.RepositoryCheckoutResult{}, classify(resp, err)
		}
		ref = current.GetDefaultBranch()
	}

	sha, resp, err := c.Repositories.GetCommitSHA1(ctx, org, repo.Spec.Name, ref, "")
	if err != nil {
		return backend.RepositoryCheckoutResult{}, classify(resp, fmt.Errorf("resolve ref %q: %w", ref, err))
	}

	tree, resp, err := c.Git.GetTree(ctx, org, repo.Spec.Name, sha, true)
	if err != nil {
		return backend.RepositoryCheckoutResult{}, classify(resp, fmt.Errorf("read tree at %s: %w", sha, err))
	}

	result := backend.RepositoryCheckoutResult{Ref: ref, CommitSHA: sha}
	if tree.GetTruncated() {
		result.Skipped = append(result.Skipped, "(tree truncated by the host: repository has more entries than the tree API returns)")
	}

	// Deterministic order — the tree API's order is unspecified across pages.
	entries := make([]*gogithub.TreeEntry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		if e.GetType() == "blob" {
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetPath() < entries[j].GetPath() })

	var total int64
	for _, entry := range entries {
		path := entry.GetPath()
		switch {
		case len(result.Files) >= maxFiles:
			result.Skipped = appendSkip(result.Skipped, path+" (file-count cap)")
			continue
		case int64(entry.GetSize()) > maxFileBytes:
			result.Skipped = appendSkip(result.Skipped, path+" (file too large)")
			continue
		case total+int64(entry.GetSize()) > maxTotalBytes:
			result.Skipped = appendSkip(result.Skipped, path+" (total-size cap)")
			continue
		}
		raw, resp, err := c.Git.GetBlobRaw(ctx, org, repo.Spec.Name, entry.GetSHA())
		if err != nil {
			return backend.RepositoryCheckoutResult{}, classify(resp, fmt.Errorf("read blob %s: %w", path, err))
		}
		if isBinaryContent(raw) {
			result.Skipped = appendSkip(result.Skipped, path+" (binary)")
			continue
		}
		total += int64(len(raw))
		result.Files = append(result.Files, backend.RepositoryCommitFile{Path: path, Content: string(raw)})
	}
	return result, nil
}

// isBinaryContent applies the standard text heuristic: a NUL byte or invalid
// UTF-8 marks the blob binary. Empty files are text.
func isBinaryContent(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return true
	}
	return !utf8.Valid(raw)
}

// appendSkip bounds the skip list so a giant binary-heavy repository cannot
// inflate the result (the CR status caps at 100 entries anyway).
func appendSkip(skipped []string, entry string) []string {
	const maxSkipped = 100
	if len(skipped) >= maxSkipped {
		return skipped
	}
	if len(skipped) == maxSkipped-1 {
		return append(skipped, "(more paths skipped)")
	}
	return append(skipped, entry)
}
