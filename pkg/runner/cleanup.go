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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// repositoryCleanup is deliberately read from the approved base, not the
// agent's edited worktree. Executable files are exported from that same base.
// It is an opt-in contract for trusted repository code, not a sandbox escape
// available to an untrusted ticket or a generated file.
var errNoRepositoryCleanup = errors.New("repository has no cleanup hook")

type repositoryCleanup struct {
	Cleanup []string `json:"cleanup"`
}

func runRepositoryCleanup(workdir, base string) error {
	if workdir == "" || !commitPattern.MatchString(base) {
		return errNoRepositoryCleanup
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// ls-tree distinguishes an absent opt-in from a broken repository.
	entry, err := gitOutput(ctx, workdir, "ls-tree", base, "--", ".railgrid/runner.json")
	if err != nil {
		return fmt.Errorf("read repository cleanup configuration: %w", err)
	}
	if entry == "" {
		return errNoRepositoryCleanup
	}
	raw, err := gitOutput(ctx, workdir, "show", base+":.railgrid/runner.json")
	if err != nil {
		return fmt.Errorf("read repository cleanup configuration: %w", err)
	}
	if len(raw) > 16384 {
		return errors.New("repository cleanup configuration exceeds 16 KiB")
	}
	var config repositoryCleanup
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return errors.New("invalid repository cleanup configuration")
	}
	if len(config.Cleanup) == 0 {
		return errNoRepositoryCleanup
	}
	if len(config.Cleanup) > 32 || config.Cleanup[0] == "" {
		return errors.New("invalid repository cleanup command")
	}
	trusted, err := os.MkdirTemp("", "railgrid-cleanup-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(trusted) }()
	archive := filepath.Join(trusted, "source.tar")
	if _, err := gitOutput(ctx, workdir, "archive", "--format=tar", "--output="+archive, base); err != nil {
		return fmt.Errorf("export approved cleanup source: %w", err)
	}
	source := filepath.Join(trusted, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		return err
	}
	unpack := exec.CommandContext(ctx, "tar", "-xf", archive, "-C", source)
	unpack.Env = cleanupEnvironment(workdir)
	if err := unpack.Run(); err != nil {
		return fmt.Errorf("extract approved cleanup source: %w", err)
	}
	cmd := exec.CommandContext(ctx, config.Cleanup[0], config.Cleanup[1:]...)
	cmd.Dir = source
	cmd.Env = cleanupEnvironment(workdir)
	if err := configureCleanupProcess(cmd); err != nil {
		return err
	}
	cmd.WaitDelay = time.Second
	// Child output can contain secrets. Do not attach it to receipts or PRs.
	logPath := filepath.Join(workdir, ".railgrid-cleanup.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		// Recovery is allowed to retry an interrupted cleanup. Replace the
		// pathname, rather than following a link left by repository code.
		if err = os.Remove(logPath); err != nil {
			return err
		}
		log, err = os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	}
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return errors.New("repository cleanup failed; inspect .railgrid-cleanup.log in the retained worktree")
	}
	return nil
}

// cleanupEnvironment intentionally excludes credentials and language/tool injection
// variables. Repository code receives a target path, never executable task files.
func cleanupEnvironment(workdir string) []string {
	env := []string{"LANG=C.UTF-8", "RAILGRID_CLEANUP_ROOT=" + workdir}
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "DOCKER_HOST", "DOCKER_CONTEXT", "XDG_RUNTIME_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

// Called under r.mu after terminal state is durable. No post-terminal SSE
// events: lifecycle maintenance is recorded in durable state and service logs.
func (r *Runner) scheduleCleanupLocked(attempt *attemptRecord) {
	if !attempt.CleanupEligible || !attempt.Receipt.Phase.IsTerminal() || attempt.CleanupDone || attempt.cleanupRunning || attempt.Receipt.Workdir == "" || attempt.CleanupAttempts >= 3 {
		return
	}
	attempt.cleanupRunning = true
	attempt.CleanupAttempts++
	if err := r.persistLocked(); err != nil {
		attempt.cleanupRunning = false
		attempt.CleanupAttempts--
		log.Printf("persist cleanup scheduling: %v", err)
		return
	}
	id, workdir, base := attempt.Receipt.AttemptID, attempt.Receipt.Workdir, attempt.Start.BaseCommit
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		// Bound startup work even when many terminal attempts need recovery.
		r.cleanupSlots <- struct{}{}
		defer func() { <-r.cleanupSlots }()
		err := runRepositoryCleanup(workdir, base)
		r.mu.Lock()
		defer r.mu.Unlock()
		current := r.state.Attempts[id]
		if current == nil {
			return
		}
		current.cleanupRunning = false
		current.CleanupDone = err == nil || errors.Is(err, errNoRepositoryCleanup)
		current.CleanupError = ""
		if !current.CleanupDone {
			current.CleanupError = err.Error()
			log.Printf("repository cleanup %s incomplete: %s", id, err)
		}
		if err := r.persistLocked(); err != nil {
			log.Printf("persist cleanup result %s: %v", id, err)
		}
	}()
}
