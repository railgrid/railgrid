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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	persistentExecAgentBodyLimit = 2 << 20
	persistentExecRetryAttempts  = 3
)

var executionGroupResource = schema.GroupResource{Group: "infrastructure.kedge.faros.sh", Resource: "executions"}

type execCoordinatorRequest struct {
	Action         ExecAction `json:"action"`
	SessionID      string     `json:"sessionID"`
	RequestID      string     `json:"requestID"`
	CallerKey      string     `json:"callerKey"`
	Fingerprint    string     `json:"fingerprint,omitempty"`
	Argv           []string   `json:"argv,omitempty"`
	WorkDir        string     `json:"workDir,omitempty"`
	TimeoutMS      int        `json:"timeoutMs,omitempty"`
	MaxOutputBytes int        `json:"maxOutputBytes,omitempty"`
	SourceRevision uint64     `json:"sourceRevision,omitempty"`
	SourceDigest   string     `json:"sourceDigest,omitempty"`
}

// PersistentExecutor is intentionally stateless. The component coordinator
// owns idempotency, sessions, cancellation, and durable results on its private
// platform-state PVC; any provider replica can forward any lifecycle action.
type PersistentExecutor struct {
	runtime Runtime
}

func NewPersistentExecutor(runtime Runtime) (*PersistentExecutor, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime is required for persistent exec")
	}
	return &PersistentExecutor{runtime: runtime}, nil
}

func (e *PersistentExecutor) Start(ctx context.Context, call ExecCall) (ExecResult, error) {
	if err := validatePersistentExecCall(call); err != nil {
		return ExecResult{}, err
	}
	fingerprint, err := persistentExecCallFingerprint(call)
	if err != nil {
		return ExecResult{}, err
	}
	limits, _ := limitsForCapability(call.Capability)
	request := execCoordinatorRequest{
		Action: ExecActionStart, SessionID: execSessionID(call), RequestID: call.Request.RequestID,
		CallerKey: call.CallerKey, Fingerprint: fingerprint, Argv: call.Request.Argv,
		WorkDir: call.Request.Workdir, TimeoutMS: int(execTimeoutSeconds(call)) * 1000,
		MaxOutputBytes: limits.outputBytes, SourceRevision: call.Request.SourceRevision,
		SourceDigest: call.Request.SourceDigest,
	}
	return e.execute(ctx, call.ControlTarget, request)
}

func (e *PersistentExecutor) Poll(ctx context.Context, call ExecCall) (ExecResult, error) {
	return e.lifecycle(ctx, call, ExecActionPoll)
}

func (e *PersistentExecutor) Cancel(ctx context.Context, call ExecCall) (ExecResult, error) {
	return e.lifecycle(ctx, call, ExecActionCancel)
}

func (e *PersistentExecutor) lifecycle(ctx context.Context, call ExecCall, action ExecAction) (ExecResult, error) {
	if err := validatePersistentExecTarget(call); err != nil {
		return ExecResult{}, err
	}
	return e.execute(ctx, call.ControlTarget, execCoordinatorRequest{
		Action: action, SessionID: strings.TrimSpace(call.Request.SessionID),
		RequestID: strings.TrimSpace(call.Request.RequestID), CallerKey: call.CallerKey,
	})
}

func (e *PersistentExecutor) execute(ctx context.Context, target ResolvedTarget, input execCoordinatorRequest) (ExecResult, error) {
	transport, err := e.runtime.Transport()
	if err != nil {
		return ExecResult{}, fmt.Errorf("runtime transport unavailable: %w", err)
	}
	base, err := url.Parse(e.runtime.Host())
	if err != nil || base.Scheme == "" || base.Host == "" {
		return ExecResult{}, fmt.Errorf("invalid runtime host: %v", err)
	}
	token, err := e.runtime.ControlToken(ctx, target.TokenSecretNamespace, target.TokenSecretName)
	if err != nil {
		return ExecResult{}, fmt.Errorf("control token unavailable: %w", err)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return ExecResult{}, fmt.Errorf("encode exec coordinator request: %w", err)
	}
	client := &http.Client{Transport: transport}
	var lastErr error
	for attempt := 0; attempt < persistentExecRetryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ExecResult{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * 50 * time.Millisecond):
			}
		}
		requestURL := *base
		requestURL.Path = serviceProxyPath(target, "")
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), strings.NewReader(string(payload)))
		if err != nil {
			return ExecResult{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(controlTokenHeader, token)
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("exec coordinator request: %w", err)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, persistentExecAgentBodyLimit))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("read exec coordinator response: %w", readErr)
			continue
		}
		if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout {
			lastErr = fmt.Errorf("exec coordinator temporarily unavailable: %s", strings.TrimSpace(string(body)))
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return ExecResult{}, execCoordinatorStatusError(resp.StatusCode, input.SessionID, strings.TrimSpace(string(body)))
		}
		var result ExecResult
		if err := json.Unmarshal(body, &result); err != nil {
			lastErr = fmt.Errorf("decode exec coordinator response: %w", err)
			continue
		}
		return result, nil
	}
	return ExecResult{}, lastErr
}

func execCoordinatorStatusError(status int, sessionID, message string) error {
	if len(message) > 2048 {
		message = message[:2048] + "..."
	}
	cause := fmt.Errorf("exec coordinator: %s", message)
	switch status {
	case http.StatusForbidden, http.StatusUnauthorized:
		return apierrors.NewForbidden(executionGroupResource, sessionID, cause)
	case http.StatusNotFound:
		return apierrors.NewNotFound(executionGroupResource, sessionID)
	case http.StatusConflict:
		return apierrors.NewConflict(executionGroupResource, sessionID, cause)
	default:
		return cause
	}
}

func validatePersistentExecTarget(call ExecCall) error {
	if strings.TrimSpace(call.CallerKey) == "" {
		return fmt.Errorf("persistent executor caller binding is required")
	}
	if call.ControlTarget.ServiceName == "" || call.ControlTarget.ServicePort == "" {
		return fmt.Errorf("persistent executor control Service is unavailable")
	}
	if call.ControlTarget.TokenSecretNamespace == "" || call.ControlTarget.TokenSecretName == "" {
		return fmt.Errorf("persistent executor control token Secret is unavailable")
	}
	return nil
}

func validatePersistentExecCall(call ExecCall) error {
	if err := validatePersistentExecTarget(call); err != nil {
		return err
	}
	if call.Request.Action != ExecActionStart || strings.TrimSpace(call.IdempotencyKey) == "" {
		return fmt.Errorf("persistent executor start requires a start request and idempotency key")
	}
	if strings.TrimSpace(call.RuntimeNamespace) == "" {
		return fmt.Errorf("persistent executor runtime namespace is required")
	}
	if !path.IsAbs(call.WorkingDir) || path.Clean(call.WorkingDir) == "/" {
		return fmt.Errorf("persistent executor working directory must be an absolute non-root path")
	}
	if call.Request.SourceRevision == 0 {
		return fmt.Errorf("sourceRevision is required for persistent execution")
	}
	digest := strings.TrimPrefix(strings.TrimSpace(call.Request.SourceDigest), "sha256:")
	if len(digest) != sha256.Size*2 {
		return fmt.Errorf("sourceDigest must be a SHA-256 hex digest")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("sourceDigest must be a SHA-256 hex digest")
	}
	return nil
}

func persistentExecCallFingerprint(call ExecCall) (string, error) {
	payload, err := json.Marshal(struct {
		Namespace, Resource, Name, Component, WorkingDir, WorkspacePath, Digest string
		Revision                                                                uint64
		Argv                                                                    []string
		Workdir                                                                 string
		Timeout                                                                 int32
	}{call.RuntimeNamespace, call.Resource, call.Name, call.Component, call.WorkingDir, call.WorkspacePath, call.Request.SourceDigest, call.Request.SourceRevision, call.Request.Argv, call.Request.Workdir, call.Request.TimeoutSeconds})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func execSessionID(call ExecCall) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		call.CallerKey, call.Workspace, call.Resource, call.Name, call.Component, call.IdempotencyKey,
	}, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func execTimeoutSeconds(call ExecCall) int32 {
	if call.Request.TimeoutSeconds > 0 {
		return call.Request.TimeoutSeconds
	}
	limits, _ := limitsForCapability(call.Capability)
	return limits.timeoutSeconds
}

func execTerminalState(state string) bool {
	switch state {
	case "succeeded", "failed", "canceled", "timed_out":
		return true
	default:
		return false
	}
}
