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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
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
	execDispatchMaxTries = 9
	// Bound every operation that happens before the executor confirms a launch.
	// The budget is total across admission, retries, and helper startup.
	execStartTimeout = 35 * time.Second
	// Keep the number of simultaneously running command trees small even when
	// many sessions are accepted into the durable queue.
	execMaxConcurrentCommands = 4
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

var errExecMissingTerminalReceipt = errors.New("stateless executor response is missing a terminal execution receipt")

type execSessionRecord struct {
	SessionID           string           `json:"sessionID"`
	RequestID           string           `json:"requestID"`
	CallerKey           string           `json:"callerKey"`
	Fingerprint         string           `json:"fingerprint"`
	CanceledBeforeStart bool             `json:"canceledBeforeStart,omitempty"`
	Result              workerExecResult `json:"result"`
	CreatedAt           time.Time        `json:"createdAt"`
	UpdatedAt           time.Time        `json:"updatedAt"`
}

type execDispatcher interface {
	Start(context.Context, persistentExecRequest) (execExecution, error)
}

// execExecution is a command that has passed validation and started. Waiting
// for its result is separate from dispatch so workspace sync can proceed
// while the command is running.
type execExecution interface {
	Wait(context.Context) (execResponse, error)
}

type completedExecExecution struct {
	response execResponse
	err      error
}

func (e completedExecExecution) Wait(context.Context) (execResponse, error) {
	return e.response, e.err
}

type httpExecDispatcher struct {
	url          string
	client       *http.Client
	startTimeout time.Duration
}

func (d *httpExecDispatcher) Start(ctx context.Context, req persistentExecRequest) (execExecution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	startTimeout := d.startTimeout
	if startTimeout <= 0 {
		startTimeout = execStartTimeout
	}
	requestCtx, cancelRequest := context.WithCancel(ctx)
	deadline := time.Now().Add(startTimeout)
	var startMu sync.Mutex
	startComplete := false
	startTimedOut := false
	startTimer := time.AfterFunc(startTimeout, func() {
		startMu.Lock()
		cancel := !startComplete
		if cancel {
			startTimedOut = true
		}
		startMu.Unlock()
		if cancel {
			cancelRequest()
		}
	})
	transferred := false
	defer func() {
		startTimer.Stop()
		if !transferred {
			cancelRequest()
		}
	}()

	startError := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		startMu.Lock()
		if !startComplete && !startTimedOut && !time.Now().Before(deadline) {
			startTimedOut = true
		}
		timedOut := startTimedOut
		startMu.Unlock()
		if timedOut {
			cancelRequest()
			return fmt.Errorf("stateless executor start exceeded %s budget: %w", startTimeout, context.DeadlineExceeded)
		}
		return err
	}
	finishStart := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		startMu.Lock()
		if !startTimedOut && !startComplete && !time.Now().Before(deadline) {
			startTimedOut = true
		}
		timedOut := startTimedOut
		if !timedOut {
			startComplete = true
			startTimer.Stop()
		}
		startMu.Unlock()
		if timedOut {
			cancelRequest()
			return fmt.Errorf("stateless executor start exceeded %s budget: %w", startTimeout, context.DeadlineExceeded)
		}
		return nil
	}

	raw, err := json.Marshal(req)
	if err != nil {
		return nil, startError(err)
	}
	var resp *http.Response
	for attempt := 1; attempt <= execDispatchMaxTries; attempt++ {
		httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, d.url, bytes.NewReader(raw))
		if err != nil {
			return nil, startError(err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err = d.client.Do(httpReq)
		if err == nil {
			break
		}
		if !isExecDialRefused(err) || attempt == execDispatchMaxTries {
			return nil, startError(fmt.Errorf("dispatch stateless execution: %w", err))
		}
		if err := waitExecDispatchRetry(requestCtx, attempt); err != nil {
			return nil, startError(fmt.Errorf("dispatch stateless execution: %w", err))
		}
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(resp.Body, execRequestMaxBytes))
		if err != nil {
			return nil, startError(err)
		}
		return nil, startError(fmt.Errorf("stateless executor rejected request: %s", strings.TrimSpace(string(body))))
	}
	if resp.Header.Get(execStartedHeader) == "true" {
		if err := finishStart(); err != nil {
			_ = resp.Body.Close()
			return nil, err
		}
		transferred = true
		return &httpExecExecution{response: resp, cancel: cancelRequest}, nil
	}
	// Older executor instances return only after completion. Keep that response
	// shape working during a rolling update; new instances stream a start ack.
	// The startup budget remains active until this body is completely read and
	// validated, so a flushed header without an acknowledgement cannot hold the
	// workspace mutation lock indefinitely.
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, execResponseMaxBytes))
	if err != nil {
		return nil, startError(err)
	}
	result, err := decodeExecResponse(body)
	if err != nil {
		return nil, startError(fmt.Errorf("decode stateless executor response: %w", err))
	}
	if err := finishStart(); err != nil {
		return nil, err
	}
	return completedExecExecution{response: result}, nil
}

// Execute keeps a synchronous convenience for existing callers and tests.
func (d *httpExecDispatcher) Execute(ctx context.Context, req persistentExecRequest) (execResponse, error) {
	execution, err := d.Start(ctx, req)
	if err != nil {
		return execResponse{}, err
	}
	return execution.Wait(ctx)
}

type httpExecExecution struct {
	response *http.Response
	cancel   context.CancelFunc
}

func (e *httpExecExecution) Wait(ctx context.Context) (execResponse, error) {
	defer func() {
		_ = e.response.Body.Close()
		if e.cancel != nil {
			e.cancel()
		}
	}()
	body, err := io.ReadAll(io.LimitReader(e.response.Body, execResponseMaxBytes))
	if err != nil {
		if ctx.Err() != nil {
			return execResponse{}, ctx.Err()
		}
		return execResponse{}, err
	}
	result, err := decodeExecResponse(body)
	if err != nil {
		return execResponse{}, fmt.Errorf("decode stateless executor response: %w", err)
	}
	if !terminalExecPhase(result.Phase) {
		return execResponse{}, fmt.Errorf("%w (phase %q)", errExecMissingTerminalReceipt, result.Phase)
	}
	if result.CleanupUnproven {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = errExecCleanupUnproven.Error()
		}
		return result, fmt.Errorf("%w: %s", errExecCleanupUnproven, message)
	}
	if result.Error != "" {
		return result, errors.New(result.Error)
	}
	return result, nil
}

func decodeExecResponse(body []byte) (execResponse, error) {
	// ExitCode is an int on the compatibility-facing response type, so decoding
	// an empty object would otherwise fabricate a successful zero exit code.
	var receipt struct {
		ExitCode *int `json:"exitCode"`
	}
	if err := json.Unmarshal(body, &receipt); err != nil {
		return execResponse{}, err
	}
	if receipt.ExitCode == nil {
		return execResponse{}, fmt.Errorf("%w: exitCode is absent", errExecMissingTerminalReceipt)
	}
	var result execResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return execResponse{}, err
	}
	return result, nil
}

// isExecDialRefused accepts only a connection refusal returned from the dial
// phase of this POST. Once a connection succeeds, transport errors are
// ambiguous: the executor may already have launched the command.
func isExecDialRefused(err error) bool {
	var requestErr *url.Error
	if !errors.As(err, &requestErr) || !strings.EqualFold(requestErr.Op, http.MethodPost) {
		return false
	}
	var opErr *net.OpError
	return errors.As(requestErr.Err, &opErr) && opErr.Op == "dial" && errors.Is(opErr.Err, syscall.ECONNREFUSED)
}

func waitExecDispatchRetry(ctx context.Context, attempt int) error {
	delay := 100 * time.Millisecond
	for retry := 1; retry < attempt && delay < time.Second; retry++ {
		delay *= 2
	}
	if delay > time.Second {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
	execSlots    chan struct{}
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
		execSlots: make(chan struct{}, execMaxConcurrentCommands),
		exit:      os.Exit, logf: log.Printf,
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
	existing, found, err := w.readRecord(req.SessionID)
	if err != nil {
		return workerExecResult{}, err
	}
	if found {
		if existing.SessionID != req.SessionID {
			return workerExecResult{}, &workerHTTPError{status: http.StatusConflict, message: "execution record session binding is invalid"}
		}
		if existing.CallerKey != req.CallerKey {
			return workerExecResult{}, &workerHTTPError{status: http.StatusForbidden, message: "execution session belongs to another caller"}
		}
		if existing.RequestID != req.RequestID {
			return workerExecResult{}, &workerHTTPError{status: http.StatusConflict, message: "idempotency key was already used for a different execution request"}
		}
		if existing.CanceledBeforeStart {
			return existing.Result, nil
		}
		if existing.Fingerprint != req.Fingerprint {
			return workerExecResult{}, &workerHTTPError{status: http.StatusConflict, message: "idempotency key was already used for a different execution request"}
		}
		return existing.Result, nil
	}
	byRequest, found, err := w.findByRequest(req.CallerKey, req.RequestID)
	if err != nil {
		return workerExecResult{}, err
	}
	if found {
		if byRequest.SessionID != req.SessionID || byRequest.Fingerprint != req.Fingerprint {
			return workerExecResult{}, &workerHTTPError{status: http.StatusConflict, message: "idempotency key was already used for a different execution request"}
		}
		return byRequest.Result, nil
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
	if err := w.gcLocked(time.Now()); err != nil {
		return workerExecResult{}, err
	}
	record, found, err := w.readRecord(req.SessionID)
	if err != nil {
		return workerExecResult{}, err
	}
	if !found {
		if req.RequestID == "" {
			return workerExecResult{}, &workerHTTPError{status: http.StatusNotFound, message: "execution session not found"}
		}
		byRequest, found, err := w.findByRequest(req.CallerKey, req.RequestID)
		if err != nil {
			return workerExecResult{}, err
		}
		if found && byRequest.SessionID != req.SessionID {
			return workerExecResult{}, &workerHTTPError{status: http.StatusConflict, message: "idempotency key is already bound to another execution session"}
		}
		count, err := w.recordCount()
		if err != nil {
			return workerExecResult{}, err
		}
		if count >= execSessionCapacity {
			return workerExecResult{}, &workerHTTPError{status: http.StatusServiceUnavailable, message: "exec coordinator session capacity is exhausted"}
		}
		now := time.Now().UTC()
		record = execSessionRecord{
			SessionID: req.SessionID, RequestID: req.RequestID, CallerKey: req.CallerKey,
			CanceledBeforeStart: true,
			Result:              workerExecResult{SessionID: req.SessionID, RequestID: req.RequestID, State: "canceled"},
			CreatedAt:           now, UpdatedAt: now,
		}
		if err := w.persistRecord(record); err != nil {
			return workerExecResult{}, err
		}
		return record.Result, nil
	}
	if record.SessionID != req.SessionID {
		return workerExecResult{}, &workerHTTPError{status: http.StatusConflict, message: "execution record session binding is invalid"}
	}
	if record.CallerKey != req.CallerKey {
		return workerExecResult{}, &workerHTTPError{status: http.StatusForbidden, message: "execution session belongs to another caller"}
	}
	if req.RequestID != "" && record.RequestID != req.RequestID {
		return workerExecResult{}, &workerHTTPError{status: http.StatusForbidden, message: "execution request belongs to another caller request"}
	}
	if record.CanceledBeforeStart {
		return record.Result, nil
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
	if record.SessionID != req.SessionID {
		return execSessionRecord{}, &workerHTTPError{status: http.StatusConflict, message: "execution record session binding is invalid"}
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
	select {
	case w.execSlots <- struct{}{}:
		defer func() { <-w.execSlots }()
	case <-ctx.Done():
		w.finish(req.SessionID, workerExecResult{State: "canceled"}, ctx.Err())
		return
	}

	// Hold the workspace mutation lock until the executor verifies the requested
	// revision/digest and starts the process. That evidence is a launch precondition;
	// the command may observe later changes in the shared workspace after sync resumes.
	w.mutationMu.Lock()
	if ctx.Err() != nil {
		w.mutationMu.Unlock()
		w.finish(req.SessionID, workerExecResult{State: "canceled"}, ctx.Err())
		return
	}
	running, err := w.setRunning(req.SessionID)
	if err != nil {
		w.mutationMu.Unlock()
		w.clearActive(req.SessionID)
		w.fatalPersistence(req.SessionID, err)
		return
	}
	if !running {
		w.mutationMu.Unlock()
		w.clearActive(req.SessionID)
		return
	}
	execution, err := w.dispatcher.Start(ctx, req.persistentExecRequest)
	w.mutationMu.Unlock()
	if err != nil {
		result := workerExecResult{State: "failed"}
		if errors.Is(err, context.Canceled) {
			result.State = "canceled"
		}
		w.finish(req.SessionID, result, err)
		return
	}
	response, runErr := execution.Wait(ctx)
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
	// Legacy synchronous executors return a response only after completion and
	// may not include Phase. A streaming executor must provide a known terminal
	// phase before its exitCode can be treated as a process result.
	hasTerminalReceipt := !response.CleanupUnproven && !errors.Is(err, errExecCleanupUnproven) &&
		(terminalExecPhase(response.Phase) || (response.Phase == "" && err == nil))
	state := "succeeded"
	if response.CleanupUnproven || errors.Is(err, errExecCleanupUnproven) {
		state = "failed"
	} else if response.Cancelled || response.Phase == "cancelled" || errors.Is(err, context.Canceled) {
		state = "canceled"
	} else if response.TimedOut || response.Phase == "timed_out" {
		state = "timed_out"
	} else if err != nil || response.Error != "" || response.Phase == "failed" || response.ExitCode != 0 || !hasTerminalReceipt {
		state = "failed"
	}
	result := workerExecResult{State: state, Stdout: response.Stdout, Stderr: response.Stderr,
		Truncated: response.StdoutTruncated || response.StderrTruncated}
	if hasTerminalReceipt {
		exitCode := int32(response.ExitCode)
		result.ExitCode = &exitCode
	}
	if err != nil && result.Stderr == "" {
		result.Stderr = err.Error()
	} else if response.Error != "" && result.Stderr == "" {
		result.Stderr = response.Error
	} else if response.CleanupUnproven && result.Stderr == "" {
		result.Stderr = errExecCleanupUnproven.Error()
	} else if !hasTerminalReceipt && result.Stderr == "" {
		result.Stderr = errExecMissingTerminalReceipt.Error()
	}
	return result
}

func terminalExecPhase(phase string) bool {
	switch phase {
	case "completed", "timed_out", "cancelled", "failed":
		return true
	default:
		return false
	}
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
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		filenameID := strings.TrimSuffix(entry.Name(), ".json")
		record, found, err := w.readRecord(filenameID)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		count++
		if !workerTerminal(record.Result.State) {
			continue
		}
		at := record.UpdatedAt
		if record.CanceledBeforeStart && !record.CreatedAt.IsZero() {
			// Keep the cancellation intent for a fixed interval from its
			// creation. Repeated CANCEL requests must not extend it forever.
			at = record.CreatedAt
		}
		if at.Before(now.Add(-execSessionRetention)) {
			if err := os.Remove(w.recordPath(filenameID)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			count--
			continue
		}
		if !record.CanceledBeforeStart {
			terminal = append(terminal, terminalRecord{w.recordPath(filenameID), at})
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].at.Before(terminal[j].at) })
	for _, record := range terminal {
		if count <= execSessionCapacity {
			break
		}
		if err := os.Remove(record.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		count--
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
