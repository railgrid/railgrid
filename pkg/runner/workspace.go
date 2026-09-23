/*
Copyright 2026 The Railgrid Authors.

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
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// prepareWorkspace produces the private worktree an attempt runs in. There
// are two ways the runner can reach the approved commit, and it does not need
// the operator to have staged anything for the second:
//
//   - an enrolled local checkout, which the runner treats as read-only and
//     refreshes from its own origin when the approved base has moved past it;
//   - a clone the runner owns, kept under its state directory and filled from
//     the remote the coordinator names with the attempt.
//
// Either way the task worktree is a fresh clone at the approved commit.
func prepareWorkspace(ctx context.Context, cfg Config, request StartRequest, dispatched *RepositorySource) (string, error) {
	repo := cfg.Repositories[request.RepositoryID]
	baseCommit := strings.TrimSpace(request.BaseCommit)
	if !commitPattern.MatchString(baseCommit) {
		return "", errors.New("baseCommit must be a full 40-character commit")
	}
	baseCommit = strings.ToLower(baseCommit)
	if repo.BaseCommit != "" && strings.ToLower(strings.TrimSpace(repo.BaseCommit)) != baseCommit {
		return "", errors.New("base commit is not the exact commit enrolled for this repository")
	}
	remote, credential, err := repositoryRemote(repo, dispatched)
	if err != nil {
		return "", err
	}

	var source string
	var sourceHasCommit bool
	if strings.TrimSpace(repo.Source) != "" {
		source, sourceHasCommit, err = enrolledCheckout(ctx, repo, baseCommit)
	} else {
		source, sourceHasCommit, err = managedClone(ctx, cfg, request.RepositoryID, remote, credential, baseCommit)
	}
	if err != nil {
		return "", err
	}
	if !sourceHasCommit && remote == "" {
		return "", errors.New("base commit is not available in the enrolled source")
	}

	workdir := filepath.Join(cfg.StateDir, "worktrees", request.TaskID, request.AttemptID)
	if info, err := os.Lstat(workdir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("task worktree path must not be a symlink")
		}
		if !info.IsDir() {
			return "", errors.New("task worktree path exists and is not a directory")
		}
		if err := verifyTaskPath(cfg.StateDir, workdir); err != nil {
			return "", err
		}
		head, err := gitOutput(ctx, workdir, "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(head) != baseCommit {
			return "", errors.New("existing task worktree does not match approved base commit")
		}
		return workdir, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect task worktree: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(workdir), 0o700); err != nil {
		return "", fmt.Errorf("create task worktree parent: %w", err)
	}
	if err := verifyTaskPath(cfg.StateDir, workdir); err != nil {
		return "", err
	}
	// --no-local keeps clone setup read-only from the source repository. In
	// particular, this avoids `git worktree add`, which edits the interactive
	// checkout's administrative metadata.
	if _, err := gitOutput(ctx, "", "clone", "--no-local", "--no-hardlinks", "--no-checkout", "--upload-pack=git-upload-pack", "--config", "core.hooksPath=/dev/null", source, workdir); err != nil {
		_ = os.RemoveAll(workdir)
		return "", fmt.Errorf("clone task worktree: %w", err)
	}
	if !sourceHas(ctx, workdir, baseCommit) {
		// A clone copies branches and tags, not the remote-tracking refs a
		// refreshed checkout holds the base under, so the commit is fetched
		// from the source over local transport first; the remote remains the
		// fallback for a commit the source only has unreachably.
		var err error
		if sourceHasCommit {
			err = fetchExactCommit(ctx, workdir, source, baseCommit, nil)
		}
		if !sourceHasCommit || err != nil {
			if remote == "" {
				_ = os.RemoveAll(workdir)
				if err != nil {
					return "", err
				}
				return "", errors.New("base commit is not available in the cloned source")
			}
			err = fetchExactCommit(ctx, workdir, remote, baseCommit, credential)
		}
		if err != nil {
			_ = os.RemoveAll(workdir)
			return "", err
		}
		resolved, err := gitOutput(ctx, workdir, "rev-parse", "--verify", baseCommit+"^{commit}")
		if err != nil {
			_ = os.RemoveAll(workdir)
			return "", errors.New("fetched base commit could not be resolved")
		}
		if strings.TrimSpace(resolved) != baseCommit {
			_ = os.RemoveAll(workdir)
			return "", errors.New("fetched base commit did not resolve to the requested commit")
		}
	}
	if _, err := gitOutput(ctx, workdir, "checkout", "--detach", "--force", baseCommit); err != nil {
		_ = os.RemoveAll(workdir)
		return "", fmt.Errorf("checkout approved base commit: %w", err)
	}
	return workdir, nil
}

// enrolledCheckout resolves a repository enrolled as a directory on this host
// and reports whether it already holds the approved base commit.
func enrolledCheckout(ctx context.Context, repo RepositoryConfig, baseCommit string) (string, bool, error) {
	source, err := filepath.Abs(repo.Source)
	if err != nil {
		return "", false, fmt.Errorf("resolve repository source: %w", err)
	}
	if info, err := os.Stat(source); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("source is not a directory")
		}
		return "", false, fmt.Errorf("repository source unavailable: %w", err)
	}
	if sourceHas(ctx, source, baseCommit) {
		return source, true, nil
	}
	// The approved base moved past the enrolled checkout — a merged pull
	// request, most often. The checkout is the operator's own clone, so it
	// is brought current from its own origin exactly as the operator would,
	// with the operator's Git configuration and credentials for it. Only
	// the runner runs this, never the harness, and only remote-tracking
	// refs move: the checkout's branch and working tree are left alone.
	if err := refreshEnrolledSource(ctx, source); err != nil {
		log.Printf("enrolled source %s could not be refreshed from its origin: %v", source, err)
		return source, false, nil
	}
	return source, sourceHas(ctx, source, baseCommit), nil
}

// managedClone is the repository copy the runner owns. Nothing is expected to
// exist on the host: the directory is created on first use and filled from the
// remote, and it is reused by every later attempt on the same repository.
func managedClone(ctx context.Context, cfg Config, repositoryID, remote string, credential *gitCredential, baseCommit string) (string, bool, error) {
	if remote == "" {
		return "", false, fmt.Errorf("repository %q has no checkout on this host and the attempt named no clone source", repositoryID)
	}
	if !identifierPattern.MatchString(repositoryID) {
		return "", false, fmt.Errorf("repository ID %q is invalid", repositoryID)
	}
	dir := filepath.Join(cfg.StateDir, "repositories", repositoryID+".git")
	info, err := os.Lstat(dir)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", false, errors.New("repository cache path is not a private directory")
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return "", false, fmt.Errorf("create repository cache parent: %w", err)
		}
		if _, err := gitOutput(ctx, "", "init", "--bare", "--quiet", dir); err != nil {
			_ = os.RemoveAll(dir)
			return "", false, fmt.Errorf("create repository cache: %w", err)
		}
	default:
		return "", false, fmt.Errorf("inspect repository cache: %w", err)
	}
	if sourceHas(ctx, dir, baseCommit) {
		return dir, true, nil
	}
	// Asking for the commit by ID is what reaches a pull-request head, which
	// no branch points at. A remote that refuses it still serves the branches,
	// which is where an approved base normally lives.
	pinned := "refs/railgrid/commits/" + baseCommit
	err = fetchRefspec(ctx, dir, remote, credential, "+"+baseCommit+":"+pinned)
	if err != nil || !sourceHas(ctx, dir, baseCommit) {
		if branchErr := fetchRefspec(ctx, dir, remote, credential, "+refs/heads/*:refs/heads/*"); branchErr != nil {
			if err == nil {
				err = branchErr
			}
			return "", false, err
		}
	}
	if !sourceHas(ctx, dir, baseCommit) {
		return "", false, errors.New("base commit is not available from the repository remote")
	}
	return dir, true, nil
}

// repositoryRemote decides where a repository is fetched from. A clone source
// dispatched with the attempt wins over an enrolled remote: it is the one that
// carries a credential, and it is minted for this attempt.
func repositoryRemote(repo RepositoryConfig, dispatched *RepositorySource) (string, *gitCredential, error) {
	if dispatched != nil && strings.TrimSpace(dispatched.RemoteURL) != "" {
		remote, err := validateCloneSource(*dispatched)
		if err != nil {
			return "", nil, err
		}
		return remote, credentialFor(*dispatched), nil
	}
	enrolled := strings.TrimSpace(repo.FetchRemoteURL)
	if enrolled == "" {
		return "", nil, nil
	}
	remote, err := validateFetchRemoteURL(enrolled)
	if err != nil {
		return "", nil, fmt.Errorf("repository fetch remote URL is not allowed: %w", err)
	}
	return remote, nil, nil
}

// validateCloneSource applies the same URL rules to a dispatched source as to
// an enrolled one. The coordinator is authenticated, but it is still not
// allowed to point the runner at an arbitrary transport or to smuggle
// credentials through the URL.
func validateCloneSource(source RepositorySource) (string, error) {
	remote, err := validateFetchRemoteURL(strings.TrimSpace(source.RemoteURL))
	if err != nil {
		return "", fmt.Errorf("clone source URL is not allowed: %w", err)
	}
	if source.Token != "" && fetchRemoteKindOf(remote) != fetchRemoteHTTPS {
		return "", errors.New("a clone credential is only used with an HTTPS remote")
	}
	if strings.ContainsAny(source.Username, ":\r\n") || hasControlCharacter(source.Token) {
		return "", errors.New("clone credential contains an invalid character")
	}
	return remote, nil
}

func credentialFor(source RepositorySource) *gitCredential {
	if strings.TrimSpace(source.Token) == "" {
		return nil
	}
	username := strings.TrimSpace(source.Username)
	if username == "" {
		// What GitHub expects beside a token; every other HTTPS host that
		// takes basic auth ignores the username.
		username = "x-access-token"
	}
	return &gitCredential{Username: username, Token: source.Token}
}

func verifyWorkspace(ctx context.Context, cfg Config, request StartRequest, workdir string) error {
	if err := verifyTaskPath(cfg.StateDir, workdir); err != nil {
		return err
	}
	info, err := os.Lstat(workdir)
	if err != nil {
		return fmt.Errorf("task worktree is unavailable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("task worktree is not a private directory")
	}
	baseCommit := strings.ToLower(strings.TrimSpace(request.BaseCommit))
	if !commitPattern.MatchString(baseCommit) {
		return errors.New("approved base commit is not a full commit")
	}
	head, err := gitOutput(ctx, workdir, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("inspect task worktree: %w", err)
	}
	if strings.TrimSpace(head) != baseCommit {
		return errors.New("task worktree no longer matches the approved base commit")
	}
	// A repository the runner clones for itself has no enrollment to check
	// against; the worktree on disk, verified above, is the whole claim. An
	// enrollment that does exist still constrains what may be resumed.
	repo := cfg.Repositories[request.RepositoryID]
	if repo.BaseCommit != "" && strings.ToLower(strings.TrimSpace(repo.BaseCommit)) != baseCommit {
		return errors.New("repository enrollment no longer permits the approved base commit")
	}
	return nil
}

// sourceHas reports whether dir holds commit as an object. Presence is not
// reachability: an object fetched into FETCH_HEAD alone counts here and is
// then refused by a reachability-checking local fetch, which is what the
// opted-in remote fallback is for.
func sourceHas(ctx context.Context, dir, commit string) bool {
	resolved, err := gitOutput(ctx, dir, "rev-parse", "--verify", commit+"^{commit}")
	return err == nil && strings.TrimSpace(resolved) == commit
}

// refreshEnrolledSource fetches the enrolled checkout's own origin. Unlike
// every other Git invocation here it runs with the operator's own Git
// configuration — that is where the credentials for the operator's clone
// live — but with hooks disabled and prompts off, and it moves nothing but
// remote-tracking refs.
func refreshEnrolledSource(ctx context.Context, source string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "git", "-c", "core.hooksPath=/dev/null", "-c", "fetch.recurseSubmodules=false", "fetch", "--no-tags", "--no-write-fetch-head", "--no-prune", "origin")
	cmd.Dir = source
	cmd.Env = operatorGitEnvironment()
	var stderr boundedGitBuffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if cmdCtx.Err() != nil {
			return errors.New("git fetch timed out")
		}
		return fmt.Errorf("git fetch origin failed: %s", logLine(stderr.String()))
	}
	return nil
}

// operatorGitEnvironment is the process environment with git's own overrides
// removed, so the operator's global configuration applies, and prompts off.
func operatorGitEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		key, _, ok := strings.Cut(value, "=")
		if ok && strings.HasPrefix(key, "GIT_") {
			continue
		}
		env = append(env, value)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0")
}

func verifyTaskPath(stateDir, workdir string) error {
	stateRoot, err := filepath.Abs(stateDir)
	if err != nil {
		return fmt.Errorf("resolve runner state directory: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(workdir))
	if err != nil {
		return fmt.Errorf("resolve task worktree parent: %w", err)
	}
	if !pathWithin(stateRoot, parent) {
		return errors.New("task worktree parent escapes the runner state directory")
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Every Git invocation is fixed by the runner. Do not let a developer's
	// global config, credential helper, SSH agent, GitHub token, or hook change
	// the meaning of an approved source/commit operation.
	safeArgs := append([]string{"-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(cmdCtx, "git", safeArgs...)
	cmd.Env = sanitizedGitEnvironment()
	if dir != "" {
		cmd.Dir = dir
	}
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
	return strings.TrimSpace(stdout.String()), nil
}

func sanitizedGitEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+5)
	for _, value := range os.Environ() {
		key, _, ok := strings.Cut(value, "=")
		if ok && (strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "GH_") || strings.HasPrefix(key, "GITHUB_") || key == "SSH_AUTH_SOCK") {
			continue
		}
		env = append(env, value)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=file",
	)
}

func pathWithin(root, candidate string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func artifactSource(workdir, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("artifact path must be relative to the task worktree")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact path escapes the task worktree")
	}
	candidate := filepath.Join(workdir, clean)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve artifact path: %w", err)
	}
	if !pathWithin(workdir, resolved) {
		return "", errors.New("artifact path escapes the task worktree")
	}
	return resolved, nil
}

func validateArtifactRelativePath(relative string) error {
	if relative == "" || filepath.IsAbs(relative) {
		return errors.New("artifact path must be relative to the task worktree")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("artifact path escapes the task worktree")
	}
	return nil
}
