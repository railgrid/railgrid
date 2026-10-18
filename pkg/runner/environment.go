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
	"os"
	"path/filepath"
	"strings"
	"time"
)

// warmEnvironment is a durable exclusive lease, not a second execution session.
// A parked attempt retains ownership. Terminal attempts release only after the
// harness has exited and immutable result artifacts have been exported.
type warmEnvironment struct {
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
	env.Evicting = true
	if a := r.state.Attempts[env.AttemptID]; a != nil {
		r.scheduleCleanupLocked(a)
	}
}

// Called with the same lock that admits attempts, before a child can start.
func (r *Runner) prepareEnvironmentLocked(ctx context.Context, req StartRequest, source *RepositorySource) (string, error) {
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
		if env.Evicting {
			return "", errors.New("previous environment cleanup is pending")
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
		current, head, err := environmentFingerprint(ctx, env.Workdir)
		if err != nil || current != env.Fingerprint || head != env.Head {
			r.evictEnvironmentLocked(env)
			return "", errors.New("released checkout changed; retained edits will not be reset")
		}
		// The approved base is either unchanged or exactly the snapshot we exported.
		// Mixed reset updates only HEAD/index; all working files stay untouched.
		if _, err = gitOutput(ctx, env.Workdir, "reset", "--mixed", req.BaseCommit); err != nil {
			return "", err
		}
		if err = archiveEnvironmentScreenshots(r.cfg, env); err != nil {
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
			return "", errors.New("idle environment cleanup is pending before starting another ticket")
		}
	}
	workdir, err := prepareWorkspace(ctx, r.cfg, req, source)
	if err != nil {
		return "", err
	}
	cfg, policy, err := warmPolicy(ctx, workdir, req.BaseCommit)
	if err != nil {
		return "", err
	}
	if cfg.IdleTimeoutSeconds == 0 {
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
	h := sha256.New()
	branch, err := gitOutput(ctx, workdir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", "", err
	}
	_, _ = fmt.Fprintf(h, "branch:%s\x00", branch)
	for _, path := range paths {
		if strings.HasPrefix(path, screenshotDirectory+"/") {
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
		f, err := os.Open(full)
		if err != nil {
			return "", "", err
		}
		_, copyErr := io.Copy(h, f)
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
	if env.Evicting {
		return false
	}
	if !env.ReleasedAt.IsZero() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	digest, head, err := environmentFingerprint(ctx, env.Workdir)
	if err != nil {
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
			continue
		}
		data, err := os.ReadFile(artifact.Path)
		if err != nil {
			continue
		}
		var doc gitResultDocument
		if json.Unmarshal(data, &doc) == nil {
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
						if !env.ReleasedAt.IsZero() && !time.Now().Before(env.ExpiresAt) {
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
	env := r.state.Environments[a.Start.EnvironmentID]
	if env == nil || env.AttemptID != attemptID || !env.NeedsPrepare {
		r.mu.Unlock()
		return nil
	}
	workdir, base := env.Workdir, env.Base
	r.mu.Unlock()
	err := runRepositoryHook(ctx, workdir, base, true)
	r.mu.Lock()
	defer r.mu.Unlock()
	env.NeedsPrepare = false
	if err != nil {
		env.Evicting = true
	}
	return errors.Join(err, r.persistLocked())
}
