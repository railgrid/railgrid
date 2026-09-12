/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	gitResultBundleName = "git-result.bundle"
	gitResultJSONName   = "git-result.json"
	gitResultRef        = "refs/heads/runner-result"
	gitResultMessage    = "Implementation snapshot"
	gitResultName       = "Faros Runner"
	gitResultEmail      = "runner@localhost"
	gitResultDate       = "2000-01-01T00:00:00Z"
)

type gitResultExport struct {
	artifacts []gitResultArtifact
}

type gitResultArtifact struct {
	artifact Artifact
	path     string
}

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

// isGitResultArtifactName reports whether the runner owns an artifact name.
// Harness-produced artifacts cannot use these names because the runner adds
// their immutable records only after it has validated a completed turn.
func isGitResultArtifactName(name string) bool {
	return name == gitResultBundleName || name == gitResultJSONName
}

func cleanupGitResultArtifacts(result gitResultExport) {
	for _, item := range result.artifacts {
		if item.path != "" {
			_ = os.Remove(item.path)
		}
	}
}

// finishGitResultLocked adds generated artifacts and the terminal event to the
// same persisted state image. On a save failure it restores the in-memory
// attempt journal so the caller can durably record failure instead.
func (r *Runner) finishGitResultLocked(attempt *attemptRecord, result gitResultExport) error {
	if attempt == nil || attempt.Receipt.Phase.IsTerminal() {
		return errors.New("attempt is already terminal")
	}
	priorReceipt := receiptForResponse(attempt.Receipt)
	priorEvents := append([]Event(nil), r.state.Events[attempt.Receipt.AttemptID]...)
	added := make([]string, 0, len(result.artifacts))
	restore := func() {
		attempt.Receipt = priorReceipt
		for _, id := range added {
			delete(r.state.Artifacts, id)
		}
		r.state.Events[attempt.Receipt.AttemptID] = priorEvents
	}
	for _, item := range result.artifacts {
		if !isGitResultArtifactName(item.artifact.Name) {
			return errors.New("git result artifact name is not reserved")
		}
		if !pathWithin(r.cfg.StateDir, item.path) {
			return errors.New("git result artifact path is outside runner state")
		}
		if _, exists := r.state.Artifacts[item.artifact.ID]; exists {
			return errors.New("git result artifact ID already exists")
		}
		r.state.Artifacts[item.artifact.ID] = artifactRecord{Artifact: item.artifact, Path: item.path}
		added = append(added, item.artifact.ID)
		attempt.Receipt.Artifacts = append(attempt.Receipt.Artifacts, item.artifact)
		if _, err := r.appendEventLocked(attempt.Receipt.AttemptID, EventArtifact, "Git result artifact exported: "+item.artifact.Name, nil); err != nil {
			restore()
			return err
		}
	}
	attempt.Receipt.Phase = PhaseCompleted
	attempt.Receipt.Blocker = ""
	attempt.Receipt.UpdatedAt = eventNow()
	if _, err := r.appendEventLocked(attempt.Receipt.AttemptID, EventCompleted, "", nil); err != nil {
		restore()
		return err
	}
	r.releaseResourcesLocked(attempt.Receipt.AttemptID, resourceRequests(attempt.Resources))
	if err := r.persistLocked(); err != nil {
		restore()
		for _, resource := range attempt.Resources {
			r.state.Reservations[resource+"/"+attempt.Receipt.AttemptID] = reservationRecord{AttemptID: attempt.Receipt.AttemptID}
		}
		return err
	}
	return nil
}

// exportGitResult snapshots the task worktree without changing its HEAD or
// normal index. It uses a temporary index rooted at the approved base commit,
// then creates one commit object and a v2 bundle that advertises only the
// runner-result ref. All files are written below the runner-owned artifact
// directory and are returned to the caller for one durable receipt commit.
func exportGitResult(ctx context.Context, cfg Config, request StartRequest, workdir string) (gitResultExport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return gitResultExport{}, err
	}
	baseCommit := strings.ToLower(strings.TrimSpace(request.BaseCommit))
	if !commitPattern.MatchString(baseCommit) {
		return gitResultExport{}, errors.New("approved base commit is not a full commit")
	}
	if err := verifyTaskPath(cfg.StateDir, workdir); err != nil {
		return gitResultExport{}, err
	}
	if info, err := os.Lstat(workdir); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err == nil {
			err = errors.New("task worktree is not a private directory")
		}
		return gitResultExport{}, err
	}

	maxSize := cfg.MaxArtifactSize
	if maxSize <= 0 {
		maxSize = defaultMaxArtifactSize
	}
	artifactDir := filepath.Join(cfg.StateDir, "artifacts", request.AttemptID)
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return gitResultExport{}, fmt.Errorf("create Git result artifact directory: %w", err)
	}
	if err := os.Chmod(artifactDir, 0o700); err != nil {
		return gitResultExport{}, fmt.Errorf("protect Git result artifact directory: %w", err)
	}

	indexDir := filepath.Join(cfg.StateDir, "git-result")
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		return gitResultExport{}, fmt.Errorf("create Git result temporary directory: %w", err)
	}
	indexFile, err := os.CreateTemp(indexDir, "index-*.tmp")
	if err != nil {
		return gitResultExport{}, fmt.Errorf("create Git result temporary index: %w", err)
	}
	indexPath := indexFile.Name()
	if err := indexFile.Close(); err != nil {
		_ = os.Remove(indexPath)
		return gitResultExport{}, fmt.Errorf("close Git result temporary index: %w", err)
	}
	if err := os.Remove(indexPath); err != nil {
		return gitResultExport{}, fmt.Errorf("prepare Git result temporary index: %w", err)
	}
	defer func() { _ = os.Remove(indexPath) }()
	indexEnv := map[string]string{"GIT_INDEX_FILE": indexPath}

	if _, err := gitResultCommand(ctx, workdir, indexEnv, nil, "read-tree", baseCommit); err != nil {
		return gitResultExport{}, fmt.Errorf("initialize Git result index: %w", err)
	}
	baseTree, err := gitOutput(ctx, workdir, "rev-parse", baseCommit+"^{tree}")
	if err != nil {
		return gitResultExport{}, fmt.Errorf("resolve approved base tree: %w", err)
	}

	basePaths, err := gitResultCommand(ctx, workdir, nil, nil, "ls-tree", "-r", "--name-only", "-z", baseCommit)
	if err != nil {
		return gitResultExport{}, fmt.Errorf("list approved base Git paths: %w", err)
	}
	for _, path := range nulPaths(basePaths) {
		_, info, err := inspectGitResultPath(workdir, path)
		if errors.Is(err, os.ErrNotExist) || (err == nil && info.IsDir()) {
			if _, err := gitResultCommand(ctx, workdir, indexEnv, nil, "update-index", "--remove", "--", path); err != nil {
				return gitResultExport{}, fmt.Errorf("remove missing base Git path %q: %w", path, err)
			}
		} else if err != nil {
			return gitResultExport{}, fmt.Errorf("inspect base Git path %q: %w", path, err)
		}
	}
	paths, err := worktreeSnapshotPaths(ctx, workdir)
	if err != nil {
		return gitResultExport{}, err
	}
	for _, path := range paths {
		_, info, err := inspectGitResultPath(workdir, path)
		if errors.Is(err, os.ErrNotExist) {
			// ls-files includes tracked paths removed from the worktree. The
			// base-path pass above removes those entries from the temporary index.
			continue
		}
		if err != nil {
			return gitResultExport{}, fmt.Errorf("inspect snapshot path %q: %w", path, err)
		}
		if info.IsDir() {
			// A tracked file can be replaced by a directory. Its old base
			// entry was removed above, while any nonignored files below it are
			// included by ls-files as separate paths.
			continue
		}
		mode, object, err := hashWorktreePath(ctx, workdir, path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return gitResultExport{}, fmt.Errorf("snapshot %q: %w", path, err)
		}
		if _, err := gitResultCommand(ctx, workdir, indexEnv, nil, "update-index", "--add", "--cacheinfo", mode, object, path); err != nil {
			return gitResultExport{}, fmt.Errorf("stage Git result path %q: %w", path, err)
		}
	}
	tree, err := gitResultCommand(ctx, workdir, indexEnv, nil, "write-tree")
	if err != nil {
		return gitResultExport{}, fmt.Errorf("write Git result tree: %w", err)
	}
	tree = strings.TrimSpace(tree)
	if !commitPattern.MatchString(tree) {
		return gitResultExport{}, errors.New("git result tree is not a full object ID")
	}

	document := gitResultDocument{
		Version:      "git-result/v1",
		TaskID:       request.TaskID,
		AttemptID:    request.AttemptID,
		AttemptEpoch: request.AttemptEpoch,
		BaseCommit:   baseCommit,
		NoChanges:    tree == strings.TrimSpace(baseTree),
	}
	var bundlePath string
	if !document.NoChanges {
		document.Tree = tree
		commit, err := createGitResultCommit(ctx, workdir, tree, baseCommit)
		if err != nil {
			return gitResultExport{}, err
		}
		document.Commit = commit
		bundlePath, document.BundleSHA256, err = createGitResultBundle(ctx, workdir, artifactDir, commit, baseCommit, maxSize)
		if err != nil {
			return gitResultExport{}, err
		}
	}

	jsonBytes, err := json.Marshal(document)
	if err != nil {
		if bundlePath != "" {
			_ = os.Remove(bundlePath)
		}
		return gitResultExport{}, fmt.Errorf("encode Git result document: %w", err)
	}
	jsonBytes = append(jsonBytes, '\n')
	if int64(len(jsonBytes)) > maxSize {
		if bundlePath != "" {
			_ = os.Remove(bundlePath)
		}
		return gitResultExport{}, fmt.Errorf("git result document exceeds %d-byte limit", maxSize)
	}
	jsonPath, err := writeGitResultJSON(artifactDir, jsonBytes)
	if err != nil {
		if bundlePath != "" {
			_ = os.Remove(bundlePath)
		}
		return gitResultExport{}, err
	}
	jsonArtifact, err := makeGitResultArtifact(gitResultJSONName, jsonPath, "application/json")
	if err != nil {
		_ = os.Remove(jsonPath)
		if bundlePath != "" {
			_ = os.Remove(bundlePath)
		}
		return gitResultExport{}, err
	}
	result := gitResultExport{artifacts: []gitResultArtifact{jsonArtifact}}
	if bundlePath != "" {
		bundleArtifact, err := makeGitResultArtifact(gitResultBundleName, bundlePath, "application/x-git-bundle")
		if err != nil {
			_ = os.Remove(jsonPath)
			_ = os.Remove(bundlePath)
			return gitResultExport{}, err
		}
		result.artifacts = append(result.artifacts, bundleArtifact)
	}
	return result, nil
}

func worktreeSnapshotPaths(ctx context.Context, workdir string) ([]string, error) {
	listed, err := gitResultCommand(ctx, workdir, nil, nil, "ls-files", "-z", "-c", "-o", "--exclude-standard", "--")
	if err != nil {
		return nil, fmt.Errorf("list tracked and nonignored Git paths: %w", err)
	}
	paths := nulPaths(listed)
	sort.Strings(paths)
	return paths, nil
}

func nulPaths(value string) []string {
	parts := strings.Split(value, "\x00")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			paths = append(paths, part)
		}
	}
	return paths
}

func hashWorktreePath(ctx context.Context, workdir, path string) (string, string, error) {
	full, info, err := inspectGitResultPath(workdir, path)
	if err != nil {
		return "", "", err
	}
	var input io.Reader
	var closeFile *os.File
	mode := "100644"
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(full)
		if err != nil {
			return "", "", err
		}
		input = strings.NewReader(target)
		mode = "120000"
	case info.Mode().IsRegular():
		closeFile, err = os.Open(full)
		if err != nil {
			return "", "", err
		}
		input = closeFile
		if info.Mode().Perm()&0o111 != 0 {
			mode = "100755"
		}
	default:
		return "", "", fmt.Errorf("unsupported file type %s", info.Mode())
	}
	defer func() {
		if closeFile != nil {
			_ = closeFile.Close()
		}
	}()
	object, err := gitResultCommand(ctx, workdir, nil, input, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", "", err
	}
	object = strings.TrimSpace(object)
	if !commitPattern.MatchString(object) {
		return "", "", errors.New("git path hash is not a full object ID")
	}
	return mode, object, nil
}

// inspectGitResultPath validates a Git relative path while preserving a final
// symlink as a Git link. Every parent component is checked with Lstat so a
// tracked path cannot follow a directory symlink outside the task worktree.
func inspectGitResultPath(workdir, path string) (string, os.FileInfo, error) {
	if path == "" || filepath.IsAbs(path) {
		return "", nil, errors.New("git path is not relative")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", nil, errors.New("git path escapes the task worktree")
	}
	full := filepath.Join(workdir, clean)
	if !pathWithin(workdir, full) {
		return "", nil, errors.New("git path escapes the task worktree")
	}
	current := workdir
	parts := strings.Split(clean, string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", nil, err
		}
		if index < len(parts)-1 {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", nil, errors.New("git path has a symlink parent")
			}
			if !info.IsDir() {
				if info.Mode().IsRegular() {
					// A regular file replaced a base directory. Descendants
					// below it are absent from the current worktree and must
					// be removed from the temporary index.
					return "", nil, os.ErrNotExist
				}
				return "", nil, errors.New("git path parent is not a directory")
			}
		}
		if index == len(parts)-1 {
			return current, info, nil
		}
	}
	return "", nil, errors.New("git path is empty")
}

func createGitResultCommit(ctx context.Context, workdir, tree, baseCommit string) (string, error) {
	env := map[string]string{
		"GIT_AUTHOR_NAME":     gitResultName,
		"GIT_AUTHOR_EMAIL":    gitResultEmail,
		"GIT_AUTHOR_DATE":     gitResultDate,
		"GIT_COMMITTER_NAME":  gitResultName,
		"GIT_COMMITTER_EMAIL": gitResultEmail,
		"GIT_COMMITTER_DATE":  gitResultDate,
	}
	commit, err := gitResultCommand(ctx, workdir, env, strings.NewReader(gitResultMessage+"\n"), "commit-tree", tree, "-p", baseCommit)
	if err != nil {
		return "", fmt.Errorf("create Git result commit: %w", err)
	}
	commit = strings.TrimSpace(commit)
	if !commitPattern.MatchString(commit) {
		return "", errors.New("git result commit is not a full object ID")
	}
	return commit, nil
}

func createGitResultBundle(ctx context.Context, workdir, artifactDir, commit, baseCommit string, maxSize int64) (string, string, error) {
	previousRef, refErr := gitResultCommand(ctx, workdir, nil, nil, "rev-parse", "-q", "--verify", gitResultRef)
	hadPreviousRef := refErr == nil && strings.TrimSpace(previousRef) != ""
	// rev-parse exits one when the fixed runner-result ref is absent. Any
	// nonzero result is treated as absence; update-ref below remains the
	// authoritative operation and reports real repository failures.
	previousRef = strings.TrimSpace(previousRef)
	defer func() {
		if hadPreviousRef {
			_, _ = gitResultCommand(context.Background(), workdir, nil, nil, "update-ref", gitResultRef, previousRef)
		} else {
			_, _ = gitResultCommand(context.Background(), workdir, nil, nil, "update-ref", "-d", gitResultRef)
		}
	}()
	if _, err := gitResultCommand(ctx, workdir, nil, nil, "update-ref", gitResultRef, commit); err != nil {
		return "", "", fmt.Errorf("prepare Git result ref: %w", err)
	}
	tmp, err := os.CreateTemp(artifactDir, ".git-result-bundle-*.tmp")
	if err != nil {
		return "", "", fmt.Errorf("create Git result bundle: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", "", fmt.Errorf("close Git result bundle: %w", err)
	}
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := gitResultCommand(ctx, workdir, nil, nil, "bundle", "create", "--version=2", tmpPath, gitResultRef, "^"+baseCommit); err != nil {
		return "", "", fmt.Errorf("create Git result bundle: %w", err)
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		return "", "", fmt.Errorf("stat Git result bundle: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", "", errors.New("git result bundle is not a regular file")
	}
	if info.Size() > maxSize {
		return "", "", fmt.Errorf("git result bundle exceeds %d-byte limit", maxSize)
	}
	digest, err := digestFile(tmpPath, maxSize)
	if err != nil {
		return "", "", fmt.Errorf("digest Git result bundle: %w", err)
	}
	dest := filepath.Join(artifactDir, gitResultBundleName)
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("replace Git result bundle: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return "", "", fmt.Errorf("publish Git result bundle: %w", err)
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		_ = os.Remove(dest)
		return "", "", fmt.Errorf("protect Git result bundle: %w", err)
	}
	tmpPath = ""
	return dest, digest, nil
}

func digestFile(path string, maxSize int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxSize+1))
	if err != nil {
		return "", err
	}
	if written > maxSize {
		return "", fmt.Errorf("file exceeds %d-byte limit", maxSize)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeGitResultJSON(artifactDir string, contents []byte) (string, error) {
	tmp, err := os.CreateTemp(artifactDir, ".git-result-json-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create Git result document: %w", err)
	}
	tmpPath := tmp.Name()
	remove := true
	defer func() {
		_ = tmp.Close()
		if remove {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return "", fmt.Errorf("protect Git result document: %w", err)
	}
	if _, err := tmp.Write(contents); err != nil {
		return "", fmt.Errorf("write Git result document: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync Git result document: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close Git result document: %w", err)
	}
	dest := filepath.Join(artifactDir, gitResultJSONName)
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("replace Git result document: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return "", fmt.Errorf("publish Git result document: %w", err)
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		_ = os.Remove(dest)
		return "", fmt.Errorf("protect Git result document: %w", err)
	}
	remove = false
	return dest, nil
}

func makeGitResultArtifact(name, path, mediaType string) (gitResultArtifact, error) {
	info, err := os.Stat(path)
	if err != nil {
		return gitResultArtifact{}, err
	}
	if !info.Mode().IsRegular() {
		return gitResultArtifact{}, errors.New("git result artifact is not a regular file")
	}
	digest, err := digestFile(path, info.Size())
	if err != nil {
		return gitResultArtifact{}, err
	}
	return gitResultArtifact{
		artifact: Artifact{ID: newArtifactID(name), Name: name, Digest: "sha256:" + digest, Length: info.Size(), MediaType: mediaType, CreatedAt: eventNow()},
		path:     path,
	}, nil
}

func gitResultCommand(ctx context.Context, dir string, extraEnv map[string]string, stdin io.Reader, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	safeArgs := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-c", "core.fsmonitor=false", "-c", "core.logAllRefUpdates=false"}, args...)
	cmd := exec.CommandContext(cmdCtx, "git", safeArgs...)
	cmd.Env = sanitizedGitEnvironment()
	for key, value := range extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = stdin
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", errors.New(message)
	}
	return stdout.String(), nil
}
