// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package harness

// A REPOSITORY attempt: the second of the protocol's two attempt shapes.
//
// A conversational turn runs in a workspace the runner keeps; a repository
// attempt runs in a fresh clone of an approved commit, chains onto no harness
// session, and exports a Git result when it completes — the snapshot commit as
// a bundle, described by git-result.json. This is what a coding coordinator
// (the Factory provider) dispatches through this provider: the agent carries
// the edge, the harness and the credential, and the coordinator carries the
// repository, the commit and a short-lived clone token.
//
// The clone token is DISPATCH DATA exactly like the harness credential: held
// for the turn, put on the start, never persisted, logged or placed on an
// event. A turn re-joined after a restart needs none — the runner has already
// cloned — and a fresh turn without one cannot start.
//
// What comes back is VERIFIED here, before the provider records it: the
// document must describe this attempt, pin the base that was asked for, and
// hash to the bundle that was fetched. A result that fails any of that is not
// a result, and the run fails with "git result unusable: …" rather than
// handing a coordinator a commit it cannot trust.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/railgrid/railgrid/pkg/runner"

	"github.com/railgrid/provider-agents/backend"
)

// Repository makes a turn a repository attempt. Set on Config; nil means a
// conversational workspace turn.
type Repository struct {
	// RepositoryID names the repository on the runner (its enrolled id); a
	// runner/v1 identifier.
	RepositoryID string
	// BaseCommit is the approved commit the attempt starts from: 40 hex,
	// lower-case.
	BaseCommit string
	// CommitMessage is the subject the exported snapshot commit carries. Empty
	// keeps the runner's canonical message.
	CommitMessage string
	// Source is where the runner clones from, token included. Nil on a turn
	// re-joined after a restart, when the runner has already cloned.
	Source *runner.RepositorySource

	RequiredCapabilities []string
	RequiredToolchains   []string
	RequiredEnvironment  []string
	Verification         runner.VerificationRequirements

	// ApprovedInput, when set, is forwarded verbatim as the dispatch's approved
	// input envelope instead of the provenance envelope a conversational turn
	// sends. It must be a non-empty JSON object; the runner refuses anything
	// else.
	ApprovedInput json.RawMessage
}

// ArtifactReader fetches one artifact's bytes from the runner. The shared
// runner client satisfies it; dispatch.Runner deliberately does not, because
// only a repository attempt has anything to fetch.
type ArtifactReader interface {
	Artifact(ctx context.Context, attemptID, artifactID string, w io.Writer) (runner.Artifact, error)
}

// The two artifacts a runner exports for a repository attempt, and the bounds
// a fetch of each is held to. The names are the runner's (pkg/runner
// git_result.go) and are reserved there: a harness cannot produce them.
const (
	GitResultJSONName   = "git-result.json"
	GitResultBundleName = "git-result.bundle"

	maxGitResultJSONBytes   = 64 << 10
	maxGitResultBundleBytes = 32 << 20

	gitResultVersion = "git-result/v1"
	// gitResultUnusable prefixes every verification failure, so a reader of the
	// run's message can tell "the harness failed" from "the harness finished
	// and what it produced cannot be used".
	gitResultUnusable = "git result unusable: "
)

var (
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// ErrCloneCredentialUnavailable is why a fresh repository dispatch cannot
// start after the provider restarted: the token was dispatch data and died
// with the process. The coordinator retries with a fresh one.
var ErrCloneCredentialUnavailable = errors.New("clone credential not available after restart; retry")

// gitResultDocument is the runner's git-result.json (pkg/runner
// gitResultDocument), read back.
type gitResultDocument struct {
	Version      string `json:"version"`
	TaskID       string `json:"taskID"`
	AttemptID    string `json:"attemptID"`
	AttemptEpoch uint64 `json:"attemptEpoch"`
	BaseCommit   string `json:"baseCommit"`
	Commit       string `json:"commit,omitempty"`
	Tree         string `json:"tree,omitempty"`
	BundleSHA256 string `json:"bundleSHA256,omitempty"`
	NoChanges    bool   `json:"noChanges"`
}

// validate checks the coordinates a repository attempt is dispatched with.
func (r *Repository) validate() error {
	switch {
	case r == nil:
		return nil
	case !identifierPattern.MatchString(strings.TrimSpace(r.RepositoryID)):
		return errors.New("a repository attempt needs a repository id (a runner identifier)")
	case !commitPattern.MatchString(r.BaseCommit):
		return errors.New("a repository attempt needs a base commit (40 lower-case hex)")
	}
	return nil
}

// unusable renders a verification failure.
func unusable(format string, args ...any) error {
	return errors.New(gitResultUnusable + fmt.Sprintf(format, args...))
}

// gitResult fetches and verifies the two artifacts a completed repository
// attempt reports. It is the only reader of the runner's artifact endpoint in
// this provider, and everything it accepts is checked against what was asked
// for — the run id, the base commit — not against what the runner says about
// itself.
func (b *Backend) gitResult(ctx context.Context, receipt runner.Receipt) (*backend.RepositoryResult, []backend.Artifact, error) {
	repo := b.cfg.Repository
	if repo == nil {
		return nil, nil, nil
	}
	jsonRef, bundleRef := artifactsNamed(receipt.Artifacts)
	if jsonRef == nil {
		return nil, nil, unusable("artifact missing")
	}
	if b.cfg.Artifacts == nil {
		return nil, nil, unusable("this runner connection cannot serve artifacts")
	}
	attemptID := firstNonEmpty(receipt.AttemptID, b.cfg.AttemptID)

	jsonBytes, err := b.fetchArtifact(ctx, attemptID, *jsonRef, maxGitResultJSONBytes)
	if err != nil {
		return nil, nil, err
	}
	var bundleBytes []byte
	if bundleRef != nil {
		if bundleBytes, err = b.fetchArtifact(ctx, attemptID, *bundleRef, maxGitResultBundleBytes); err != nil {
			return nil, nil, err
		}
	}
	result, err := verifyGitResult(b.cfg.AttemptID, repo.BaseCommit, jsonBytes, bundleBytes, bundleRef != nil)
	if err != nil {
		return nil, nil, err
	}
	artifacts := []backend.Artifact{{
		Name: GitResultJSONName, Digest: result.ResultDigest, Size: int64(len(jsonBytes)), Data: jsonBytes,
		MediaType: firstNonEmpty(jsonRef.MediaType, "application/json"),
	}}
	if bundleRef != nil {
		artifacts = append(artifacts, backend.Artifact{
			Name: GitResultBundleName, Digest: result.BundleDigest, Size: result.BundleSize, Data: bundleBytes,
			MediaType: firstNonEmpty(bundleRef.MediaType, "application/x-git-bundle"),
		})
	}
	return &result, artifacts, nil
}

// artifactsNamed picks the two reserved artifacts out of a receipt.
func artifactsNamed(artifacts []runner.Artifact) (jsonRef, bundleRef *runner.Artifact) {
	for i := range artifacts {
		switch artifacts[i].Name {
		case GitResultJSONName:
			if jsonRef == nil {
				jsonRef = &artifacts[i]
			}
		case GitResultBundleName:
			if bundleRef == nil {
				bundleRef = &artifacts[i]
			}
		}
	}
	return jsonRef, bundleRef
}

// fetchArtifact reads one artifact, bounded, and checks what arrived against
// what the receipt announced. The client recomputes the digest against the
// runner's Digest header on the way; this checks it against the RECEIPT too,
// so a body that matches neither the announced header nor the announced record
// is refused either way.
func (b *Backend) fetchArtifact(ctx context.Context, attemptID string, ref runner.Artifact, limit int64) ([]byte, error) {
	if ref.Length > limit {
		return nil, unusable("%s is %d bytes, over the %d-byte bound", ref.Name, ref.Length, limit)
	}
	var buf bytes.Buffer
	fetched, err := b.cfg.Artifacts.Artifact(ctx, attemptID, ref.ID, &boundedWriter{w: &buf, remaining: limit})
	if err != nil {
		if errors.Is(err, errArtifactTooLarge) {
			return nil, unusable("%s exceeds the %d-byte bound", ref.Name, limit)
		}
		return nil, unusable("fetching %s: %v", ref.Name, err)
	}
	data := buf.Bytes()
	if ref.Length > 0 && int64(len(data)) != ref.Length {
		return nil, unusable("%s is %d bytes but the receipt announced %d", ref.Name, len(data), ref.Length)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if announced := stripSHA256(ref.Digest); announced != "" && announced != got {
		return nil, unusable("%s hashes to %s but the receipt announced %s", ref.Name, got, announced)
	}
	if fetchedDigest := stripSHA256(fetched.Digest); fetchedDigest != "" && fetchedDigest != got {
		return nil, unusable("%s hashes to %s but the runner served %s", ref.Name, got, fetchedDigest)
	}
	return data, nil
}

// verifyGitResult checks the document against the attempt it must describe
// and the bundle it must hash to. runID is the attempt (and, for a repository
// attempt, the task); baseCommit is the one that was requested.
func verifyGitResult(runID, baseCommit string, jsonBytes, bundleBytes []byte, bundlePresent bool) (backend.RepositoryResult, error) {
	var doc gitResultDocument
	if err := json.Unmarshal(jsonBytes, &doc); err != nil {
		return backend.RepositoryResult{}, unusable("%s does not decode: %v", GitResultJSONName, err)
	}
	switch {
	case doc.Version != gitResultVersion:
		return backend.RepositoryResult{}, unusable("version %q, want %s", doc.Version, gitResultVersion)
	case doc.TaskID != runID || doc.AttemptID != runID:
		return backend.RepositoryResult{}, unusable("describes task %q attempt %q, not run %s", doc.TaskID, doc.AttemptID, runID)
	case doc.AttemptEpoch != 1:
		return backend.RepositoryResult{}, unusable("attempt epoch %d, want 1", doc.AttemptEpoch)
	case strings.ToLower(doc.BaseCommit) != baseCommit:
		return backend.RepositoryResult{}, unusable("base commit %s, want the requested %s", doc.BaseCommit, baseCommit)
	}
	sum := sha256.Sum256(jsonBytes)
	result := backend.RepositoryResult{
		BaseCommit:   baseCommit,
		NoChanges:    doc.NoChanges,
		ResultDigest: hex.EncodeToString(sum[:]),
	}
	if doc.NoChanges {
		if bundlePresent || doc.Commit != "" || doc.Tree != "" || doc.BundleSHA256 != "" {
			return backend.RepositoryResult{}, unusable("reports no changes but exports a snapshot")
		}
		return result, nil
	}
	commit, tree := strings.ToLower(doc.Commit), strings.ToLower(doc.Tree)
	switch {
	case !commitPattern.MatchString(commit):
		return backend.RepositoryResult{}, unusable("commit %q is not a full object id", doc.Commit)
	case !commitPattern.MatchString(tree):
		return backend.RepositoryResult{}, unusable("tree %q is not a full object id", doc.Tree)
	case commit == baseCommit:
		return backend.RepositoryResult{}, unusable("commit equals the base commit")
	case !bundlePresent:
		return backend.RepositoryResult{}, unusable("%s is missing", GitResultBundleName)
	}
	bundleSum := sha256.Sum256(bundleBytes)
	bundleDigest := hex.EncodeToString(bundleSum[:])
	if want := strings.ToLower(stripSHA256(doc.BundleSHA256)); want != bundleDigest {
		return backend.RepositoryResult{}, unusable("bundle hashes to %s but the document says %s", bundleDigest, doc.BundleSHA256)
	}
	result.Commit, result.Tree = commit, tree
	result.BundleDigest, result.BundleSize = bundleDigest, int64(len(bundleBytes))
	return result, nil
}

// stripSHA256 drops the optional "sha256:" prefix a digest may carry.
func stripSHA256(digest string) string {
	digest = strings.TrimSpace(digest)
	digest = strings.TrimPrefix(digest, "sha256:")
	return strings.ToLower(digest)
}

var errArtifactTooLarge = errors.New("artifact exceeds its bound")

// boundedWriter refuses bytes past a limit, so a runner that announced one
// length and streams another cannot fill memory.
type boundedWriter struct {
	w         io.Writer
	remaining int64
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > b.remaining {
		return 0, errArtifactTooLarge
	}
	n, err := b.w.Write(p)
	b.remaining -= int64(n)
	return n, err
}
