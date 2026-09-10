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

package dataplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// componentStatusBodyLimit bounds the dev agent /status response read while
	// resolving the applied source revision.
	componentStatusBodyLimit = 64 << 10
	// componentStatusTimeout bounds that one internal hop so a wedged agent
	// cannot hold the exec request open.
	componentStatusTimeout = 15 * time.Second
)

// Poll cadence and wait budget for action "run". They are variables only so
// tests can shorten them.
var (
	execRunPollInitial = 250 * time.Millisecond
	execRunPollMax     = time.Second
	// execRunWaitBudget is how long run polls before handing the still-running
	// session back to the caller: the effective command timeout plus slack for
	// queueing and coordinator bookkeeping, capped below the ~100s idle
	// timeout common to fronting proxies (e.g. Cloudflare's 524) so a long
	// command degrades to "poll it" instead of a gateway error.
	execRunWaitBudget = func(timeoutSeconds int32) time.Duration {
		return min(time.Duration(timeoutSeconds)*time.Second+10*time.Second, execRunMaxWait)
	}
)

// execRunMaxWait caps a single synchronous run request.
const execRunMaxWait = 90 * time.Second

// errNoAppliedSource reports that the component has no current applied source
// revision to default an exec to.
var errNoAppliedSource = errors.New("component reports no applied source revision")

// newExecIdempotencyKey mints the random 128-bit key used when a run caller
// omits Idempotency-Key.
func newExecIdempotencyKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate idempotency key: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// runExec starts the command and polls it with backoff until it reaches a
// terminal state or the wait budget expires. On expiry it returns the last
// observed (non-terminal) result, which always carries the sessionID, so the
// caller can continue with action "poll". Leaving the HTTP request (ctx
// cancellation) never cancels the command; it keeps running in the
// coordinator and remains pollable.
func runExec(ctx context.Context, executor Executor, call ExecCall) (ExecResult, error) {
	startCall := call
	startCall.Request.Action = ExecActionStart
	result, err := executor.Start(ctx, startCall)
	if err != nil {
		return ExecResult{}, err
	}
	sessionID := result.SessionID
	if execTerminalState(result.State) || sessionID == "" {
		return result, nil
	}
	pollCall := call
	pollCall.Request = ExecRequest{Action: ExecActionPoll, SessionID: sessionID, RequestID: call.Request.RequestID}
	deadline := time.Now().Add(execRunWaitBudget(execTimeoutSeconds(call)))
	delay := execRunPollInitial
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return result, nil
		}
		timer := time.NewTimer(min(delay, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ExecResult{}, ctx.Err()
		case <-timer.C:
		}
		polled, err := executor.Poll(ctx, pollCall)
		if err != nil {
			return ExecResult{}, fmt.Errorf("exec session %s was started but polling it failed (poll it again with action %q): %w", sessionID, ExecActionPoll, err)
		}
		if polled.SessionID == "" {
			polled.SessionID = sessionID
		}
		result = polled
		if execTerminalState(result.State) {
			return result, nil
		}
		delay = min(2*delay, execRunPollMax)
	}
}

// fetchComponentSource reads the source revision/digest the live dev agent
// reports as applied and verified (GET /status on its control Service). A
// zero revision or empty digest is reported as errNoAppliedSource: no sync has
// been applied yet, a dependency reload is still pending, or the managed files
// no longer match the manifest.
func fetchComponentSource(ctx context.Context, rt Runtime, target ResolvedTarget) (uint64, string, error) {
	transport, err := rt.Transport()
	if err != nil {
		return 0, "", fmt.Errorf("runtime transport unavailable: %w", err)
	}
	base, err := url.Parse(rt.Host())
	if err != nil || base.Scheme == "" || base.Host == "" {
		return 0, "", fmt.Errorf("invalid runtime host: %v", err)
	}
	var token string
	if target.TokenSecretName != "" {
		token, err = rt.ControlToken(ctx, target.TokenSecretNamespace, target.TokenSecretName)
		if err != nil {
			return 0, "", fmt.Errorf("control token unavailable: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, componentStatusTimeout)
	defer cancel()
	requestURL := *base
	requestURL.Path = serviceProxyPath(target, "")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return 0, "", err
	}
	if token != "" {
		req.Header.Set(controlTokenHeader, token)
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("component status request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, componentStatusBodyLimit))
	if err != nil {
		return 0, "", fmt.Errorf("read component status: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(body))
		if len(message) > 512 {
			message = message[:512] + "..."
		}
		return 0, "", fmt.Errorf("component status returned %d: %s", resp.StatusCode, message)
	}
	var status struct {
		SourceRevision uint64 `json:"sourceRevision"`
		SourceDigest   string `json:"sourceDigest"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return 0, "", fmt.Errorf("decode component status: %w", err)
	}
	digest := strings.TrimSpace(status.SourceDigest)
	if status.SourceRevision == 0 || digest == "" {
		return 0, "", errNoAppliedSource
	}
	return status.SourceRevision, digest, nil
}
