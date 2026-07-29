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
	"strings"
	"syscall"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/patchtoolcalls"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"google.golang.org/genai"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

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
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
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
	_ projectAssistantRunRequest,
	_ *projectEinoAssistantRunState,
) *adk.ModelRetryConfig {
	return &adk.ModelRetryConfig{
		MaxRetries: 2,
		ShouldRetry: func(
			ctx context.Context,
			retryCtx *adk.RetryContext,
		) *adk.RetryDecision {
			if retryCtx == nil || ctx.Err() != nil {
				return &adk.RetryDecision{}
			}
			if retryCtx.OutputMessage == nil {
				if !projectEinoAssistantShouldRetryModelError(retryCtx.Err) {
					return &adk.RetryDecision{}
				}
				return &adk.RetryDecision{
					Retry:        true,
					RejectReason: "transient model provider failure",
				}
			}
			return &adk.RetryDecision{}
		},
	}
}

func projectEinoAssistantWillRetry(err error) bool {
	var retryError *adk.WillRetryError
	return errors.As(err, &retryError)
}

type projectEinoAssistantSafeToolErrorMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
}

var errProjectAssistantPlanRetirement = errors.New("assistant plan retirement failed")
var errProjectAssistantPlanGrantPersistence = errors.New("assistant plan grant persistence failed")

func (m *projectEinoAssistantSafeToolErrorMiddleware) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	_ *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(
		ctx context.Context,
		argumentsInJSON string,
		opts ...einotool.Option,
	) (string, error) {
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err == nil || projectEinoAssistantPropagateToolError(err) {
			return result, err
		}
		return truncateProjectToolInfo(
			"Tool call failed: " + projectEinoAssistantSafeErrorText(err),
		), nil
	}, nil
}

func (m *projectEinoAssistantSafeToolErrorMiddleware) WrapEnhancedInvokableToolCall(
	_ context.Context,
	endpoint adk.EnhancedInvokableToolCallEndpoint,
	_ *adk.ToolContext,
) (adk.EnhancedInvokableToolCallEndpoint, error) {
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
				Text: truncateProjectToolInfo(
					"Tool call failed: " + projectEinoAssistantSafeErrorText(err),
				),
			}},
		}, nil
	}, nil
}

func projectEinoAssistantPropagateToolError(err error) bool {
	if _, ok := compose.IsInterruptRerunError(err); ok {
		return true
	}
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, adk.ErrStreamCanceled) ||
		errors.Is(err, errProjectAssistantPlanRetirement) ||
		errors.Is(err, errProjectAssistantPlanGrantPersistence) ||
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
