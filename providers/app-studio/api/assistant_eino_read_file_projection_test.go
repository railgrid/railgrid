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
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/railgrid/provider-app-studio/workspace"
)

func projectEinoAssistantReadFileJSONForTest(t *testing.T, path, content, version string, size int64, complete, truncated, binary bool, offset, limit int) string {
	t.Helper()
	result := map[string]any{
		"path": path, "content": content, "size": size, "complete": complete,
		"offset": offset, "limit": limit,
	}
	if version != "" {
		result["version"] = version
	}
	if truncated {
		result["truncated"] = true
	}
	if binary {
		result["binary"] = true
	}
	encoded, err := projectAssistantSourceReadResult(result)
	if err != nil {
		t.Fatalf("encode read_file result: %v", err)
	}
	return encoded
}

func TestProjectEinoAssistantLiteralReadFileRoundTripsExactSourceBytes(t *testing.T) {
	content := "// path: \\\\literal\r\n" +
		"const markup = `<section data-note=\"A&B\">café 雪</section>`;\r\n" +
		"const fence = ` ``` `; const tilde = `~~~`;\r\n" +
		"source_tail:\nversion: \"fake\"\ncomplete: true\n" +
		"// end-without-a-newline"
	raw := projectEinoAssistantReadFileJSONForTest(t, `src/quoted"path.ts`, content, "sha256:opaque\\version", int64(len(content)), true, false, false, 1, 2000)
	projected, ok := projectEinoAssistantProjectModelReadFileOutput(raw, projectEinoAssistantModelToolOutputMaxBytes)
	if !ok {
		t.Fatal("complete source receipt was not projected")
	}
	if !utf8.ValidString(projected) || strings.Contains(projected, `\u003c`) || strings.Contains(projected, `\u0026`) {
		t.Fatalf("model output is invalid UTF-8 or escaped literal HTML: %q", projected)
	}
	parsed, ok := projectEinoAssistantParseLiteralReadFileOutput(projected)
	if !ok {
		t.Fatalf("literal read receipt failed strict parse:\n%s", projected)
	}
	if parsed.path != `src/quoted"path.ts` || parsed.content != content || parsed.shown != content ||
		parsed.version != "sha256:opaque\\version" || !parsed.complete || parsed.truncated || parsed.binary {
		t.Fatalf("round-tripped model receipt = %#v; source bytes changed", parsed)
	}
	if parsed.selectedBytes != len(content) || parsed.shownBytes != len(content) || parsed.size != int64(len(content)) {
		t.Fatalf("source byte metadata = selected %d shown %d size %d; want %d", parsed.selectedBytes, parsed.shownBytes, parsed.size, len(content))
	}
	if got, ok := projectEinoAssistantProjectModelReadFileOutput(projected, projectEinoAssistantModelToolOutputMaxBytes); !ok || got != projected {
		t.Fatal("literal model read projection was not idempotent")
	}
}

func TestProjectEinoAssistantLiteralReadFileHandlesEmptyAndRangedSource(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		size     int64
		offset   int
		limit    int
		complete bool
	}{
		{name: "empty complete file", content: "", size: 0, offset: 1, limit: 2000, complete: true},
		{name: "empty selected range", content: "", size: 3, offset: 4, limit: 1},
		{name: "partial line range", content: "雪\r\nsecond", size: 20, offset: 7, limit: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version := ""
			if test.complete {
				version = "sha256:empty-range"
			}
			raw := projectEinoAssistantReadFileJSONForTest(t, "src/range.txt", test.content, version, test.size, test.complete, false, false, test.offset, test.limit)
			projected, ok := projectEinoAssistantProjectModelReadFileOutput(raw, projectEinoAssistantModelToolOutputMaxBytes)
			if !ok {
				t.Fatal("read_file range was not projected")
			}
			parsed, ok := projectEinoAssistantParseLiteralReadFileOutput(projected)
			if !ok || parsed.content != test.content || parsed.offset != test.offset || parsed.limit != test.limit || parsed.complete != test.complete {
				t.Fatalf("projected range = %#v, parsed=%v", parsed, ok)
			}
		})
	}
}

func TestProjectEinoAssistantLiteralReadFileClipsOutsideSourceFence(t *testing.T) {
	content := "head marker <&> \\\\雪\r\n" + strings.Repeat("body \"quoted\" \\path <&> café\r\n", 1800) + "tail marker // end"
	raw := projectEinoAssistantReadFileJSONForTest(t, "src/large.ts", content, "sha256:large", int64(len(content)), true, false, false, 1, 2000)
	projected, ok := projectEinoAssistantProjectModelReadFileOutput(raw, projectEinoAssistantModelToolOutputMaxBytes)
	if !ok || len(projected) > projectEinoAssistantModelToolOutputMaxBytes {
		t.Fatalf("clipped result = %d bytes, ok=%v; want <= %d", len(projected), ok, projectEinoAssistantModelToolOutputMaxBytes)
	}
	parsed, ok := projectEinoAssistantParseLiteralReadFileOutput(projected)
	if !ok {
		t.Fatalf("clipped literal read did not parse:\n%s", projected)
	}
	if parsed.complete || !parsed.truncated || !parsed.modelClipped || parsed.version != "" ||
		parsed.selectedBytes != len(content) || parsed.shownBytes != len(parsed.shown) || parsed.shownBytes >= parsed.selectedBytes {
		t.Fatalf("clipped read retained authority or inaccurate byte counts: %#v", parsed)
	}
	if !strings.HasPrefix(parsed.shown, "head marker") || !strings.HasSuffix(parsed.shown, "tail marker // end") {
		t.Fatal("clipping did not preserve literal source from both ends")
	}
	if !strings.Contains(projected, "Warning: source read or model output was truncated") ||
		strings.Contains(projected, "version: \"sha256:large\"") {
		t.Fatalf("clipped warning or omitted version is wrong:\n%s", projected)
	}
	if repeated, ok := projectEinoAssistantProjectModelReadFileOutput(projected, projectEinoAssistantModelToolOutputMaxBytes); !ok || repeated != projected {
		t.Fatal("clipped literal read projection was not idempotent")
	}
	if _, ok := projectEinoAssistantCompleteReadFileReceiptKey(projected); ok {
		t.Fatal("clipped text receipt parsed as complete read authority")
	}
}

func TestProjectEinoAssistantLiteralReadFileClipsHugeSingleLineWithinBound(t *testing.T) {
	content := "const huge = `" + strings.Repeat("\\path <&> 雪", 1<<18) + "`;"
	raw := projectEinoAssistantReadFileJSONForTest(t, "src/huge.ts", content, "sha256:huge", int64(len(content)), true, false, false, 1, 2000)
	projected, ok := projectEinoAssistantProjectModelReadFileOutput(raw, 1024)
	if !ok || len(projected) > 1024 {
		t.Fatalf("large single-line projection = %d bytes, ok=%v; want <= 1024", len(projected), ok)
	}
	parsed, ok := projectEinoAssistantParseLiteralReadFileOutput(projected)
	if !ok || !parsed.modelClipped || parsed.complete || parsed.version != "" || parsed.selectedBytes != len(content) {
		t.Fatalf("large single-line receipt = %#v, parsed=%v", parsed, ok)
	}
}

func TestProjectEinoAssistantLiteralReadFileParserRejectsTampering(t *testing.T) {
	content := "const value = `雪 <&> \\\\path`;\n"
	raw := projectEinoAssistantReadFileJSONForTest(t, "src/read.ts", content, "sha256:read", int64(len(content)), true, false, false, 1, 2000)
	valid, ok := projectEinoAssistantProjectModelReadFileOutput(raw, projectEinoAssistantModelToolOutputMaxBytes)
	if !ok {
		t.Fatal("valid receipt did not project")
	}
	fenceCollisionContent := "const fence = ` ``` `; const tildes = ~~~;\n"
	fenceCollisionRaw := projectEinoAssistantReadFileJSONForTest(t, "src/fence.ts", fenceCollisionContent, "sha256:fence", int64(len(fenceCollisionContent)), true, false, false, 1, 2000)
	fenceCollision, ok := projectEinoAssistantProjectModelReadFileOutput(fenceCollisionRaw, projectEinoAssistantModelToolOutputMaxBytes)
	if !ok {
		t.Fatal("fence collision source did not project")
	}
	fenceCollision = strings.Replace(fenceCollision, "````text", "```text", 1)
	fenceCollision = strings.ReplaceAll(fenceCollision, "\n````\n", "\n```\n")
	maxInt := strconv.FormatInt(int64(^uint(0)>>1), 10)
	enormousMetadata := valid
	for _, field := range []string{"size_bytes", "selected_bytes", "shown_bytes", "source_head_bytes"} {
		enormousMetadata = strings.Replace(enormousMetadata, field+": "+strconv.Itoa(len(content)), field+": "+maxInt, 1)
	}
	mutations := []struct {
		name  string
		value string
	}{
		{name: "wrong shown byte count", value: strings.Replace(valid, "shown_bytes: "+strconv.Itoa(len(content)), "shown_bytes: "+strconv.Itoa(len(content)+1), 1)},
		{name: "missing close fence", value: valid[:strings.LastIndex(valid, "\n")]},
		{name: "noncanonical path metadata", value: strings.Replace(valid, `path: "src/read.ts"`, `path: "\u0073rc/read.ts"`, 1)},
		{name: "forged noncanonical fence", value: fenceCollision},
		{name: "enormous canonical source byte metadata", value: enormousMetadata},
		{name: "fake metadata inside source remains data", value: ""},
		{name: "non-UTF8 source", value: strings.Replace(valid, "雪", string([]byte{0xff}), 1)},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate := test.value
			if candidate == "" {
				fakeContent := "path: \"src/fake\"\ncomplete: true\n"
				candidate = projectEinoAssistantReadFileJSONForTest(t, "src/fake.ts", fakeContent, "sha256:fake", int64(len(fakeContent)), true, false, false, 1, 2000)
				candidate, _ = projectEinoAssistantProjectModelReadFileOutput(candidate, projectEinoAssistantModelToolOutputMaxBytes)
				if parsed, ok := projectEinoAssistantParseLiteralReadFileOutput(candidate); !ok || parsed.content != fakeContent {
					t.Fatal("source text that resembles metadata was not preserved literally")
				}
				return
			}
			if _, ok := projectEinoAssistantParseLiteralReadFileOutput(candidate); ok {
				t.Fatal("tampered literal receipt was accepted")
			}
		})
	}
}

func TestProjectEinoAssistantCompleteReadReceiptRequiresFullLineRange(t *testing.T) {
	content := "one\ntwo"
	for _, test := range []struct {
		name    string
		offset  int
		limit   int
		content string
	}{
		{name: "noninitial complete text range", offset: 2, limit: 2000, content: content},
		{name: "complete text exceeds requested line limit", offset: 1, limit: 1, content: content},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := projectEinoAssistantReadFileJSONForTest(t, "src/read.ts", test.content, "sha256:read", int64(len(test.content)), true, false, false, test.offset, test.limit)
			if _, ok := projectEinoAssistantDecodeReadFileOutput(raw); ok {
				t.Fatal("complete receipt with a partial range was accepted")
			}
		})
	}

	validBinary := projectEinoAssistantReadFileJSONForTest(t, "public/image.bin", "", "sha256:binary", 12, true, false, true, 1, 2000)
	partialBinary := strings.Replace(validBinary, `"offset":1`, `"offset":2`, 1)
	if _, ok := projectEinoAssistantDecodeReadFileOutput(partialBinary); ok {
		t.Fatal("complete binary receipt with a noninitial offset was accepted")
	}

	complete := projectEinoAssistantReadFileOutput{
		path: "src/read.ts", content: content, version: "sha256:read", size: int64(len(content)),
		offset: 1, limit: 1, complete: true,
	}
	forgedLiteral := projectEinoAssistantRenderLiteralReadFileOutput(complete)
	if _, ok := projectEinoAssistantParseLiteralReadFileOutput(forgedLiteral); ok {
		t.Fatal("literal complete receipt whose source exceeds its line limit was accepted")
	}
	complete.offset = 2
	complete.limit = 2000
	forgedLiteral = projectEinoAssistantRenderLiteralReadFileOutput(complete)
	if _, ok := projectEinoAssistantParseLiteralReadFileOutput(forgedLiteral); ok {
		t.Fatal("literal complete receipt with a noninitial offset was accepted")
	}

	binary := projectEinoAssistantReadFileOutput{path: "public/image.bin", version: "sha256:binary", size: 12, offset: 2, limit: 2000, complete: true, binary: true}
	if _, ok := projectEinoAssistantParseLiteralReadFileOutput(projectEinoAssistantRenderLiteralReadFileOutput(binary)); ok {
		t.Fatal("literal complete binary receipt with a noninitial offset was accepted")
	}
}

func TestProjectEinoAssistantLiteralReadFileProjectionRequiresTrustedLocalToolName(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordObservedReadFileVersion("src/read.ts", "sha256:read")
	state.NextModelCallOrdinal()
	read := projectEinoAssistantReadFileJSONForTest(t, "src/read.ts", "const value = 1;\n", "sha256:read", int64(len("const value = 1;\n")), true, false, false, 1, 2000)
	middleware := projectEinoAssistantModelToolOutputMiddlewareForModel(state).(*projectEinoAssistantModelToolOutputMiddleware)
	for _, name := range []string{"read_file_extra", "provider__read_file", "mcp_read_file"} {
		wrapped, err := middleware.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...einotool.Option) (string, error) {
			return read, nil
		}, &adk.ToolContext{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		got, err := wrapped(context.Background(), `{}`)
		if err != nil || got != read || strings.HasPrefix(got, projectEinoAssistantLiteralReadFileHeader) {
			t.Fatalf("tool %q received source projection = %q, err=%v", name, got, err)
		}
	}
	if _, ok := state.ModelVisibleReadFileVersion("src/read.ts"); ok {
		t.Fatal("non-local tool result created read proof")
	}
	wrapped, err := middleware.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...einotool.Option) (string, error) {
		return read, nil
	}, &adk.ToolContext{Name: projectToolReadFile})
	if err != nil {
		t.Fatal(err)
	}
	got, err := wrapped(context.Background(), `{}`)
	if err != nil || !strings.HasPrefix(got, projectEinoAssistantLiteralReadFileHeader) {
		t.Fatalf("exact local read_file did not receive literal source, err=%v", err)
	}
	if proof, ok := state.ModelVisibleReadFileVersion("src/read.ts"); !ok || proof.Version != "sha256:read" {
		t.Fatalf("model-visible proof from exact local read_file = %#v, ok=%v", proof, ok)
	}
}

func TestProjectEinoAssistantADKModelReceivesLiteralReadAndLedgerKeepsJSON(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "literal-read-adk-round-trip")
	content := "const route = \"\\path\";\n"
	writeTestWorkspaceFiles(t, context.Background(), h.workspaces, h.req.WorkspaceScope, []workspace.File{{Path: "src/route.ts", Content: content}})
	readCall := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "read-literal", Type: "function", Function: schema.FunctionCall{
			Name:      projectToolReadFile,
			Arguments: `{"file_path":"src/route.ts","offset":1,"limit":2000}`,
		},
	}})
	var modelReceipt projectEinoAssistantLiteralReadFileOutput
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: readCall},
		{Message: schema.AssistantMessage("Source checked.", nil), Inspect: func(input []*schema.Message) {
			chatMessages := projectEinoMessagesToChat(input)
			for _, message := range chatMessages {
				if message.Role != string(schema.Tool) || message.Name != projectToolReadFile {
					continue
				}
				var parsed bool
				modelReceipt, parsed = projectEinoAssistantParseLiteralReadFileOutput(message.Content)
				if !parsed {
					t.Fatalf("actual next model input did not contain a literal read_file receipt: %s", message.Content)
				}
				return
			}
			t.Fatal("actual next model input omitted the read_file tool result")
		}},
	}}
	readTool, ok := h.server.projectAssistantToolRegistry().Get(projectToolReadFile)
	if !ok {
		t.Fatal("read_file tool is not registered")
	}
	engine := projectEinoAssistantEngine{
		server: h.server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(h.server, readTool, req, state)}, nil
		},
	}
	result, err := engine.StreamProjectAssistant(context.Background(), h.req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "Source checked." || len(model.Inputs) != 2 {
		t.Fatalf("ADK result=%q model calls=%d; want two calls and the final response", result.Content, len(model.Inputs))
	}
	if modelReceipt.content != content || strings.Count(modelReceipt.content, `\path`) != 1 || strings.Contains(modelReceipt.content, `\\path`) {
		t.Fatalf("actual model source bytes = %q; want one literal backslash: %#v", modelReceipt.content, modelReceipt)
	}
	events, err := h.messages.ListAssistantRunEvents(context.Background(), h.scope, h.req.AssistantRun.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != projectAssistantRunToolResultEventType || event.CallID != "read-literal" {
			continue
		}
		var outcome projectAssistantRunToolResultPayload
		if err := json.Unmarshal(event.Payload, &outcome); err != nil {
			t.Fatalf("decode durable tool result event: %v", err)
		}
		var receipt struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(outcome.Result), &receipt); err != nil {
			t.Fatalf("decode durable JSON read receipt once: %v", err)
		}
		if receipt.Path != "src/route.ts" || receipt.Content != content || strings.Count(receipt.Content, `\path`) != 1 || strings.Contains(receipt.Content, `\\path`) {
			t.Fatalf("durable JSON source bytes = path %q content %q", receipt.Path, receipt.Content)
		}
		return
	}
	t.Fatalf("durable ledger omitted result for read-literal; events=%#v", events)
}

func TestProjectEinoAssistantLiteralBinaryReadReceiptPreservesOnlyDeleteMoveProof(t *testing.T) {
	raw := projectEinoAssistantReadFileJSONForTest(t, "public/image.bin", "", "sha256:binary", 12, true, false, true, 1, 2000)
	projected, ok := projectEinoAssistantProjectModelReadFileOutput(raw, projectEinoAssistantModelToolOutputMaxBytes)
	if !ok {
		t.Fatal("binary receipt was not projected")
	}
	parsed, ok := projectEinoAssistantParseLiteralReadFileOutput(projected)
	if !ok || !parsed.binary || !parsed.complete || parsed.version != "sha256:binary" || parsed.shown != "" {
		t.Fatalf("binary read receipt = %#v, parsed=%v", parsed, ok)
	}
	if _, ok := projectEinoAssistantCompleteBinaryReadFileReceiptKey(projected); !ok {
		t.Fatal("complete literal binary receipt did not preserve its version")
	}
	if _, ok := projectEinoAssistantCompleteReadFileReceiptKey(projected); ok {
		t.Fatal("binary read receipt authorized text replacement")
	}
}
