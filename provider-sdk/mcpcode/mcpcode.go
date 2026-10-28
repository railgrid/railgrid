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

// Package mcpcode is the client side of the hub aggregate's code mode.
//
// The aggregate MCP endpoint serves two tools, `search` and `execute`, and
// reaches every federated provider tool through JavaScript the caller sends
// (pkg/hub/mcpaggregate/codemode.go in the hub). A program that wants to call
// ONE tool by name does not want to write JavaScript; this package writes the
// one line it needs — `() => railgrid.call(<tool>, <args>)` — and decodes the
// envelope that comes back, so a machine consumer (the CLI, App Studio, the
// agents provider) speaks the same wire as a model would, with no second
// surface on the hub.
//
// Numbers cross into the sandbox as JavaScript numbers (float64): an integer
// above 2^53 in args must be a string. On the way back, CallResult.Data is the
// provider's exact structured JSON, taken from the `raw` field the aggregate
// adds for this purpose, so IDs survive the round trip.
//
// A tool whose declared action requires consent is refused by railgrid.call
// and listed directly on the aggregate instead; CallError.ConsentRequired
// tells a consumer to fall back to a plain tools/call for it.
package mcpcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// ToolSearch is the aggregate's catalog tool.
	ToolSearch = "search"
	// ToolExecute is the aggregate's execution tool.
	ToolExecute = "execute"

	// CatalogCode is the `search` program that returns the federated
	// catalog in the shape DecodeCatalog reads: one entry per tool a script
	// may call, with the same name, schema and _meta a proxy tool for it
	// would carry.
	CatalogCode = `() => spec.tools.map(t => ({name: t.name, title: t.title, description: t.description, inputSchema: t.inputSchema, annotations: t.annotations, _meta: t._meta, consentRequired: t.consentRequired}))`

	callPrefix = "() => railgrid.call("
)

// CallCode returns the `execute` program that invokes one federated tool with
// args and returns what railgrid.call returned. args is a JSON object: a Go
// value that marshals to one, a json.RawMessage, or nil for no arguments.
func CallCode(tool string, args any) (string, error) {
	if strings.TrimSpace(tool) == "" {
		return "", errors.New("mcpcode: tool name is empty")
	}
	name, err := json.Marshal(tool)
	if err != nil {
		return "", err
	}
	a, err := marshalArgs(args)
	if err != nil {
		return "", err
	}
	return callPrefix + string(name) + ", " + string(a) + ")", nil
}

// ParseCallCode is the inverse of CallCode, for fakes and tests that stand in
// for the aggregate. ok is false for any other program.
func ParseCallCode(code string) (tool string, args json.RawMessage, ok bool) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, callPrefix) || !strings.HasSuffix(code, ")") {
		return "", nil, false
	}
	inner := code[len(callPrefix) : len(code)-1]
	dec := json.NewDecoder(strings.NewReader(inner))
	if err := dec.Decode(&tool); err != nil || tool == "" {
		return "", nil, false
	}
	rest := strings.TrimSpace(inner[dec.InputOffset():])
	if !strings.HasPrefix(rest, ",") {
		return "", nil, false
	}
	rest = strings.TrimSpace(rest[1:])
	if !json.Valid([]byte(rest)) || !strings.HasPrefix(rest, "{") {
		return "", nil, false
	}
	return tool, json.RawMessage(rest), true
}

// CallParams returns tools/call params invoking tool through execute:
// {"name": "execute", "arguments": {"code": ...}}.
func CallParams(tool string, args any) (map[string]any, error) {
	code, err := CallCode(tool, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": ToolExecute, "arguments": map[string]any{"code": code}}, nil
}

// CatalogParams returns tools/call params that fetch the catalog through
// search.
func CatalogParams() map[string]any {
	return map[string]any{"name": ToolSearch, "arguments": map[string]any{"code": CatalogCode}}
}

func marshalArgs(args any) ([]byte, error) {
	switch v := args.(type) {
	case nil:
		return []byte("{}"), nil
	case json.RawMessage:
		t := bytes.TrimSpace(v)
		if len(t) == 0 || bytes.Equal(t, []byte("null")) {
			return []byte("{}"), nil
		}
		if !json.Valid(t) || t[0] != '{' {
			return nil, errors.New("mcpcode: args must be a JSON object")
		}
		return t, nil
	case []byte:
		return marshalArgs(json.RawMessage(v))
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("mcpcode: encoding args: %w", err)
		}
		if len(b) == 0 || b[0] != '{' {
			return nil, errors.New("mcpcode: args must be a JSON object")
		}
		return b, nil
	}
}

// CatalogTool is one federated tool as the aggregate's catalog shows it.
type CatalogTool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
	// Meta is the tool's _meta: the registry-resolved coordinate under
	// "railgrid", the namespaced app resource under "ui".
	Meta map[string]any `json:"_meta,omitempty"`
	// ConsentRequired marks a tool railgrid.call refuses; it is listed
	// directly on the aggregate and called with a plain tools/call.
	ConsentRequired bool `json:"consentRequired,omitempty"`
}

// Call is the aggregate's audit record of one railgrid.call.
type Call struct {
	Tool       string `json:"tool"`
	OK         bool   `json:"ok"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

// Envelope is the structured result of search and execute.
type Envelope struct {
	Result        json.RawMessage `json:"result"`
	Logs          []string        `json:"logs,omitempty"`
	LogsTruncated bool            `json:"logsTruncated,omitempty"`
	Calls         []Call          `json:"calls,omitempty"`
	Error         string          `json:"error,omitempty"`
}

// RunError is a failed run: the script did not compile, threw, or hit a
// limit. The aggregate reports it as an isError tool result; Message is
// what it said.
type RunError struct {
	Message string
}

func (e *RunError) Error() string { return e.Message }

// CallError is a railgrid.call that threw inside a CallCode program: an
// unknown tool, a refusal, or a provider failure.
type CallError struct {
	Message string
}

func (e *CallError) Error() string { return e.Message }

// ConsentRequired reports a refusal because the tool's declared action needs
// human consent; the caller should invoke it with a plain tools/call.
func (e *CallError) ConsentRequired() bool {
	return strings.Contains(e.Message, "requires human consent")
}

// UnknownTool reports that no federated tool of that name exists.
func (e *CallError) UnknownTool() bool { return strings.Contains(e.Message, "unknown tool") }

// toolResult is the generic MCP tools/call result shape this package reads
// and writes.
type toolResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           bool              `json:"isError,omitempty"`
	Meta              map[string]any    `json:"_meta,omitempty"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (r toolResult) texts() []string {
	var out []string
	for _, c := range r.Content {
		var tc textContent
		if json.Unmarshal(c, &tc) == nil && tc.Type == "text" && tc.Text != "" {
			out = append(out, tc.Text)
		}
	}
	return out
}

// DecodeEnvelope reads the tools/call result of a search or execute call (the
// JSON-RPC `result` object). A failed run is returned as *RunError, with the
// envelope's logs and calls still filled in.
func DecodeEnvelope(result json.RawMessage) (Envelope, error) {
	var tr toolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		return Envelope{}, fmt.Errorf("mcpcode: decoding tool result: %w", err)
	}
	payload := bytes.TrimSpace(tr.StructuredContent)
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		texts := tr.texts()
		if len(texts) == 0 {
			if tr.IsError {
				return Envelope{}, &RunError{Message: "the aggregate reported an error without a message"}
			}
			return Envelope{}, errors.New("mcpcode: tool result carries no envelope")
		}
		payload = []byte(texts[0])
	}
	var env Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		if tr.IsError {
			return Envelope{}, &RunError{Message: string(payload)}
		}
		return Envelope{}, fmt.Errorf("mcpcode: decoding envelope: %w", err)
	}
	if tr.IsError {
		msg := env.Error
		if msg == "" {
			msg = string(payload)
		}
		return env, &RunError{Message: msg}
	}
	return env, nil
}

// DecodeCatalog reads the result of a CatalogParams call.
func DecodeCatalog(result json.RawMessage) ([]CatalogTool, error) {
	env, err := DecodeEnvelope(result)
	if err != nil {
		return nil, err
	}
	var tools []CatalogTool
	if err := json.Unmarshal(env.Result, &tools); err != nil {
		return nil, fmt.Errorf("mcpcode: decoding catalog: %w", err)
	}
	if tools == nil {
		tools = []CatalogTool{}
	}
	return tools, nil
}

// CallResult is what railgrid.call returned for the one tool a CallCode
// program invoked.
type CallResult struct {
	// OK is false when the tool reported an error (IsError on its result).
	OK      bool
	IsError bool
	// Text is the tool's text content, joined.
	Text string
	// Data is the tool's structured result as the provider sent it — exact
	// bytes, not a float64 round trip — or empty.
	Data json.RawMessage
	// Content is the tool's MCP content array, item by item.
	Content []json.RawMessage
	// Meta is the tool result's _meta (provider sources, presentation).
	Meta map[string]any
}

// callPayload mirrors the aggregate's callResultPayload.
type callPayload struct {
	OK      bool              `json:"ok"`
	IsError bool              `json:"isError"`
	Text    string            `json:"text"`
	Data    json.RawMessage   `json:"data,omitempty"`
	Raw     string            `json:"raw,omitempty"`
	Content []json.RawMessage `json:"content"`
	Meta    map[string]any    `json:"_meta,omitempty"`
}

// DecodeCallResult reads the result of a CallParams execute call. A
// railgrid.call that threw is returned as *CallError; any other script
// failure as *RunError. A tool that ran but reported an error is returned
// as a CallResult with OK false, for the caller to render.
func DecodeCallResult(result json.RawMessage) (CallResult, error) {
	env, err := DecodeEnvelope(result)
	if err != nil {
		var re *RunError
		if errors.As(err, &re) && strings.Contains(re.Message, "railgrid.call(") {
			return CallResult{}, &CallError{Message: re.Message}
		}
		return CallResult{}, err
	}
	payload := bytes.TrimSpace(env.Result)
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		return CallResult{}, errors.New("mcpcode: execute returned no call result")
	}
	var p callPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return CallResult{}, fmt.Errorf("mcpcode: decoding call result: %w", err)
	}
	data := bytes.TrimSpace(p.Data)
	if p.Raw != "" {
		data = []byte(p.Raw)
	}
	if bytes.Equal(data, []byte("null")) {
		data = nil
	}
	out := CallResult{OK: p.OK, IsError: p.IsError, Text: p.Text, Data: data, Content: p.Content, Meta: p.Meta}
	if out.Content == nil {
		out.Content = []json.RawMessage{}
	}
	return out, nil
}

// ToolResultJSON renders the call as the MCP tools/call result the tool
// itself would have returned: {"content", "structuredContent", "isError",
// "_meta"}. Consumers that already parse that shape feed it this.
func (r CallResult) ToolResultJSON() json.RawMessage {
	tr := toolResult{Content: r.Content, IsError: r.IsError, Meta: r.Meta}
	if tr.Content == nil {
		tr.Content = []json.RawMessage{}
	}
	if len(r.Data) > 0 {
		tr.StructuredContent = r.Data
	}
	b, err := json.Marshal(tr)
	if err != nil {
		return json.RawMessage(`{"content":[],"isError":true}`)
	}
	return b
}

// EncodeCatalogResult renders a search tools/call result carrying tools, the
// way the aggregate does. For fakes that stand in for the aggregate.
func EncodeCatalogResult(tools []CatalogTool) json.RawMessage {
	if tools == nil {
		tools = []CatalogTool{}
	}
	body, _ := json.Marshal(tools)
	return encodeEnvelope(Envelope{Result: body}, false)
}

// EncodeCallResult renders the execute tools/call result for a CallCode
// program whose railgrid.call returned innerResult — the invoked tool's own
// tools/call result JSON — the way the aggregate does.
func EncodeCallResult(tool string, innerResult json.RawMessage) (json.RawMessage, error) {
	var inner toolResult
	if err := json.Unmarshal(innerResult, &inner); err != nil {
		return nil, fmt.Errorf("mcpcode: decoding inner tool result: %w", err)
	}
	if inner.Content == nil {
		inner.Content = []json.RawMessage{}
	}
	p := callPayload{OK: !inner.IsError, IsError: inner.IsError, Text: strings.Join(inner.texts(), "\n"), Content: inner.Content, Meta: inner.Meta}
	if sc := bytes.TrimSpace(inner.StructuredContent); len(sc) > 0 && !bytes.Equal(sc, []byte("null")) {
		p.Data = sc
		p.Raw = string(sc)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return encodeEnvelope(Envelope{Result: body, Calls: []Call{{Tool: tool, OK: p.OK}}}, false), nil
}

// EncodeRunError renders a failed run (isError) with message, the way the
// aggregate does.
func EncodeRunError(message string) json.RawMessage {
	return encodeEnvelope(Envelope{Result: json.RawMessage("null"), Error: message}, true)
}

// RefusalMessage spells the railgrid.call errors the aggregate throws, so a
// fake produces messages CallError classifies the same way.
func RefusalMessage(tool, reason string) string {
	switch reason {
	case "unknown":
		return fmt.Sprintf("script threw: GoError: railgrid.call(%q): unknown tool; pick a name from spec.tools (use search to find it)", tool)
	case "consent":
		return fmt.Sprintf("script threw: GoError: railgrid.call(%q): this tool backs an action that requires human consent and cannot run inside a script; call it directly as an MCP tool", tool)
	default:
		return fmt.Sprintf("script threw: GoError: railgrid.call(%q): %s", tool, reason)
	}
}

func encodeEnvelope(env Envelope, isError bool) json.RawMessage {
	if env.Result == nil {
		env.Result = json.RawMessage("null")
	}
	body, _ := json.Marshal(env)
	text, _ := json.Marshal(textContent{Type: "text", Text: string(body)})
	out, _ := json.Marshal(toolResult{Content: []json.RawMessage{text}, StructuredContent: body, IsError: isError})
	return out
}
