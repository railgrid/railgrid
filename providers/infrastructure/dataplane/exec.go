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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// ExecAction is the lifecycle operation requested by an exec call. Commands
// are intentionally asynchronous: the provider dispatches them to the live
// component agent without holding an HTTP request open for the whole workload.
type ExecAction string

const (
	ExecActionStart  ExecAction = "start"
	ExecActionPoll   ExecAction = "poll"
	ExecActionCancel ExecAction = "cancel"
)

const (
	// These are provider ceilings. A TemplateDataPlaneExec may lower each
	// value, but can never raise it.
	ExecDefaultTimeoutSeconds = 120
	ExecMaxTimeoutSeconds     = 120
	ExecDefaultOutputBytes    = 256 << 10
	ExecMaxOutputBytes        = 256 << 10

	execMaxBodyBytes       = 512 << 10
	execMaxArgv            = 64
	execMaxArgBytes        = 4096
	execMaxWorkdirBytes    = 256
	execMaxDigestBytes     = 128
	execMaxIdentifierBytes = 128
)

// ExecRequest is the JSON body accepted by the component /exec route. Start
// carries the durable source revision/digest applied by /sync; the persistent
// executor runs against that live component workspace. Poll and cancel carry
// only a session ID.
type ExecRequest struct {
	Action         ExecAction `json:"action"`
	SessionID      string     `json:"sessionID,omitempty"`
	RequestID      string     `json:"requestID,omitempty"`
	SourceRevision uint64     `json:"sourceRevision,omitempty"`
	Argv           []string   `json:"argv,omitempty"`
	Workdir        string     `json:"workdir,omitempty"`
	TimeoutSeconds int32      `json:"timeoutSeconds,omitempty"`
	SourceDigest   string     `json:"sourceDigest,omitempty"`
}

// ExecResult is the bounded response returned by an Executor. Output is
// truncated by the data-plane handler to the declared capability ceiling.
type ExecResult struct {
	SessionID string `json:"sessionID,omitempty"`
	RequestID string `json:"requestID,omitempty"`
	State     string `json:"state"`
	ExitCode  *int32 `json:"exitCode,omitempty"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ExecCall is the executor-facing request. It includes the already-authorized
// instance and the provider-owned contract capability, but never includes the
// caller bearer token or the runtime control token.
type ExecCall struct {
	Workspace  string
	Resource   string
	Name       string
	Component  string
	Instance   *unstructured.Unstructured
	Capability *infrav1alpha1.TemplateDataPlaneExec
	// WorkingDir is the absolute working directory from the matching
	// development component (defaulting to /workspace).
	WorkingDir string
	// WorkspacePath is the project-relative component prefix used when the
	// caller computed SourceDigest. It comes from the platform-owned Template.
	WorkspacePath string
	// CallerKey is a one-way digest of the forwarded caller token. It binds
	// poll/cancel to the principal that started the session without exposing a
	// runtime credential to the live component agent.
	CallerKey string
	// RuntimeNamespace is read from the instance status at the contract's
	// RuntimeNamespacePath. It is provider-resolved and never request supplied.
	RuntimeNamespace string
	// ControlTarget is the live development component's control Service. It is
	// resolved from the platform-owned instance status and contract; callers
	// cannot select or override it. PersistentExecutor uses it for /exec.
	ControlTarget  ResolvedTarget
	Request        ExecRequest
	IdempotencyKey string
}

func execCallerKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}

// ExecAuthorization is the explicit policy hook called after the caller has
// successfully GET-authorized the instance and before execution starts. An
// implementation may enforce tenant/user policy, quotas, approvals, or an
// allowlist that is intentionally outside the Template contract.
type ExecAuthorization struct {
	Workspace string
	Resource  string
	Name      string
	Component string
	User      string
	// CallerToken is the bearer used for the instance GET and is supplied only
	// to the policy hook, never to an Executor or runtime request.
	CallerToken string
	Action      ExecAction
	Request     ExecRequest
	Instance    *unstructured.Unstructured
}

// ExecAuthorizer is deliberately separate from Executor. The executor owns
// runtime mechanics; the authorizer owns caller/policy decisions.
type ExecAuthorizer interface {
	AuthorizeExec(context.Context, ExecAuthorization) error
}

// Executor starts, polls, and cancels isolated command runs. Each method must
// honor ctx cancellation. Implementations deduplicate starts by
// ExecCall.IdempotencyKey and retain bounded runtime records across worker
// restarts so provider replicas remain interchangeable.
type Executor interface {
	Start(context.Context, ExecCall) (ExecResult, error)
	Poll(context.Context, ExecCall) (ExecResult, error)
	Cancel(context.Context, ExecCall) (ExecResult, error)
}

type execLimits struct {
	timeoutSeconds int32
	outputBytes    int
}

func limitsForCapability(capability *infrav1alpha1.TemplateDataPlaneExec) (execLimits, error) {
	limits := execLimits{
		timeoutSeconds: ExecDefaultTimeoutSeconds,
		outputBytes:    ExecDefaultOutputBytes,
	}
	if capability == nil {
		return limits, fmt.Errorf("component does not declare an exec capability")
	}
	if capability.MaxTimeoutSeconds > ExecMaxTimeoutSeconds || capability.MaxTimeoutSeconds < 0 {
		return limits, fmt.Errorf("exec maxTimeoutSeconds must be between 0 and %d", ExecMaxTimeoutSeconds)
	}
	if capability.MaxOutputBytes > ExecMaxOutputBytes || capability.MaxOutputBytes < 0 {
		return limits, fmt.Errorf("exec maxOutputBytes must be between 0 and %d", ExecMaxOutputBytes)
	}
	if capability.MaxTimeoutSeconds != 0 {
		limits.timeoutSeconds = capability.MaxTimeoutSeconds
	}
	if capability.MaxOutputBytes != 0 {
		limits.outputBytes = int(capability.MaxOutputBytes)
	}
	return limits, nil
}

func decodeExecRequest(w http.ResponseWriter, r *http.Request, capability *infrav1alpha1.TemplateDataPlaneExec) (ExecRequest, string, error) {
	limits, err := limitsForCapability(capability)
	if err != nil {
		return ExecRequest{}, "", err
	}
	r.Body = http.MaxBytesReader(w, r.Body, execMaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req ExecRequest
	if err := dec.Decode(&req); err != nil {
		return ExecRequest{}, "", fmt.Errorf("decode exec request: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return ExecRequest{}, "", fmt.Errorf("exec request must contain one JSON object")
		}
		return ExecRequest{}, "", fmt.Errorf("decode trailing exec request data: %w", err)
	}

	action := req.Action
	if action != ExecActionStart && action != ExecActionPoll && action != ExecActionCancel {
		return ExecRequest{}, "", fmt.Errorf("action must be %q, %q, or %q", ExecActionStart, ExecActionPoll, ExecActionCancel)
	}
	if len(req.SessionID) > execMaxIdentifierBytes || len(req.RequestID) > execMaxIdentifierBytes {
		return ExecRequest{}, "", fmt.Errorf("sessionID and requestID must be at most %d bytes", execMaxIdentifierBytes)
	}
	if strings.IndexByte(req.SessionID, 0) >= 0 || strings.IndexByte(req.RequestID, 0) >= 0 {
		return ExecRequest{}, "", fmt.Errorf("sessionID and requestID must not contain NUL")
	}
	if len(req.Workdir) > execMaxWorkdirBytes {
		return ExecRequest{}, "", fmt.Errorf("workdir must be at most %d bytes", execMaxWorkdirBytes)
	}
	if req.Workdir != "" {
		if _, err := normalizeExecWorkdir(req.Workdir); err != nil {
			return ExecRequest{}, "", fmt.Errorf("workdir: %w", err)
		}
	}
	if len(req.SourceDigest) > execMaxDigestBytes || strings.IndexByte(req.SourceDigest, 0) >= 0 {
		return ExecRequest{}, "", fmt.Errorf("sourceDigest must be at most %d bytes and contain no NUL", execMaxDigestBytes)
	}

	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > execMaxIdentifierBytes || strings.IndexByte(key, 0) >= 0 {
		return ExecRequest{}, "", fmt.Errorf("Idempotency-Key must be at most %d bytes and contain no NUL", execMaxIdentifierBytes)
	}
	if action == ExecActionStart {
		if key == "" {
			return ExecRequest{}, "", fmt.Errorf("Idempotency-Key is required for start")
		}
		if req.RequestID != "" && req.RequestID != key {
			return ExecRequest{}, "", fmt.Errorf("requestID must match Idempotency-Key")
		}
		req.RequestID = key
		if req.SessionID != "" {
			return ExecRequest{}, "", fmt.Errorf("sessionID is not accepted for start")
		}
		if len(req.Argv) == 0 || len(req.Argv) > execMaxArgv {
			return ExecRequest{}, "", fmt.Errorf("start argv must contain between 1 and %d arguments", execMaxArgv)
		}
		for i, arg := range req.Argv {
			if arg == "" || len(arg) > execMaxArgBytes || strings.IndexByte(arg, 0) >= 0 {
				return ExecRequest{}, "", fmt.Errorf("start argv[%d] must be non-empty, at most %d bytes, and contain no NUL", i, execMaxArgBytes)
			}
		}
		if req.SourceDigest == "" {
			return ExecRequest{}, "", fmt.Errorf("sourceDigest is required for start")
		}
		if req.SourceRevision == 0 {
			return ExecRequest{}, "", fmt.Errorf("sourceRevision is required for start")
		}
		if req.TimeoutSeconds < 0 || req.TimeoutSeconds > limits.timeoutSeconds {
			return ExecRequest{}, "", fmt.Errorf("timeoutSeconds must be between 0 and %d", limits.timeoutSeconds)
		}
	} else {
		if req.SessionID == "" {
			return ExecRequest{}, "", fmt.Errorf("sessionID is required for %s", action)
		}
		if len(req.Argv) != 0 || req.Workdir != "" || req.TimeoutSeconds != 0 || req.SourceRevision != 0 || req.SourceDigest != "" {
			return ExecRequest{}, "", fmt.Errorf("%s accepts only sessionID and optional requestID", action)
		}
		if key == "" {
			key = req.RequestID
		}
	}
	return req, key, nil
}

func normalizeExecWorkdir(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "." {
		return value, nil
	}
	return normalizeExecPath(value)
}

func normalizeExecPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("must be workspace-relative")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("escapes the workspace")
	}
	for _, component := range strings.Split(clean, "/") {
		if component == ".kedge-platform" {
			return "", fmt.Errorf("uses reserved platform metadata directory")
		}
	}
	return clean, nil
}

func boundExecResult(result ExecResult, outputBytes int) ExecResult {
	if outputBytes < 0 {
		outputBytes = 0
	}
	remaining := outputBytes
	if len(result.Stdout) > remaining {
		result.Stdout = result.Stdout[:remaining]
		result.Stderr = ""
		result.Truncated = true
		return result
	}
	remaining -= len(result.Stdout)
	if len(result.Stderr) > remaining {
		result.Stderr = result.Stderr[:remaining]
		result.Truncated = true
	}
	return result
}
