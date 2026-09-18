// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
)

const MaxSnapshotBundleBytes = 25 << 20

var _ backend.SnapshotPublisher = (*Backend)(nil)

func (b *Backend) VerifySnapshot(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, input backend.Snapshot) error {
	return b.withSnapshot(ctx, conn, cred, repo, input, nil)
}
func (b *Backend) PublishSnapshot(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, input backend.Snapshot, branch, expected string) error {
	if !validBranch(branch) || expected != "" && !objectID.MatchString(expected) {
		return errors.New("invalid branch lease")
	}
	return b.withSnapshot(ctx, conn, cred, repo, input, func(ctx context.Context, g *snapshotGit, remote string) error {
		return g.advance(ctx, remote, input.Commit, branch, expected)
	})
}
func (b *Backend) withSnapshot(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, input backend.Snapshot, apply func(context.Context, *snapshotGit, string) error) error {
	if !objectID.MatchString(input.BaseCommit) || !objectID.MatchString(input.Commit) || !objectID.MatchString(input.Tree) || input.BaseCommit == input.Commit || len(input.Bundle) == 0 || len(input.Bundle) > MaxSnapshotBundleBytes {
		return errors.New("invalid bounded Git snapshot")
	}
	if _, err := b.collaborationClient(ctx, conn, cred, repo); err != nil {
		return err
	}
	base := "https://github.com"
	if conn.Spec.BaseURL != "" {
		u, err := url.Parse(conn.Spec.BaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid Git host")
		}
		// The Git transport uses the GHES origin, not the REST /api/v3 prefix.
		base = u.Scheme + "://" + u.Host
	}
	remote := base + "/" + url.PathEscape(owner(conn, repo)) + "/" + url.PathEscape(repo.Spec.Name) + ".git"
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("", "code-snapshot-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	g := &snapshotGit{dir: dir, token: cred.Token}
	if _, err = g.run(ctx, "init", "--bare", "."); err != nil {
		return err
	}
	if _, err = g.run(ctx, "fetch", "--no-tags", "--depth=1", "--filter=blob:none", remote, input.BaseCommit); err != nil {
		return errors.New("snapshot base commit unavailable")
	}
	if err := g.verify(ctx, input); err != nil {
		return err
	}
	if apply != nil {
		return apply(ctx, g, remote)
	}
	return nil
}

func (g *snapshotGit) verify(ctx context.Context, input backend.Snapshot) error {
	bundlePath := filepath.Join(g.dir, "snapshot.bundle")
	if err := os.WriteFile(bundlePath, input.Bundle, 0600); err != nil {
		return err
	}
	if _, err := g.run(ctx, "bundle", "verify", bundlePath); err != nil {
		return errors.New("snapshot bundle prerequisites invalid")
	}
	heads, err := g.run(ctx, "bundle", "list-heads", bundlePath)
	if err != nil || strings.TrimSpace(heads) != input.Commit+" refs/heads/runner-result" {
		return errors.New("snapshot bundle must expose only the exact result commit")
	}
	if _, err = g.run(ctx, "bundle", "unbundle", bundlePath); err != nil {
		return errors.New("snapshot bundle import failed")
	}
	raw, err := g.run(ctx, "cat-file", "commit", input.Commit)
	if err != nil {
		return errors.New("snapshot commit unavailable")
	}
	if err := verifySnapshotCommit(raw, input.Tree, input.BaseCommit, time.Now()); err != nil {
		return err
	}
	tree, err := g.run(ctx, "rev-parse", input.Commit+"^{tree}")
	if err != nil || strings.TrimSpace(tree) != input.Tree {
		return errors.New("snapshot tree mismatch")
	}
	return nil
}

// The public Railgrid Runner snapshot format. A snapshot commit is verified
// header by header: exactly one parent (the approved base), the tree the
// runner reported, a fixed public identity, no other headers (nothing signed,
// merged or re-encoded can ride along) and a fixed message. The one value the
// runner chooses is the time it took the snapshot, which must be the same for
// author and committer and fall in a window that rules out a fabricated past
// or future without failing an honest clock.
const (
	snapshotIdentity = "Railgrid Runner <runner@localhost>"
	snapshotMessage  = "Implementation snapshot\n"
	// snapshotClockSkew is how far ahead of this host a worker's clock may be.
	snapshotClockSkew = time.Hour
)

// snapshotNotBefore is the earliest a snapshot can honestly have been taken.
var snapshotNotBefore = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func verifySnapshotCommit(raw, tree, base string, now time.Time) error {
	invalid := errors.New("snapshot must contain one parent and canonical public metadata")
	headers, message, ok := strings.Cut(raw, "\n\n")
	if !ok || message != snapshotMessage {
		return invalid
	}
	lines := strings.Split(headers, "\n")
	if len(lines) != 4 || lines[0] != "tree "+tree || lines[1] != "parent "+base {
		return invalid
	}
	author, ok := strings.CutPrefix(lines[2], "author ")
	if !ok {
		return invalid
	}
	committer, ok := strings.CutPrefix(lines[3], "committer ")
	if !ok || committer != author {
		return invalid
	}
	stamp, ok := strings.CutPrefix(author, snapshotIdentity+" ")
	if !ok {
		return invalid
	}
	seconds, zone, ok := strings.Cut(stamp, " ")
	if !ok || zone != "+0000" {
		return invalid
	}
	unix, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil || strconv.FormatInt(unix, 10) != seconds {
		return invalid
	}
	taken := time.Unix(unix, 0).UTC()
	if taken.Before(snapshotNotBefore) || taken.After(now.Add(snapshotClockSkew)) {
		return errors.New("snapshot time is outside the accepted window")
	}
	return nil
}

type snapshotGit struct{ dir, token string }
type boundedGitOutput struct {
	bytes.Buffer
	overflow bool
}

func (w *boundedGitOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (1 << 20) - w.Len()
	if len(p) > remaining {
		p = p[:remaining]
		w.overflow = true
	}
	_, _ = w.Buffer.Write(p)
	return n, nil
}
func (g *snapshotGit) run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = g.dir
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + g.dir, "LANG=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=5", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+g.token)), "GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=/dev/null", "GIT_CONFIG_KEY_2=protocol.file.allow", "GIT_CONFIG_VALUE_2=never", "GIT_CONFIG_KEY_3=protocol.ext.allow", "GIT_CONFIG_VALUE_3=never", "GIT_CONFIG_KEY_4=http.followRedirects", "GIT_CONFIG_VALUE_4=false"}
	var output boundedGitOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return "", errors.New("git snapshot command failed")
	}
	if output.overflow {
		return "", errors.New("git snapshot command output exceeds limit")
	}
	return output.String(), nil
}

// advance is the single receive-pack dispatch; failure never triggers a fallback push.
func (g *snapshotGit) advance(ctx context.Context, remote, commit, branch, expected string) error {
	if !validBranch(branch) || !objectID.MatchString(commit) || expected != "" && !objectID.MatchString(expected) {
		return errors.New("invalid branch lease")
	}
	_, err := g.run(ctx, "push", "--porcelain", "--force-with-lease=refs/heads/"+branch+":"+expected, remote, commit+":refs/heads/"+branch)
	if err != nil {
		return errors.New("git branch publication failed; inspect the exact branch head before retrying")
	}
	return nil
}
