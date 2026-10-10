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

package api

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// projectEinoAssistantModelToolOutputMaxBytes is the model-facing limit for a
// single tool result. The durable tool ledger runs behind this middleware, so
// it retains the complete result while the next model request gets a bounded
// view. Keeping this limit in bytes mirrors the request-size constraint used by
// Codex and makes the bound independent of the model's tokenizer.
const projectEinoAssistantModelToolOutputMaxBytes = 10_000

const projectEinoAssistantToolOutputTruncationNotice = "Warning: truncated tool output"

// projectEinoAssistantModelToolOutputMiddleware bounds only the value returned
// to the agent/model. It must be registered before the ledger and telemetry
// wrappers: Eino applies the first registered handler outermost, while each
// inner handler observes the endpoint's original result.
type projectEinoAssistantModelToolOutputMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	maxBytes int
	runState *projectEinoAssistantRunState
}

func projectEinoAssistantModelToolOutputMiddlewareForModel(runStates ...*projectEinoAssistantRunState) adk.ChatModelAgentMiddleware {
	var runState *projectEinoAssistantRunState
	if len(runStates) > 0 {
		runState = runStates[0]
	}
	return &projectEinoAssistantModelToolOutputMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		maxBytes:                     projectEinoAssistantModelToolOutputMaxBytes,
		runState:                     runState,
	}
}

func (m *projectEinoAssistantModelToolOutputMiddleware) limit() int {
	if m == nil || m.maxBytes <= 0 {
		return projectEinoAssistantModelToolOutputMaxBytes
	}
	return m.maxBytes
}

func (m *projectEinoAssistantModelToolOutputMiddleware) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...einotool.Option) (string, error) {
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return result, err
		}
		projected := ""
		if toolCtx != nil && toolCtx.Name == projectToolReadFile {
			if literal, ok := projectEinoAssistantProjectModelReadFileOutput(result, m.limit()); ok {
				projected = literal
			}
			if projected == "" {
				projected = projectEinoAssistantTruncateGenericToolOutput(result, m.limit())
			}
			m.recordModelVisibleReadFileVersion(projected)
		} else {
			projected = projectEinoAssistantTruncateGenericToolOutput(result, m.limit())
		}
		return projected, nil
	}, nil
}

func (m *projectEinoAssistantModelToolOutputMiddleware) recordModelVisibleReadFileVersion(projected string) {
	if m == nil || m.runState == nil {
		return
	}
	key, ok := projectEinoAssistantCompleteReadFileReceiptKey(projected)
	if !ok {
		key, ok = projectEinoAssistantCompleteBinaryReadFileReceiptKey(projected)
	}
	if !ok {
		return
	}
	m.runState.RecordModelVisibleReadFileVersion(
		key.path,
		key.version,
		key.binary,
		m.runState.CurrentModelCallOrdinal(),
	)
}

func (m *projectEinoAssistantModelToolOutputMiddleware) WrapEnhancedInvokableToolCall(
	_ context.Context,
	endpoint adk.EnhancedInvokableToolCallEndpoint,
	_ *adk.ToolContext,
) (adk.EnhancedInvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argument *schema.ToolArgument, opts ...einotool.Option) (*schema.ToolResult, error) {
		result, err := endpoint(ctx, argument, opts...)
		if err != nil || result == nil {
			return result, err
		}
		return projectEinoAssistantTruncateModelEnhancedToolResult(result, m.limit()), nil
	}, nil
}

// projectEinoAssistantTruncateModelToolOutput bounds generic model-facing tool
// output. Local read_file results use the literal-source projector only after
// their exact trusted tool identity is established.
func projectEinoAssistantTruncateModelToolOutput(value string, maxBytes int) string {
	return projectEinoAssistantTruncateGenericToolOutput(value, maxBytes)
}

// projectEinoAssistantTruncateGenericToolOutput keeps both ends of a result so
// that the model retains the useful beginning (headers, status, schema) and
// the useful end (errors, summaries, final records). The notice is deliberately
// machine-readable and reports source bytes/lines plus omitted source bytes.
func projectEinoAssistantTruncateGenericToolOutput(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}

	originalBytes := len(value)
	originalLines := strings.Count(value, "\n") + 1
	omittedBytes := originalBytes
	var marker string
	var head, tail string

	// The marker contains omittedBytes, which depends on how many bytes fit
	// around it. Iterate until the value stabilizes; this normally converges in
	// two passes and keeps the final output within the requested byte limit.
	for range 8 {
		marker = fmt.Sprintf(
			"\n[%s: original_bytes=%d original_lines=%d omitted_bytes=%d max_bytes=%d]\n",
			projectEinoAssistantToolOutputTruncationNotice,
			originalBytes,
			originalLines,
			omittedBytes,
			maxBytes,
		)
		if len(marker) >= maxBytes {
			// A caller-provided limit smaller than the metadata itself cannot
			// preserve every field. Keep the result valid UTF-8 and bounded.
			return projectEinoAssistantUTF8Prefix(marker, maxBytes)
		}

		contentBudget := maxBytes - len(marker)
		headBudget := contentBudget / 2
		tailBudget := contentBudget - headBudget
		head = projectEinoAssistantUTF8Prefix(value, headBudget)
		tail = projectEinoAssistantUTF8Suffix(value, tailBudget)
		nextOmittedBytes := originalBytes - len(head) - len(tail)
		if nextOmittedBytes == omittedBytes {
			return head + marker + tail
		}
		omittedBytes = nextOmittedBytes
	}

	// Defensive fallback for unusual UTF-8 boundaries or an unexpectedly
	// unstable marker width. Recompute the marker with the final retained byte
	// count and trim the content budget once more if needed.
	marker = fmt.Sprintf(
		"\n[%s: original_bytes=%d original_lines=%d omitted_bytes=%d max_bytes=%d]\n",
		projectEinoAssistantToolOutputTruncationNotice,
		originalBytes,
		originalLines,
		omittedBytes,
		maxBytes,
	)
	if len(marker) >= maxBytes {
		return projectEinoAssistantUTF8Prefix(marker, maxBytes)
	}
	contentBudget := maxBytes - len(marker)
	head = projectEinoAssistantUTF8Prefix(value, contentBudget/2)
	tail = projectEinoAssistantUTF8Suffix(value, contentBudget-contentBudget/2)
	return head + marker + tail
}

func projectEinoAssistantTruncateModelEnhancedToolResult(result *schema.ToolResult, maxBytes int) *schema.ToolResult {
	if result == nil || len(result.Parts) == 0 {
		return result
	}

	textIndexes := make([]int, 0, len(result.Parts))
	var textBuilder strings.Builder
	for index, part := range result.Parts {
		if part.Type != schema.ToolPartTypeText {
			continue
		}
		if len(textIndexes) > 0 {
			// Keep boundaries between text blocks explicit while applying one
			// budget to the complete model-visible text payload.
			textBuilder.WriteByte('\n')
		}
		textIndexes = append(textIndexes, index)
		textBuilder.WriteString(part.Text)
	}
	if len(textIndexes) == 0 {
		return result
	}
	combinedText := textBuilder.String()
	truncatedText := projectEinoAssistantTruncateModelToolOutput(combinedText, maxBytes)
	if truncatedText == combinedText {
		return result
	}

	parts := append([]schema.ToolOutputPart(nil), result.Parts...)
	for _, index := range textIndexes {
		parts[index].Text = ""
	}
	if len(textIndexes) == 1 {
		parts[textIndexes[0]].Text = truncatedText
	} else if markerOffset := strings.Index(truncatedText, "\n["+projectEinoAssistantToolOutputTruncationNotice); markerOffset >= 0 {
		// Keep the prefix in the first text slot and the marker/suffix in the
		// last text slot. Empty intermediate text slots retain their original
		// positions relative to images/audio/files without duplicating output.
		parts[textIndexes[0]].Text = truncatedText[:markerOffset]
		parts[textIndexes[len(textIndexes)-1]].Text = truncatedText[markerOffset:]
	} else {
		// If a caller supplied a limit smaller than the metadata marker, the
		// UTF-8-safe marker prefix is the complete bounded result.
		parts[textIndexes[0]].Text = truncatedText
	}

	// Do not mutate result in place: inner durable handlers may retain the
	// pointer they returned for persistence or replay. Only text fields are
	// replaced; all non-text pointers and per-part metadata remain intact.
	return &schema.ToolResult{Parts: parts}
}

func projectEinoAssistantUTF8Prefix(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func projectEinoAssistantUTF8Suffix(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.ValidString(value[start:]) {
		start++
	}
	return value[start:]
}
