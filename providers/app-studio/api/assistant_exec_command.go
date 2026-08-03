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

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino-examples/adk/common/tool/graphtool"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectAssistantExecDefaultTimeout   = 30
	projectAssistantExecMaxTimeout       = 120
	projectAssistantExecMaxArgv          = 32
	projectAssistantExecMaxArgBytes      = 256
	projectAssistantExecMaxWorkdir       = 256
	projectAssistantExecMaxSnapshot      = 8 << 20
	projectAssistantExecMaxOutput        = 1 << 20
	projectAssistantExecPollInterval     = 250 * time.Millisecond
	projectAssistantExecPollTimeout      = 2 * time.Minute
	projectAssistantExecCancelTimeout    = 5 * time.Second
	projectAssistantExecSnapshotAttempts = 3
)

var errProjectAssistantExecRevisionChanged = errors.New("workspace mutation revision changed while preparing the execution snapshot")

type projectAssistantExecCommandInput struct {
	Component      string   `json:"component"`
	Argv           []string `json:"argv"`
	Workdir        string   `json:"workdir,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
}

type projectSandboxExecFile struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Executable bool   `json:"executable,omitempty"`
}

type projectAssistantExecSnapshotEntry struct {
	path string
	file projectSandboxExecFile
}

// projectSandboxExecRequest is the typed infrastructure data-plane protocol.
// Normal execution targets the already-synchronized live development
// workspace. App Studio sends the expected durable source revision/digest,
// never a second source snapshot or any credentials/environment.
type projectSandboxExecRequest struct {
	Action         string   `json:"action"`
	SessionID      string   `json:"sessionID,omitempty"`
	RequestID      string   `json:"requestID,omitempty"`
	Argv           []string `json:"argv,omitempty"`
	Workdir        string   `json:"workdir,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	SourceRevision uint64   `json:"sourceRevision,omitempty"`
	SourceDigest   string   `json:"sourceDigest,omitempty"`
}

type projectSandboxExecResponse struct {
	SessionID string `json:"sessionID,omitempty"`
	RequestID string `json:"requestID,omitempty"`
	State     string `json:"state"`
	ExitCode  *int   `json:"exitCode,omitempty"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type projectAssistantExecCommandResult struct {
	Status          string   `json:"status"`
	Summary         string   `json:"summary"`
	Component       string   `json:"component,omitempty"`
	SessionID       string   `json:"sessionID,omitempty"`
	ExitCode        *int     `json:"exitCode,omitempty"`
	Stdout          []string `json:"stdout,omitempty"`
	Stderr          []string `json:"stderr,omitempty"`
	OutputTruncated bool     `json:"outputTruncated,omitempty"`
	DurationMS      int64    `json:"durationMs,omitempty"`
	SourceRevision  uint64   `json:"sourceRevision,omitempty"`
	SourceDigest    string   `json:"sourceDigest,omitempty"`
	SyncStatus      string   `json:"syncStatus,omitempty"`
	Blockers        []string `json:"blockers,omitempty"`
}

// projectAssistantExecMetadata is the allowlisted, public execution contract
// shared by approval interrupts and action-feed items. It intentionally omits
// environment, credentials, image, and raw process output; command output can
// contain application secrets and remains available only to the model/tool
// boundary under the existing bounded result contract.
type projectAssistantExecMetadata struct {
	Component        string   `json:"component,omitempty"`
	Argv             []string `json:"argv,omitempty"`
	Workdir          string   `json:"workdir,omitempty"`
	TimeoutSeconds   int      `json:"timeoutSeconds,omitempty"`
	NetworkProfile   string   `json:"networkProfile,omitempty"`
	AuthorityProfile string   `json:"authorityProfile,omitempty"`
	WritebackPolicy  string   `json:"writebackPolicy,omitempty"`
	Status           string   `json:"status,omitempty"`
	Summary          string   `json:"summary,omitempty"`
	ExitCode         *int     `json:"exitCode,omitempty"`
	DurationMS       int64    `json:"durationMs,omitempty"`
	OutputTruncated  bool     `json:"outputTruncated,omitempty"`
}

func cloneProjectAssistantExecMetadata(src *projectAssistantExecMetadata) *projectAssistantExecMetadata {
	if src == nil {
		return nil
	}
	out := *src
	out.Argv = append([]string(nil), src.Argv...)
	if src.ExitCode != nil {
		code := *src.ExitCode
		out.ExitCode = &code
	}
	return &out
}

// mergeProjectAssistantExecMetadata keeps the original, server-authored
// command request fields when an intermediate checkpoint/update only carries
// lifecycle data, while allowing a terminal result to replace status and
// outcome fields. This is deliberately field-wise instead of choosing one
// whole pointer: permission/checkpoint events and terminal events carry
// different subsets of the disclosure.
func mergeProjectAssistantExecMetadata(existing, next *projectAssistantExecMetadata) *projectAssistantExecMetadata {
	if existing == nil {
		return cloneProjectAssistantExecMetadata(next)
	}
	if next == nil {
		return cloneProjectAssistantExecMetadata(existing)
	}
	out := cloneProjectAssistantExecMetadata(next)
	if out.Component == "" {
		out.Component = existing.Component
	}
	if len(out.Argv) == 0 {
		out.Argv = append([]string(nil), existing.Argv...)
	}
	if out.Workdir == "" {
		out.Workdir = existing.Workdir
	}
	if out.TimeoutSeconds == 0 {
		out.TimeoutSeconds = existing.TimeoutSeconds
	}
	if out.NetworkProfile == "" {
		out.NetworkProfile = existing.NetworkProfile
	}
	if out.AuthorityProfile == "" {
		out.AuthorityProfile = existing.AuthorityProfile
	}
	if out.WritebackPolicy == "" {
		out.WritebackPolicy = existing.WritebackPolicy
	}
	if out.Status == "" {
		out.Status = existing.Status
	}
	if out.Summary == "" {
		out.Summary = existing.Summary
	}
	if out.ExitCode == nil && existing.ExitCode != nil {
		code := *existing.ExitCode
		out.ExitCode = &code
	}
	if out.DurationMS == 0 {
		out.DurationMS = existing.DurationMS
	}
	if !out.OutputTruncated {
		out.OutputTruncated = existing.OutputTruncated
	}
	return out
}

// projectAssistantExecMetadataForToolArguments projects only the execution
// contract that the portal needs. It never carries environment values or raw
// stdout/stderr, and masks argv tokens that look like credential material.
func projectAssistantExecMetadataForToolArguments(name string, args map[string]any, result string, status string) *projectAssistantExecMetadata {
	if projectToolBaseName(name) != projectToolExecCommand {
		return nil
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil
	}
	var input projectAssistantExecCommandInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil
	}
	normalized, _ := normalizeProjectAssistantExecCommandInput(&input)
	if normalized == nil {
		return nil
	}
	metadata := &projectAssistantExecMetadata{
		Component:        normalized.Component,
		Argv:             projectAssistantExecPublicArgv(normalized.Argv),
		Workdir:          normalized.Workdir,
		TimeoutSeconds:   normalized.TimeoutSeconds,
		NetworkProfile:   "application-runtime",
		AuthorityProfile: "application-container",
		WritebackPolicy:  "runtime-workspace-only",
		Status:           strings.TrimSpace(status),
	}
	if strings.TrimSpace(result) == "" {
		return metadata
	}
	var commandResult projectAssistantExecCommandResult
	if err := json.Unmarshal([]byte(result), &commandResult); err != nil {
		return metadata
	}
	if commandResult.Status != "" {
		metadata.Status = commandResult.Status
	}
	metadata.Summary = trimProjectAssistantWorkflowString(commandResult.Summary, 240)
	metadata.ExitCode = commandResult.ExitCode
	metadata.DurationMS = commandResult.DurationMS
	metadata.OutputTruncated = commandResult.OutputTruncated
	return metadata
}

func projectAssistantExecPublicArgv(argv []string) []string {
	out := append([]string(nil), argv...)
	redactNext := false
	for index, token := range out {
		lower := strings.ToLower(strings.TrimSpace(token))
		if redactNext {
			out[index] = "[redacted]"
			redactNext = false
			continue
		}
		if projectAssistantExecSensitiveArg(lower) {
			if strings.Contains(token, "=") {
				out[index] = token[:strings.IndexByte(token, '=')+1] + "[redacted]"
			} else {
				out[index] = token
				redactNext = true
			}
			continue
		}
		if projectAssistantExecSensitiveValue(lower) {
			out[index] = "[redacted]"
		}
	}
	return out
}

func projectAssistantExecSensitiveArg(value string) bool {
	value = strings.TrimLeft(value, "-")
	for _, marker := range []string{"token", "password", "passwd", "secret", "apikey", "api-key", "authorization", "credential", "private-key", "cookie"} {
		if value == marker || strings.HasPrefix(value, marker+"=") {
			return true
		}
	}
	return false
}

func projectAssistantExecSensitiveValue(value string) bool {
	for _, marker := range []string{"secret=", "password=", "token=", "bearer "} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func newProjectAssistantExecCommandGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantExecCommandInput, *projectAssistantExecCommandResult]()
	workflow.AddLambdaNode("exec-command", compose.InvokableLambda(execProjectAssistantCommand(runCtx))).
		AddInput(compose.START)
	workflow.End().AddInput("exec-command")
	inner, err := graphtool.NewInvokableGraphTool(
		workflow,
		projectToolExecCommand,
		"Run one approved compiler, test, or lint argv in the synchronized development runtime for one component. App Studio sends an expected source revision/digest rather than a second source snapshot; no App Studio credentials or environment overrides are forwarded, and the command cannot write back to App Studio source.",
		compose.WithGraphName("app-studio-exec-command"),
	)
	if err != nil {
		return nil, err
	}
	spec, ok := projectAssistantWorkflowToolSpec(projectToolExecCommand)
	if !ok {
		return nil, fmt.Errorf("project assistant workflow spec %q is not configured", projectToolExecCommand)
	}
	permitted, err := applyProjectAssistantGraphToolPermission(inner, spec, runCtx)
	if err != nil {
		return nil, err
	}
	// Preserve fail-closed permission semantics: Never rejects the tool without
	// inspecting or canonicalizing its arguments. Allowed and approval-gated
	// commands still require the same bounded canonical input before they can
	// reach either execution or an approval interrupt.
	if projectAssistantPermissionForV2(spec, runCtx.ApprovalMode, runCtx.RunState, nil, false) == projectAssistantPermissionDeny {
		return permitted, nil
	}
	invokable, ok := permitted.(einotool.InvokableTool)
	if !ok {
		return nil, fmt.Errorf("project assistant graph tool %q is not invokable after permission wrapping", projectToolExecCommand)
	}
	// Validate and canonicalize the command before the approval wrapper sees
	// it. Otherwise a model-generated argv that the workflow would reject only
	// after approval can leave the run waiting on an unrenderable permission
	// request. The preflight also records failures through the same durable tool
	// ledger used by the graph tool, so the model receives a replayable result.
	return projectAssistantExecCommandPreflightTool{
		InvokableTool: invokable,
		spec:          spec,
		ledger:        runCtx.EventLedger,
	}, nil
}

// projectAssistantExecCommandPreflightTool is deliberately outside the
// approval wrapper. An approval interrupt must represent an executable,
// canonical command; malformed or over-bounded model arguments are ordinary
// tool failures and must never become pending permission state.
type projectAssistantExecCommandPreflightTool struct {
	einotool.InvokableTool
	spec   projectAssistantToolSpec
	ledger *projectAssistantRunEventLedger
}

func (t projectAssistantExecCommandPreflightTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.InvokableTool.Info(ctx)
}

func (t projectAssistantExecCommandPreflightTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...einotool.Option) (string, error) {
	callID := compose.GetToolCallID(ctx)
	args, err := projectEinoToolArguments(argumentsInJSON)
	if err != nil {
		return t.finishInvalid(ctx, callID, nil, argumentsInJSON, fmt.Errorf("invalid arguments: %w", err))
	}
	normalized, err := normalizeProjectAssistantExecCommandArguments(args)
	if err != nil {
		return t.finishInvalid(ctx, callID, args, argumentsInJSON, fmt.Errorf("invalid arguments: %w", err))
	}
	normalizedJSON := projectEinoToolArgumentsString(normalized)
	return t.InvokableTool.InvokableRun(ctx, normalizedJSON, opts...)
}

func (t projectAssistantExecCommandPreflightTool) finishInvalid(
	ctx context.Context,
	callID string,
	args map[string]any,
	argumentsInJSON string,
	reason error,
) (string, error) {
	if reason == nil {
		reason = errors.New("invalid arguments")
	}
	if args == nil {
		args = map[string]any{"invalidArguments": argumentsInJSON}
	}
	result := projectEinoAssistantSafeToolFailureResult(projectToolBaseName(t.spec.Name), reason)
	if t.ledger == nil {
		return result, nil
	}
	decision, err := t.ledger.RecordToolRequest(ctx, callID, t.spec, args)
	if err != nil {
		return "", err
	}
	if decision.Replay != nil {
		return decision.Replay.InvokeResult()
	}
	outcome, err := t.ledger.FinishToolCall(ctx, decision.Token, result, reason)
	if err != nil {
		return "", err
	}
	return outcome.Result, nil
}

func execProjectAssistantCommand(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantExecCommandInput) (*projectAssistantExecCommandResult, error) {
	return func(ctx context.Context, input *projectAssistantExecCommandInput) (*projectAssistantExecCommandResult, error) {
		current := runCtx.current()
		args, blockers := normalizeProjectAssistantExecCommandInput(input)
		if len(blockers) > 0 {
			return &projectAssistantExecCommandResult{Status: "blocked", Summary: "Command execution was rejected.", Blockers: blockers}, nil
		}
		server, id, target, blocked := projectAssistantRuntimeCallContext(ctx, current)
		if blocked != nil {
			return &projectAssistantExecCommandResult{Status: blocked.Status, Summary: blocked.Summary, Blockers: blocked.Blockers}, nil
		}
		component, componentInfo, err := projectAssistantExecComponent(target, args.Component)
		if err != nil {
			return &projectAssistantExecCommandResult{Status: "blocked", Summary: "Command execution was rejected.", Blockers: []string{err.Error()}}, nil
		}
		var (
			revision                uint64
			syncStatus, syncFailure string
			sourceRevision          uint64
			digest                  string
		)
		for attempt := 0; attempt < projectAssistantExecSnapshotAttempts; attempt++ {
			revision, syncStatus, syncFailure = projectAssistantExecSyncEvidence(ctx, current)
			if syncStatus != "succeeded" {
				break
			}
			_, digest, sourceRevision, err = projectAssistantExecSnapshot(ctx, current, componentInfo, revision)
			if !errors.Is(err, errProjectAssistantExecRevisionChanged) {
				break
			}
		}
		if syncStatus != "succeeded" {
			if syncFailure == "" {
				syncFailure = "the latest workspace mutation has not completed development synchronization"
			}
			return &projectAssistantExecCommandResult{Status: "blocked", Summary: "Command execution was blocked until the exact workspace revision is synchronized.", Component: component, SourceRevision: revision, SyncStatus: syncStatus, Blockers: []string{syncFailure}}, nil
		}
		if err != nil {
			return &projectAssistantExecCommandResult{Status: "blocked", Summary: "Command execution was blocked because an exact workspace snapshot could not be prepared.", Component: component, SourceRevision: revision, SyncStatus: syncStatus, Blockers: []string{err.Error()}}, nil
		}
		if sourceRevision == 0 {
			return &projectAssistantExecCommandResult{Status: "blocked", Summary: "Command execution was blocked because the durable workspace revision could not be read.", Component: component, SourceRevision: revision, SourceDigest: digest, SyncStatus: syncStatus, Blockers: []string{"project workspace source revision is unavailable"}}, nil
		}
		requestID := projectAssistantExecRequestID(current.AssistantRunID, compose.GetToolCallID(ctx))
		start := projectSandboxExecRequest{Action: "start", RequestID: requestID, Argv: args.Argv, Workdir: args.Workdir, TimeoutSeconds: args.TimeoutSeconds, SourceRevision: sourceRevision, SourceDigest: digest}
		started, err := projectAssistantExecCall(ctx, server, id, target.dataPlaneRefFor(component), start)
		if err != nil {
			return &projectAssistantExecCommandResult{Status: "error", Summary: "Command execution could not start: " + err.Error(), Component: component, SourceRevision: sourceRevision, SourceDigest: digest, SyncStatus: syncStatus}, nil
		}
		if started.SessionID == "" {
			return &projectAssistantExecCommandResult{Status: "error", Summary: "Command execution returned no session ID.", Component: component, SourceRevision: sourceRevision, SourceDigest: digest, SyncStatus: syncStatus}, nil
		}
		startedAt := time.Now()
		result := started
		cancelSent := false
		cancelSession := func() {
			if cancelSent {
				return
			}
			cancelSent = true
			cancelCtx, cancel := context.WithTimeout(context.Background(), projectAssistantExecCancelTimeout)
			defer cancel()
			_, _ = projectAssistantExecCall(cancelCtx, server, id, target.dataPlaneRefFor(component), projectSandboxExecRequest{Action: "cancel", SessionID: started.SessionID, RequestID: requestID})
		}
		defer func() {
			// A request can be canceled while the HTTP poll is in flight. Keep
			// the remote process bounded even when that poll returns ctx.Err
			// before the select below gets a chance to send the cancel action.
			if !projectAssistantExecTerminal(result.State) {
				cancelSession()
			}
		}()
		deadline := time.NewTimer(projectAssistantExecPollTimeout)
		defer deadline.Stop()
		for !projectAssistantExecTerminal(result.State) {
			select {
			case <-ctx.Done():
				cancelSession()
				return projectAssistantExecResult(result, component, sourceRevision, digest, syncStatus, time.Since(startedAt), "canceled"), nil
			case <-deadline.C:
				cancelSession()
				return projectAssistantExecResult(result, component, sourceRevision, digest, syncStatus, time.Since(startedAt), "timed_out"), nil
			case <-time.After(projectAssistantExecPollInterval):
			}
			result, err = projectAssistantExecCall(ctx, server, id, target.dataPlaneRefFor(component), projectSandboxExecRequest{Action: "poll", SessionID: started.SessionID, RequestID: requestID})
			if err != nil {
				if ctx.Err() != nil {
					cancelSession()
					return projectAssistantExecResult(result, component, sourceRevision, digest, syncStatus, time.Since(startedAt), "canceled"), nil
				}
				return &projectAssistantExecCommandResult{Status: "error", Summary: "Command execution polling failed: " + err.Error(), Component: component, SessionID: started.SessionID, SourceRevision: sourceRevision, SourceDigest: digest, SyncStatus: syncStatus}, nil
			}
		}
		return projectAssistantExecResult(result, component, sourceRevision, digest, syncStatus, time.Since(startedAt), ""), nil
	}
}

func projectAssistantExecResult(raw projectSandboxExecResponse, component string, revision uint64, digest, syncStatus string, duration time.Duration, override string) *projectAssistantExecCommandResult {
	status := override
	if status == "" {
		switch raw.State {
		case "succeeded":
			status = "succeeded"
		case "failed":
			status = "failed"
		case "canceled", "cancelled":
			status = "canceled"
		case "timed_out":
			status = "timed_out"
		default:
			status = "error"
		}
	}
	stdout, stdoutTruncated := boundedProjectAssistantExecOutput(raw.Stdout)
	stderr, stderrTruncated := boundedProjectAssistantExecOutput(raw.Stderr)
	result := &projectAssistantExecCommandResult{Status: status, Component: component, SessionID: raw.SessionID, ExitCode: raw.ExitCode, Stdout: stdout, Stderr: stderr, OutputTruncated: raw.Truncated || stdoutTruncated || stderrTruncated, DurationMS: duration.Milliseconds(), SourceRevision: revision, SourceDigest: digest, SyncStatus: syncStatus}
	result.Summary = fmt.Sprintf("Command %s in component %q.", status, component)
	return result
}

func boundedProjectAssistantExecOutput(raw string) ([]string, bool) {
	raw = strings.TrimRight(raw, "\n")
	if raw == "" {
		return nil, false
	}
	truncated := false
	if len(raw) > projectAssistantExecMaxOutput {
		raw = raw[:projectAssistantExecMaxOutput]
		truncated = true
	}
	lines := strings.Split(raw, "\n")
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
		truncated = true
	}
	for i := range lines {
		lines[i] = trimProjectAssistantWorkflowString(lines[i], 4096)
	}
	return lines, truncated
}

func projectAssistantExecCall(ctx context.Context, server *Server, id identity, ref dataPlaneRef, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return projectSandboxExecResponse{}, fmt.Errorf("encode exec request: %w", err)
	}
	body, status, err := server.dataPlanePostBoundedWithHeaders(ctx, id, ref, dataPlaneVerbExec, payload, projectAssistantExecMaxOutput*2, http.Header{"Idempotency-Key": []string{request.RequestID}})
	if err != nil {
		return projectSandboxExecResponse{}, err
	}
	if status < 200 || status >= 300 {
		return projectSandboxExecResponse{}, fmt.Errorf("exec endpoint returned %d: %s", status, truncateProjectToolInfo(string(body)))
	}
	var response projectSandboxExecResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return projectSandboxExecResponse{}, fmt.Errorf("decode exec response: %w", err)
	}
	return response, nil
}

func projectAssistantExecComponent(target projectDevelopmentSyncTargetInfo, requested string) (string, projectTemplateComponent, error) {
	component := strings.TrimSpace(requested)
	if component == "" {
		return "", projectTemplateComponent{}, errors.New("component is required")
	}
	info, ok := target.Components[component]
	if !ok {
		return "", projectTemplateComponent{}, fmt.Errorf("unknown component %q; available components: %s", component, strings.Join(target.sortedComponents(), ", "))
	}
	return component, info, nil
}

func normalizeProjectAssistantExecCommandInput(input *projectAssistantExecCommandInput) (*projectAssistantExecCommandInput, []string) {
	if input == nil {
		return nil, []string{"component and argv are required"}
	}
	out := &projectAssistantExecCommandInput{Component: strings.TrimSpace(input.Component), Argv: append([]string(nil), input.Argv...), Workdir: strings.TrimSpace(input.Workdir), TimeoutSeconds: input.TimeoutSeconds}
	var blockers []string
	if out.Component == "" {
		blockers = append(blockers, "component is required")
	}
	if len(out.Argv) == 0 || len(out.Argv) > projectAssistantExecMaxArgv {
		blockers = append(blockers, fmt.Sprintf("argv must contain between 1 and %d tokens", projectAssistantExecMaxArgv))
	}
	for index, token := range out.Argv {
		if token == "" || len([]byte(token)) > projectAssistantExecMaxArgBytes || strings.IndexByte(token, 0) >= 0 {
			blockers = append(blockers, fmt.Sprintf("argv token %d is empty, too large, or contains NUL", index+1))
		}
	}
	if len([]byte(out.Workdir)) > projectAssistantExecMaxWorkdir || strings.IndexByte(out.Workdir, 0) >= 0 || strings.Contains(out.Workdir, "\\") || path.IsAbs(out.Workdir) {
		blockers = append(blockers, "workdir must be a bounded relative path")
	} else if out.Workdir != "" {
		clean := path.Clean(out.Workdir)
		if clean == ".." || strings.HasPrefix(clean, "../") {
			blockers = append(blockers, "workdir must remain under the selected component workspace")
		} else {
			out.Workdir = clean
		}
	}
	if out.TimeoutSeconds == 0 {
		out.TimeoutSeconds = projectAssistantExecDefaultTimeout
	}
	if out.TimeoutSeconds < 1 || out.TimeoutSeconds > projectAssistantExecMaxTimeout {
		blockers = append(blockers, fmt.Sprintf("timeoutSeconds must be between 1 and %d", projectAssistantExecMaxTimeout))
	}
	return out, blockers
}

// normalizeProjectAssistantExecCommandArguments decodes the model-facing map
// through the same typed input path used by the workflow, then re-encodes the
// bounded/defaulted values as the canonical tool arguments. Keeping this
// conversion beside the workflow normalizer prevents approval metadata and
// execution from accepting different command shapes.
func normalizeProjectAssistantExecCommandArguments(args map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("exec_command arguments could not be encoded: %w", err)
	}
	var input projectAssistantExecCommandInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("exec_command arguments could not be decoded: %w", err)
	}
	normalized, blockers := normalizeProjectAssistantExecCommandInput(&input)
	if len(blockers) > 0 {
		return nil, errors.New(strings.Join(blockers, "; "))
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("exec_command arguments could not be canonicalized: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(canonical, &out); err != nil {
		return nil, fmt.Errorf("exec_command canonical arguments could not be decoded: %w", err)
	}
	return out, nil
}

func projectAssistantExecSyncEvidence(ctx context.Context, runCtx projectAssistantWorkflowRunContext) (uint64, string, string) {
	if runCtx.RunState == nil {
		return 0, "unknown", "assistant run state is unavailable"
	}
	revision, _ := runCtx.RunState.SourceMutationRevisions()
	if revision == 0 {
		// A newly-created project can already have source in the FileStore while
		// its creation/hydration sync is still queued.  There is no positive
		// development-sync receipt in a fresh run state, so treating revision zero
		// as succeeded would let exec race that first sync and run against an
		// empty or stale live workspace.  Promote this one-time bootstrap into
		// the same durable revision/evidence state used by normal mutations.
		if runCtx.Server == nil || runCtx.Project == nil {
			return 0, "unknown", "initial workspace synchronization has not been scheduled"
		}
		revision = runCtx.RunState.BeginDevelopmentSyncForNextMutation()
		if revision == 0 {
			return 0, "unknown", "initial workspace synchronization revision is unavailable"
		}
		scheduled := runCtx.Server.scheduleDevelopmentSyncAfterMutationWithCompletion(
			runCtx.Identity,
			runCtx.Project,
			projectActionWorkspaceSync,
			func(syncErr error) { runCtx.RunState.CompleteDevelopmentSync(revision, syncErr) },
		)
		// Keep the synthetic bootstrap revision aligned with the run state's
		// source revision even when scheduling fails, so a repeated tool call
		// observes the failed evidence rather than creating another bootstrap.
		runCtx.RunState.RecordSourceMutation()
		if !scheduled {
			runCtx.RunState.CompleteDevelopmentSync(revision, errors.New("initial workspace synchronization was not scheduled"))
		}
	}
	status, failure := runCtx.RunState.WaitForDevelopmentSync(ctx, revision, dataPlaneCallTimeout)
	return revision, status, failure
}

func projectAssistantExecSnapshot(ctx context.Context, runCtx projectAssistantWorkflowRunContext, component projectTemplateComponent, expectedRevision uint64) ([]projectSandboxExecFile, string, uint64, error) {
	if runCtx.Workspace == nil {
		return nil, "", 0, errors.New("project workspace store is not configured")
	}
	root := path.Clean(strings.TrimSpace(component.WorkspacePath))
	if root == "" {
		root = "."
	}
	for attempt := 0; attempt < projectAssistantExecSnapshotAttempts; attempt++ {
		sourceRevisionBefore, err := runCtx.Workspace.SourceRevision(ctx, runCtx.WorkspaceScope)
		if err != nil {
			return nil, "", 0, err
		}
		list, err := runCtx.Workspace.ListFiles(ctx, runCtx.WorkspaceScope, workspace.ListOptions{Limit: workspace.MaxListLimit})
		if err != nil {
			return nil, "", 0, err
		}
		if list.Truncated {
			return nil, "", 0, fmt.Errorf("workspace snapshot exceeds the %d-file limit", workspace.MaxListLimit)
		}
		paths := projectAssistantExecComponentPaths(list, root)
		entries := make([]projectAssistantExecSnapshotEntry, 0, len(paths))
		total := 0
		retry := false
		for _, clean := range paths {
			relative := clean
			if root != "." {
				relative = strings.TrimPrefix(clean, root+"/")
			}
			read, readErr := runCtx.Workspace.ReadFile(ctx, runCtx.WorkspaceScope, workspace.ReadOptions{Path: clean, MaxBytes: workspace.MaxWriteBytes})
			if readErr != nil {
				if errors.Is(readErr, fs.ErrNotExist) {
					retry = true
					break
				}
				return nil, "", 0, readErr
			}
			if read.Binary || read.Truncated {
				return nil, "", 0, fmt.Errorf("workspace file %q is not bounded UTF-8 source", clean)
			}
			total += len([]byte(read.Content))
			if total > projectAssistantExecMaxSnapshot {
				return nil, "", 0, fmt.Errorf("component snapshot exceeds %d bytes", projectAssistantExecMaxSnapshot)
			}
			entries = append(entries, projectAssistantExecSnapshotEntry{path: clean, file: projectSandboxExecFile{Path: relative, Content: read.Content}})
		}
		if retry {
			continue
		}
		files, digest := projectAssistantExecSnapshotDigest(entries)

		// FileStore deliberately exposes separate bounded list/read/digest
		// operations. Re-list and then compare its digest under the store lock
		// before accepting this bundle, so a concurrent mutation cannot leave
		// the executor with bytes from one revision and a digest from another.
		confirm, err := runCtx.Workspace.ListFiles(ctx, runCtx.WorkspaceScope, workspace.ListOptions{Limit: workspace.MaxListLimit})
		if err != nil {
			return nil, "", 0, err
		}
		if confirm.Truncated {
			return nil, "", 0, fmt.Errorf("workspace snapshot exceeds the %d-file limit", workspace.MaxListLimit)
		}
		if !projectAssistantExecStringSlicesEqual(paths, projectAssistantExecComponentPaths(confirm, root)) {
			continue
		}
		if len(paths) > 0 {
			currentDigest, err := projectAssistantExecWorkspaceDigest(ctx, runCtx, paths, root)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return nil, "", 0, err
			}
			if currentDigest != digest {
				continue
			}
		}
		// Bind the accepted bytes to the exact mutation revision whose
		// development sync was verified by the caller. If another assistant
		// mutation landed during List/Read/Digest, the outer preparation loop
		// waits for that newer revision and captures again.
		if runCtx.RunState != nil {
			currentRevision, _ := runCtx.RunState.SourceMutationRevisions()
			if currentRevision != expectedRevision {
				return nil, "", 0, errProjectAssistantExecRevisionChanged
			}
		}
		sourceRevisionAfter, err := runCtx.Workspace.SourceRevision(ctx, runCtx.WorkspaceScope)
		if err != nil {
			return nil, "", 0, err
		}
		if sourceRevisionBefore == 0 || sourceRevisionBefore != sourceRevisionAfter {
			continue
		}
		return files, digest, sourceRevisionBefore, nil
	}
	return nil, "", 0, errors.New("workspace changed while preparing the execution snapshot")
}

func projectAssistantExecWorkspaceDigest(ctx context.Context, runCtx projectAssistantWorkflowRunContext, paths []string, root string) (string, error) {
	entries := make([]projectAssistantExecSnapshotEntry, 0, len(paths))
	for _, clean := range paths {
		read, err := runCtx.Workspace.ReadFile(ctx, runCtx.WorkspaceScope, workspace.ReadOptions{Path: clean, MaxBytes: workspace.MaxWriteBytes})
		if err != nil {
			return "", err
		}
		if read.Binary || read.Truncated {
			return "", fmt.Errorf("workspace file %q is not bounded UTF-8 source", clean)
		}
		relative := clean
		if root != "." {
			relative = strings.TrimPrefix(clean, root+"/")
		}
		entries = append(entries, projectAssistantExecSnapshotEntry{
			path: clean,
			file: projectSandboxExecFile{Path: relative, Content: read.Content},
		})
	}
	_, digest := projectAssistantExecSnapshotDigest(entries)
	return digest, nil
}

func projectAssistantExecComponentPaths(list workspace.FileList, root string) []string {
	paths := make([]string, 0, len(list.Files))
	prefix := root + "/"
	for _, info := range list.Files {
		clean := path.Clean(info.Path)
		if root != "." && !strings.HasPrefix(clean, prefix) {
			continue
		}
		paths = append(paths, clean)
	}
	sort.Strings(paths)
	return paths
}

func projectAssistantExecSnapshotDigest(entries []projectAssistantExecSnapshotEntry) ([]projectSandboxExecFile, string) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	hash := sha256.New()
	files := make([]projectSandboxExecFile, 0, len(entries))
	for _, entry := range entries {
		// entry.file.Path is component-relative and matches the development
		// agent's managed-manifest digest. The full workspace path is still
		// used by the caller's FileStore digest confirmation below.
		_, _ = hash.Write([]byte(entry.file.Path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(entry.file.Content))
		_, _ = hash.Write([]byte{0})
		files = append(files, entry.file)
	}
	return files, hex.EncodeToString(hash.Sum(nil))
}

func projectAssistantExecStringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func projectAssistantExecRequestID(runID, callID string) string {
	runID = strings.TrimSpace(runID)
	callID = strings.TrimSpace(callID)
	if runID == "" && callID == "" {
		// Model-issued tool calls normally provide both values. Keep a
		// deterministic fallback for direct/internal invocations so the
		// infrastructure contract's required idempotency key is still met.
		runID = "anonymous"
		callID = "anonymous"
	}
	sum := sha256.Sum256([]byte(runID + "\x00" + callID))
	return "appstudio-exec-" + hex.EncodeToString(sum[:16])
}

func projectAssistantExecTerminal(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "succeeded", "failed", "canceled", "timed_out", "cancelled":
		return true
	default:
		return false
	}
}
