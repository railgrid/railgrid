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
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestProjectEinoAssistantTruncateModelToolOutputPreservesEndsAndMetadata(t *testing.T) {
	const maxBytes = projectEinoAssistantModelToolOutputMaxBytes
	value := "BEGIN\n" + strings.Repeat("中間 payload\n", 1800) + "END"
	if len(value) <= maxBytes {
		t.Fatalf("fixture is %d bytes; want more than %d", len(value), maxBytes)
	}

	got := projectEinoAssistantTruncateModelToolOutput(value, maxBytes)
	if len(got) > maxBytes {
		t.Fatalf("truncated output is %d bytes; want <= %d", len(got), maxBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncated output is not valid UTF-8")
	}
	if !strings.HasPrefix(got, "BEGIN\n") {
		t.Fatalf("truncated output lost the beginning: %q", got[:testMinInt(len(got), 80)])
	}
	if !strings.HasSuffix(got, "END") {
		t.Fatalf("truncated output lost the end: %q", got[testMaxInt(0, len(got)-80):])
	}
	for _, field := range []string{
		projectEinoAssistantToolOutputTruncationNotice,
		"original_bytes=",
		"original_lines=",
		"omitted_bytes=",
		"max_bytes=10000",
	} {
		if !strings.Contains(got, field) {
			t.Fatalf("truncated output missing %q: %q", field, got)
		}
	}
}

func TestProjectEinoAssistantTruncateModelToolOutputDowngradesReadEvidence(t *testing.T) {
	const maxBytes = projectEinoAssistantModelToolOutputMaxBytes
	content := "// projected App.jsx\n" + strings.Repeat("const projectedReadMarker = true;\n", 1200)
	raw := `{"path":"src/App.jsx","content":` + quoteJSON(content) + `,"size":100000,"version":"sha256:projected-app","complete":true}`

	got := projectEinoAssistantTruncateModelToolOutput(raw, maxBytes)
	if got == raw || len(got) > maxBytes {
		t.Fatalf("read projection = %d bytes; want a changed result within %d", len(got), maxBytes)
	}
	var projected struct {
		Path                     string `json:"path"`
		Content                  string `json:"content"`
		Version                  string `json:"version"`
		Complete                 bool   `json:"complete"`
		ModelProjectionTruncated bool   `json:"modelProjectionTruncated"`
	}
	if err := json.Unmarshal([]byte(got), &projected); err != nil {
		t.Fatalf("read projection is not valid JSON: %v\n%s", err, got)
	}
	if projected.Path != "src/App.jsx" || projected.Version != "" || projected.Complete || !projected.ModelProjectionTruncated {
		t.Fatalf("read envelope metadata = %#v; want path retained and full-read evidence cleared", projected)
	}
	if !strings.Contains(projected.Content, "projected App.jsx") ||
		!strings.Contains(projected.Content, projectEinoAssistantToolOutputTruncationNotice) {
		t.Fatalf("read content lost projection evidence: %q", projected.Content)
	}
}

func TestProjectEinoAssistantModelToolOutputMiddlewareKeepsLedgerResultWhole(t *testing.T) {
	value := "prefix\n" + strings.Repeat("x", 900) + "\nsuffix"
	ledgerValue := ""
	middleware := &projectEinoAssistantModelToolOutputMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		maxBytes:                     128,
	}
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			ledgerValue = value
			return value, nil
		},
		&adk.ToolContext{Name: "read_file", CallID: "call-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	modelValue, err := wrapped(context.Background(), "{}")
	if err != nil {
		t.Fatal(err)
	}
	if ledgerValue != value {
		t.Fatalf("ledger observed %d bytes; want original %d", len(ledgerValue), len(value))
	}
	if len(modelValue) > 128 {
		t.Fatalf("model output is %d bytes; want <= 128", len(modelValue))
	}
	if modelValue == value {
		t.Fatal("model output was not truncated")
	}
	if !utf8.ValidString(modelValue) {
		t.Fatal("model output is not valid UTF-8")
	}
}

func TestProjectEinoAssistantModelToolOutputMiddlewarePreservesEnhancedMedia(t *testing.T) {
	value := "prefix\n" + strings.Repeat("payload ", 900) + "\nsuffix"
	mediaURL := "https://example.test/preview.png"
	image := &schema.ToolOutputImage{
		MessagePartCommon: schema.MessagePartCommon{URL: &mediaURL, MIMEType: "image/png"},
	}
	original := &schema.ToolResult{Parts: []schema.ToolOutputPart{
		{Type: schema.ToolPartTypeText, Text: value},
		{Type: schema.ToolPartTypeImage, Image: image, Extra: map[string]any{"source": "preview"}},
	}}
	middleware := &projectEinoAssistantModelToolOutputMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		maxBytes:                     128,
	}
	wrapped, err := middleware.WrapEnhancedInvokableToolCall(
		context.Background(),
		func(context.Context, *schema.ToolArgument, ...einotool.Option) (*schema.ToolResult, error) {
			return original, nil
		},
		&adk.ToolContext{Name: "inspect_development_preview", CallID: "call-2"},
	)
	if err != nil {
		t.Fatal(err)
	}
	modelResult, err := wrapped(context.Background(), &schema.ToolArgument{Text: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if modelResult == original {
		t.Fatal("expected a cloned enhanced result after text truncation")
	}
	if original.Parts[0].Text != value {
		t.Fatal("middleware mutated the durable enhanced text result")
	}
	if modelResult.Parts[1].Type != schema.ToolPartTypeImage || modelResult.Parts[1].Image != image {
		t.Fatal("middleware did not preserve the non-text image part")
	}
	if modelResult.Parts[1].Extra["source"] != "preview" {
		t.Fatal("middleware did not preserve enhanced part metadata")
	}
	if len(modelResult.Parts[0].Text) > 128 || !utf8.ValidString(modelResult.Parts[0].Text) {
		t.Fatalf("enhanced text output is invalid or over limit: bytes=%d", len(modelResult.Parts[0].Text))
	}
}

func TestProjectEinoAssistantEnhancedToolResultUsesAggregateTextBudget(t *testing.T) {
	const maxBytes = 128
	first := strings.Repeat("first ", 20)
	second := strings.Repeat("second ", 20)
	third := strings.Repeat("third ", 20)
	mediaURL := "https://example.test/preview.png"
	original := &schema.ToolResult{Parts: []schema.ToolOutputPart{
		{Type: schema.ToolPartTypeText, Text: first},
		{Type: schema.ToolPartTypeImage, Image: &schema.ToolOutputImage{
			MessagePartCommon: schema.MessagePartCommon{URL: &mediaURL, MIMEType: "image/png"},
		}},
		{Type: schema.ToolPartTypeText, Text: second},
		{Type: schema.ToolPartTypeText, Text: third},
	}}
	middleware := &projectEinoAssistantModelToolOutputMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		maxBytes:                     maxBytes,
	}
	wrapped, err := middleware.WrapEnhancedInvokableToolCall(
		context.Background(),
		func(context.Context, *schema.ToolArgument, ...einotool.Option) (*schema.ToolResult, error) {
			return original, nil
		},
		&adk.ToolContext{Name: "inspect_development_preview", CallID: "call-aggregate"},
	)
	if err != nil {
		t.Fatal(err)
	}
	modelResult, err := wrapped(context.Background(), &schema.ToolArgument{Text: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if modelResult == original {
		t.Fatal("expected a cloned enhanced result after aggregate truncation")
	}
	modelTextBytes := 0
	for _, part := range modelResult.Parts {
		if part.Type == schema.ToolPartTypeText {
			modelTextBytes += len(part.Text)
		}
	}
	if modelTextBytes > maxBytes {
		t.Fatalf("aggregate model-visible text is %d bytes; want <= %d", modelTextBytes, maxBytes)
	}
	joinedModelText := modelResult.Parts[0].Text + modelResult.Parts[2].Text + modelResult.Parts[3].Text
	if !strings.Contains(joinedModelText, projectEinoAssistantToolOutputTruncationNotice) {
		t.Fatalf("aggregate truncation metadata missing: %q", joinedModelText)
	}
	if modelResult.Parts[1].Type != schema.ToolPartTypeImage || modelResult.Parts[1].Image == nil {
		t.Fatal("aggregate truncation did not preserve the non-text part and order")
	}
	if original.Parts[0].Text != first || original.Parts[2].Text != second || original.Parts[3].Text != third {
		t.Fatal("aggregate truncation mutated the durable enhanced result")
	}
}

func TestProjectEinoAssistantModelToolOutputMiddlewarePropagatesErrors(t *testing.T) {
	wantErr := errors.New("backend unavailable")
	middleware := &projectEinoAssistantModelToolOutputMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		maxBytes:                     128,
	}
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return "partial", wantErr
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := wrapped(context.Background(), "{}")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if got != "partial" {
		t.Fatalf("partial result = %q, want unchanged", got)
	}
}

func testMinInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func testMaxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
