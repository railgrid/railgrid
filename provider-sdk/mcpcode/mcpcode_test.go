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

package mcpcode

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCallCodeRoundTrip(t *testing.T) {
	code, err := CallCode("infra__provision", map[string]any{"size": 3, "name": "a) => {", "u": " "})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code, `() => railgrid.call("infra__provision", {`) || !strings.HasSuffix(code, ")") {
		t.Fatalf("code = %s", code)
	}
	tool, args, ok := ParseCallCode(code)
	if !ok || tool != "infra__provision" {
		t.Fatalf("parse: ok=%v tool=%q", ok, tool)
	}
	var got map[string]any
	if err := json.Unmarshal(args, &got); err != nil || got["size"] != float64(3) || got["name"] != "a) => {" || got["u"] != " " {
		t.Fatalf("args = %s (%v)", args, err)
	}
	for _, bad := range []string{"", "() => 1", `() => railgrid.call("x")`, `() => railgrid.call("x", [1])`, `railgrid.call("x", {})`} {
		if _, _, ok := ParseCallCode(bad); ok {
			t.Fatalf("ParseCallCode accepted %q", bad)
		}
	}
}

func TestCallCodeArgsForms(t *testing.T) {
	for _, args := range []any{nil, json.RawMessage(nil), json.RawMessage("null"), json.RawMessage(" {} "), []byte(`{}`), map[string]any{}} {
		code, err := CallCode("t", args)
		if err != nil || code != `() => railgrid.call("t", {})` {
			t.Fatalf("CallCode(%T %v) = %q, %v", args, args, code, err)
		}
	}
	for _, args := range []any{json.RawMessage(`[1]`), json.RawMessage(`nope`), 42, "s"} {
		if _, err := CallCode("t", args); err == nil {
			t.Fatalf("CallCode accepted args %v", args)
		}
	}
	if _, err := CallCode(" ", nil); err == nil {
		t.Fatal("CallCode accepted an empty tool name")
	}
}

func TestEncodeDecodeCallResult(t *testing.T) {
	inner := json.RawMessage(`{"content":[{"type":"text","text":"ran"},{"type":"image","data":"AA==","mimeType":"image/png"}],"structuredContent":{"id":9007199254740993},"_meta":{"railgrid":{"sources":[]}}}`)
	result, err := EncodeCallResult("infra__provision", inner)
	if err != nil {
		t.Fatal(err)
	}
	call, err := DecodeCallResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if !call.OK || call.IsError || call.Text != "ran" || string(call.Data) != `{"id":9007199254740993}` || len(call.Content) != 2 || call.Meta["railgrid"] == nil {
		t.Fatalf("call = %+v", call)
	}
	var rt struct {
		Content           []json.RawMessage `json:"content"`
		StructuredContent json.RawMessage   `json:"structuredContent"`
		IsError           bool              `json:"isError"`
		Meta              map[string]any    `json:"_meta"`
	}
	if err := json.Unmarshal(call.ToolResultJSON(), &rt); err != nil {
		t.Fatal(err)
	}
	if len(rt.Content) != 2 || string(rt.StructuredContent) != `{"id":9007199254740993}` || rt.IsError || rt.Meta["railgrid"] == nil {
		t.Fatalf("ToolResultJSON = %s", call.ToolResultJSON())
	}
	env, err := DecodeEnvelope(result)
	if err != nil || len(env.Calls) != 1 || env.Calls[0].Tool != "infra__provision" || !env.Calls[0].OK {
		t.Fatalf("envelope = %+v, %v", env, err)
	}
}

func TestDecodeCallResultToolError(t *testing.T) {
	result, err := EncodeCallResult("t", json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"template not found"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	call, err := DecodeCallResult(result)
	if err != nil || call.OK || !call.IsError || call.Text != "template not found" || call.Data != nil {
		t.Fatalf("call = %+v, err = %v", call, err)
	}
}

func TestDecodeCallResultRefusals(t *testing.T) {
	var ce *CallError
	_, err := DecodeCallResult(EncodeRunError(RefusalMessage("nope", "unknown")))
	if !errors.As(err, &ce) || !ce.UnknownTool() || ce.ConsentRequired() {
		t.Fatalf("unknown: err = %v", err)
	}
	_, err = DecodeCallResult(EncodeRunError(RefusalMessage("code__branches", "consent")))
	if !errors.As(err, &ce) || !ce.ConsentRequired() {
		t.Fatalf("consent: err = %v", err)
	}
	var re *RunError
	_, err = DecodeCallResult(EncodeRunError("code did not compile: SyntaxError"))
	if !errors.As(err, &re) || errors.As(err, &ce) {
		t.Fatalf("compile failure: err = %v", err)
	}
}

func TestDecodeCatalog(t *testing.T) {
	tools, err := DecodeCatalog(EncodeCatalogResult([]CatalogTool{
		{Name: "code__branches", Description: "list", InputSchema: json.RawMessage(`{"type":"object"}`), Meta: map[string]any{"railgrid": map[string]any{"action": map[string]any{"id": "branches/v1"}}}, ConsentRequired: true},
	}))
	if err != nil || len(tools) != 1 || tools[0].Name != "code__branches" || !tools[0].ConsentRequired || tools[0].Meta["railgrid"] == nil {
		t.Fatalf("tools = %+v, err = %v", tools, err)
	}
	empty, err := DecodeCatalog(EncodeCatalogResult(nil))
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty = %v, %v", empty, err)
	}
}

func TestDecodeEnvelopeFromTextOnly(t *testing.T) {
	// A client that dropped structuredContent still has the envelope in text.
	result := json.RawMessage(`{"content":[{"type":"text","text":"{\"result\":[\"a\"]}"}]}`)
	env, err := DecodeEnvelope(result)
	if err != nil || string(env.Result) != `["a"]` {
		t.Fatalf("env = %+v, %v", env, err)
	}
}

func TestParams(t *testing.T) {
	p, err := CallParams("t", map[string]any{"a": 1})
	if err != nil || p["name"] != ToolExecute {
		t.Fatalf("CallParams = %v, %v", p, err)
	}
	if CatalogParams()["name"] != ToolSearch {
		t.Fatal("CatalogParams name")
	}
}
