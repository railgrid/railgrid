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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	execSessionRetention = 10 * time.Minute
	execSessionCapacity  = 128
)

type workerExecRequest struct {
	Action      string `json:"action"`
	SessionID   string `json:"sessionID"`
	RequestID   string `json:"requestID,omitempty"`
	CallerKey   string `json:"callerKey"`
	Fingerprint string `json:"fingerprint,omitempty"`
	persistentExecRequest
}

type workerExecResult struct {
	SessionID string `json:"sessionID,omitempty"`
	RequestID string `json:"requestID,omitempty"`
	State     string `json:"state"`
	ExitCode  *int32 `json:"exitCode,omitempty"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type execSessionRecord struct {
	SessionID   string           `json:"sessionID"`
	RequestID   string           `json:"requestID"`
	CallerKey   string           `json:"callerKey"`
	Fingerprint string           `json:"fingerprint"`
	Result      workerExecResult `json:"result"`
	CreatedAt   time.Time        `json:"createdAt"`
	UpdatedAt   time.Time        `json:"updatedAt"`
}

type execDispatcher interface {
	Execute(context.Context, persistentExecRequest) (execResponse, error)
}

type httpExecDispatcher struct {
	url    string
	client *http.Client
}

func (d *httpExecDispatcher) Execute(ctx context.Context, req persistentExecRequest) (execResponse, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return execResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(raw))
	if err != nil {
		return execResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(httpReq)
	if err != nil {
		return execResponse{}, fmt.Errorf("dispatch stateless execution: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, execRequestMaxBytes))
	if err != nil {
		return execResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return execResponse{}, fmt.Errorf("stateless executor rejected request: %s", strings.TrimSpace(string(body)))
	}
	var result execResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return execResponse{}, fmt.Errorf("decode stateless executor response: %w", err)
	}
	return result, nil
}

// execCoordinator owns all durable execution authority. The executor receives
// one already-authorized argv request and has no token, sessions, or state.
type execCoordinator struct {
	workspace    string
	stateDir     string
	recordDir    string
	token        string
	dispatcher   execDispatcher
	mutationMu   *sync.Mutex
	writeSession func(execSessionRecord) error
	exit         func(int)
	logf         func(string, ...any)
	mu           sync.Mutex
	active       map[string]context.CancelFunc
}

func newExecCoordinator(workspace, stateDir, token string, dispatcher execDispatcher, mutationMu *sync.Mutex) (*execCoordinator, error) {
	prepared, err := prepareCoordinatorState(stateDir, workspace)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("exec coordinator control token is required")
	}
	if dispatcher == nil {
		return nil, errors.New("stateless executor dispatcher is required")
	}
	if mutationMu == nil {
		mutationMu = &sync.Mutex{}
	}
	w := &execCoordinator{
		workspace: workspace, stateDir: prepared, recordDir: filepath.Join(prepared, stateSessionsDir),
		token: token, dispatcher: dispatcher, mutationMu: mutationMu, active: map[string]context.CancelFunc{},
		exit: os.Exit, logf: log.Printf,
	}
	w.writeSession = w.writeRecord
	if err := w.recoverSessions(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *execCoordinator) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/exec" {
		http.NotFound(rw, r)
		return
	}
	if r.Method != http.MethodPost {
		rw.Header().Set("Allow", http.MethodPost)
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !subtleConstantTimeCompare(r.Header.Get(controlTokenHeader), w.token) {
		http.Error(rw, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, execRequestMaxBytes)
	var req workerExecRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(rw, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(rw, "request body must contain one JSON object", http.StatusBadRequest)
		return
	}
	if err := validateWorkerIdentity(req); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	var result workerExecResult
	var err error
	switch req.Action {
	case "start":
		result, err = w.start(req)
	case "poll":
		result, err = w.poll(req)
	case "cancel":
		result, err = w.cancel(req)
	default:
		err = &workerHTTPError{status: http.StatusBadRequest, message: "unknown exec action"}
	}
	if err != nil {
		status := http.StatusInternalServerError
		var httpErr *workerHTTPError
		if errors.As(err, &httpErr) {
			status = httpErr.status
		}
		http.Error(rw, err.Error(), status)
		return
	}
	writeJSON(rw, http.StatusOK, result)
}

type workerHTTPError struct {
	status  int
	message string
}

func (e *workerHTTPError) Error() string { return e.message }

func validateWorkerIdentity(req workerExecRequest) error {
	if len(req.SessionID) != 32 || strings.Trim(req.SessionID, "0123456789abcdef") != "" {
		return errors.New("sessionID must be 32 lowercase hex characters")
	}
	if strings.TrimSpace(req.CallerKey) == "" || len(req.CallerKey) > 128 || len(req.RequestID) > 128 {
		return errors.New("callerKey is required and callerKey/requestID must be bounded")
	}
	if req.Action == "start" {
		if strings.TrimSpace(req.RequestID) == "" {
			return errors.New("start requestID is required")
		}
		if len(req.Fingerprint) != 64 || strings.Trim(req.Fingerprint, "0123456789abcdef") != "" {
			return errors.New("start fingerprint must be 64 lowercase hex characters")
		}
	}
	return nil
}

func (w *execCoordinator) start(req workerExecRequest) (workerExecResult, error) {
	if err := validatePersistentExecRequest(req.persistentExecRequest); err != nil {
		return workerExecResult{}, &workerHTTPError{status: http.StatusBadRequest, message: err.Error()}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.gcLocked(time.Now()); err != nil {
		return workerExecResult{}, err
	}
	byRequest, found, err := w.findByRequest(req.CallerKey, req.RequestID)
	if err != nil {
		return workerExecResult{}, err
	}
	if found {
		if byRequest.Fingerprint != req.Fingerprint {
			return workerExecResult{}, &workerHTTPError{status: http.StatusConflict, message: "idempotency key was already used for a different execution request"}
		}
		return byRequest.Result, nil
	}
	existing, found, err := w.readRecord(req.SessionID)
	if err != nil {
		return workerExecResult{}, err
	}
	if found {
		if existing.CallerKey != req.CallerKey {
			return workerExecResult{}, &workerHTTPError{status: http.StatusForbidden, message: "execution session belongs to another caller"}
		}
		if existing.RequestID != req.RequestID || existing.Fingerprint != req.Fingerprint {
			return workerExecResult{}, &workerHTTPError{status: http.StatusConflict, message: "idempotency key was already used for a different execution request"}
		}
		return existing.Result, nil
	}
	count, err := w.recordCount()
	if err != nil {
		return workerExecResult{}, err
	}
	if count >= execSessionCapacity {
		return workerExecResult{}, &workerHTTPError{status: http.StatusServiceUnavailable, message: "exec coordinator session capacity is exhausted"}
	}
	now := time.Now().UTC()
	record := execSessionRecord{SessionID: req.SessionID, RequestID: req.RequestID, CallerKey: req.CallerKey, Fingerprint: req.Fingerprint,
		Result: workerExecResult{SessionID: req.SessionID, RequestID: req.RequestID, State: "queued"}, CreatedAt: now, UpdatedAt: now}
	// Durability precedes dispatch: after this write, restart recovery can prove
	// the command must fail rather than accidentally run it a second time.
	if err := w.persistRecord(record); err != nil {
		return workerExecResult{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	w.active[req.SessionID] = cancel
	go w.run(runCtx, req)
	return record.Result, nil
}

func (w *execCoordinator) poll(req workerExecRequest) (workerExecResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	record, err := w.boundRecord(req)
	if err != nil {
		return workerExecResult{}, err
	}
	return record.Result, nil
}

func (w *execCoordinator) cancel(req workerExecRequest) (workerExecResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	record, err := w.boundRecord(req)
	if err != nil {
		return workerExecResult{}, err
	}
	if workerTerminal(record.Result.State) {
		return record.Result, nil
	}
	if cancel := w.active[req.SessionID]; cancel != nil {
		cancel()
	}
	record.Result.State = "canceled"
	record.UpdatedAt = time.Now().UTC()
	if err := w.persistRecord(record); err != nil {
		return workerExecResult{}, err
	}
	return record.Result, nil
}

func (w *execCoordinator) boundRecord(req workerExecRequest) (execSessionRecord, error) {
	record, found, err := w.readRecord(req.SessionID)
	if err != nil {
		return execSessionRecord{}, err
	}
	if !found {
		return execSessionRecord{}, &workerHTTPError{status: http.StatusNotFound, message: "execution session not found"}
	}
	if record.CallerKey != req.CallerKey {
		return execSessionRecord{}, &workerHTTPError{status: http.StatusForbidden, message: "execution session belongs to another caller"}
	}
	if req.RequestID != "" && record.RequestID != req.RequestID {
		return execSessionRecord{}, &workerHTTPError{status: http.StatusForbidden, message: "execution request belongs to another caller request"}
	}
	return record, nil
}

func (w *execCoordinator) run(ctx context.Context, req workerExecRequest) {
	// One coordinator-owned boundary covers validation in the stateless
	// executor and the complete command lifetime. Sync holds this same lock
	// through reload/restart, so neither mutation can interleave.
	w.mutationMu.Lock()
	defer w.mutationMu.Unlock()
	if ctx.Err() != nil {
		w.finish(req.SessionID, workerExecResult{State: "canceled"}, ctx.Err())
		return
	}
	running, err := w.setRunning(req.SessionID)
	if err != nil {
		w.clearActive(req.SessionID)
		w.fatalPersistence(req.SessionID, err)
		return
	}
	if !running {
		w.clearActive(req.SessionID)
		return
	}
	response, runErr := w.dispatcher.Execute(ctx, req.persistentExecRequest)
	w.finish(req.SessionID, workerResultFromExec(response, runErr), runErr)
}

func (w *execCoordinator) setRunning(sessionID string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	record, found, err := w.readRecord(sessionID)
	if err != nil {
		return false, fmt.Errorf("read queued execution session: %w", err)
	}
	if !found {
		return false, errors.New("durable queued execution record disappeared")
	}
	if workerTerminal(record.Result.State) {
		return false, nil
	}
	record.Result.State = "running"
	record.UpdatedAt = time.Now().UTC()
	if err := w.persistRecord(record); err != nil {
		return false, fmt.Errorf("persist running execution session: %w", err)
	}
	return true, nil
}

func (w *execCoordinator) clearActive(sessionID string) {
	w.mu.Lock()
	delete(w.active, sessionID)
	w.mu.Unlock()
}

func (w *execCoordinator) finish(sessionID string, result workerExecResult, runErr error) {
	w.mu.Lock()
	record, found, err := w.readRecord(sessionID)
	if err != nil || !found {
		delete(w.active, sessionID)
		w.mu.Unlock()
		if err == nil {
			err = errors.New("durable execution record disappeared")
		}
		w.fatalPersistence(sessionID, err)
		return
	}
	if workerTerminal(record.Result.State) {
		delete(w.active, sessionID)
		w.mu.Unlock()
		return
	}
	result.SessionID, result.RequestID = record.SessionID, record.RequestID
	if runErr != nil && result.Stderr == "" {
		result.Stderr = runErr.Error()
	}
	record.Result = result
	record.UpdatedAt = time.Now().UTC()
	err = w.persistRecord(record)
	delete(w.active, sessionID)
	w.mu.Unlock()
	if err != nil {
		w.fatalPersistence(sessionID, err)
	}
}

func (w *execCoordinator) persistRecord(record execSessionRecord) error {
	return w.writeSession(record)
}

func (w *execCoordinator) fatalPersistence(sessionID string, err error) {
	w.logf("FATAL: persist execution session %s: %v", sessionID, err)
	w.exit(1)
}

func workerResultFromExec(response execResponse, err error) workerExecResult {
	state := "succeeded"
	if response.Cancelled || errors.Is(err, context.Canceled) {
		state = "canceled"
	} else if response.TimedOut {
		state = "timed_out"
	} else if err != nil || response.ExitCode != 0 {
		state = "failed"
	}
	exitCode := int32(response.ExitCode)
	result := workerExecResult{State: state, ExitCode: &exitCode, Stdout: response.Stdout, Stderr: response.Stderr,
		Truncated: response.StdoutTruncated || response.StderrTruncated}
	if err != nil && result.Stderr == "" {
		result.Stderr = err.Error()
	}
	return result
}

func workerTerminal(state string) bool {
	switch state {
	case "succeeded", "failed", "timed_out", "canceled":
		return true
	default:
		return false
	}
}

func (w *execCoordinator) recordPath(sessionID string) string {
	return filepath.Join(w.recordDir, sessionID+".json")
}

func (w *execCoordinator) readRecord(sessionID string) (execSessionRecord, bool, error) {
	raw, err := os.ReadFile(w.recordPath(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return execSessionRecord{}, false, nil
	}
	if err != nil {
		return execSessionRecord{}, false, err
	}
	var record execSessionRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return execSessionRecord{}, false, fmt.Errorf("decode exec session %s: %w", sessionID, err)
	}
	return record, true, nil
}

func (w *execCoordinator) writeRecord(record execSessionRecord) (retErr error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(w.recordDir, ".session-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, w.recordPath(record.SessionID)); err != nil {
		return err
	}
	dir, err := os.Open(w.recordDir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func (w *execCoordinator) recoverSessions() error {
	entries, err := os.ReadDir(w.recordDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		sessionID := strings.TrimSuffix(entry.Name(), ".json")
		record, found, err := w.readRecord(sessionID)
		if err != nil {
			return err
		}
		if found && !workerTerminal(record.Result.State) {
			record.Result.State = "failed"
			record.Result.Stderr = "execution interrupted by coordinator restart; command was not redispatched"
			record.UpdatedAt = time.Now().UTC()
			if err := w.persistRecord(record); err != nil {
				return err
			}
		}
	}
	return w.gcLocked(time.Now())
}

func (w *execCoordinator) gcLocked(now time.Time) error {
	entries, err := os.ReadDir(w.recordDir)
	if err != nil {
		return err
	}
	type terminalRecord struct {
		path string
		at   time.Time
	}
	var terminal []terminalRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, found, err := w.readRecord(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return err
		}
		if found && workerTerminal(record.Result.State) {
			terminal = append(terminal, terminalRecord{w.recordPath(record.SessionID), record.UpdatedAt})
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].at.Before(terminal[j].at) })
	for i, record := range terminal {
		if record.at.Before(now.Add(-execSessionRetention)) || len(entries)-i > execSessionCapacity {
			if err := os.Remove(record.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func (w *execCoordinator) recordCount() (int, error) {
	entries, err := os.ReadDir(w.recordDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count, nil
}

func (w *execCoordinator) findByRequest(callerKey, requestID string) (execSessionRecord, bool, error) {
	entries, err := os.ReadDir(w.recordDir)
	if err != nil {
		return execSessionRecord{}, false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, found, err := w.readRecord(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return execSessionRecord{}, false, err
		}
		if found && record.CallerKey == callerKey && record.RequestID == requestID {
			return record, true, nil
		}
	}
	return execSessionRecord{}, false, nil
}
