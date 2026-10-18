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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// warmEnvironment is a durable exclusive lease, not a second execution session.
// A parked attempt retains ownership. Terminal attempts release only after the
// harness has exited and immutable result artifacts have been exported.
type warmEnvironment struct {
	checking     bool      // transient ownership fence while hashing outside r.mu
	AttemptID    string    `json:"attemptID"`
	Workdir      string    `json:"workdir"`
	RepositoryID string    `json:"repositoryID"`
	Remote       string    `json:"remote"`
	Base         string    `json:"base"`
	Snapshot     string    `json:"snapshot,omitempty"`
	Policy       string    `json:"policy"`
	IdleSeconds  int       `json:"idleSeconds"`
	Fingerprint  string    `json:"fingerprint,omitempty"`
	Head         string    `json:"head,omitempty"`
	ReleasedAt   time.Time `json:"releasedAt,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt,omitempty"`
	Evicting     bool      `json:"evicting,omitempty"`
	NeedsPrepare bool      `json:"needsPrepare,omitempty"`
}

func warmPolicy(ctx context.Context, workdir, base string) (repositoryCleanup, string, error) {
	var cfg repositoryCleanup
	entry, err := gitOutput(ctx, workdir, "ls-tree", base, "--", ".railgrid/runner.json")
	if err != nil || entry == "" {
		return cfg, "", err
	}
	raw, err := gitOutput(ctx, workdir, "show", base+":.railgrid/runner.json")
	if err != nil {
		return cfg, "", err
	}
	if len(raw) > 16384 {
		return cfg, "", errors.New("runner repository policy exceeds 16 KiB")
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, "", errors.New("invalid runner repository policy")
	}
	if cfg.IdleTimeoutSeconds < 0 || cfg.IdleTimeoutSeconds > 86400 {
		return cfg, "", errors.New("idleTimeoutSeconds must be between 0 and 86400")
	}
	if cfg.IdleTimeoutSeconds > 0 && (len(cfg.Cleanup) == 0 || len(cfg.Prepare) == 0) {
		return cfg, "", errors.New("warm environments require prepare and cleanup commands")
	}
	sum := sha256.Sum256([]byte(raw))
	return cfg, hex.EncodeToString(sum[:]), nil
}

func (r *Runner) evictEnvironmentLocked(env *warmEnvironment) {
	if env.checking {
		return
	}
	env.Evicting = true
	if a := r.state.Attempts[env.AttemptID]; a != nil {
		r.scheduleCleanupLocked(a)
	}
}

// Called with the same lock that admits attempts, before a child can start.
func (r *Runner) prepareEnvironmentLocked(ctx context.Context, req StartRequest, source *RepositorySource) (string, error) {
	for _, env := range r.state.Environments {
		if env.checking {
			return "", errors.New("environment verification is in progress")
		}
	}
	if req.EnvironmentID == "" {
		return prepareWorkspace(ctx, r.cfg, req, source)
	}
	if r.state.Environments == nil {
		r.state.Environments = map[string]*warmEnvironment{}
	}
	if pinned := r.cfg.Repositories[req.RepositoryID].BaseCommit; pinned != "" && !strings.EqualFold(pinned, req.BaseCommit) {
		return "", errors.New("base commit is not the exact commit enrolled for this repository")
	}
	remote, _, err := repositoryRemote(r.cfg.Repositories[req.RepositoryID], source)
	if err != nil {
		return "", err
	}
	env := r.state.Environments[req.EnvironmentID]
	if env != nil {
		if env.checking {
			return "", errors.New("environment verification is in progress")
		}
		if env.Evicting {
			return "", r.environmentCleanupErrorLocked(env)
		}
		if env.ReleasedAt.IsZero() {
			return "", errors.New("environment is exclusively held by another attempt")
		}
		if !time.Now().Before(env.ExpiresAt) {
			r.evictEnvironmentLocked(env)
			return "", errors.New("expired environment cleanup is pending")
		}
		cfg, policy, err := warmPolicy(ctx, env.Workdir, req.BaseCommit)
		if err != nil || env.RepositoryID != req.RepositoryID || env.Remote != remote || (req.BaseCommit != env.Base && req.BaseCommit != env.Snapshot) || policy != env.Policy || cfg.IdleTimeoutSeconds == 0 {
			r.evictEnvironmentLocked(env)
			return "", errors.New("environment is incompatible with this dispatch; retained checkout will not be reset")
		}
		current, head, err := r.fingerprintEnvironmentLocked(ctx, req.EnvironmentID, env)
		if r.closed {
			return "", errors.New("runner is closed")
		}
		if err := r.checkEpochLocked(req.TaskID, req.AttemptID, req.AttemptEpoch); err != nil {
			return "", err
		}
		if r.state.Attempts[req.AttemptID] != nil {
			return "", errors.New("attempt changed during environment verification")
		}
		if err := r.checkStartRequirementsLocked(req); err != nil {
			return "", err
		}
		if err != nil || current != env.Fingerprint || head != env.Head {
			r.evictEnvironmentLocked(env)
			return "", errors.New("released checkout changed; retained edits will not be reset")
		}
		// The approved base is either unchanged or exactly the snapshot we exported.
		// Mixed reset updates only HEAD/index; all working files stay untouched.
		if err = archiveEnvironmentScreenshots(r.cfg, env); err != nil {
			return "", err
		}
		if _, err = gitOutput(ctx, env.Workdir, "reset", "--mixed", req.BaseCommit); err != nil {
			return "", err
		}
		if prior := r.state.Attempts[env.AttemptID]; prior != nil {
			prior.CleanupDelegated = true
		}
		env.AttemptID = req.AttemptID
		env.Base = req.BaseCommit
		env.Snapshot = ""
		env.NeedsPrepare = true
		env.ReleasedAt = time.Time{}
		env.ExpiresAt = time.Time{}
		return env.Workdir, nil
	}
	// Bound resource use: an idle environment is evicted before another ticket
	// starts. Active/parked environments are never candidates for eviction.
	for _, other := range r.state.Environments {
		if !other.ReleasedAt.IsZero() {
			r.evictEnvironmentLocked(other)
			return "", r.environmentCleanupErrorLocked(other)
		}
	}
	candidate := filepath.Join(r.cfg.StateDir, "worktrees", req.TaskID, req.AttemptID)
	_, beforeErr := os.Lstat(candidate)
	workdir, err := prepareWorkspace(ctx, r.cfg, req, source)
	if err != nil {
		return "", err
	}
	keepClone := false
	defer func() {
		// Only remove a clone created by this call, never a retained retry checkout.
		if !keepClone && errors.Is(beforeErr, os.ErrNotExist) {
			if err := os.RemoveAll(workdir); err != nil {
				log.Printf("remove unused warm clone: %v", err)
			}
		}
	}()
	cfg, policy, err := warmPolicy(ctx, workdir, req.BaseCommit)
	if err != nil {
		return "", err
	}
	if cfg.IdleTimeoutSeconds == 0 {
		keepClone = true
		return workdir, nil
	}
	sum := sha256.Sum256([]byte(req.EnvironmentID))
	target := filepath.Join(r.cfg.StateDir, "worktrees", ".environments", hex.EncodeToString(sum[:]), req.AttemptID)
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", err
	}
	if err = verifyTaskPath(r.cfg.StateDir, target); err != nil {
		return "", err
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		// A crash before acceptance persisted can leave a pristine clone here.
		// Re-adopt only that exact clean base; never overwrite retained edits.
		head, headErr := gitOutput(ctx, target, "rev-parse", "HEAD")
		dirty, dirtyErr := gitOutput(ctx, target, "status", "--porcelain")
		if headErr != nil || dirtyErr != nil || head != req.BaseCommit || dirty != "" {
			return "", errors.New("unowned environment checkout is not pristine; refusing to overwrite it")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	} else if err = os.Rename(workdir, target); err != nil {
		return "", err
	}
	r.state.Environments[req.EnvironmentID] = &warmEnvironment{AttemptID: req.AttemptID, Workdir: target, RepositoryID: req.RepositoryID, Remote: remote, Base: req.BaseCommit, Policy: policy, IdleSeconds: cfg.IdleTimeoutSeconds}
	return target, nil
}

func archiveEnvironmentScreenshots(cfg Config, env *warmEnvironment) error {
	source := filepath.Join(env.Workdir, screenshotDirectory)
	if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	// Keep original files outside the next attempt's export directory. Published
	// artifact bytes are already immutable and remain readable under their IDs.
	target := filepath.Join(cfg.StateDir, "environment-history", env.AttemptID, "screenshots")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		// A retry may find an earlier archive. Preserve both generations.
		generation, err := os.MkdirTemp(filepath.Dir(target), "screenshots-")
		if err != nil {
			return err
		}
		target = filepath.Join(generation, "screenshots")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Existing repository assets are source files, not disposable evidence.
	if commitPattern.MatchString(env.Base) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tracked, err := gitResultCommand(ctx, env.Workdir, nil, nil, "ls-tree", "-r", "--name-only", "-z", env.Base, "--", screenshotDirectory)
		if err != nil {
			return err
		}
		keep := map[string]bool{}
		for _, path := range nulPaths(tracked) {
			keep[path] = true
		}
		if len(keep) > 0 {
			return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				rel, err := filepath.Rel(source, path)
				if err != nil {
					return err
				}
				if keep[screenshotDirectory+"/"+filepath.ToSlash(rel)] {
					return nil
				}
				destination := filepath.Join(target, rel)
				if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
					return err
				}
				return os.Rename(path, destination)
			})
		}
	}
	return os.Rename(source, target)
}

func environmentFingerprint(ctx context.Context, workdir string) (string, string, error) {
	head, err := gitOutput(ctx, workdir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	paths, err := worktreeSnapshotPaths(ctx, workdir)
	if err != nil {
		return "", "", err
	}
	trackedScreenshots, err := gitResultCommand(ctx, workdir, nil, nil, "ls-tree", "-r", "--name-only", "-z", "HEAD", "--", screenshotDirectory)
	if err != nil {
		return "", "", err
	}
	tracked := map[string]bool{}
	for _, path := range nulPaths(trackedScreenshots) {
		tracked[path] = true
	}
	h := sha256.New()
	var hashed int64
	const maxFingerprintBytes = 256 << 20
	branch, err := gitOutput(ctx, workdir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", "", err
	}
	_, _ = fmt.Fprintf(h, "branch:%s\x00", branch)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		if strings.HasPrefix(path, screenshotDirectory+"/") && !tracked[path] {
			continue
		}
		full, info, err := inspectGitResultPath(workdir, path)
		if errors.Is(err, os.ErrNotExist) {
			_, _ = fmt.Fprintf(h, "%s:deleted\x00", path)
			continue
		}
		if err != nil {
			return "", "", err
		}
		_, _ = fmt.Fprintf(h, "%s:%s\x00", path, info.Mode())
		if info.IsDir() {
			continue
		}
		if !info.Mode().IsRegular() {
			return "", "", errors.New("cannot fingerprint non-regular worktree file")
		}
		if info.Size() > maxFingerprintBytes-hashed {
			return "", "", errors.New("warm fingerprint exceeds 256 MiB; ignore build outputs before reuse")
		}
		f, err := os.Open(full)
		if err != nil {
			return "", "", err
		}
		n, copyErr := io.Copy(h, io.LimitReader(fingerprintReader{ctx, f}, maxFingerprintBytes-hashed+1))
		hashed += n
		if hashed > maxFingerprintBytes {
			copyErr = errors.New("warm fingerprint exceeds 256 MiB")
		}
		closeErr := f.Close()
		if err = errors.Join(copyErr, closeErr); err != nil {
			return "", "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), head, nil
}

// True means the warm lease owns terminal cleanup, including eviction retries.
func (r *Runner) retainEnvironmentLocked(a *attemptRecord) bool {
	if a == nil || !a.Receipt.Phase.IsTerminal() {
		return false
	}
	env := r.state.Environments[a.Start.EnvironmentID]
	if env == nil || env.AttemptID != a.Receipt.AttemptID {
		return false
	}
	if env.checking {
		return true
	}
	if env.Evicting {
		return false
	}
	if !env.ReleasedAt.IsZero() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	digest, head, err := r.fingerprintEnvironmentLocked(ctx, a.Start.EnvironmentID, env)
	if err != nil {
		log.Printf("warm environment %s: fingerprint failed: %v", a.Receipt.AttemptID, err)
		env.Evicting = true
		return false
	}
	env.Fingerprint = digest
	env.Head = head
	for _, ref := range a.Receipt.Artifacts {
		if ref.Name != gitResultJSONName {
			continue
		}
		artifact, ok := r.state.Artifacts[ref.ID]
		if !ok {
			log.Printf("warm environment %s: snapshot artifact %s missing from state", a.Receipt.AttemptID, ref.ID)
			continue
		}
		data, err := os.ReadFile(artifact.Path)
		if err != nil {
			log.Printf("warm environment %s: cannot read snapshot artifact %s", a.Receipt.AttemptID, ref.ID)
			continue
		}
		var doc gitResultDocument
		if err := json.Unmarshal(data, &doc); err != nil {
			log.Printf("warm environment %s: invalid snapshot artifact %s", a.Receipt.AttemptID, ref.ID)
		} else {
			env.Snapshot = doc.Commit
		}
	}
	env.ReleasedAt = time.Now().UTC()
	env.ExpiresAt = env.ReleasedAt.Add(time.Duration(env.IdleSeconds) * time.Second)
	// Retain at most one idle stack when multiple active attempts finish.
	for _, other := range r.state.Environments {
		if other != env && !other.ReleasedAt.IsZero() {
			r.evictEnvironmentLocked(other)
		}
	}
	if err = r.persistLocked(); err != nil {
		env.Evicting = true
		return false
	}
	return true
}

func (r *Runner) startEnvironmentExpiry() {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.environmentStop:
				return
			case <-ticker.C:
				r.mu.Lock()
				if !r.closed {
					for _, env := range r.state.Environments {
						if env.Evicting || (!env.ReleasedAt.IsZero() && !time.Now().Before(env.ExpiresAt)) {
							r.evictEnvironmentLocked(env)
						}
					}
				}
				r.mu.Unlock()
			}
		}
	}()
}

// Preparation runs outside the runner mutex and shares the attempt's cancellation.
func (r *Runner) prepareLeasedEnvironment(ctx context.Context, attemptID string) error {
	r.mu.Lock()
	a := r.state.Attempts[attemptID]
	if a == nil {
		r.mu.Unlock()
		return errors.New("attempt not found during preparation")
	}
	env := r.state.Environments[a.Start.EnvironmentID]
	if env == nil || env.AttemptID != attemptID || !env.NeedsPrepare {
		r.mu.Unlock()
		return nil
	}
	workdir, base := env.Workdir, env.Base
	r.mu.Unlock()
	err := runRepositoryHook(ctx, workdir, base, hookPrepare)
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.state.Environments[a.Start.EnvironmentID]; current != env || env.AttemptID != attemptID {
		return err
	}
	if err == nil {
		env.NeedsPrepare = false
	} else if ctx.Err() == nil {
		env.Evicting = true
	}
	return errors.Join(err, r.persistLocked())
}

// The lease fence prevents transfer and cleanup while the global mutex is free.
func (r *Runner) fingerprintEnvironmentLocked(ctx context.Context, key string, env *warmEnvironment) (string, string, error) {
	env.checking = true
	owner, workdir := env.AttemptID, env.Workdir
	r.wg.Add(1)
	r.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	digest, head, err := environmentFingerprint(bounded, workdir)
	cancel()
	r.mu.Lock()
	r.wg.Done()
	env.checking = false
	if r.state.Environments[key] != env || env.AttemptID != owner {
		return "", "", errors.New("environment ownership changed during verification")
	}
	return digest, head, err
}

func (r *Runner) environmentCleanupErrorLocked(env *warmEnvironment) error {
	a := r.state.Attempts[env.AttemptID]
	if a == nil || (a.CleanupAttempts >= 3 && !a.cleanupRunning && !a.CleanupDone) {
		return protocolError(ErrorUnavailable, false, fmt.Sprintf("environment cleanup exhausted retries for attempt %s; inspect its cleanup error and retained workdir, then use the documented operator recovery procedure", env.AttemptID), nil)
	}
	return protocolError(ErrorUnavailable, true, "environment cleanup is pending for attempt "+env.AttemptID, nil)
}

// Check cancellation between disk reads as well as between Git subprocesses.
type fingerprintReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r fingerprintReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
