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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
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

type execWorker struct {
	workspace string
	token     string
	recordDir string
	exit      func(int)
	protected bool

	mu      sync.Mutex
	active  map[string]context.CancelFunc
	runSlot chan struct{}
}

func runExecWorker() error {
	cfg, err := configFromEnv()
	if err != nil {
		return err
	}
	if err := enableChildSubreaper(); err != nil {
		return err
	}
	if os.Getpid() != 1 {
		log.Printf("WARNING: exec-worker is pid %d; production isolation requires container PID 1", os.Getpid())
	}
	worker, err := newExecWorker(cfg.WorkDir, cfg.ControlToken, true)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: defaultExecAddr, Handler: worker, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("kedge exec-worker listening on %s (workdir=%s)", defaultExecAddr, cfg.WorkDir)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("exec-worker server: %v", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}

func newExecWorker(workspace, token string, requireProtected bool) (*execWorker, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" || strings.TrimSpace(token) == "" {
		return nil, errors.New("exec worker requires workspace and control token")
	}
	recordDir := filepath.Join(workspace, platformMetadataDir, "exec-sessions")
	if requireProtected {
		if err := validateProtectedPlatformState(workspace); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(recordDir, 0o770); err != nil {
		return nil, fmt.Errorf("create exec session directory: %w", err)
	}
	w := &execWorker{
		workspace: workspace, token: token, recordDir: recordDir, exit: os.Exit,
		protected: requireProtected, active: map[string]context.CancelFunc{}, runSlot: make(chan struct{}, 1),
	}
	if err := w.recoverSessions(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *execWorker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
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
	var (
		result workerExecResult
		err    error
	)
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

func (w *execWorker) start(req workerExecRequest) (workerExecResult, error) {
	if err := validatePersistentExecRequest(req.persistentExecRequest); err != nil {
		return workerExecResult{}, &workerHTTPError{status: http.StatusBadRequest, message: err.Error()}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.gcLocked(time.Now()); err != nil {
		return workerExecResult{}, err
	}
	byRequest, requestFound, err := w.findByRequest(req.CallerKey, req.RequestID)
	if err != nil {
		return workerExecResult{}, err
	}
	if requestFound {
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
		return workerExecResult{}, &workerHTTPError{status: http.StatusServiceUnavailable, message: "exec-worker session capacity is exhausted"}
	}
	now := time.Now().UTC()
	record := execSessionRecord{
		SessionID: req.SessionID, RequestID: req.RequestID, CallerKey: req.CallerKey, Fingerprint: req.Fingerprint,
		Result: workerExecResult{SessionID: req.SessionID, RequestID: req.RequestID, State: "queued"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := w.writeRecord(record); err != nil {
		return workerExecResult{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	w.active[req.SessionID] = cancel
	go w.run(runCtx, req)
	return record.Result, nil
}

func (w *execWorker) poll(req workerExecRequest) (workerExecResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	record, err := w.boundRecord(req)
	if err != nil {
		return workerExecResult{}, err
	}
	return record.Result, nil
}

func (w *execWorker) cancel(req workerExecRequest) (workerExecResult, error) {
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
	if err := w.writeRecord(record); err != nil {
		return workerExecResult{}, err
	}
	return record.Result, nil
}

func (w *execWorker) boundRecord(req workerExecRequest) (execSessionRecord, error) {
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

func (w *execWorker) run(ctx context.Context, req workerExecRequest) {
	select {
	case w.runSlot <- struct{}{}:
		defer func() { <-w.runSlot }()
	case <-ctx.Done():
		w.finish(req.SessionID, workerExecResult{State: "canceled"}, nil)
		return
	}
	lock, err := acquireWorkspaceMutationLock(ctx, w.workspace, w.protected)
	if err != nil {
		state := "failed"
		if errors.Is(err, context.Canceled) {
			state = "canceled"
		}
		w.finish(req.SessionID, workerExecResult{State: state, Stderr: err.Error()}, nil)
		return
	}
	defer func() { _ = lock.Close() }()
	if !w.setRunning(req.SessionID) {
		return
	}
	req.persistentExecRequest.DropCredentials = true
	response, runErr := runPersistentExec(ctx, w.workspace, req.persistentExecRequest)
	result := workerResultFromExec(response, runErr)
	fatalCleanup := execCleanupRequiresWorkerExit(runErr)
	w.finish(req.SessionID, result, runErr)
	if fatalCleanup {
		w.exit(1)
	}
}

func execCleanupRequiresWorkerExit(err error) bool {
	return errors.Is(err, errExecCleanupUnproven)
}

func (w *execWorker) setRunning(sessionID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	record, found, err := w.readRecord(sessionID)
	if err != nil || !found || workerTerminal(record.Result.State) {
		return false
	}
	record.Result.State = "running"
	record.UpdatedAt = time.Now().UTC()
	return w.writeRecord(record) == nil
}

func (w *execWorker) finish(sessionID string, result workerExecResult, runErr error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer delete(w.active, sessionID)
	record, found, err := w.readRecord(sessionID)
	if err != nil || !found || record.Result.State == "canceled" {
		return
	}
	result.SessionID = record.SessionID
	result.RequestID = record.RequestID
	if runErr != nil && result.Stderr == "" {
		result.Stderr = runErr.Error()
	}
	record.Result = result
	record.UpdatedAt = time.Now().UTC()
	if err := w.writeRecord(record); err != nil {
		log.Printf("persist exec completion %s: %v", sessionID, err)
		w.exit(1)
	}
}

func workerResultFromExec(response execResponse, err error) workerExecResult {
	state := "failed"
	switch {
	case response.Cancelled:
		state = "canceled"
	case response.TimedOut:
		state = "timed_out"
	case err == nil && response.Phase == "completed" && response.ExitCode == 0:
		state = "succeeded"
	}
	exitCode := int32(response.ExitCode)
	result := workerExecResult{
		State: state, ExitCode: &exitCode, Stdout: response.Stdout, Stderr: response.Stderr,
		Truncated: response.StdoutTruncated || response.StderrTruncated,
	}
	if err != nil {
		result.Stderr = strings.TrimSpace(result.Stderr + "\n" + err.Error())
	}
	return result
}

func workerTerminal(state string) bool {
	switch state {
	case "succeeded", "failed", "canceled", "timed_out":
		return true
	default:
		return false
	}
}

func (w *execWorker) recordPath(sessionID string) string {
	return filepath.Join(w.recordDir, sessionID+".json")
}

func (w *execWorker) readRecord(sessionID string) (execSessionRecord, bool, error) {
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

func (w *execWorker) writeRecord(record execSessionRecord) (retErr error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(w.recordDir, ".session-*")
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
	if err := tmp.Chmod(0o660); err != nil {
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
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	return err
}

func (w *execWorker) recoverSessions() error {
	w.mu.Lock()
	defer w.mu.Unlock()
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
			record.Result.Stderr = "execution interrupted by exec-worker restart; command was not redispatched"
			record.UpdatedAt = time.Now().UTC()
			if err := w.writeRecord(record); err != nil {
				return err
			}
		}
	}
	return w.gcLocked(time.Now())
}

func (w *execWorker) gcLocked(now time.Time) error {
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
			terminal = append(terminal, terminalRecord{path: w.recordPath(record.SessionID), at: record.UpdatedAt})
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

func (w *execWorker) recordCount() (int, error) {
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

func (w *execWorker) findByRequest(callerKey, requestID string) (execSessionRecord, bool, error) {
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
