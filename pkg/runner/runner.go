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
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/faroshq/faros/pkg/runner/harness"
)

const (
	maxIdentifierBytes = 128
	maxMessageBytes    = 8 << 10

	// This is the blocker that runner/v1 wrote when Close cancelled an
	// adapter. Keep it exact because the v1 -> v2 migration only reopens this
	// ambiguous receipt when the rest of the durable state proves that no
	// operator cancellation was requested.
	legacyShutdownCancellationBlocker = "harness exited after cancellation"
	shutdownBlocker                   = "runner shut down while harness was active; reconcile the existing harness session before resuming"
)

// Runner is the generic local execution service. It owns protocol state and
// workspace/resource boundaries; harness.Adapter owns the child process.
type Runner struct {
	cfg     Config
	adapter harness.Adapter
	store   *stateStore
	state   persistedState
	lock    *processLock

	mu           sync.Mutex
	running      map[string]context.CancelFunc
	subscribers  map[string]map[chan Event]struct{}
	wg           sync.WaitGroup
	listener     net.Listener
	closed       bool
	closeDone    chan struct{}
	closeErr     error
	shutdown     map[string]struct{}
	capabilities Capabilities
}

// executionGate keeps adapter callbacks ordered with terminal result
// reconciliation. An adapter may emit from another goroutine, so the runner
// must wait for callbacks that started before Run returned before accepting
// the adapter's result.
type executionGate struct {
	mu     sync.Mutex
	cond   *sync.Cond
	active int
	closed bool
}

func newExecutionGate() *executionGate {
	gate := &executionGate{}
	gate.cond = sync.NewCond(&gate.mu)
	return gate
}

func (g *executionGate) begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.active++
	return true
}

func (g *executionGate) end() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	if g.active == 0 {
		g.cond.Broadcast()
	}
}

func (g *executionGate) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	for g.active > 0 {
		g.cond.Wait()
	}
}

// New creates a runner, loads durable state, probes the harness without model
// calls, and acquires the state-directory singleton lock.
func New(cfg Config, adapter harness.Adapter) (*Runner, error) {
	if adapter == nil {
		return nil, errors.New("runner harness adapter is required")
	}
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	store, err := newStateStore(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	lock, err := acquireProcessLock(filepath.Join(cfg.StateDir, "runner.lock"))
	if err != nil {
		return nil, err
	}
	state, err := store.load()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	info, probeErr := adapter.Probe(context.Background())
	capabilities := Capabilities{
		ProtocolVersion: cfg.ProtocolVersion,
		RunnerID:        cfg.RunnerID,
		Version:         cfg.Version,
		OS:              runtime.GOOS,
		Architecture:    runtime.GOARCH,
		Toolchains:      append([]string(nil), cfg.Toolchains...),
		Environment:     append([]string(nil), cfg.Environment...),
		Capacity:        Capacity{Maximum: cfg.MaximumCapacity},
		Ready:           probeErr == nil && info.Ready && len(info.Reasons) == 0,
	}
	verificationCapabilities := append([]string(nil), cfg.Verification...)
	verificationCapabilities = appendUnique(verificationCapabilities, gitResultCapability)
	if hasFetchRemote(cfg.Repositories) {
		verificationCapabilities = appendUnique(verificationCapabilities, gitFetchCapability)
	}
	capabilities.Verification = verificationCapabilities
	harnessReasons := append([]string(nil), info.Reasons...)
	if probeErr != nil {
		reason := "harness probe failed: " + probeErr.Error()
		capabilities.Reasons = []string{reason}
		harnessReasons = append(harnessReasons, reason)
	}
	capabilities.Harnesses = []HarnessCapability{{Name: info.Name, Version: info.Version, Ready: probeErr == nil && info.Ready && len(info.Reasons) == 0, Reasons: harnessReasons}}
	capabilities.Reasons = append(capabilities.Reasons, info.Reasons...)
	if !info.Ready && probeErr == nil && len(info.Reasons) == 0 {
		capabilities.Reasons = append(capabilities.Reasons, "harness is not ready")
	}
	r := &Runner{
		cfg:          cfg,
		adapter:      adapter,
		store:        store,
		state:        state,
		lock:         lock,
		running:      map[string]context.CancelFunc{},
		subscribers:  map[string]map[chan Event]struct{}{},
		closeDone:    make(chan struct{}),
		shutdown:     map[string]struct{}{},
		capabilities: capabilities,
	}
	if err := r.recoverLocked(); err != nil {
		_ = lock.Close()
		return nil, err
	}
	if err := r.persistLocked(); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return r, nil
}

// NewRunner is an explicit alias for callers that prefer the type name.
func NewRunner(cfg Config, adapter harness.Adapter) (*Runner, error) {
	return New(cfg, adapter)
}

// NewServer is retained as a transport-oriented constructor alias.
func NewServer(cfg Config, adapter harness.Adapter) (*Runner, error) {
	return New(cfg, adapter)
}

// Capabilities returns a copy of the current discovery document.
func (r *Runner) Capabilities() Capabilities {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.capabilitiesLocked()
}

func (r *Runner) capabilitiesLocked() Capabilities {
	c := r.capabilities
	c.Harnesses = append([]HarnessCapability(nil), c.Harnesses...)
	c.Toolchains = append([]string(nil), c.Toolchains...)
	c.Environment = append([]string(nil), c.Environment...)
	c.Verification = append([]string(nil), c.Verification...)
	c.Reasons = append([]string(nil), c.Reasons...)
	c.Capacity.Used = r.activeCountLocked()
	if c.Capacity.Maximum <= 0 {
		c.Capacity.Maximum = 1
	}
	return c
}

// Handler returns the authenticated loopback runner protocol handler. The
// handler itself does not bind a socket; ListenAndServe enforces loopback.
func (r *Runner) Handler() http.Handler {
	return http.HandlerFunc(r.serveHTTP)
}

// ListenAndServe starts the runner on the configured loopback address and
// stops when ctx is cancelled.
func (r *Runner) ListenAndServe(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !isLoopbackListenAddress(r.cfg.Listen) {
		return fmt.Errorf("runner listen address %q is not loopback-only", r.cfg.Listen)
	}
	listener, err := net.Listen("tcp", r.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen for runner: %w", err)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = listener.Close()
		return errors.New("runner is closed")
	}
	r.listener = listener
	r.mu.Unlock()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	server := &http.Server{Handler: r.Handler()}
	err = server.Serve(listener)
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

// Close gracefully drains active adapters, waits for them to exit, closes the
// listener, and releases the singleton state lock. An adapter interrupted by
// this shutdown remains resumable; an explicit Cancel request is still
// terminal.
func (r *Runner) Close() error {
	r.mu.Lock()
	if r.closed {
		done := r.closeDone
		r.mu.Unlock()
		<-done
		r.mu.Lock()
		err := r.closeErr
		r.mu.Unlock()
		return err
	}
	r.closed = true
	for attemptID, cancel := range r.running {
		r.shutdown[attemptID] = struct{}{}
		cancel()
	}
	listener := r.listener
	r.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	r.wg.Wait()
	closeErr := r.lock.Close()
	r.mu.Lock()
	r.closeErr = closeErr
	close(r.closeDone)
	r.mu.Unlock()
	return closeErr
}

// Start accepts an approved attempt and returns once its receipt and accepted
// event have been durably persisted. Execution continues asynchronously.
func (r *Runner) Start(ctx context.Context, request StartRequest) (Receipt, error) {
	if err := validateProtocolVersion(request.ProtocolVersion); err != nil {
		return Receipt{}, protocolError(ErrorUnsupportedVersion, false, err.Error(), nil)
	}
	if err := validateStartRequest(request); err != nil {
		return Receipt{}, protocolError(ErrorInvalidRequest, false, err.Error(), nil)
	}
	fingerprint := fingerprintOf(request)
	opKey := operationKey("start", request.TaskID, request.AttemptID, request.AttemptEpoch, request.RequestID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Receipt{}, protocolError(ErrorUnavailable, true, "runner is closed", nil)
	}
	if prior, ok := r.state.Operations[opKey]; ok {
		if prior.Fingerprint != fingerprint {
			priorReceipt, _ := r.receiptForAttemptLocked(prior.AttemptID)
			return Receipt{}, protocolError(ErrorIdempotencyConflict, false, "request ID was already used with different content", &priorReceipt)
		}
		if attempt := r.state.Attempts[prior.AttemptID]; attempt != nil && attempt.Receipt.Phase == PhaseAccepted && !attempt.CancelPending {
			if err := r.startExecutionLocked(prior.AttemptID, attempt.Start.Instructions, attempt.Start.Model); err != nil {
				return Receipt{}, protocolError(ErrorUnavailable, true, "persist starting attempt: "+err.Error(), &attempt.Receipt)
			}
		}
		return r.receiptForAttemptLocked(prior.AttemptID)
	}
	if err := r.checkEpochLocked(request.TaskID, request.AttemptID, request.AttemptEpoch); err != nil {
		return Receipt{}, err
	}
	if existing, ok := r.state.Attempts[request.AttemptID]; ok {
		if existing.Fingerprint != fingerprint || existing.Receipt.TaskID != request.TaskID || existing.Receipt.AttemptEpoch != request.AttemptEpoch {
			return Receipt{}, protocolError(ErrorIdempotencyConflict, false, "attempt ID is already bound to different content", &existing.Receipt)
		}
		return existing.Receipt, nil
	}
	if err := r.checkStartRequirementsLocked(request); err != nil {
		return Receipt{}, err
	}
	if err := r.reserveResourcesLocked(request.AttemptID, request.Resources); err != nil {
		return Receipt{}, err
	}
	workdir, err := prepareWorkspace(ctx, r.cfg, request)
	if err != nil {
		r.releaseResourcesLocked(request.AttemptID, request.Resources)
		return Receipt{}, protocolError(ErrorUnavailable, true, err.Error(), nil)
	}
	now := eventNow()
	receipt := Receipt{
		ProtocolVersion: ProtocolVersion,
		TaskID:          request.TaskID,
		AttemptID:       request.AttemptID,
		AttemptEpoch:    request.AttemptEpoch,
		Phase:           PhaseAccepted,
		Workdir:         workdir,
		Resources:       resourceNames(request.Resources),
		AcceptedAt:      now,
		UpdatedAt:       now,
	}
	r.state.Attempts[request.AttemptID] = &attemptRecord{
		Receipt:       receipt,
		Start:         cloneStartRequest(request),
		Fingerprint:   fingerprint,
		Resources:     resourceNames(request.Resources),
		ArtifactSpecs: append([]ArtifactSpec(nil), request.Artifacts...),
	}
	r.state.Operations[opKey] = operationRecord{Fingerprint: fingerprint, AttemptID: request.AttemptID}
	if _, err := r.appendEventLocked(request.AttemptID, EventAccepted, "attempt accepted", nil); err != nil {
		delete(r.state.Operations, opKey)
		delete(r.state.Attempts, request.AttemptID)
		r.releaseResourcesLocked(request.AttemptID, request.Resources)
		return Receipt{}, err
	}
	if err := r.persistLocked(); err != nil {
		return Receipt{}, protocolError(ErrorUnavailable, true, "persist accepted attempt: "+err.Error(), nil)
	}
	if err := r.startExecutionLocked(request.AttemptID, request.Instructions, request.Model); err != nil {
		return Receipt{}, protocolError(ErrorUnavailable, true, "persist starting attempt: "+err.Error(), &r.state.Attempts[request.AttemptID].Receipt)
	}
	return receiptForResponse(r.state.Attempts[request.AttemptID].Receipt), nil
}

// Inspect returns the durable current receipt.
func (r *Runner) Inspect(_ context.Context, attemptID string) (Receipt, error) {
	if err := validateIdentifier("attemptID", attemptID); err != nil {
		return Receipt{}, protocolError(ErrorInvalidRequest, false, err.Error(), nil)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	attempt, ok := r.state.Attempts[attemptID]
	if !ok {
		return Receipt{}, protocolError(ErrorUnavailable, false, "attempt not found", nil)
	}
	return receiptForResponse(attempt.Receipt), nil
}

// Cancel requests adapter cancellation. It returns while the attempt is in
// cancelling; only adapter exit transitions it to cancelled.
func (r *Runner) Cancel(_ context.Context, request CancelRequest) (Receipt, error) {
	if err := validateMutationIdentity(request.TaskID, request.AttemptID, request.AttemptEpoch, request.RequestID); err != nil {
		return Receipt{}, protocolError(ErrorInvalidRequest, false, err.Error(), nil)
	}
	if request.ProtocolVersion != ProtocolVersion {
		return Receipt{}, protocolError(ErrorUnsupportedVersion, false, "protocolVersion must be runner/v1", nil)
	}
	fingerprint := fingerprintOf(request)
	opKey := operationKey("cancel", request.TaskID, request.AttemptID, request.AttemptEpoch, request.RequestID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Receipt{}, protocolError(ErrorUnavailable, true, "runner is closed", nil)
	}
	if prior, ok := r.state.Operations[opKey]; ok {
		if prior.Fingerprint != fingerprint {
			priorReceipt, _ := r.receiptForAttemptLocked(prior.AttemptID)
			return Receipt{}, protocolError(ErrorIdempotencyConflict, false, "request ID was already used with different content", &priorReceipt)
		}
		return r.receiptForAttemptLocked(prior.AttemptID)
	}
	attempt, err := r.attemptForMutationLocked(request.TaskID, request.AttemptID, request.AttemptEpoch)
	if err != nil {
		return Receipt{}, err
	}
	r.state.Operations[opKey] = operationRecord{Fingerprint: fingerprint, AttemptID: request.AttemptID}
	if attempt.Receipt.Phase.IsTerminal() {
		if err := r.persistLocked(); err != nil {
			return Receipt{}, protocolError(ErrorUnavailable, true, err.Error(), &attempt.Receipt)
		}
		return receiptForResponse(attempt.Receipt), nil
	}
	attempt.CancelPending = true
	attempt.Receipt.Phase = PhaseCancelling
	attempt.Receipt.Blocker = "cancellation requested; waiting for harness exit"
	attempt.Receipt.UpdatedAt = eventNow()
	if _, err := r.appendEventLocked(request.AttemptID, EventProgress, "cancellation requested", nil); err != nil {
		return Receipt{}, err
	}
	if err := r.persistLocked(); err != nil {
		return Receipt{}, protocolError(ErrorUnavailable, true, err.Error(), &attempt.Receipt)
	}
	if cancel := r.running[request.AttemptID]; cancel != nil {
		cancel()
	} else {
		// A recovered attempt has no child to wait for. It is safe to acknowledge
		// cancellation immediately while retaining the worktree and session.
		r.finishLocked(attempt, PhaseCancelled, "cancelled after restart; no active harness process", nil)
	}
	return receiptForResponse(attempt.Receipt), nil
}

// Resume continues a needs-input attempt with its existing session and
// worktree. It never accepts a different instruction envelope or session ID.
func (r *Runner) Resume(ctx context.Context, request ResumeRequest) (Receipt, error) {
	if err := validateMutationIdentity(request.TaskID, request.AttemptID, request.AttemptEpoch, request.RequestID); err != nil {
		return Receipt{}, protocolError(ErrorInvalidRequest, false, err.Error(), nil)
	}
	if request.ProtocolVersion != ProtocolVersion {
		return Receipt{}, protocolError(ErrorUnsupportedVersion, false, "protocolVersion must be runner/v1", nil)
	}
	fingerprint := fingerprintOf(request)
	opKey := operationKey("resume", request.TaskID, request.AttemptID, request.AttemptEpoch, request.RequestID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Receipt{}, protocolError(ErrorUnavailable, true, "runner is closed", nil)
	}
	if prior, ok := r.state.Operations[opKey]; ok {
		if prior.Fingerprint != fingerprint {
			priorReceipt, _ := r.receiptForAttemptLocked(prior.AttemptID)
			return Receipt{}, protocolError(ErrorIdempotencyConflict, false, "request ID was already used with different content", &priorReceipt)
		}
		return r.receiptForAttemptLocked(prior.AttemptID)
	}
	attempt, err := r.attemptForMutationLocked(request.TaskID, request.AttemptID, request.AttemptEpoch)
	if err != nil {
		return Receipt{}, err
	}
	if attempt.Receipt.Phase != PhaseNeedsInput {
		if attempt.Receipt.Phase.IsTerminal() {
			return Receipt{}, protocolError(ErrorCheckpointUnavailable, false, "attempt is not resumable", &attempt.Receipt)
		}
		return Receipt{}, protocolError(ErrorBusy, true, "attempt is still executing", &attempt.Receipt)
	}
	if request.SessionID == "" || request.SessionID != attempt.Receipt.SessionID {
		return Receipt{}, protocolError(ErrorCheckpointUnavailable, false, "resume session does not match the attempt", &attempt.Receipt)
	}
	if len(request.ApprovedInput) > 0 && !equivalentJSON(request.ApprovedInput, attempt.Start.ApprovedInput) {
		return Receipt{}, protocolError(ErrorForbidden, false, "resume cannot amend approved input", &attempt.Receipt)
	}
	if request.Instructions != "" && request.Instructions != attempt.Start.Instructions {
		return Receipt{}, protocolError(ErrorForbidden, false, "resume cannot amend approved instructions", &attempt.Receipt)
	}
	if strings.TrimSpace(request.Resolution) == "" {
		return Receipt{}, protocolError(ErrorInvalidRequest, false, "resume requires an explicit resolution", &attempt.Receipt)
	}
	if attempt.Receipt.SessionID == "" {
		return Receipt{}, protocolError(ErrorCheckpointUnavailable, false, "attempt has no recoverable session", &attempt.Receipt)
	}
	if err := verifyWorkspace(ctx, r.cfg, attempt.Start, attempt.Receipt.Workdir); err != nil {
		return Receipt{}, protocolError(ErrorCheckpointUnavailable, false, err.Error(), &attempt.Receipt)
	}
	if err := r.checkStartRequirementsLocked(attempt.Start); err != nil {
		return Receipt{}, err
	}
	if err := r.reserveResourcesLocked(request.AttemptID, resourceRequests(attempt.Resources)); err != nil {
		return Receipt{}, err
	}
	r.state.Operations[opKey] = operationRecord{Fingerprint: fingerprint, AttemptID: request.AttemptID}
	attempt.CancelPending = false
	attempt.Receipt.Phase = PhaseAccepted
	attempt.Receipt.Blocker = ""
	attempt.Receipt.LastError = nil
	attempt.Receipt.UpdatedAt = eventNow()
	if _, err := r.appendEventLocked(request.AttemptID, EventAccepted, "resume accepted", nil); err != nil {
		return Receipt{}, err
	}
	if err := r.persistLocked(); err != nil {
		return Receipt{}, protocolError(ErrorUnavailable, true, err.Error(), &attempt.Receipt)
	}
	instructions := attempt.Start.Instructions + "\n\nResolution:\n" + request.Resolution
	if err := r.startExecutionLocked(request.AttemptID, instructions, attempt.Start.Model); err != nil {
		return Receipt{}, protocolError(ErrorUnavailable, true, "persist starting resume: "+err.Error(), &attempt.Receipt)
	}
	return receiptForResponse(attempt.Receipt), nil
}

func (r *Runner) checkStartRequirementsLocked(request StartRequest) error {
	if !r.capabilities.Ready {
		return protocolError(ErrorUnavailable, true, "runner is not ready: "+strings.Join(r.capabilities.Reasons, "; "), nil)
	}
	if request.Limits.MaxTurns > 1 {
		return protocolError(ErrorUnsupportedCapability, false, "runner supports at most one harness turn per execution", nil)
	}
	for _, required := range request.RequiredCapabilities {
		if !contains(r.capabilities.Verification, required) && !contains(r.cfg.Environment, required) && !contains(r.cfg.Toolchains, required) {
			return protocolError(ErrorUnsupportedCapability, false, "required capability is unavailable: "+required, nil)
		}
	}
	for _, required := range request.RequiredToolchains {
		if !contains(r.cfg.Toolchains, required) {
			return protocolError(ErrorUnsupportedCapability, false, "required toolchain is unavailable: "+required, nil)
		}
	}
	for _, required := range request.RequiredEnvironment {
		if !contains(r.cfg.Environment, required) {
			return protocolError(ErrorUnsupportedCapability, false, "required environment is unavailable: "+required, nil)
		}
	}
	if request.RequiredHarness != "" {
		found := false
		for _, h := range r.capabilities.Harnesses {
			if h.Name == request.RequiredHarness && (request.RequiredHarnessVersion == "" || h.Version == request.RequiredHarnessVersion) && h.Ready {
				found = true
				break
			}
		}
		if !found {
			return protocolError(ErrorUnsupportedCapability, false, "required harness is unavailable", nil)
		}
	}
	for _, resource := range request.Resources {
		if _, ok := r.cfg.Resources[resource.Name]; !ok {
			return protocolError(ErrorUnsupportedCapability, false, "resource is not configured: "+resource.Name, nil)
		}
	}
	if r.activeCountLocked() >= r.cfg.MaximumCapacity {
		return protocolError(ErrorBusy, true, "runner execution capacity is full", nil)
	}
	return nil
}

func (r *Runner) checkEpochLocked(taskID, attemptID string, epoch uint64) error {
	var highest uint64
	for id, attempt := range r.state.Attempts {
		if attempt.Receipt.TaskID != taskID || id == attemptID {
			continue
		}
		if attempt.Receipt.AttemptEpoch > highest {
			highest = attempt.Receipt.AttemptEpoch
		}
	}
	if highest != 0 && epoch <= highest {
		for _, attempt := range r.state.Attempts {
			if attempt.Receipt.TaskID == taskID && attempt.Receipt.AttemptEpoch == highest {
				return protocolError(ErrorStaleAttempt, false, "attempt epoch is obsolete", &attempt.Receipt)
			}
		}
	}
	return nil
}

func (r *Runner) attemptForMutationLocked(taskID, attemptID string, epoch uint64) (*attemptRecord, error) {
	attempt, ok := r.state.Attempts[attemptID]
	if !ok {
		return nil, protocolError(ErrorUnavailable, false, "attempt not found", nil)
	}
	if attempt.Receipt.TaskID != taskID || attempt.Receipt.AttemptEpoch != epoch {
		return nil, protocolError(ErrorStaleAttempt, false, "attempt identity or epoch is obsolete", &attempt.Receipt)
	}
	return attempt, nil
}

func (r *Runner) reserveResourcesLocked(attemptID string, requested []ResourceRequest) error {
	for _, request := range requested {
		resource, ok := r.cfg.Resources[request.Name]
		if !ok {
			return protocolError(ErrorUnsupportedCapability, false, "resource is not configured: "+request.Name, nil)
		}
		used := 0
		for key, reservation := range r.state.Reservations {
			if reservation.AttemptID == attemptID {
				continue
			}
			separator := strings.LastIndexByte(key, '/')
			if separator > 0 && key[:separator] == request.Name {
				used++
			}
		}
		if used >= resource.Capacity {
			return protocolError(ErrorBusy, true, "resource is already reserved: "+request.Name, nil)
		}
	}
	for _, request := range requested {
		r.state.Reservations[request.Name+"/"+attemptID] = reservationRecord{AttemptID: attemptID}
	}
	return nil
}

func (r *Runner) releaseResourcesLocked(attemptID string, requested []ResourceRequest) {
	for _, request := range requested {
		delete(r.state.Reservations, request.Name+"/"+attemptID)
	}
}

func (r *Runner) startExecutionLocked(attemptID, instructions, model string) error {
	if _, running := r.running[attemptID]; running {
		return nil
	}
	attempt, ok := r.state.Attempts[attemptID]
	if !ok {
		return errors.New("attempt not found")
	}
	previousPhase := attempt.Receipt.Phase
	previousBlocker := attempt.Receipt.Blocker
	previousUpdatedAt := attempt.Receipt.UpdatedAt
	execCtx := context.Background()
	var cancel context.CancelFunc
	if attempt.Start.Limits.MaxDurationSeconds > 0 {
		execCtx, cancel = context.WithTimeout(execCtx, time.Duration(attempt.Start.Limits.MaxDurationSeconds)*time.Second)
	} else {
		execCtx, cancel = context.WithCancel(execCtx)
	}
	r.running[attemptID] = cancel
	attempt.Receipt.Phase = PhaseStarting
	attempt.Receipt.Blocker = ""
	attempt.Receipt.UpdatedAt = eventNow()
	if err := r.persistLocked(); err != nil {
		cancel()
		delete(r.running, attemptID)
		attempt.Receipt.Phase = previousPhase
		attempt.Receipt.Blocker = previousBlocker
		attempt.Receipt.UpdatedAt = previousUpdatedAt
		return err
	}
	launch := harness.Launch{AttemptID: attemptID, Workdir: attempt.Receipt.Workdir, SessionID: attempt.Receipt.SessionID, Instructions: instructions, Model: model}
	r.wg.Add(1)
	go r.execute(execCtx, launch, attemptID)
	return nil
}

func (r *Runner) execute(ctx context.Context, launch harness.Launch, attemptID string) {
	defer r.wg.Done()
	gate := newExecutionGate()
	result, runErr := r.adapter.Run(ctx, launch, func(event harness.Event) error {
		if !gate.begin() {
			return errors.New("harness event arrived after adapter execution ended")
		}
		defer gate.end()
		return r.handleHarnessEvent(attemptID, event)
	})
	gate.close()
	r.mu.Lock()
	attempt, ok := r.state.Attempts[attemptID]
	if !ok {
		delete(r.shutdown, attemptID)
		delete(r.running, attemptID)
		r.mu.Unlock()
		return
	}
	_, shutdown := r.shutdown[attemptID]
	if attempt.Receipt.LastError != nil {
		blocker := attempt.Receipt.Blocker
		if blocker == "" {
			blocker = attempt.Receipt.LastError.Message
		}
		delete(r.running, attemptID)
		delete(r.shutdown, attemptID)
		r.finishLocked(attempt, PhaseFailed, blocker, nil)
		r.mu.Unlock()
		return
	}
	if attempt.Receipt.SessionID == "" && result.SessionID != "" {
		attempt.Receipt.SessionID = result.SessionID
	}
	if result.SessionID != "" && result.SessionID != attempt.Receipt.SessionID {
		delete(r.running, attemptID)
		delete(r.shutdown, attemptID)
		r.finishLocked(attempt, PhaseFailed, "harness returned a foreign session ID", errors.New("foreign session ID"))
		r.mu.Unlock()
		return
	}
	if attempt.LimitExceeded || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		blocker := attempt.Receipt.Blocker
		if blocker == "" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			blocker = "harness exceeded the approved execution duration"
		}
		delete(r.running, attemptID)
		delete(r.shutdown, attemptID)
		r.finishLocked(attempt, PhaseFailed, blocker, nil)
		r.mu.Unlock()
		return
	}
	if attempt.CancelPending {
		delete(r.running, attemptID)
		delete(r.shutdown, attemptID)
		r.finishLocked(attempt, PhaseCancelled, legacyShutdownCancellationBlocker, nil)
		r.mu.Unlock()
		return
	}
	// Close cancels the adapter context so the child process can be drained.
	// That cancellation is a resumable interruption, unlike an explicit
	// Cancel request. Only map a context cancellation to needs_input here; if
	// an adapter completed or failed before shutdown won the reconciliation
	// race, its terminal result below remains authoritative.
	resultPhase := Phase(result.Phase)
	shutdownInterrupted := (resultPhase == PhaseCancelled || resultPhase == PhaseNeedsInput || resultPhase == "") && (runErr == nil || errors.Is(runErr, context.Canceled))
	if shutdown && errors.Is(ctx.Err(), context.Canceled) && shutdownInterrupted {
		delete(r.running, attemptID)
		delete(r.shutdown, attemptID)
		r.finishLocked(attempt, PhaseNeedsInput, shutdownBlocker, nil)
		r.mu.Unlock()
		return
	}
	if runErr != nil {
		delete(r.running, attemptID)
		delete(r.shutdown, attemptID)
		r.finishLocked(attempt, PhaseFailed, runErr.Error(), runErr)
		r.mu.Unlock()
		return
	}
	switch Phase(result.Phase) {
	case PhaseCompleted:
		if !attempt.Start.ExportGitResult {
			delete(r.running, attemptID)
			delete(r.shutdown, attemptID)
			r.finishLocked(attempt, PhaseCompleted, "", nil)
			r.mu.Unlock()
			return
		}
		request := cloneStartRequest(attempt.Start)
		workdir := attempt.Receipt.Workdir
		r.mu.Unlock()
		gitResult, exportErr := exportGitResult(ctx, r.cfg, request, workdir)
		r.mu.Lock()
		attempt, ok = r.state.Attempts[attemptID]
		if !ok {
			cleanupGitResultArtifacts(gitResult)
			delete(r.running, attemptID)
			delete(r.shutdown, attemptID)
			r.mu.Unlock()
			return
		}
		_, shutdown = r.shutdown[attemptID]
		if attempt.CancelPending {
			cleanupGitResultArtifacts(gitResult)
			delete(r.running, attemptID)
			delete(r.shutdown, attemptID)
			r.finishLocked(attempt, PhaseCancelled, legacyShutdownCancellationBlocker, nil)
			r.mu.Unlock()
			return
		}
		if shutdown && errors.Is(ctx.Err(), context.Canceled) {
			cleanupGitResultArtifacts(gitResult)
			delete(r.running, attemptID)
			delete(r.shutdown, attemptID)
			r.finishLocked(attempt, PhaseNeedsInput, shutdownBlocker, nil)
			r.mu.Unlock()
			return
		}
		if exportErr != nil {
			cleanupGitResultArtifacts(gitResult)
			delete(r.running, attemptID)
			delete(r.shutdown, attemptID)
			message := "git result export failed: " + exportErr.Error()
			r.finishLocked(attempt, PhaseFailed, message, errors.New(message))
			r.mu.Unlock()
			return
		}
		delete(r.running, attemptID)
		delete(r.shutdown, attemptID)
		if err := r.finishGitResultLocked(attempt, gitResult); err != nil {
			cleanupGitResultArtifacts(gitResult)
			// The durable state transaction failed before a completed receipt
			// could be committed. Keep the attempt terminally failed so callers
			// never observe completion without its immutable result artifacts.
			message := "persist git result export: " + err.Error()
			r.finishLocked(attempt, PhaseFailed, message, errors.New(message))
		}
		r.mu.Unlock()
	case PhaseCancelled:
		delete(r.running, attemptID)
		delete(r.shutdown, attemptID)
		r.finishLocked(attempt, PhaseCancelled, "harness cancelled", nil)
		r.mu.Unlock()
	case PhaseNeedsInput:
		delete(r.running, attemptID)
		delete(r.shutdown, attemptID)
		r.finishLocked(attempt, PhaseNeedsInput, result.Blocker, nil)
		r.mu.Unlock()
	case PhaseFailed:
		delete(r.running, attemptID)
		delete(r.shutdown, attemptID)
		r.finishLocked(attempt, PhaseFailed, result.Blocker, nil)
		r.mu.Unlock()
	default:
		delete(r.running, attemptID)
		delete(r.shutdown, attemptID)
		r.finishLocked(attempt, PhaseFailed, "harness returned no terminal phase", nil)
		r.mu.Unlock()
	}
}

func (r *Runner) handleHarnessEvent(attemptID string, event harness.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	attempt, ok := r.state.Attempts[attemptID]
	if !ok {
		return errors.New("attempt no longer exists")
	}
	if attempt.Receipt.LastError != nil {
		return firstHarnessFailure(attempt)
	}
	if attempt.Receipt.Phase.IsTerminal() {
		return fmt.Errorf("harness event arrived after attempt entered terminal phase %s", attempt.Receipt.Phase)
	}
	fail := func(err error) error {
		return r.failHarnessEventLocked(attemptID, attempt, err)
	}
	if event.SessionID != "" {
		if attempt.Receipt.SessionID != "" && event.SessionID != attempt.Receipt.SessionID {
			return fail(errors.New("harness event returned a foreign session ID"))
		}
		if attempt.Receipt.SessionID == "" {
			// The adapter's session event is the first durable identity of a new
			// Codex thread. Persisting it in the same journal transaction makes
			// it available before any turn event and avoids passing a fabricated
			// resume ID to a fresh harness session.
			attempt.Receipt.SessionID = event.SessionID
		}
	}
	if event.Data != nil && len(event.Data) > r.cfg.MaxEventBytes {
		return fail(fmt.Errorf("harness event exceeds %d-byte limit", r.cfg.MaxEventBytes))
	}
	if event.Message != "" && len(event.Message) > maxMessageBytes {
		event.Message = event.Message[:maxMessageBytes]
	}
	eventBytes := int64(len(event.Message) + len(event.Data))
	if limit := attempt.Start.Limits.MaxOutputBytes; limit > 0 && attempt.OutputBytes+eventBytes > int64(limit) {
		attempt.LimitExceeded = true
		return fail(errors.New("harness output limit exceeded"))
	}
	attempt.OutputBytes += eventBytes
	if event.Type == "" {
		event.Type = EventProgress
	}
	protocolType := event.Type
	switch event.Type {
	case "session", "turn_started", "turn_completed":
		protocolType = EventProgress
	case EventAccepted, EventStarted, EventProgress, EventCheckpoint, EventNeedsInput, EventArtifact, EventCompleted, EventFailed, EventCancelled:
	default:
		protocolType = EventProgress
	}
	if _, err := r.appendEventLocked(attemptID, protocolType, event.Message, event.Data); err != nil {
		return fail(err)
	}
	switch protocolType {
	case EventStarted, EventProgress, EventCheckpoint:
		if attempt.Receipt.Phase != PhaseCancelling {
			attempt.Receipt.Phase = PhaseRunning
		}
	case EventNeedsInput:
		attempt.Receipt.Phase = PhaseNeedsInput
		attempt.Receipt.Blocker = event.Message
	case EventCompleted, EventFailed:
		// The adapter's terminal result is authoritative. Recording the event
		// before Run returns must not make cancellation look complete.
		if attempt.Receipt.Phase != PhaseCancelling {
			attempt.Receipt.Phase = PhaseRunning
		}
	case EventCancelled:
		// Cancellation is terminal only after Adapter.Run returns.
		attempt.Receipt.Phase = PhaseCancelling
	}
	attempt.Receipt.UpdatedAt = eventNow()
	if protocolType == EventArtifact {
		if err := r.recordArtifactLocked(attempt, event.Data); err != nil {
			return fail(err)
		}
	}
	if err := r.persistLocked(); err != nil {
		return fail(err)
	}
	return nil
}

func (r *Runner) failHarnessEventLocked(attemptID string, attempt *attemptRecord, failure error) error {
	if failure == nil {
		return nil
	}
	if attempt.Receipt.LastError != nil {
		return firstHarnessFailure(attempt)
	}
	message := failure.Error()
	if message == "" {
		message = "harness event handling failed"
	}
	attempt.CancelPending = true
	attempt.Receipt.Phase = PhaseCancelling
	attempt.Receipt.Blocker = message
	attempt.Receipt.LastError = &Error{Code: ErrorUnavailable, Retryable: false, Message: message}
	attempt.Receipt.UpdatedAt = eventNow()
	_, appendErr := r.appendEventLocked(attemptID, EventProgress, message, nil)
	persistErr := r.persistLocked()
	if cancel := r.running[attemptID]; cancel != nil {
		cancel()
	}
	if appendErr != nil || persistErr != nil {
		return errors.Join(failure, appendErr, persistErr)
	}
	return failure
}

func firstHarnessFailure(attempt *attemptRecord) error {
	if attempt.Receipt.LastError != nil && attempt.Receipt.LastError.Message != "" {
		return errors.New(attempt.Receipt.LastError.Message)
	}
	return errors.New("harness event handling failed")
}

func (r *Runner) recordArtifactLocked(attempt *attemptRecord, data json.RawMessage) error {
	var input struct {
		Name      string `json:"name"`
		Path      string `json:"path"`
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
	}
	if len(data) == 0 || json.Unmarshal(data, &input) != nil {
		return errors.New("artifact event requires a JSON name/path payload")
	}
	if attempt.Start.ExportGitResult && isGitResultArtifactName(input.Name) {
		return errors.New("artifact name is reserved for the runner Git result export")
	}
	var spec *ArtifactSpec
	for i := range attempt.ArtifactSpecs {
		if attempt.ArtifactSpecs[i].Name == input.Name {
			spec = &attempt.ArtifactSpecs[i]
			break
		}
	}
	if spec == nil {
		return errors.New("artifact name is not in the approved allowlist")
	}
	for _, prior := range attempt.Receipt.Artifacts {
		if prior.Name == input.Name {
			return errors.New("artifact name has already been recorded and is immutable")
		}
	}
	if spec.Path != "" && spec.Path != input.Path {
		return errors.New("artifact path differs from the approved allowlist")
	}
	if input.MediaType == "" {
		input.MediaType = spec.MediaType
	}
	if input.MediaType == "" {
		input.MediaType = "application/octet-stream"
	}
	source, err := artifactSource(attempt.Receipt.Workdir, input.Path)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("artifact source is not a regular file")
		}
		return err
	}
	if info.Size() > r.cfg.MaxArtifactSize {
		return fmt.Errorf("artifact exceeds %d-byte limit", r.cfg.MaxArtifactSize)
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	artifactDir := filepath.Join(r.cfg.StateDir, "artifacts", attempt.Receipt.AttemptID)
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return err
	}
	artifactID := newArtifactID(input.Name)
	dest := filepath.Join(artifactDir, artifactID+".bin")
	tmp, err := os.CreateTemp(artifactDir, ".artifact-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(f, r.cfg.MaxArtifactSize+1))
	if err != nil {
		return err
	}
	if written > r.cfg.MaxArtifactSize {
		return fmt.Errorf("artifact exceeds %d-byte limit", r.cfg.MaxArtifactSize)
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if spec.Digest != "" && spec.Digest != digest {
		return errors.New("artifact digest differs from the approved allowlist")
	}
	if input.Digest != "" && input.Digest != digest {
		return errors.New("artifact event digest does not match bytes")
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	artifact := Artifact{ID: artifactID, Name: input.Name, Digest: digest, Length: written, MediaType: input.MediaType, CreatedAt: eventNow()}
	r.state.Artifacts[artifactID] = artifactRecord{Artifact: artifact, Path: dest}
	attempt.Receipt.Artifacts = append(attempt.Receipt.Artifacts, artifact)
	return nil
}

func (r *Runner) finishLocked(attempt *attemptRecord, phase Phase, blocker string, lastErr error) {
	if attempt.Receipt.Phase.IsTerminal() && phase != PhaseCancelling {
		return
	}
	attempt.Receipt.Phase = phase
	attempt.Receipt.Blocker = blocker
	attempt.Receipt.UpdatedAt = eventNow()
	if lastErr != nil && attempt.Receipt.LastError == nil {
		attempt.Receipt.LastError = &Error{Code: ErrorUnavailable, Retryable: false, Message: lastErr.Error()}
	}
	eventType := EventProgress
	switch phase {
	case PhaseCompleted:
		eventType = EventCompleted
	case PhaseFailed:
		eventType = EventFailed
	case PhaseCancelled:
		eventType = EventCancelled
	case PhaseNeedsInput:
		eventType = EventNeedsInput
	}
	alreadyRecorded := false
	if events := r.state.Events[attempt.Receipt.AttemptID]; len(events) > 0 {
		last := events[len(events)-1]
		alreadyRecorded = last.Type == eventType && phase == PhaseNeedsInput
	}
	if !alreadyRecorded {
		_, _ = r.appendEventLocked(attempt.Receipt.AttemptID, eventType, blocker, nil)
	}
	r.releaseResourcesLocked(attempt.Receipt.AttemptID, resourceRequests(attempt.Resources))
	_ = r.persistLocked()
}

func (r *Runner) appendEventLocked(attemptID, eventType, message string, data json.RawMessage) (Event, error) {
	attempt, ok := r.state.Attempts[attemptID]
	if !ok {
		return Event{}, errors.New("attempt not found")
	}
	if len(data) > r.cfg.MaxEventBytes {
		return Event{}, fmt.Errorf("event data exceeds %d-byte limit", r.cfg.MaxEventBytes)
	}
	events := r.state.Events[attemptID]
	var cursor uint64
	if len(events) > 0 {
		cursor = events[len(events)-1].Cursor
	}
	event := Event{Cursor: cursor + 1, Timestamp: eventNow(), AttemptEpoch: attempt.Receipt.AttemptEpoch, Type: eventType, Message: boundedMessage(message), Data: cloneRaw(data)}
	events = append(events, event)
	if len(events) > r.cfg.MaxEvents {
		events = events[len(events)-r.cfg.MaxEvents:]
	}
	r.state.Events[attemptID] = events
	attempt.Receipt.Cursor = event.Cursor
	attempt.Receipt.UpdatedAt = event.Timestamp
	for subscriber := range r.subscribers[attemptID] {
		select {
		case subscriber <- event:
		default:
		}
	}
	return event, nil
}

func (r *Runner) recoverLocked() error {
	changed := false
	for attemptID, attempt := range r.state.Attempts {
		if attempt == nil {
			delete(r.state.Attempts, attemptID)
			changed = true
			continue
		}
		if _, migrated := r.state.migratedLegacyShutdown[attemptID]; migrated {
			delete(r.state.migratedLegacyShutdown, attemptID)
			if _, err := r.appendEventLocked(attemptID, EventNeedsInput, attempt.Receipt.Blocker, nil); err != nil {
				return err
			}
			changed = true
			continue
		}
		if attempt.Receipt.Phase.IsTerminal() || attempt.Receipt.Phase == PhaseNeedsInput {
			continue
		}
		attempt.Receipt.Phase = PhaseNeedsInput
		attempt.Receipt.Blocker = "runner restarted; reconcile the existing harness session before resuming"
		attempt.Receipt.UpdatedAt = eventNow()
		if _, err := r.appendEventLocked(attemptID, EventNeedsInput, attempt.Receipt.Blocker, nil); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		return nil
	}
	return nil
}

func (r *Runner) persistLocked() error { return r.store.save(r.state) }

func (r *Runner) receiptForAttemptLocked(attemptID string) (Receipt, error) {
	attempt, ok := r.state.Attempts[attemptID]
	if !ok {
		return Receipt{}, protocolError(ErrorUnavailable, false, "attempt not found", nil)
	}
	return receiptForResponse(attempt.Receipt), nil
}

func (r *Runner) activeCountLocked() int {
	count := 0
	for _, attempt := range r.state.Attempts {
		if !attempt.Receipt.Phase.IsTerminal() && attempt.Receipt.Phase != PhaseNeedsInput {
			count++
		}
	}
	return count
}

func validateStartRequest(request StartRequest) error {
	if err := validateProtocolVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if err := validateMutationIdentity(request.TaskID, request.AttemptID, request.AttemptEpoch, request.RequestID); err != nil {
		return err
	}
	if strings.TrimSpace(request.RepositoryID) == "" || strings.TrimSpace(request.BaseCommit) == "" {
		return errors.New("repositoryID and baseCommit are required")
	}
	if strings.TrimSpace(request.Instructions) == "" {
		return errors.New("instructions are required")
	}
	if len(request.ApprovedInput) == 0 {
		return errors.New("approvedInput provenance envelope is required")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(request.ApprovedInput, &envelope); err != nil || len(envelope) == 0 {
		return errors.New("approvedInput must be a nonempty JSON object")
	}
	if !nonEmptyJSON(envelope["provenance"]) && !nonEmptyJSON(envelope["manualAuthorization"]) && !nonEmptyJSON(envelope["authorization"]) {
		return errors.New("approvedInput must include provenance or manualAuthorization")
	}
	for _, resource := range request.Resources {
		if err := validateIdentifier("resource name", resource.Name); err != nil {
			return err
		}
	}
	resourceNamesSeen := map[string]struct{}{}
	for _, resource := range request.Resources {
		if _, ok := resourceNamesSeen[resource.Name]; ok {
			return errors.New("resource reservations contain duplicate names")
		}
		resourceNamesSeen[resource.Name] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, artifact := range request.Artifacts {
		if err := validateIdentifier("artifact name", artifact.Name); err != nil {
			return err
		}
		if request.ExportGitResult && isGitResultArtifactName(artifact.Name) {
			return errors.New("artifact name is reserved for the runner Git result export")
		}
		if _, ok := seen[artifact.Name]; ok {
			return errors.New("artifact allowlist contains duplicate names")
		}
		seen[artifact.Name] = struct{}{}
		if artifact.Path == "" {
			return errors.New("artifact allowlist requires an approved relative path")
		}
		if err := validateArtifactRelativePath(artifact.Path); err != nil {
			return err
		}
	}
	if request.Limits.MaxDurationSeconds < 0 || request.Limits.MaxOutputBytes < 0 || request.Limits.MaxTurns < 0 {
		return errors.New("execution limits cannot be negative")
	}
	return nil
}

func nonEmptyJSON(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var value any
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func equivalentJSON(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return fingerprintOf(leftValue) == fingerprintOf(rightValue)
}

func validateMutationIdentity(taskID, attemptID string, epoch uint64, requestID string) error {
	if err := validateIdentifier("taskID", taskID); err != nil {
		return err
	}
	if err := validateIdentifier("attemptID", attemptID); err != nil {
		return err
	}
	if epoch == 0 {
		return errors.New("attemptEpoch is required")
	}
	return validateIdentifier("requestID", requestID)
}

func validateProtocolVersion(value string) error {
	if value != ProtocolVersion {
		return errors.New("protocolVersion must be runner/v1")
	}
	return nil
}

func validateIdentifier(name, value string) error {
	if value == "" || len(value) > maxIdentifierBytes || !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}

func protocolError(code string, retryable bool, message string, receipt *Receipt) *Error {
	var copied *Receipt
	if receipt != nil {
		c := receiptForResponse(*receipt)
		copied = &c
	}
	return &Error{Code: code, Retryable: retryable, Message: message, Receipt: copied}
}

func operationKey(kind, taskID, attemptID string, epoch uint64, requestID string) string {
	return kind + "\x00" + taskID + "\x00" + attemptID + "\x00" + fmt.Sprint(epoch) + "\x00" + requestID
}

func fingerprintOf(value any) string {
	b, _ := json.Marshal(value)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

func cloneStartRequest(request StartRequest) StartRequest {
	request.ApprovedInput = cloneRaw(request.ApprovedInput)
	request.RequiredCapabilities = append([]string(nil), request.RequiredCapabilities...)
	request.RequiredToolchains = append([]string(nil), request.RequiredToolchains...)
	request.RequiredEnvironment = append([]string(nil), request.RequiredEnvironment...)
	request.Verification.Names = append([]string(nil), request.Verification.Names...)
	request.Verification.Commands = append([]string(nil), request.Verification.Commands...)
	request.Resources = append([]ResourceRequest(nil), request.Resources...)
	request.Artifacts = append([]ArtifactSpec(nil), request.Artifacts...)
	return request
}

func receiptForResponse(receipt Receipt) Receipt {
	receipt.Artifacts = append([]Artifact(nil), receipt.Artifacts...)
	receipt.Resources = append([]string(nil), receipt.Resources...)
	if receipt.LastError != nil {
		lastErr := *receipt.LastError
		receipt.LastError = &lastErr
	}
	return receipt
}

func boundedMessage(value string) string {
	if len(value) <= maxMessageBytes {
		return value
	}
	return value[:maxMessageBytes]
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	if contains(values, value) {
		return values
	}
	return append(values, value)
}

func resourceNames(requests []ResourceRequest) []string {
	result := make([]string, 0, len(requests))
	for _, request := range requests {
		result = append(result, request.Name)
	}
	sort.Strings(result)
	return result
}

func resourceRequests(names []string) []ResourceRequest {
	result := make([]ResourceRequest, 0, len(names))
	for _, name := range names {
		result = append(result, ResourceRequest{Name: name})
	}
	return result
}

func newArtifactID(name string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "artifact-" + name + "-" + fmt.Sprint(time.Now().UnixNano())
	}
	return "artifact-" + name + "-" + hex.EncodeToString(raw[:])
}
