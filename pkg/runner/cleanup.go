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

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// repositoryCleanup is deliberately read from the approved base, not the
// agent's edited worktree. Executable files are exported from that same base.
// It is an opt-in contract for trusted repository code, not a sandbox escape
// available to an untrusted ticket or a generated file.
var errNoRepositoryCleanup = errors.New("repository has no cleanup hook")

type hookKind string

const (
	hookCleanup hookKind = "cleanup"
	hookPrepare hookKind = "prepare"
)

type repositoryCleanup struct {
	Cleanup            []string `json:"cleanup"`
	Prepare            []string `json:"prepare,omitempty"`
	IdleTimeoutSeconds int      `json:"idleTimeoutSeconds,omitempty"`
}

func runRepositoryCleanup(workdir, base string) error {
	return runRepositoryHook(context.Background(), workdir, base, hookCleanup)
}

func runRepositoryHook(parent context.Context, workdir, base string, kind hookKind) error {
	if workdir == "" || !commitPattern.MatchString(base) {
		return errNoRepositoryCleanup
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	// ls-tree distinguishes an absent opt-in from a broken repository.
	entry, err := gitOutput(ctx, workdir, "ls-tree", base, "--", ".railgrid/runner.json")
	if err != nil {
		return fmt.Errorf("read repository hook configuration: %w", err)
	}
	if entry == "" {
		return errNoRepositoryCleanup
	}
	raw, err := gitOutput(ctx, workdir, "show", base+":.railgrid/runner.json")
	if err != nil {
		return fmt.Errorf("read repository hook configuration: %w", err)
	}
	if len(raw) > 16384 {
		return errors.New("repository hook configuration exceeds 16 KiB")
	}
	var config repositoryCleanup
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return errors.New("invalid repository hook configuration")
	}
	hookName := string(kind)
	argv := config.Cleanup
	if kind == hookPrepare {
		argv = config.Prepare
	}
	if len(argv) == 0 {
		return errNoRepositoryCleanup
	}
	if len(argv) > 32 || argv[0] == "" {
		return errors.New("invalid repository hook command")
	}
	trusted, err := os.MkdirTemp("", "railgrid-cleanup-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(trusted) }()
	archive := filepath.Join(trusted, "source.tar")
	if _, err := gitOutput(ctx, workdir, "archive", "--format=tar", "--output="+archive, base); err != nil {
		return fmt.Errorf("export approved hook source: %w", err)
	}
	source := filepath.Join(trusted, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		return err
	}
	unpack := exec.CommandContext(ctx, "tar", "-xf", archive, "-C", source)
	unpack.Env = cleanupEnvironment(workdir)
	if err := unpack.Run(); err != nil {
		return fmt.Errorf("extract approved hook source: %w", err)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = source
	cmd.Env = cleanupEnvironment(workdir)
	if err := configureCleanupProcess(cmd); err != nil {
		return err
	}
	cmd.WaitDelay = time.Second
	// Child output can contain secrets. Do not attach it to receipts or PRs.
	logPath := filepath.Join(workdir, ".railgrid-"+hookName+".log")
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
		return fmt.Errorf("repository %s failed; inspect .railgrid-%s.log in the retained worktree", hookName, hookName)
	}
	return nil
}

// cleanupEnvironment intentionally excludes credentials and language/tool injection
// variables. Repository code receives a target path, never executable task files.
func cleanupEnvironment(workdir string) []string {
	env := []string{"LANG=C.UTF-8", "RAILGRID_CLEANUP_ROOT=" + workdir, "RAILGRID_PREPARE_ROOT=" + workdir}
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
	if attempt.CleanupDelegated || attempt.ProcessCleanupUnconfirmed {
		return
	}
	if r.retainEnvironmentLocked(attempt) {
		return
	}
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
		if current.CleanupDone {
			for key, env := range r.state.Environments {
				if env.AttemptID == id && env.Evicting {
					delete(r.state.Environments, key)
				}
			}
		}
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

// cleanupObservationLocked projects only bounded journal state. Hook output and
// local worktree paths are intentionally never returned to remote callers.
func (r *Runner) cleanupObservationLocked(attempt *attemptRecord) *CleanupObservation {
	if attempt == nil {
		return nil
	}
	obs := &CleanupObservation{State: CleanupNotApplicable, Attempts: attempt.CleanupAttempts, ObservedAt: time.Now().UTC()}
	if attempt.Receipt.Phase.IsTerminal() && attempt.ProcessCleanupUnconfirmed {
		obs.State = CleanupFailed
		obs.Reason = "process_teardown_unconfirmed"
		return obs
	}
	if !attempt.Receipt.Phase.IsTerminal() || !attempt.CleanupEligible {
		return obs
	}
	if attempt.CleanupDelegated {
		obs.State = CleanupDelegated
		obs.Reason = "successor_attempt"
		if env := r.state.Environments[attempt.Start.EnvironmentID]; env != nil && !env.ExpiresAt.IsZero() {
			expiresAt := env.ExpiresAt.UTC()
			obs.ExpiresAt = &expiresAt
		}
		return obs
	}
	if env := r.state.Environments[attempt.Start.EnvironmentID]; env != nil && env.AttemptID == attempt.Receipt.AttemptID && !env.Evicting {
		obs.State = CleanupRetained
		obs.Reason = "warm_environment"
		if !env.ExpiresAt.IsZero() {
			expiresAt := env.ExpiresAt.UTC()
			obs.ExpiresAt = &expiresAt
		}
		return obs
	}
	switch {
	case attempt.CleanupDone:
		obs.State = CleanupDone
	case attempt.cleanupRunning:
		obs.State = CleanupRunning
	case attempt.CleanupAttempts >= 3:
		obs.State = CleanupFailed
		obs.Reason = "retry_limit_reached"
	default:
		obs.State = CleanupPending
	}
	return obs
}

// recoverProcessCleanupsAtStartup gives saved process scopes one bounded
// recovery attempt before retention runs. Unavailable or inconclusive probes
// remain quarantined for a later retention tick or operator recovery.
func (r *Runner) recoverProcessCleanupsAtStartup() {
	changed := false
	recovery, canRecover := r.adapter.(harness.ProcessCleanupRecovery)
	for _, attempt := range r.state.Attempts {
		if attempt == nil || attempt.ProcessCleanupReference == "" {
			continue
		}
		if !canRecover {
			attempt.ProcessCleanupUnconfirmed = true
			changed = true
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		done, err := recovery.RecoverProcessCleanup(ctx, attempt.ProcessCleanupReference)
		cancel()
		if err != nil || !done {
			attempt.ProcessCleanupUnconfirmed = true
			if err != nil {
				log.Printf("process cleanup recovery at startup failed: %v", err)
			}
			changed = true
			continue
		}
		attempt.ProcessCleanupUnconfirmed = false
		attempt.ProcessCleanupReference = ""
		changed = true
	}
	if changed {
		if err := r.persistLocked(); err != nil {
			log.Printf("persist startup process cleanup recovery: %v", err)
		}
	}
}

// scheduleProcessCleanupRecoveryLocked re-probes saved scopes at retention
// cadence. The attempt remains protected while the adapter works outside the
// runner mutex.
func (r *Runner) scheduleProcessCleanupRecoveryLocked() {
	recovery, ok := r.adapter.(harness.ProcessCleanupRecovery)
	if !ok {
		return
	}
	for id, attempt := range r.state.Attempts {
		if attempt == nil || !attempt.ProcessCleanupUnconfirmed || attempt.ProcessCleanupReference == "" || attempt.cleanupRunning {
			continue
		}
		attempt.cleanupRunning = true
		ref := attempt.ProcessCleanupReference
		r.wg.Add(1)
		go func(id, ref string) {
			defer r.wg.Done()
			r.cleanupSlots <- struct{}{}
			defer func() { <-r.cleanupSlots }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			done, err := recovery.RecoverProcessCleanup(ctx, ref)
			cancel()
			r.mu.Lock()
			defer r.mu.Unlock()
			current := r.state.Attempts[id]
			if current == nil {
				return
			}
			current.cleanupRunning = false
			if err != nil {
				log.Printf("process cleanup recovery %s failed: %v", id, err)
				return
			}
			if done {
				current.ProcessCleanupUnconfirmed = false
				current.ProcessCleanupReference = ""
				if persistErr := r.persistLocked(); persistErr != nil {
					log.Printf("persist process cleanup recovery %s: %v", id, persistErr)
				}
				r.scheduleCleanupLocked(current)
			}
		}(id, ref)
	}
}
