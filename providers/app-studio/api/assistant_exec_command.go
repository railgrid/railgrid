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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino-examples/adk/common/tool/graphtool"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectAssistantExecDefaultTimeout    = 30
	projectAssistantExecMaxTimeout        = 120
	projectAssistantExecMaxArgv           = 32
	projectAssistantExecMaxArgBytes       = 256
	projectAssistantExecMaxWorkdir        = 256
	projectAssistantExecMaxSnapshot       = 8 << 20
	projectAssistantExecMaxOutput         = 1 << 20
	projectAssistantExecPollInterval      = 250 * time.Millisecond
	projectAssistantExecPollTimeout       = 2 * time.Minute
	projectAssistantExecCancelTimeout     = 5 * time.Second
	projectAssistantExecSnapshotAttempts  = 3
	projectAssistantExecStartRetryTimeout = 10 * time.Second
	projectAssistantExecStartRetryPoll    = 250 * time.Millisecond
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
// environment, credentials, image, and raw session identity. Terminal stdout
// and stderr are copied only from the server-owned, bounded result envelope and
// are bounded again before entering the public thread/action projection.
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
	Stdout           []string `json:"stdout,omitempty"`
	Stderr           []string `json:"stderr,omitempty"`
	OutputTruncated  bool     `json:"outputTruncated,omitempty"`
}

// projectAssistantExecCommandToolSpecForRun keeps the ordinary multi-component
// workflow contract intact while making an active run sandbox's single
// component explicit to the model. The run sandbox is deliberately backed by
// the universal template's canonical "workspace" component; this is a
// presentation constraint, not a new execution authority or component alias.
func projectAssistantExecCommandToolSpecForRun(spec projectAssistantToolSpec, runCtx projectAssistantWorkflowRunContext) projectAssistantToolSpec {
	if projectToolBaseName(spec.Name) != projectToolExecCommand {
		return spec
	}
	if runCtx.RunState == nil || (!runCtx.RunState.SandboxRemoteEnabled() && runCtx.RunState.Sandbox() == nil) {
		return spec
	}
	spec.Description = "Run one approved compiler, test, or lint argv in the synchronized active per-run universal coding sandbox. It supports Go, Node.js, and Python, exposes exactly one component named \"workspace\", and has no public preview. ALWAYS pass component=\"workspace\"; do not use app, frontend, backend, or any other component name. Pass argv tokens rather than a shell string; App Studio forwards no credentials or environment overrides. Commands MUST NOT mutate source files: use App Studio source tools for changes, and run formatters in check/diff mode (for example, gofmt -d, never gofmt -w). Direct command writes are not persisted and invalidate the synchronized source evidence required by later commands."
	spec.Parameters = projectAssistantExecCommandParametersForRun(spec.Parameters)
	return spec
}

func projectAssistantExecCommandParametersForRun(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return raw
	}
	properties, ok := document["properties"].(map[string]any)
	if !ok {
		return raw
	}
	component, ok := properties["component"].(map[string]any)
	if !ok {
		return raw
	}
	component["description"] = "The active per-run universal sandbox has exactly one component: workspace. Always use workspace."
	component["enum"] = []any{projectAssistantRunSandboxWorkspaceVerb}
	encoded, err := json.Marshal(document)
	if err != nil {
		return raw
	}
	return encoded
}

func cloneProjectAssistantExecMetadata(src *projectAssistantExecMetadata) *projectAssistantExecMetadata {
	if src == nil {
		return nil
	}
	out := *src
	out.Argv = projectAssistantExecPublicArgv(src.Argv)
	var stdoutTruncated, stderrTruncated bool
	out.Stdout, stdoutTruncated = projectAssistantExecPublicOutput(src.Stdout)
	out.Stderr, stderrTruncated = projectAssistantExecPublicOutput(src.Stderr)
	out.Summary = trimProjectAssistantWorkflowString(projectAssistantExecRedactSecrets(src.Summary), 240)
	out.OutputTruncated = src.OutputTruncated || stdoutTruncated || stderrTruncated
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
	if len(out.Stdout) == 0 {
		out.Stdout = append([]string(nil), existing.Stdout...)
	}
	if len(out.Stderr) == 0 {
		out.Stderr = append([]string(nil), existing.Stderr...)
	}
	if !out.OutputTruncated {
		out.OutputTruncated = existing.OutputTruncated
	}
	// A checkpoint assembled from mixed lifecycle events can fill fields from
	// either side of the merge. Re-run the public boundary after those
	// fallbacks so a malformed/internal event cannot reintroduce raw output or
	// credential-bearing argv into the action projection.
	out.Argv = projectAssistantExecPublicArgv(out.Argv)
	out.Summary = trimProjectAssistantWorkflowString(projectAssistantExecRedactSecrets(out.Summary), 240)
	var stdoutTruncated, stderrTruncated bool
	out.Stdout, stdoutTruncated = projectAssistantExecPublicOutput(out.Stdout)
	out.Stderr, stderrTruncated = projectAssistantExecPublicOutput(out.Stderr)
	out.OutputTruncated = out.OutputTruncated || stdoutTruncated || stderrTruncated
	return out
}

// projectAssistantExecMetadataForToolArguments projects only the execution
// contract that the portal needs. It never carries environment values or raw
// session identity, masks argv tokens that look like credential material, and
// bounds terminal output independently from the model-facing result.
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
	metadata.Summary = trimProjectAssistantWorkflowString(projectAssistantExecRedactSecrets(commandResult.Summary), 240)
	metadata.ExitCode = commandResult.ExitCode
	metadata.DurationMS = commandResult.DurationMS
	stdout, stdoutTruncated := projectAssistantExecPublicOutput(commandResult.Stdout)
	stderr, stderrTruncated := projectAssistantExecPublicOutput(commandResult.Stderr)
	metadata.Stdout = stdout
	metadata.Stderr = stderr
	metadata.OutputTruncated = commandResult.OutputTruncated || stdoutTruncated || stderrTruncated
	return metadata
}

func projectAssistantExecPublicOutput(lines []string) ([]string, bool) {
	if len(lines) == 0 {
		return nil, false
	}
	raw := strings.Join(lines, "\n")
	sanitized := strings.ReplaceAll(raw, "\x00", "\ufffd")
	redacted := projectAssistantExecRedactSecrets(sanitized)
	bounded, truncated := boundedProjectAssistantExecOutput(redacted)
	// Sanitization is not truncation. Preserve the server's explicit
	// outputTruncated flag separately and report only whether the bounded
	// projection had to discard bytes.
	return bounded, truncated
}

func projectAssistantExecPublicArgv(argv []string) []string {
	out := append([]string(nil), argv...)
	redactNext := false
	for index, token := range out {
		lower := strings.ToLower(strings.TrimSpace(token))
		if redactNext {
			out[index] = projectAssistantExecRedactSecrets(token)
			if out[index] == token {
				out[index] = "[redacted]"
			}
			redactNext = false
			continue
		}
		if redacted := projectAssistantExecRedactSecrets(token); redacted != token {
			out[index] = redacted
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
	for _, marker := range []string{"token", "password", "passwd", "secret", "apikey", "api-key", "api_key", "access-token", "access_token", "authorization", "credential", "private-key", "private_key", "secret-key", "secret_key", "cookie"} {
		if value == marker || strings.HasPrefix(value, marker+"=") {
			return true
		}
	}
	return false
}

func projectAssistantExecSensitiveValue(value string) bool {
	for _, marker := range []string{"secret=", "password=", "token=", "api_key=", "api-key=", "access_token=", "access-token=", "authorization=", "cookie=", "bearer "} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// These patterns include the complete value token so ReplaceAllStringFunc can
// retain JSON/string quoting while replacing only the secret contents. A
// replacement such as {"token":"[redacted]"} must remain valid and readable
// rather than consuming the closing quote or object delimiter.
var projectAssistantExecKeyValueSecretPattern = regexp.MustCompile(`(?i)(\b(?:token|authorization|api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|password|passwd|secret|credential|private[_-]?key|secret[_-]?key|cookie)\b["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|(?:Bearer\s+)?[^\s,;{}\[\]"']+)`)
var projectAssistantExecEnvSecretPattern = regexp.MustCompile(`(?i)(\b[A-Z][A-Z0-9_]*(?:TOKEN|KEY|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH|COOKIE)\b["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|(?:Bearer\s+)?[^\s,;{}\[\]"']+)`)

var projectAssistantExecSecretPatterns = []*regexp.Regexp{
	// Authorization headers and query-string credentials are frequently
	// emitted without a key/value delimiter around the secret itself.
	regexp.MustCompile(`(?i)(\bBearer\s+)([A-Za-z0-9._~+/=-]{8,})`),
	regexp.MustCompile(`(?i)([?&](?:token|api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret|authorization|credential)=)([^&\s,;{}\[\]"']+)`),
	// Avoid returning private-key material even when it is presented as a
	// multiline PEM block rather than a key/value pair.
	regexp.MustCompile(`(?is)(-----BEGIN [^-]*PRIVATE KEY-----).*?(-----END [^-]*PRIVATE KEY-----)`),
	// A few provider ecosystems use recognizable opaque key prefixes without
	// printing a field name (for example sk-... or ghp-...).
	regexp.MustCompile(`(?i)\b(?:sk|pk)[_-][A-Za-z0-9_-]{12,}\b|\b(?:ghp|github_pat)[_-][A-Za-z0-9_-]{12,}\b|\bxox[baprs][-_][A-Za-z0-9_-]{12,}\b|\b(?:AKIA|ASIA)[A-Z0-9]{16}\b|\bAIza[A-Za-z0-9_-]{20,}\b`),
}

func projectAssistantExecRedactKeyValueSecrets(value string, pattern *regexp.Regexp) string {
	return pattern.ReplaceAllStringFunc(value, func(match string) string {
		submatches := pattern.FindStringSubmatch(match)
		if len(submatches) < 2 {
			return "[redacted]"
		}
		prefix := submatches[1]
		secret := strings.TrimSpace(match[len(prefix):])
		if len(secret) >= 2 && ((secret[0] == '"' && secret[len(secret)-1] == '"') || (secret[0] == '\'' && secret[len(secret)-1] == '\'')) {
			return prefix + secret[:1] + "[redacted]" + secret[len(secret)-1:]
		}
		return prefix + "[redacted]"
	})
}

func projectAssistantExecRedactSecrets(value string) string {
	redacted := projectAssistantExecRedactKeyValueSecrets(value, projectAssistantExecKeyValueSecretPattern)
	redacted = projectAssistantExecRedactKeyValueSecrets(redacted, projectAssistantExecEnvSecretPattern)
	for index, pattern := range projectAssistantExecSecretPatterns {
		replacement := "$1[redacted]"
		if index == len(projectAssistantExecSecretPatterns)-2 {
			replacement = "$1[redacted]$2"
		}
		if index == len(projectAssistantExecSecretPatterns)-1 {
			replacement = "[redacted]"
		}
		redacted = pattern.ReplaceAllString(redacted, replacement)
	}
	return redacted
}

func newProjectAssistantExecCommandGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantExecCommandInput, *projectAssistantExecCommandResult]()
	workflow.AddLambdaNode("exec-command", compose.InvokableLambda(execProjectAssistantCommand(runCtx))).
		AddInput(compose.START)
	workflow.End().AddInput("exec-command")
	spec, ok := projectAssistantWorkflowToolSpec(projectToolExecCommand)
	if !ok {
		return nil, fmt.Errorf("project assistant workflow spec %q is not configured", projectToolExecCommand)
	}
	presentation := projectAssistantExecCommandToolSpecForRun(spec, runCtx)
	inner, err := graphtool.NewInvokableGraphTool(
		workflow,
		projectToolExecCommand,
		presentation.Description,
		compose.WithGraphName("app-studio-exec-command"),
	)
	if err != nil {
		return nil, err
	}
	if presentation.Description != spec.Description || string(presentation.Parameters) != string(spec.Parameters) {
		info, infoErr := inner.Info(context.Background())
		if infoErr != nil {
			return nil, infoErr
		}
		info.Desc = presentation.Description
		if info.Extra == nil {
			info.Extra = map[string]any{}
		}
		info.Extra[projectEinoToolParametersExtraKey] = string(presentation.Parameters)
		var parameters jsonschema.Schema
		if err := json.Unmarshal(presentation.Parameters, &parameters); err != nil {
			return nil, fmt.Errorf("decode exec_command sandbox parameters: %w", err)
		}
		info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&parameters)
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
		sandbox, sandboxErr := current.RunState.EnsureSandbox(ctx)
		if sandboxErr != nil {
			if errors.Is(sandboxErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return &projectAssistantExecCommandResult{Status: "canceled", Summary: "Command canceled before the coding sandbox was ready."}, nil
			}
			return &projectAssistantExecCommandResult{Status: "failed", Summary: "Coding sandbox setup failed: " + sandboxErr.Error()}, nil
		}
		if sandbox != nil {
			return execProjectAssistantRunSandboxCommand(ctx, current, sandbox, args)
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
		started, err := retryProjectAssistantExecStart(ctx, start, func(startCtx context.Context, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
			return projectAssistantExecCall(startCtx, server, id, target.dataPlaneRefFor(component), request)
		})
		if err != nil {
			return &projectAssistantExecCommandResult{Status: "error", Summary: "Command execution could not start: " + err.Error(), Component: component, SourceRevision: sourceRevision, SourceDigest: digest, SyncStatus: syncStatus}, nil
		}
		if started.SessionID == "" {
			return &projectAssistantExecCommandResult{Status: "error", Summary: "Command execution returned no session ID.", Component: component, SourceRevision: sourceRevision, SourceDigest: digest, SyncStatus: syncStatus}, nil
		}
		startedAt := time.Now()
		result := started
		var cancelOnce sync.Once
		cancelSession := func() {
			cancelOnce.Do(func() {
				cancelCtx, cancel := context.WithTimeout(context.Background(), projectAssistantExecCancelTimeout)
				defer cancel()
				_, _ = projectAssistantExecCall(cancelCtx, server, id, target.dataPlaneRefFor(component), projectSandboxExecRequest{Action: "cancel", SessionID: started.SessionID, RequestID: requestID})
			})
		}
		stopRemoteCancel := context.AfterFunc(ctx, cancelSession)
		defer stopRemoteCancel()
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

func execProjectAssistantRunSandboxCommand(ctx context.Context, current projectAssistantWorkflowRunContext, sandbox *projectAssistantRunSandbox, args *projectAssistantExecCommandInput) (*projectAssistantExecCommandResult, error) {
	// The universal run sandbox is intentionally a single-component execution
	// target. Keep this server-side fence independent from the target metadata:
	// a malformed or future template must not turn an assistant-run sandbox
	// into a multi-component command router merely by adding another map entry.
	if args == nil || args.Component != projectAssistantRunSandboxWorkspaceVerb {
		return &projectAssistantExecCommandResult{
			Status:   "blocked",
			Summary:  "Command execution was rejected.",
			Blockers: []string{fmt.Sprintf("run sandbox execution only permits component %q", projectAssistantRunSandboxWorkspaceVerb)},
		}, nil
	}
	component, _, err := projectAssistantExecComponent(sandbox.target, args.Component)
	if err != nil {
		return &projectAssistantExecCommandResult{Status: "blocked", Summary: "Command execution was rejected.", Blockers: []string{err.Error()}}, nil
	}
	requestID := projectAssistantExecRequestID(current.AssistantRunID, compose.GetToolCallID(ctx))
	start := projectSandboxExecRequest{Action: "start", RequestID: requestID, Argv: args.Argv, Workdir: args.Workdir, TimeoutSeconds: args.TimeoutSeconds}
	started, err := sandbox.exec(ctx, sandbox.target.dataPlaneRefFor(component), start)
	if err != nil {
		return &projectAssistantExecCommandResult{Status: "error", Summary: "Command execution could not start: " + err.Error(), Component: component}, nil
	}
	if started.SessionID == "" {
		return &projectAssistantExecCommandResult{Status: "error", Summary: "Command execution returned no session ID.", Component: component}, nil
	}
	startedAt := time.Now()
	result := started
	var cancelOnce sync.Once
	cancelSession := func() {
		cancelOnce.Do(func() {
			cancelCtx, cancel := context.WithTimeout(context.Background(), projectAssistantExecCancelTimeout)
			defer cancel()
			_, _ = sandbox.exec(cancelCtx, sandbox.target.dataPlaneRefFor(component), projectSandboxExecRequest{Action: "cancel", SessionID: started.SessionID, RequestID: requestID})
		})
	}
	stopRemoteCancel := context.AfterFunc(ctx, cancelSession)
	defer stopRemoteCancel()
	defer func() {
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
			meta := sandbox.metadataSnapshot()
			revision, digest := projectAssistantSandboxRemoteFence(meta)
			return projectAssistantExecResult(result, component, revision, digest, "succeeded", time.Since(startedAt), "canceled"), nil
		case <-deadline.C:
			cancelSession()
			meta := sandbox.metadataSnapshot()
			revision, digest := projectAssistantSandboxRemoteFence(meta)
			return projectAssistantExecResult(result, component, revision, digest, "succeeded", time.Since(startedAt), "timed_out"), nil
		case <-time.After(projectAssistantExecPollInterval):
		}
		result, err = sandbox.exec(ctx, sandbox.target.dataPlaneRefFor(component), projectSandboxExecRequest{Action: "poll", SessionID: started.SessionID, RequestID: requestID})
		if err != nil {
			if ctx.Err() != nil {
				cancelSession()
				meta := sandbox.metadataSnapshot()
				revision, digest := projectAssistantSandboxRemoteFence(meta)
				return projectAssistantExecResult(result, component, revision, digest, "succeeded", time.Since(startedAt), "canceled"), nil
			}
			return &projectAssistantExecCommandResult{Status: "error", Summary: "Command execution polling failed: " + err.Error(), Component: component, SessionID: started.SessionID}, nil
		}
	}
	meta := sandbox.metadataSnapshot()
	revision, digest := projectAssistantSandboxRemoteFence(meta)
	return projectAssistantExecResult(result, component, revision, digest, "succeeded", time.Since(startedAt), ""), nil
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

type projectAssistantExecHTTPError struct {
	status int
	detail string
}

func (e *projectAssistantExecHTTPError) Error() string {
	if e == nil {
		return "exec endpoint failed"
	}
	return fmt.Sprintf("exec endpoint returned %d: %s", e.status, e.detail)
}

type projectAssistantExecUpstreamUnavailableError struct {
	cause error
}

func (e *projectAssistantExecUpstreamUnavailableError) Error() string {
	if e == nil || e.cause == nil {
		return "exec upstream unavailable"
	}
	return e.cause.Error()
}

func (e *projectAssistantExecUpstreamUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func projectAssistantExecLooksUpstreamUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(strings.ToLower(err.Error()), "context canceled") || strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded") {
		return false
	}
	detail := strings.ToLower(err.Error())
	return strings.Contains(detail, "upstream unavailable") || strings.Contains(detail, "connection refused") || strings.Contains(detail, "service unavailable")
}

func projectAssistantExecStartRetryable(err error) bool {
	var statusErr *projectAssistantExecHTTPError
	if errors.As(err, &statusErr) {
		return statusErr.status == http.StatusBadGateway || statusErr.status == http.StatusServiceUnavailable
	}
	var unavailableErr *projectAssistantExecUpstreamUnavailableError
	return errors.As(err, &unavailableErr)
}

// retryProjectAssistantExecStart retries only the initial idempotent START
// request. The caller supplies one immutable request, including its requestID,
// so every attempt uses the same idempotency key. Poll and cancel never pass
// through this helper.
func retryProjectAssistantExecStart(ctx context.Context, request projectSandboxExecRequest, start func(context.Context, projectSandboxExecRequest) (projectSandboxExecResponse, error)) (projectSandboxExecResponse, error) {
	if start == nil {
		return projectSandboxExecResponse{}, errors.New("exec start function is not configured")
	}
	retryCtx, cancel := context.WithTimeout(ctx, projectAssistantExecStartRetryTimeout)
	defer cancel()
	ticker := time.NewTicker(projectAssistantExecStartRetryPoll)
	defer ticker.Stop()
	var lastErr error
	for {
		response, err := start(retryCtx, request)
		if err == nil {
			return response, nil
		}
		if !projectAssistantExecStartRetryable(err) {
			return projectSandboxExecResponse{}, err
		}
		lastErr = err
		select {
		case <-retryCtx.Done():
			if ctx.Err() != nil {
				return projectSandboxExecResponse{}, ctx.Err()
			}
			return projectSandboxExecResponse{}, fmt.Errorf("exec start did not become available within %s: %w", projectAssistantExecStartRetryTimeout, lastErr)
		case <-ticker.C:
		}
	}
}

func projectAssistantExecCall(ctx context.Context, server *Server, id identity, ref dataPlaneRef, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return projectSandboxExecResponse{}, fmt.Errorf("encode exec request: %w", err)
	}
	body, status, err := server.dataPlanePostBoundedWithHeaders(ctx, id, ref, dataPlaneVerbExec, payload, projectAssistantExecMaxOutput*2, http.Header{"Idempotency-Key": []string{request.RequestID}})
	if err != nil {
		if projectAssistantExecLooksUpstreamUnavailable(err) {
			return projectSandboxExecResponse{}, &projectAssistantExecUpstreamUnavailableError{cause: err}
		}
		return projectSandboxExecResponse{}, err
	}
	if status < 200 || status >= 300 {
		return projectSandboxExecResponse{}, &projectAssistantExecHTTPError{status: status, detail: truncateProjectToolInfo(string(body))}
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
