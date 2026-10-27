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
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Attempt retention.
//
// Every attempt the runner accepts is written to state.json with its receipt,
// its start request and its last cached event, and until now nothing ever took
// one out again. A machine that reviews every webhook delivery of a busy
// repository therefore accumulates hundreds of finished attempts, the file
// grows past what the loader was willing to read, and the runner cannot start
// at all. Retention closes that loop: an attempt that is finished, or parked
// with nobody coming back for it, is dropped after a window, and the number of
// such attempts is capped so a flood cannot outgrow the window.
//
// Dropping an attempt removes everything the runner keeps for it: the state
// records, its event journal, its artifacts and the per-attempt worktree. A
// workspace attempt's directory is shared with every later attempt on that
// workspace and stays; a worktree still leased to a warm environment stays
// until the environment's own expiry releases it.

const (
	defaultAttemptRetentionHours       = 24 * 7
	defaultParkedAttemptRetentionHours = 24 * 30
	defaultMaxRetainedAttempts         = 200
	// purgeInterval paces the periodic sweep; a sweep with nothing to drop is a
	// map walk, so the cadence only bounds how long an expired attempt lingers.
	purgeInterval = 10 * time.Minute
)

// purgeAttemptsLocked drops the attempts retention no longer keeps and returns
// the directories whose removal the caller should run OUTSIDE the mutex (a
// worktree can be large). Records and journals are removed in place.
//
// An attempt is never dropped while it is executing, waiting on a permission
// verdict in this process, owning a warm environment, running its cleanup hook
// or still owed one: retention only ever takes what the runner is done with.
func (r *Runner) purgeAttemptsLocked(now time.Time) (removed int, paths []string) {
	terminalWindow := time.Duration(r.cfg.AttemptRetentionHours) * time.Hour
	parkedWindow := time.Duration(r.cfg.ParkedAttemptRetentionHours) * time.Hour

	type candidate struct {
		id   string
		seen time.Time
	}
	var retained []candidate
	expired := map[string]bool{}
	for id, attempt := range r.state.Attempts {
		if attempt == nil || !r.purgeableLocked(id, attempt) {
			continue
		}
		seen := attempt.Receipt.UpdatedAt
		if seen.IsZero() {
			seen = attempt.Receipt.AcceptedAt
		}
		retained = append(retained, candidate{id: id, seen: seen})
		age := now.Sub(seen)
		switch {
		case attempt.Receipt.Phase.IsTerminal() && age > terminalWindow:
			expired[id] = true
		case attempt.Receipt.Phase == PhaseNeedsInput && age > parkedWindow:
			expired[id] = true
		}
	}
	// Over the cap, the oldest go first regardless of age, so one busy day
	// cannot grow the state beyond what the loader and the disk are sized for.
	sort.Slice(retained, func(i, j int) bool { return retained[i].seen.Before(retained[j].seen) })
	over := len(retained) - r.cfg.MaxRetainedAttempts
	for _, c := range retained {
		if over <= 0 {
			break
		}
		if !expired[c.id] {
			expired[c.id] = true
			over--
		}
	}
	for id := range expired {
		paths = append(paths, r.dropAttemptLocked(id)...)
		removed++
	}
	return removed, paths
}

// purgeableLocked reports whether retention may consider an attempt at all.
func (r *Runner) purgeableLocked(id string, attempt *attemptRecord) bool {
	if !attempt.Receipt.Phase.IsTerminal() && attempt.Receipt.Phase != PhaseNeedsInput {
		return false
	}
	if _, running := r.running[id]; running {
		return false
	}
	if r.permissions[id] != nil || attempt.cleanupRunning {
		return false
	}
	if env := r.state.Environments[attempt.Start.EnvironmentID]; env != nil && env.AttemptID == id {
		return false
	}
	// A repository cleanup hook that has not run yet gets its chances first;
	// after three failures the directory is removed as it stands.
	if attempt.Receipt.Phase.IsTerminal() && attempt.CleanupEligible && !attempt.CleanupDone && attempt.CleanupAttempts < 3 && attempt.Receipt.Workdir != "" {
		return false
	}
	return true
}

// dropAttemptLocked removes one attempt's records and journal and returns the
// directories left for removeAttemptFiles.
func (r *Runner) dropAttemptLocked(id string) []string {
	attempt := r.state.Attempts[id]
	if attempt == nil {
		return nil
	}
	delete(r.state.Attempts, id)
	delete(r.state.Events, id)
	delete(r.credentials, id)
	delete(r.shutdown, id)
	for key, op := range r.state.Operations {
		if op.AttemptID == id {
			delete(r.state.Operations, key)
		}
	}
	for key, reservation := range r.state.Reservations {
		if reservation.AttemptID == id {
			delete(r.state.Reservations, key)
		}
	}
	var paths []string
	artifactDir := filepath.Join(r.cfg.StateDir, "artifacts", id)
	for key, artifact := range r.state.Artifacts {
		if strings.HasPrefix(artifact.Path, artifactDir+string(filepath.Separator)) {
			delete(r.state.Artifacts, key)
		}
	}
	paths = append(paths, artifactDir)
	// Only the attempt's OWN worktree is removed, and only when it is exactly
	// where the runner creates one. A workspace attempt's persistent directory
	// and a warm environment's checkout live elsewhere under worktrees/ and
	// must survive the attempt that last used them.
	if workdir := attempt.Receipt.Workdir; workdir != "" && strings.TrimSpace(attempt.Start.WorkspaceID) == "" {
		if own := filepath.Join(r.cfg.StateDir, "worktrees", attempt.Receipt.TaskID, id); filepath.Clean(workdir) == own {
			paths = append(paths, own)
		}
	}
	if err := r.store.removeEvents(id); err != nil {
		log.Printf("retention: remove event journal %s: %v", id, err)
	}
	return paths
}

// removeAttemptFiles deletes what dropAttemptLocked handed back. It runs
// without the runner mutex; a directory that is already gone is fine, and a
// task directory emptied by the removal is taken with it.
func removeAttemptFiles(paths []string) {
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			log.Printf("retention: remove %s: %v", path, err)
			continue
		}
		// Best effort: os.Remove refuses a non-empty directory, which is the
		// point — a task directory with another attempt's worktree stays.
		_ = os.Remove(filepath.Dir(path))
	}
}

// purgeLocked runs one retention pass and hands the file removal to a
// background goroutine. It is the entry point for the periodic sweep and for
// acceptance of a new attempt, where the cap is what keeps the state bounded.
func (r *Runner) purgeLocked(now time.Time) int {
	removed, paths := r.purgeAttemptsLocked(now)
	r.lastPurge = now
	if len(paths) > 0 {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.cleanupSlots <- struct{}{}
			defer func() { <-r.cleanupSlots }()
			removeAttemptFiles(paths)
		}()
	}
	return removed
}
