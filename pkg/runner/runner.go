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
	"unicode/utf8"

	"github.com/railgrid/railgrid/pkg/runner/harness"
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

	mu          sync.Mutex
	running     map[string]context.CancelFunc
	subscribers map[string]map[chan Event]struct{}
	// credentials holds each live attempt's harness identity, by attempt ID.
	// It is deliberately NOT part of persistedState: a credential belongs to
	// the caller, not to this machine, so it lives only as long as the process
	// and a resume after a restart must present it again.
	credentials map[string]harness.Credential
	// permissions holds each live attempt's permission gate, by attempt ID.
	// Like credentials it is in-process only: it is the waiting state of a
	// running child, and a restart takes the child with it.
	permissions  map[string]*permissionGate
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
	verificationCapabilities = appendUnique(verificationCapabilities, clarificationCapability)
	verificationCapabilities = appendUnique(verificationCapabilities, permissionPromptCapability)
	verificationCapabilities = appendUnique(verificationCapabilities, "cancel-unseen-v1")
	// The runner fetches for itself now: it either refreshes the enrolled
	// checkout or maintains its own clone of a remote it is given, so the
	// capability no longer depends on an enrollment naming a remote.
	verificationCapabilities = appendUnique(verificationCapabilities, gitFetchCapability)
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
		credentials:  map[string]harness.Credential{},
		permissions:  map[string]*permissionGate{},
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
	// The clone source and the harness credential are dispatch data and are
	// deliberately removed here, before anything durable is derived from the
	// request: both hold a credential, and a retry that mints a fresh one must
	// remain the same request rather than an idempotency conflict.
	dispatched := request.Repository
	request.Repository = nil
	credential, err := harnessCredentialOf(request.HarnessCredential)
	if err != nil {
		return Receipt{}, protocolError(ErrorInvalidRequest, false, err.Error(), nil)
	}
	request.HarnessCredential = nil
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
			// A retry carries a freshly minted credential for the same
			// request. Take it: the old one may already have expired, and
			// the request is identical by fingerprint precisely because the
			// credential was stripped before fingerprinting.
			r.credentials[prior.AttemptID] = credential
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
	workdir, err := prepareWorkspace(ctx, r.cfg, request, dispatched)
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
	r.credentials[request.AttemptID] = credential
	r.state.Operations[opKey] = operationRecord{Fingerprint: fingerprint, AttemptID: request.AttemptID}
	if _, err := r.appendEventLocked(request.AttemptID, EventAccepted, "attempt accepted", nil); err != nil {
		delete(r.state.Operations, opKey)
		delete(r.state.Attempts, request.AttemptID)
		delete(r.credentials, request.AttemptID)
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

// Cancel requests adapter cancellation. Active attempts remain cancelling until
// adapter exit. Unseen attempts receive a durable cancellation fence before
// acknowledgement, so a delayed Start cannot launch them.
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
	if _, exists := r.state.Attempts[request.AttemptID]; !exists {
		return r.cancelUnseenLocked(request, opKey, fingerprint)
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
	// Dispatch data, stripped before fingerprinting for the same reason as on
	// Start. A resume may be the first request after a runner restart, so it
	// carries the credential rather than expecting one to have survived here.
	credential, err := harnessCredentialOf(request.HarnessCredential)
	if err != nil {
		return Receipt{}, protocolError(ErrorInvalidRequest, false, err.Error(), nil)
	}
	request.HarnessCredential = nil
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
	if request.ClarificationID != "" {
		if !validClarificationID(request.ClarificationID) {
			return Receipt{}, protocolError(ErrorInvalidRequest, false, "clarificationID is invalid", &attempt.Receipt)
		}
	}
	// A permission verdict is answered here and RETURNS here: it is handed to a
	// tool call that is still open on a child that never stopped, so the same
	// turn continues. Nothing below this block applies to it — there is no new
	// launch, no instructions to amend and no workspace to re-verify, because
	// the attempt never stopped running in the first place.
	if receipt, handled, err := r.resumePermissionLocked(attempt, request, opKey, fingerprint); handled {
		return receipt, err
	}
	if outstanding := attempt.Receipt.Clarification; outstanding != nil {
		if request.ClarificationID == "" || request.ClarificationID != outstanding.ID {
			return Receipt{}, protocolError(ErrorCheckpointUnavailable, false, "resume clarification does not match the outstanding question", &attempt.Receipt)
		}
	} else if request.ClarificationID != "" {
		return Receipt{}, protocolError(ErrorCheckpointUnavailable, false, "attempt has no outstanding clarification", &attempt.Receipt)
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
	r.credentials[request.AttemptID] = credential
	attempt.CancelPending = false
	attempt.Receipt.Phase = PhaseAccepted
	attempt.Receipt.Blocker = ""
	attempt.Receipt.Clarification = nil
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

// resumePermissionLocked handles a resume that carries a verdict on an
// outstanding permission request, and reports whether it did.
//
// handled is false only when the resume names no permission AND the attempt is
// not parked on one — the ordinary clarification-or-plain resume, which the
// caller goes on to process. Every other combination is decided here, including
// the mismatches, because answering the wrong request is exactly what the id is
// there to prevent.
func (r *Runner) resumePermissionLocked(attempt *attemptRecord, request ResumeRequest, opKey, fingerprint string) (Receipt, bool, error) {
	outstanding := attempt.Receipt.Permission
	if request.PermissionID == "" && outstanding == nil {
		if request.PermissionDecision != "" {
			return Receipt{}, true, protocolError(ErrorInvalidRequest, false, "permissionDecision requires permissionID", &attempt.Receipt)
		}
		return Receipt{}, false, nil
	}
	if outstanding == nil {
		return Receipt{}, true, protocolError(ErrorCheckpointUnavailable, false, "attempt has no outstanding permission request", &attempt.Receipt)
	}
	if request.PermissionID == "" {
		return Receipt{}, true, protocolError(ErrorCheckpointUnavailable, false, "resume must answer the outstanding permission request", &attempt.Receipt)
	}
	if !validClarificationID(request.PermissionID) {
		return Receipt{}, true, protocolError(ErrorInvalidRequest, false, "permissionID is invalid", &attempt.Receipt)
	}
	// The fence. A verdict for another request is not a verdict for this one,
	// however plausible it looks: the human who gave it was shown a different
	// call.
	if request.PermissionID != outstanding.ID {
		return Receipt{}, true, protocolError(ErrorCheckpointUnavailable, false, "resume permission does not match the outstanding request", &attempt.Receipt)
	}
	if request.ClarificationID != "" {
		return Receipt{}, true, protocolError(ErrorCheckpointUnavailable, false, "attempt has no outstanding clarification", &attempt.Receipt)
	}
	var verdict harness.PermissionVerdict
	switch request.PermissionDecision {
	case PermissionAllow:
		verdict.Allow = true
	case PermissionDeny:
		verdict.Allow = false
	default:
		return Receipt{}, true, protocolError(ErrorInvalidRequest, false, "permissionDecision must be allow or deny", &attempt.Receipt)
	}
	// Resolution is the human's own words and the only thing they say that
	// reaches the model. It is optional: a bare verdict is a complete answer.
	verdict.Message = boundedMessage(strings.TrimSpace(request.Resolution))
	if !verdict.Allow && verdict.Message == "" {
		verdict.Message = "A human denied this tool call. Do not retry it; explain what you would have done, or take another route."
	}
	if err := r.answerPermissionLocked(attempt, request.PermissionID, verdict); err != nil {
		return Receipt{}, true, protocolError(ErrorCheckpointUnavailable, false, err.Error(), &attempt.Receipt)
	}
	// Recorded only once the verdict is delivered, so a replay of the same
	// request ID returns this receipt instead of being refused as a mismatch.
	r.state.Operations[opKey] = operationRecord{Fingerprint: fingerprint, AttemptID: request.AttemptID}
	if err := r.persistLocked(); err != nil {
		return Receipt{}, true, protocolError(ErrorUnavailable, true, err.Error(), &attempt.Receipt)
	}
	return receiptForResponse(attempt.Receipt), true, nil
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
			offered := make([]string, 0, len(r.capabilities.Harnesses))
			for _, h := range r.capabilities.Harnesses {
				state := "ready"
				if !h.Ready {
					state = "not ready"
				}
				offered = append(offered, h.Name+" "+h.Version+" ("+state+")")
			}
			want := request.RequiredHarness
			if request.RequiredHarnessVersion != "" {
				want += " " + request.RequiredHarnessVersion
			}
			return protocolError(ErrorUnsupportedCapability, false, "required harness "+want+" is unavailable; this runner offers "+strings.Join(offered, ", "), nil)
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
	// The credential is in memory only. Absent means this process never
	// received one for this attempt — a restart, almost always — and there is
	// nothing to run as. Say so instead of launching a harness that would
	// authenticate as nobody.
	credential, ok := r.credentials[attemptID]
	if !ok || credential.Empty() {
		return errors.New("attempt has no harness credential in this process; resume it with one")
	}
	previousPhase := attempt.Receipt.Phase
	previousBlocker := attempt.Receipt.Blocker
	previousUpdatedAt := attempt.Receipt.UpdatedAt
	// The duration limit is enforced by a workClock rather than by
	// context.WithTimeout, because an attempt parked on a permission prompt is
	// waiting on a human, not working. A wall-clock deadline would kill a turn
	// for taking an hour that it spent doing nothing but waiting to be allowed
	// to continue. With no limit the clock is nil and cancellation is the only
	// way the context ends, exactly as before.
	execCtx, cancel := context.WithCancel(context.Background())
	clock := newWorkClock(time.Duration(attempt.Start.Limits.MaxDurationSeconds)*time.Second, cancel)
	r.running[attemptID] = cancel
	r.permissions[attemptID] = &permissionGate{clock: clock}
	attempt.Receipt.Phase = PhaseStarting
	attempt.Receipt.Blocker = ""
	attempt.Receipt.UpdatedAt = eventNow()
	if err := r.persistLocked(); err != nil {
		cancel()
		clock.stop()
		delete(r.running, attemptID)
		delete(r.permissions, attemptID)
		attempt.Receipt.Phase = previousPhase
		attempt.Receipt.Blocker = previousBlocker
		attempt.Receipt.UpdatedAt = previousUpdatedAt
		return err
	}
	// Which session this turn continues, in precedence order: the session this
	// ATTEMPT is already in (a resume), then the session the START asked to
	// continue (the previous turn of the same conversation). The harness may
	// answer with a different session id when it forks one, and the caller
	// learns the real one from the receipt.
	sessionID := attempt.Receipt.SessionID
	if sessionID == "" {
		sessionID = attempt.Start.SessionID
	}
	launch := harness.Launch{
		AttemptID:    attemptID,
		Workdir:      attempt.Receipt.Workdir,
		SessionID:    sessionID,
		Instructions: instructions,
		Model:        model,
		Credential:   credential,
	}
	if attempt.Start.AskPermission {
		// The adapter reaches a human through the runner, which is the only
		// thing here that knows what a human is. Left nil — the default, and
		// what every coordinator that has not opted in gets — an adapter keeps
		// its harness from prompting at all.
		launch.Permissions = &attemptPermissions{runner: r, attemptID: attemptID}
	}
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
	// The permission gate and its working clock belong to this execution. Take
	// them down first: whatever the result turns out to be, the child is gone
	// and nothing can be waiting on a verdict any more. Reading the clock here
	// is also the only place that can still tell a duration overrun apart from
	// any other cancellation, because the clock is what cancelled the context.
	durationExceeded := false
	if permissions := r.permissions[attemptID]; permissions != nil {
		durationExceeded = permissions.clock.expired()
		permissions.clock.stop()
		permissions.close()
		delete(r.permissions, attemptID)
	}
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
	if result.Clarification != nil {
		if Phase(result.Phase) != PhaseNeedsInput || !validHarnessClarification(result.Clarification) {
			// A clarification is meaningful only as the result of a genuine
			// needs-input interaction. Invalid or misplaced adapter data must not
			// turn an auth, approval, error, or terminal blocker into a product
			// question.
			result.Clarification = nil
		}
	}
	if attempt.LimitExceeded || durationExceeded {
		blocker := attempt.Receipt.Blocker
		if blocker == "" && durationExceeded {
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
		if result.Clarification != nil {
			attempt.Receipt.Clarification = clarificationFromHarness(result.Clarification)
		}
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
	if event.Clarification != nil && isClarificationEvent(event.Type) && validHarnessClarification(event.Clarification) {
		attempt.Receipt.Clarification = clarificationFromHarness(event.Clarification)
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
	if phase != PhaseNeedsInput {
		attempt.Receipt.Clarification = nil
	}
	// A permission park is only ever live: it exists because a child process is
	// blocked on a tool call, and finishLocked is called when that process is
	// gone. Leaving one on the receipt would advertise a verdict nobody could
	// deliver.
	attempt.Receipt.Permission = nil
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
	if phase.IsTerminal() || phase == PhaseNeedsInput {
		// The caller's identity is kept only while a turn is actually
		// executing. A terminal attempt will never launch again, and a parked
		// one is revived by a resume that carries the credential itself — so
		// holding it here would only widen the window in which somebody else's
		// credential sits in this process's memory.
		delete(r.credentials, attempt.Receipt.AttemptID)
	}
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
		// A permission park is the live state of a child process this runner no
		// longer has. Nothing in the new process can deliver a verdict into it,
		// so the request is dropped and the attempt is left waiting on an
		// ordinary resume, which relaunches the harness.
		if attempt.Receipt.Permission != nil {
			attempt.Receipt.Permission = nil
			attempt.Receipt.Blocker = "runner restarted while the harness was waiting on a permission verdict; resume to continue the session"
			attempt.Receipt.UpdatedAt = eventNow()
			changed = true
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
	for attemptID, attempt := range r.state.Attempts {
		if attempt.Receipt.Phase.IsTerminal() {
			continue
		}
		// A needs_input attempt ordinarily holds nothing: its harness turn
		// ended and the resume launches a new one. An attempt parked on a
		// PERMISSION prompt is the exception — its child is alive and blocked,
		// so it is still occupying the machine and must still count against
		// capacity.
		if attempt.Receipt.Phase == PhaseNeedsInput && r.permissions[attemptID] == nil {
			continue
		}
		count++
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
	// One of the two attempt shapes, never both. A workspace attempt has no
	// commit to verify and a repository attempt has no directory to keep, so
	// accepting both together would leave it ambiguous which one the runner is
	// meant to guarantee.
	workspaceID := strings.TrimSpace(request.WorkspaceID)
	repositoryID := strings.TrimSpace(request.RepositoryID)
	switch {
	case workspaceID != "" && repositoryID != "":
		return errors.New("workspaceID and repositoryID are mutually exclusive")
	case workspaceID != "":
		if !identifierPattern.MatchString(workspaceID) {
			return errors.New("workspaceID is not a valid identifier")
		}
		if strings.TrimSpace(request.BaseCommit) != "" {
			return errors.New("a workspace attempt cannot pin a base commit")
		}
		if request.Repository != nil {
			return errors.New("a workspace attempt cannot name a repository source")
		}
		if request.ExportGitResult {
			return errors.New("a workspace attempt cannot export a Git result")
		}
	default:
		if repositoryID == "" || strings.TrimSpace(request.BaseCommit) == "" {
			return errors.New("repositoryID and baseCommit are required unless workspaceID is set")
		}
		if !identifierPattern.MatchString(repositoryID) {
			return errors.New("repositoryID is not a valid identifier")
		}
		if request.Repository != nil {
			if _, err := validateCloneSource(*request.Repository); err != nil {
				return err
			}
		}
	}
	if err := validateCommitMessage(request.CommitMessage); err != nil {
		return err
	}
	if session := strings.TrimSpace(request.SessionID); session != "" && !identifierPattern.MatchString(session) {
		return errors.New("sessionID is not a valid identifier")
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

// maxHarnessCredentialBytes bounds a credential value. A Codex login session
// is the largest shape, and it is a small JSON document.
const maxHarnessCredentialBytes = 64 << 10

// harnessCredentialOf validates the caller's harness identity and converts it
// to the adapter-facing type.
//
// It is required on every start and resume. A runner that would fall back to
// something on the host — an environment variable, a file somebody left in the
// harness home — would be a shared billable identity, which is exactly what
// per-attempt dispatch exists to prevent. There is no fallback.
func harnessCredentialOf(value *HarnessCredential) (harness.Credential, error) {
	if value == nil {
		return harness.Credential{}, errors.New("harnessCredential is required")
	}
	kind := harness.CredentialKind(strings.TrimSpace(value.Kind))
	switch kind {
	case harness.CredentialClaudeOAuth, harness.CredentialClaudeAPIKey, harness.CredentialCodexAuth:
	default:
		return harness.Credential{}, fmt.Errorf("harnessCredential kind %q is not supported", value.Kind)
	}
	raw := strings.TrimSpace(value.Value)
	if raw == "" {
		return harness.Credential{}, errors.New("harnessCredential value is empty")
	}
	if len(raw) > maxHarnessCredentialBytes {
		return harness.Credential{}, errors.New("harnessCredential value is oversized")
	}
	// A NUL cannot survive either channel the adapters use (an environment
	// value or a JSON file), and a value carrying one would be silently
	// truncated into something that authenticates as nobody. Newlines are
	// legal inside a Codex session file and refused for the env-injected
	// kinds, where they would truncate the variable.
	if strings.ContainsRune(raw, 0) {
		return harness.Credential{}, errors.New("harnessCredential value contains a NUL")
	}
	if kind != harness.CredentialCodexAuth && strings.ContainsAny(raw, "\r\n") {
		return harness.Credential{}, errors.New("harnessCredential value contains invalid whitespace")
	}
	return harness.Credential{Kind: kind, Value: raw}, nil
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
	// Belt and braces on a security property: the clone is what becomes
	// durable state, and neither credential may ever reach it. Both callers
	// already strip them, and this makes a future one that forgets harmless.
	request.Repository = nil
	request.HarnessCredential = nil
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
	receipt.Clarification = cloneClarification(receipt.Clarification)
	receipt.Permission = clonePermission(receipt.Permission)
	if receipt.LastError != nil {
		lastErr := *receipt.LastError
		receipt.LastError = &lastErr
	}
	return receipt
}

func cloneClarification(value *Clarification) *Clarification {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validHarnessClarification(value *harness.Clarification) bool {
	if value == nil || !validClarificationID(value.ID) {
		return false
	}
	return utf8.ValidString(value.Text) && strings.TrimSpace(value.Text) != "" && len(value.Text) <= maxMessageBytes
}

func validClarificationID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxIdentifierBytes && utf8.ValidString(value) && !strings.ContainsAny(value, " \t\r\n")
}

func isClarificationEvent(eventType string) bool {
	return eventType == EventNeedsInput || eventType == "item/tool/requestUserInput"
}

func clarificationFromHarness(value *harness.Clarification) *Clarification {
	if value == nil {
		return nil
	}
	return &Clarification{ID: value.ID, Text: value.Text}
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
