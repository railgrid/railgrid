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
	"errors"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/patchtoolcalls"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"google.golang.org/genai"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectEinoAssistantDefaultModelStreamIdleTimeout = 300 * time.Second
	projectEinoAssistantDefaultModelMaxRetries        = 5
	// Keep execution bounded even when settings bypass the HTTP/Secret parser.
	// The normal App Studio configuration surface remains narrower, but retry
	// policy must also be safe for internal callers that construct settings
	// directly.
	projectEinoAssistantMaxModelRetries = 100
)

type projectEinoAssistantModelTimeoutError struct {
	Code     string
	Duration time.Duration
}

func (e *projectEinoAssistantModelTimeoutError) Error() string {
	timeout := projectEinoAssistantDefaultModelStreamIdleTimeout
	if e != nil && e.Duration > 0 {
		timeout = e.Duration
	}
	if e != nil && e.Code == "model_stream_idle_timeout" {
		return "model_stream_idle_timeout: assistant model stream produced no new data for " + timeout.String()
	}
	return "model_first_response_timeout: assistant model produced no first response for " + timeout.String()
}

func (*projectEinoAssistantModelTimeoutError) Timeout() bool   { return true }
func (*projectEinoAssistantModelTimeoutError) Temporary() bool { return true }

type projectEinoAssistantIncompleteStreamError struct{}

func (*projectEinoAssistantIncompleteStreamError) Error() string {
	return "model_stream_incomplete: assistant model stream ended before a completion marker"
}

func (*projectEinoAssistantIncompleteStreamError) Unwrap() error { return io.ErrUnexpectedEOF }

type projectEinoAssistantBoundedModel struct {
	einomodel.BaseChatModel

	firstResponseTimeout time.Duration
	streamIdleTimeout    time.Duration
	requireCompletion    bool
}

func projectEinoAssistantBoundModel(base einomodel.BaseChatModel, settings projectLLMSettings) einomodel.BaseChatModel {
	if base == nil {
		return nil
	}
	idleTimeout := settings.StreamIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = projectEinoAssistantDefaultModelStreamIdleTimeout
	}
	return &projectEinoAssistantBoundedModel{
		BaseChatModel:        base,
		firstResponseTimeout: idleTimeout,
		streamIdleTimeout:    idleTimeout,
		requireCompletion:    true,
	}
}

func (m *projectEinoAssistantBoundedModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.Message, error) {
	type result struct {
		message *schema.Message
		err     error
	}
	modelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	resultCh := make(chan result)
	go func() {
		message, err := m.BaseChatModel.Generate(modelCtx, input, opts...)
		select {
		case resultCh <- result{message: message, err: err}:
		case <-modelCtx.Done():
		}
	}()
	timer := time.NewTimer(m.firstResponseTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, &projectEinoAssistantModelTimeoutError{Code: "model_first_response_timeout", Duration: m.firstResponseTimeout}
	case result := <-resultCh:
		return result.message, result.err
	}
}

func (m *projectEinoAssistantBoundedModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	type result struct {
		reader *schema.StreamReader[*schema.Message]
		err    error
	}
	started := time.Now()
	modelCtx, cancel := context.WithCancel(ctx)
	resultCh := make(chan result)
	go func() {
		reader, err := m.BaseChatModel.Stream(modelCtx, input, opts...)
		select {
		case resultCh <- result{reader: reader, err: err}:
		case <-modelCtx.Done():
			if reader != nil {
				reader.Close()
			}
		}
	}()
	timer := time.NewTimer(m.firstResponseTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case <-timer.C:
		cancel()
		return nil, &projectEinoAssistantModelTimeoutError{Code: "model_first_response_timeout", Duration: m.firstResponseTimeout}
	case result := <-resultCh:
		if result.err != nil {
			cancel()
			return nil, result.err
		}
		if result.reader == nil {
			cancel()
			return nil, errors.New("assistant model returned no response stream")
		}
		remaining := m.firstResponseTimeout - time.Since(started)
		return projectEinoAssistantBoundedStream(
			ctx,
			modelCtx,
			cancel,
			result.reader,
			remaining,
			m.streamIdleTimeout,
			m.requireCompletion,
		), nil
	}
}

func projectEinoAssistantBoundedStream(
	ctx context.Context,
	modelCtx context.Context,
	cancel context.CancelFunc,
	source *schema.StreamReader[*schema.Message],
	firstResponseTimeout time.Duration,
	idleTimeout time.Duration,
	requireCompletion bool,
) *schema.StreamReader[*schema.Message] {
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer cancel()
		defer source.Close()
		defer writer.Close()
		wait := firstResponseTimeout
		first := true
		completed := false
		for {
			if wait <= 0 {
				writer.Send(nil, &projectEinoAssistantModelTimeoutError{Code: "model_first_response_timeout", Duration: firstResponseTimeout})
				return
			}
			type receiveResult struct {
				message *schema.Message
				err     error
			}
			received := make(chan receiveResult)
			go func() {
				message, err := source.Recv()
				select {
				case received <- receiveResult{message: message, err: err}:
				case <-modelCtx.Done():
				}
			}()
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				writer.Send(nil, ctx.Err())
				return
			case <-modelCtx.Done():
				timer.Stop()
				writer.Send(nil, modelCtx.Err())
				return
			case <-timer.C:
				code := "model_stream_idle_timeout"
				if first {
					code = "model_first_response_timeout"
				}
				writer.Send(nil, &projectEinoAssistantModelTimeoutError{Code: code, Duration: wait})
				return
			case result := <-received:
				timer.Stop()
				if errors.Is(result.err, io.EOF) {
					if requireCompletion && !completed {
						writer.Send(nil, &projectEinoAssistantIncompleteStreamError{})
					}
					return
				}
				if result.err != nil {
					writer.Send(nil, result.err)
					return
				}
				if writer.Send(result.message, nil) {
					return
				}
				if result.message != nil && result.message.ResponseMeta != nil &&
					strings.TrimSpace(result.message.ResponseMeta.FinishReason) != "" {
					completed = true
				}
				first = false
				wait = idleTimeout
			}
		}
	}()
	return reader
}

var projectEinoAssistantSerializedCookiePattern = regexp.MustCompile(
	`(?i)\\?["']\b(?:set-cookie|cookie)\b\\?["'][ \t]*:[ \t]*\\?["']`,
)

var projectEinoAssistantSecretPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{
		pattern: regexp.MustCompile(
			`(?i)(\\?["']?\bauthorization\b\\?["']?[ \t]*:[ \t]*\\?["']?(?:bearer|basic)[ \t]+)[^ \t\r\n,;\\"'}]+`,
		),
		replacement: `${1}[REDACTED]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\bbearer[ \t]+)[^ \t\r\n,;]+`),
		replacement: `${1}[REDACTED]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\b(?:set-cookie|cookie)\b[ \t]*:[ \t]*)[^\r\n]+`),
		replacement: `${1}[REDACTED]`,
	},
	{
		pattern: regexp.MustCompile(
			`(?i)(\\?["']?\b(?:api[_-]?key|access[_-]?token|token|secret|password)\b\\?["']?[ \t]*[:=][ \t]*)(?:\\"(?:[^"\\\r\n]|\\.)*\\"|\\'(?:[^'\\\r\n]|\\.)*\\'|"[^"\r\n]*"|'[^'\r\n]*'|[^ \t\r\n&,;]+)`,
		),
		replacement: `${1}[REDACTED]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.-]*://[^:/@\s]+:)[^@/\s]+(@)`),
		replacement: `${1}[REDACTED]${2}`,
	},
	{
		pattern:     regexp.MustCompile(`\bsk-[A-Za-z0-9][A-Za-z0-9_-]*\b`),
		replacement: `[REDACTED]`,
	},
}

func projectEinoAssistantShouldRetryModelError(err error) bool {
	if err == nil ||
		errors.Is(err, context.Canceled) {
		return false
	}

	var openAIError *openaimodel.APIError
	if errors.As(err, &openAIError) {
		return projectEinoAssistantRetryableHTTPStatus(openAIError.HTTPStatusCode)
	}
	var geminiError genai.APIError
	if errors.As(err, &geminiError) {
		return projectEinoAssistantRetryableHTTPStatus(geminiError.Code)
	}
	var geminiErrorPointer *genai.APIError
	if errors.As(err, &geminiErrorPointer) {
		return projectEinoAssistantRetryableHTTPStatus(geminiErrorPointer.Code)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) &&
		(networkError.Timeout() || networkError.Temporary())
}

func projectEinoAssistantRetryableHTTPStatus(status int) bool {
	return status == 408 ||
		status == 409 ||
		status == 425 ||
		status == 429 ||
		(status >= 500 && status <= 599)
}

func projectEinoAssistantModelRetryConfig(
	req projectAssistantRunRequest,
	_ *projectEinoAssistantRunState,
) *adk.ModelRetryConfig {
	maxRetries := projectEinoAssistantModelMaxRetries(req.LLM)
	baseBackoff := req.LLM.RetryBackoff
	if baseBackoff <= 0 {
		baseBackoff = 200 * time.Millisecond
	}
	return &adk.ModelRetryConfig{
		MaxRetries: maxRetries,
		BackoffFunc: func(_ context.Context, attempt int) time.Duration {
			if attempt < 1 {
				attempt = 1
			}
			delay := baseBackoff * time.Duration(1<<min(attempt-1, 6))
			if delay > 10*time.Second {
				return 10 * time.Second
			}
			return delay
		},
		ShouldRetry: func(
			ctx context.Context,
			retryCtx *adk.RetryContext,
		) *adk.RetryDecision {
			if retryCtx == nil || ctx.Err() != nil {
				return &adk.RetryDecision{}
			}
			if retryCtx.Err != nil {
				if retryCtx.RetryAttempt > maxRetries {
					return &adk.RetryDecision{}
				}
				if !projectEinoAssistantShouldRetryModelError(retryCtx.Err) {
					return &adk.RetryDecision{}
				}
				projectEinoAssistantPublishRetryAttempt(req.StreamCallbacks, retryCtx.RetryAttempt, maxRetries)
				return &adk.RetryDecision{
					Retry:        true,
					RejectReason: "transport failure",
				}
			}
			return &adk.RetryDecision{}
		},
	}
}

func projectEinoAssistantModelMaxRetries(settings projectLLMSettings) int {
	maxRetries := settings.MaxRetries
	if maxRetries == 0 && !settings.MaxRetriesConfigured {
		return projectEinoAssistantDefaultModelMaxRetries
	}
	if maxRetries < 0 {
		return 0
	}
	if maxRetries > projectEinoAssistantMaxModelRetries {
		return projectEinoAssistantMaxModelRetries
	}
	return maxRetries
}

func projectEinoAssistantWillRetry(err error) bool {
	var retryError *adk.WillRetryError
	return errors.As(err, &retryError)
}

func projectEinoAssistantPublishRetryAttempt(
	callbacks projectAssistantStreamCallbacks,
	retryAttempt int,
	maxRetries int,
) {
	if callbacks.OnStatus == nil {
		return
	}
	callbacks.OnStatus(projectEinoAssistantReconnectStatus(retryAttempt, maxRetries))
}

func projectEinoAssistantReconnectStatus(retryAttempt, maxRetries int) string {
	if retryAttempt < 1 {
		retryAttempt = 1
	}
	return "Model connection was interrupted; reconnecting " + strconv.Itoa(retryAttempt) + "/" + strconv.Itoa(maxRetries)
}

type projectEinoAssistantSafeToolErrorMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
}

var errProjectAssistantPlanRetirement = errors.New("assistant plan retirement failed")

func (m *projectEinoAssistantSafeToolErrorMiddleware) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	toolName := ""
	if toolCtx != nil {
		toolName = projectToolBaseName(toolCtx.Name)
	}
	return func(
		ctx context.Context,
		argumentsInJSON string,
		opts ...einotool.Option,
	) (string, error) {
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err == nil || projectEinoAssistantPropagateToolError(err) {
			return result, err
		}
		return projectEinoAssistantSafeToolFailureResult(toolName, err), nil
	}, nil
}

func (m *projectEinoAssistantSafeToolErrorMiddleware) WrapEnhancedInvokableToolCall(
	_ context.Context,
	endpoint adk.EnhancedInvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (adk.EnhancedInvokableToolCallEndpoint, error) {
	toolName := ""
	if toolCtx != nil {
		toolName = projectToolBaseName(toolCtx.Name)
	}
	return func(
		ctx context.Context,
		toolArgument *schema.ToolArgument,
		opts ...einotool.Option,
	) (*schema.ToolResult, error) {
		result, err := endpoint(ctx, toolArgument, opts...)
		if err == nil || projectEinoAssistantPropagateToolError(err) {
			return result, err
		}
		return &schema.ToolResult{
			Parts: []schema.ToolOutputPart{{
				Type: schema.ToolPartTypeText,
				Text: projectEinoAssistantSafeToolFailureResult(toolName, err),
			}},
		}, nil
	}, nil
}

func projectEinoAssistantSafeToolFailureResult(toolName string, err error) string {
	const prefix = "Tool call failed: "
	safeReason := projectEinoAssistantSafeErrorText(err)
	recovery := ""
	lowerReason := strings.ToLower(safeReason)
	switch {
	case toolName == projectToolApplyPatch && strings.Contains(lowerReason, "add file content cannot contain hunk headers or nested patch envelopes"):
		recovery = " Recovery: return one outer *** Begin Patch / *** End Patch envelope. Put each Add File, Update File, or Delete File section directly inside it. Under Add File, emit only the new file content—no @@ lines and no nested Begin/End markers unless they are literal content lines prefixed with '+'. Every content line must begin with '+' (the parser strips it); encode literal marker-looking content as '+ *** Update File: example'."
	case toolName == projectToolApplyPatch && strings.Contains(lowerReason, "numeric unified-diff hunk headers are not supported"):
		recovery = " Recovery: this tool treats text after @@ as a literal source anchor. Retry with exactly @@ or @@ followed by an exact class/function line copied from the file; never use line coordinates such as @@ -12,4 +12,5 @@."
	case toolName == projectToolApplyPatch && strings.Contains(lowerReason, string(workspace.PatchErrorContextNotFound)):
		recovery = " Recovery: reread the named file around the failed hunk, then retry one contextual patch with current unchanged lines. A literal @@ anchor positions the hunk after that unchanged line and must not be repeated in the hunk body; use plain @@ when changing the first line or the anchor line itself."
	case toolName == projectToolApplyPatch && strings.Contains(lowerReason, string(workspace.PatchErrorContextAmbiguous)):
		recovery = " Recovery: add more stable unchanged lines or an @@ class/function anchor so the failed hunk matches exactly one location."
	case toolName == projectToolApplyPatch && strings.Contains(lowerReason, string(workspace.PatchErrorNoChanges)):
		recovery = " Recovery: revise the contextual patch so it makes the requested change; do not verify an unchanged workspace."
	case toolName == projectToolApplyPatch && strings.Contains(lowerReason, string(workspace.PatchErrorWorkspaceConflict)):
		recovery = " Recovery: reread every affected existing file because workspace contents changed during the edit, then build a new contextual patch from current evidence."
	case toolName == projectToolApplyPatch && strings.Contains(lowerReason, string(workspace.PatchErrorStrategyChange)):
		recovery = " Recovery: reread the affected current source and submit a materially different patch; the same patch will not be dispatched again at this workspace revision."
	case toolName == projectToolApplyPatch && strings.Contains(lowerReason, string(workspace.PatchErrorInvalidPatch)):
		recovery = " Recovery: return one valid *** Begin Patch / *** End Patch envelope. Every Add File content line must begin with '+' (the parser strips it); encode literal marker-looking content as '+ *** Update File: example'. Start with Add File, Update File, or Delete File; for a move, put Move to immediately below Update File for the old path."
	}
	if recovery == "" {
		return truncateProjectToolInfo(prefix + safeReason)
	}
	reasonLimit := projectToolInfoLimit - len(prefix) - len(recovery)
	safeReason = projectEinoAssistantTruncateFailureReason(safeReason, reasonLimit)
	return prefix + safeReason + recovery
}

func projectEinoAssistantPatchRecoveryInstruction(code workspace.PatchErrorCode) string {
	switch code {
	case workspace.PatchErrorContextNotFound:
		return "Reread the named file around the failed hunk and build a new patch from the returned current source."
	case workspace.PatchErrorContextAmbiguous:
		return "Add stable unchanged lines or a literal class/function anchor so the hunk matches exactly one location."
	case workspace.PatchErrorWorkspaceConflict:
		return "Reread every affected existing file and rebuild the patch from current workspace contents."
	case workspace.PatchErrorStrategyChange:
		return "Reread the affected source and submit a materially different patch; this patch will not be dispatched again at the current revision."
	case workspace.PatchErrorNoChanges:
		return "Revise the patch so it makes the requested source change."
	default:
		return "Correct the patch using the typed error details before retrying."
	}
}

func projectEinoAssistantTruncateFailureReason(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return strings.Repeat(".", limit)
	}
	var out strings.Builder
	for _, char := range value {
		encoded := string(char)
		if out.Len()+len(encoded) > limit-3 {
			break
		}
		out.WriteString(encoded)
	}
	return strings.TrimSpace(out.String()) + "..."
}

func projectEinoAssistantPropagateToolError(err error) bool {
	if _, ok := compose.IsInterruptRerunError(err); ok {
		return true
	}
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, adk.ErrStreamCanceled) ||
		errors.Is(err, errProjectAssistantPlanRetirement) ||
		apierrors.IsForbidden(err) ||
		apierrors.IsUnauthorized(err)
}

func projectEinoAssistantSafeErrorText(err error) string {
	if err == nil {
		return ""
	}
	return projectEinoAssistantSafeText(err.Error())
}

func projectEinoAssistantSafeText(value string) string {
	value = projectEinoAssistantRedactSerializedCookieValues(value)
	for _, pattern := range projectEinoAssistantSecretPatterns {
		value = pattern.pattern.ReplaceAllString(value, pattern.replacement)
	}
	return truncateProjectToolInfo(value)
}

func projectEinoAssistantRedactSerializedCookieValues(value string) string {
	var out strings.Builder
	lastWrite := 0
	searchStart := 0
	for searchStart < len(value) {
		match := projectEinoAssistantSerializedCookiePattern.FindStringIndex(value[searchStart:])
		if match == nil {
			break
		}
		contentStart := searchStart + match[1]
		openingQuote := contentStart - 1
		openingEscapeCount := projectEinoAssistantBackslashCountBefore(value, openingQuote)

		closingQuote := -1
		closingEscapeStart := -1
		for i := contentStart; i < len(value); i++ {
			if value[i] != value[openingQuote] {
				continue
			}
			escapeCount := projectEinoAssistantBackslashCountBefore(value, i)
			if !projectEinoAssistantSerializedQuoteCloses(openingEscapeCount, escapeCount) {
				continue
			}
			if !projectEinoAssistantSerializedValueHasSafeSuffix(value, i, openingEscapeCount) {
				continue
			}
			closingQuote = i
			closingEscapeStart = i - escapeCount
			break
		}

		out.WriteString(value[lastWrite:contentStart])
		out.WriteString("[REDACTED]")
		if closingQuote >= 0 {
			lastWrite = closingEscapeStart
			searchStart = closingQuote + 1
			continue
		}

		lineEnd := len(value)
		if relativeEnd := strings.IndexAny(value[contentStart:], "\r\n"); relativeEnd >= 0 {
			lineEnd = contentStart + relativeEnd
		}
		lastWrite = lineEnd
		searchStart = lineEnd
	}
	if lastWrite == 0 {
		return value
	}
	out.WriteString(value[lastWrite:])
	return out.String()
}

func projectEinoAssistantBackslashCountBefore(value string, index int) int {
	count := 0
	for i := index - 1; i >= 0 && value[i] == '\\'; i-- {
		count++
	}
	return count
}

func projectEinoAssistantSerializedQuoteCloses(openingEscapeCount, candidateEscapeCount int) bool {
	modulus := 2 * (openingEscapeCount + 1)
	return candidateEscapeCount%modulus == openingEscapeCount
}

func projectEinoAssistantSerializedValueHasSafeSuffix(value string, closingQuote, escapeDepth int) bool {
	index := projectEinoAssistantSkipHorizontalSpace(value, closingQuote+1)
	if index >= len(value) {
		return false
	}
	if value[index] == '}' {
		return true
	}
	if value[index] != ',' {
		return false
	}

	index = projectEinoAssistantSkipHorizontalSpace(value, index+1)
	keyQuote := index + escapeDepth
	if keyQuote >= len(value) {
		return false
	}
	for i := index; i < keyQuote; i++ {
		if value[i] != '\\' {
			return false
		}
	}
	quote := value[keyQuote]
	if quote != '"' && quote != '\'' {
		return false
	}
	for i := keyQuote + 1; i < len(value); i++ {
		if value[i] != quote {
			continue
		}
		escapeCount := projectEinoAssistantBackslashCountBefore(value, i)
		if !projectEinoAssistantSerializedQuoteCloses(escapeDepth, escapeCount) {
			continue
		}
		index = projectEinoAssistantSkipHorizontalSpace(value, i+1)
		return index < len(value) && value[index] == ':'
	}
	return false
}

func projectEinoAssistantSkipHorizontalSpace(value string, index int) int {
	for index < len(value) && (value[index] == ' ' || value[index] == '\t') {
		index++
	}
	return index
}

func projectEinoAssistantPatchToolCallsMiddleware(
	ctx context.Context,
) (adk.ChatModelAgentMiddleware, error) {
	return patchtoolcalls.New(ctx, &patchtoolcalls.Config{
		PatchedContentGenerator: func(
			_ context.Context,
			toolName string,
			_ string,
		) (string, error) {
			return "The result for " + toolName +
					" was not recorded. Its completion is unknown; inspect current project or runtime state before retrying.",
				nil
		},
	})
}
