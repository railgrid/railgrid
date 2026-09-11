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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	legacyStateVersion = 1
	stateVersion       = 2
)

type persistedState struct {
	Version      int                          `json:"version"`
	Attempts     map[string]*attemptRecord    `json:"attempts"`
	Operations   map[string]operationRecord   `json:"operations"`
	Events       map[string][]Event           `json:"events"`
	Artifacts    map[string]artifactRecord    `json:"artifacts"`
	Reservations map[string]reservationRecord `json:"reservations"`

	// migratedLegacyShutdown holds one-shot in-memory markers. They let the
	// runner append a truthful needs_input event after the narrowly-scoped
	// v1 receipt migration without persisting migration bookkeeping.
	migratedLegacyShutdown map[string]struct{} `json:"-"`
}

type attemptRecord struct {
	Receipt       Receipt        `json:"receipt"`
	Start         StartRequest   `json:"start"`
	Fingerprint   string         `json:"fingerprint"`
	Resources     []string       `json:"resources,omitempty"`
	ArtifactSpecs []ArtifactSpec `json:"artifactSpecs,omitempty"`
	CancelPending bool           `json:"cancelPending,omitempty"`
	OutputBytes   int64          `json:"outputBytes,omitempty"`
	LimitExceeded bool           `json:"limitExceeded,omitempty"`
}

type operationRecord struct {
	Fingerprint string `json:"fingerprint"`
	AttemptID   string `json:"attemptID"`
}

type artifactRecord struct {
	Artifact Artifact `json:"artifact"`
	Path     string   `json:"path"`
}

type reservationRecord struct {
	AttemptID string `json:"attemptID"`
}

type stateStore struct {
	dir  string
	path string
}

func newStateStore(dir string) (*stateStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create runner state directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("protect runner state directory: %w", err)
	}
	return &stateStore{dir: dir, path: filepath.Join(dir, "state.json")}, nil
}

func (s *stateStore) load() (persistedState, error) {
	state := emptyPersistedState()
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("open runner state: %w", err)
	}
	defer func() { _ = f.Close() }()
	decoder := json.NewDecoder(io.LimitReader(f, 32<<20))
	if err := decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("decode runner state: %w", err)
	}
	switch state.Version {
	case stateVersion:
	case legacyStateVersion:
		state.Version = stateVersion
		state.migratedLegacyShutdown = migrateLegacyShutdownReceipts(state)
	default:
		return state, fmt.Errorf("unsupported runner state version %d", state.Version)
	}
	if state.Attempts == nil {
		state.Attempts = map[string]*attemptRecord{}
	}
	if state.Operations == nil {
		state.Operations = map[string]operationRecord{}
	}
	if state.Events == nil {
		state.Events = map[string][]Event{}
	}
	if state.Artifacts == nil {
		state.Artifacts = map[string]artifactRecord{}
	}
	if state.Reservations == nil {
		state.Reservations = map[string]reservationRecord{}
	}
	return state, nil
}

func emptyPersistedState() persistedState {
	return persistedState{
		Version:                stateVersion,
		Attempts:               map[string]*attemptRecord{},
		Operations:             map[string]operationRecord{},
		Events:                 map[string][]Event{},
		Artifacts:              map[string]artifactRecord{},
		Reservations:           map[string]reservationRecord{},
		migratedLegacyShutdown: map[string]struct{}{},
	}
}

// migrateLegacyShutdownReceipts repairs only the runner/v1 receipt produced by
// the old Close path. That path used the same blocker as explicit cancellation,
// so reopening is safe only when the durable record proves all of the
// following: the receipt is cancelled, CancelPending is false, no cancel
// operation references the attempt, the blocker is exact, and a session ID is
// present. Newer state versions intentionally do not apply this migration.
func migrateLegacyShutdownReceipts(state persistedState) map[string]struct{} {
	recovered := make(map[string]struct{})
	for attemptID, attempt := range state.Attempts {
		if attempt == nil || attempt.Receipt.Phase != PhaseCancelled || attempt.CancelPending || attempt.LimitExceeded || attempt.Receipt.Blocker != legacyShutdownCancellationBlocker || attempt.Receipt.LastError != nil || !validSessionID(attempt.Receipt.SessionID) {
			continue
		}
		if hasCancelOperation(state.Operations, attemptID) {
			continue
		}
		attempt.Receipt.Phase = PhaseNeedsInput
		attempt.Receipt.Blocker = shutdownBlocker
		attempt.Receipt.UpdatedAt = eventNow()
		recovered[attemptID] = struct{}{}
	}
	return recovered
}

func hasCancelOperation(operations map[string]operationRecord, attemptID string) bool {
	for key, operation := range operations {
		if !strings.HasPrefix(key, "cancel\x00") {
			continue
		}
		if operation.AttemptID == attemptID {
			return true
		}
		// The operation value is authoritative in normal state, but parsing the
		// stable key as a defensive fallback prevents a malformed cancel record
		// from being mistaken for proof that no operator cancellation exists.
		parts := strings.Split(key, "\x00")
		if len(parts) >= 3 && parts[2] == attemptID {
			return true
		}
	}
	return false
}

func validSessionID(sessionID string) bool {
	return sessionID != "" && sessionID == strings.TrimSpace(sessionID) && len(sessionID) <= maxIdentifierBytes
}

func (s *stateStore) save(state persistedState) error {
	state.Version = stateVersion
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runner state: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create runner state temporary file: %w", err)
	}
	tmpName := tmp.Name()
	removeTemp := true
	defer func() {
		_ = tmp.Close()
		if removeTemp {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("protect runner state temporary file: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		return fmt.Errorf("write runner state temporary file: %w", err)
	}
	if _, err := tmp.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write runner state terminator: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync runner state temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close runner state temporary file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace runner state: %w", err)
	}
	removeTemp = false
	if dir, err := os.Open(s.dir); err == nil {
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil {
			return fmt.Errorf("sync runner state directory: %w", syncErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close runner state directory: %w", closeErr)
		}
	}
	return nil
}

func eventNow() time.Time { return time.Now().UTC() }
