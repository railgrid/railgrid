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

package mcpaggregate

// Code mode: the aggregate's tool surface is two tools whose schemas never
// change, however many providers a tenant enables.
//
//	search(code)   runs JavaScript against `spec` — the federated tool
//	               catalog (names, descriptions, input schemas, _meta) plus
//	               every provider's declared capabilities — so the model
//	               narrows hundreds of tools down to the few it needs without
//	               any of them entering its context.
//	execute(code)  runs JavaScript with `railgrid.call(name, args)`, which
//	               invokes a federated tool exactly as a proxied tools/call
//	               would — same client, same credential rule, same caller —
//	               so one execution can chain, paginate and filter
//	               server-side and return only the answer.
//
// What does NOT change: who the call is made as. railgrid.call dispatches to
// the same providerMCPClient discovery used, so a platform provider still
// sees the caller's bearer and an org-owned provider still sees a delegated
// token, and the provider still authorizes by the caller's RBAC in the tenant
// workspace. The script itself holds no credential (see sandbox.go).
//
// The one thing a script cannot do is invoke a tool backing an action whose
// catalog entry requires consent: there is no human in the middle of a
// running function. Those tools — and only those — are registered directly
// on the aggregate as `<provider>__<tool>` (registerDirectTools), where the
// client can surface the consent prompt.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// codeModeMaxCalls caps railgrid.call invocations in one execute run.
	codeModeMaxCalls = 32
	// codeModeToolSearch and codeModeToolExecute are the two tool names.
	codeModeToolSearch  = "search"
	codeModeToolExecute = "execute"
)

// catalogTool is one federated tool as the sandbox's `spec.tools[]` shows
// it. The JSON fields are the model-facing projection — the same name,
// schema and _meta a proxy tool for it would carry; the unexported fields are
// what railgrid.call needs to dispatch.
type catalogTool struct {
	Name                string               `json:"name"`
	Provider            string               `json:"provider"`
	ProviderDisplayName string               `json:"providerDisplayName,omitempty"`
	Title               string               `json:"title,omitempty"`
	Description         string               `json:"description,omitempty"`
	InputSchema         json.RawMessage      `json:"inputSchema,omitempty"`
	Annotations         *mcp.ToolAnnotations `json:"annotations,omitempty"`
	// Meta is proxyToolMeta: the registry-resolved coordinate under
	// "railgrid" (never the provider's raw claim) and the namespaced app
	// resource under "ui", or absent.
	Meta mcp.Meta `json:"_meta,omitempty"`
	// ConsentRequired mirrors the declared action's consent flag. Such a tool
	// is refused by railgrid.call and listed directly on the aggregate.
	ConsentRequired bool `json:"consentRequired,omitempty"`

	provider ProviderTarget
	client   *providerMCPClient
	tool     discoveredTool
}

// toolCatalog is the dispatch table behind railgrid.call and the source of
// spec.tools.
type toolCatalog struct {
	tools  []*catalogTool
	byName map[string]*catalogTool
}

// buildToolCatalog projects the discovered providers into the catalog, in
// provider enumeration order then provider tool order. App-only tools (not
// model-visible) are left out, so they can be neither seen nor called.
func buildToolCatalog(found []*providerTools) *toolCatalog {
	cat := &toolCatalog{byName: map[string]*catalogTool{}, tools: []*catalogTool{}}
	for _, r := range found {
		if r == nil {
			continue
		}
		for _, t := range r.tools {
			if !appToolModelVisible(t) {
				continue
			}
			ct := &catalogTool{
				Name:                r.provider.Name + "__" + t.Name,
				Provider:            r.provider.Name,
				ProviderDisplayName: r.provider.DisplayName,
				Title:               t.Title,
				Description:         t.Description,
				InputSchema:         t.InputSchema,
				Annotations:         t.Annotations,
				Meta:                proxyToolMeta(r.provider, t),
				provider:            r.provider,
				client:              r.client,
				tool:                t,
			}
			if actionID, _ := railgridToolClaim(t.Meta); actionID != "" {
				if a, ok := r.provider.declaredAction(actionID); ok {
					ct.ConsentRequired = a.Consent.Required
				}
			}
			if _, dup := cat.byName[ct.Name]; dup {
				continue
			}
			cat.byName[ct.Name] = ct
			cat.tools = append(cat.tools, ct)
		}
	}
	return cat
}

// codeModeSpec is the `spec` global: everything a script may learn about
// the tenant's surface. It is a declaration, like the capabilities resource:
// no URL, no credential, no transport handle.
type codeModeSpec struct {
	Tenant    string                 `json:"tenant"`
	MCPServer string                 `json:"mcpServer"`
	Providers []ProviderCapabilities `json:"providers"`
	Tools     []*catalogTool         `json:"tools"`
}

// codeModeCall is the audit record of one railgrid.call, returned to the
// client alongside the result so every provider invocation a script made is
// visible, not just the script's answer.
type codeModeCall struct {
	Tool       string `json:"tool"`
	OK         bool   `json:"ok"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

// codeModeEnvelope is the structured result of both tools.
type codeModeEnvelope struct {
	Result        json.RawMessage `json:"result"`
	Logs          []string        `json:"logs,omitempty"`
	LogsTruncated bool            `json:"logsTruncated,omitempty"`
	Calls         []codeModeCall  `json:"calls,omitempty"`
	Error         string          `json:"error,omitempty"`
}

// codeArgSchema is the one input both tools take.
var codeArgSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"code": map[string]any{
			"type":        "string",
			"description": "A JavaScript function expression, e.g. `async () => { ...; return value; }`. It is called with no arguments; its return value (awaited if a promise) is the result.",
		},
	},
	"required":             []string{"code"},
	"additionalProperties": false,
}

func codeModeTools(lim sandboxLimits) (search, execute *mcp.Tool) {
	search = &mcp.Tool{
		Name:  codeModeToolSearch,
		Title: "Search the tenant's tools and capabilities with code",
		Description: "Discover what this tenant's railgrid providers can do by running JavaScript against `spec`, " +
			"instead of reading a tool list. `code` is a function expression, e.g. " +
			"`() => spec.tools.filter(t => t.provider === 'edges').map(t => t.name)`. " +
			"`spec.tools[]` is every tool `execute` may call: {name, provider, title, description, inputSchema, annotations, _meta, consentRequired}; " +
			"`_meta.railgrid` names the declared action or verb a tool implements. " +
			"`spec.providers[]` is each provider's declared contract: actions[] {id, displayName, description, readOnly, risk, consent, schemaDigest} and data-plane verbs[] {coordinate, description, stream, readOnly}. " +
			"Typical flow: list `spec.providers.map(p => p.provider)`, filter tools by provider or keyword in name/description, then return one tool's `inputSchema` before calling it. " +
			fmt.Sprintf("Sandboxed: no network, filesystem or timers; the result is capped at %d KiB, so return only what you need.", lim.MaxResultBytes>>10),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		InputSchema: codeArgSchema,
	}
	execute = &mcp.Tool{
		Name:  codeModeToolExecute,
		Title: "Run code that calls the tenant's tools",
		Description: "Run JavaScript that calls this tenant's railgrid tools through `railgrid.call(name, args)`, so several calls, pagination and filtering happen in one step. " +
			"`code` is a function expression, usually `async () => { ... }`; its return value is the result. " +
			"`railgrid.call(name, args)` invokes a tool from `spec.tools` by its exact `name` (the same names `search` shows), as you, with your permissions, and returns {ok, text, data, raw, content}: " +
			"`data` is the tool's structured result when it has one, `raw` that result's exact JSON text (use it when an ID is above 2^53), `text` its text content, `ok` false when the tool reported an error. " +
			"It throws on an unknown tool, a refused call or a provider failure, so use try/catch to continue past one. `spec` is available here too. " +
			fmt.Sprintf("Limits: %d calls per run, %s wall-clock including provider time, result capped at %d KiB; no network, filesystem or timers. ", codeModeMaxCalls, lim.MaxDuration, lim.MaxResultBytes>>10) +
			"A tool with consentRequired cannot be called from here; it is listed as an ordinary tool so the consent prompt can be shown.",
		InputSchema: codeArgSchema,
	}
	return search, execute
}

// codeModeInstructions is the aggregate's server-level guidance.
func codeModeInstructions(mcpServer, cluster string) string {
	return fmt.Sprintf(
		"You are connected to the railgrid aggregate MCP endpoint %q in tenant workspace %q.\n\n"+
			"It federates every enabled railgrid provider in this tenant (for example infrastructure, code and edge access) behind two tools. "+
			"`search` runs JavaScript against `spec`, the catalog of every provider tool (name, description, inputSchema) and every declared capability; "+
			"`execute` runs JavaScript that calls those tools with `railgrid.call(name, args)`, chaining and filtering in code so only the answer comes back. "+
			"Tool names are \"<provider>__<tool>\"; the catalog reflects which providers are enabled and healthy right now. "+
			"A tool whose action needs human consent is listed directly instead and is called as an ordinary tool.",
		mcpServer, cluster,
	)
}

// registerCodeModeTools adds search and execute to srv for this request's
// catalog and spec.
func registerCodeModeTools(srv *mcp.Server, log logr.Logger, cat *toolCatalog, spec codeModeSpec) {
	lim := defaultSandboxLimits
	specJSON, err := json.Marshal(spec)
	if err != nil {
		log.Error(err, "code mode: encoding spec")
		specJSON = []byte(`{"tenant":"","mcpServer":"","providers":[],"tools":[]}`)
	}
	searchTool, executeTool := codeModeTools(lim)

	srv.AddTool(searchTool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		code, err := decodeCodeArgument(req.Params.Arguments)
		if err != nil {
			return codeModeResult(sandboxOutcome{}, nil, err)
		}
		out, err := runSandbox(ctx, code, lim, func(_ context.Context, rt *goja.Runtime, js *jsBridge) error {
			return installSpec(rt, js, specJSON)
		})
		return codeModeResult(out, nil, err)
	})

	srv.AddTool(executeTool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		code, err := decodeCodeArgument(req.Params.Arguments)
		if err != nil {
			return codeModeResult(sandboxOutcome{}, nil, err)
		}
		var calls []codeModeCall
		out, err := runSandbox(ctx, code, lim, func(ctx context.Context, rt *goja.Runtime, js *jsBridge) error {
			if err := installSpec(rt, js, specJSON); err != nil {
				return err
			}
			return installRailgrid(ctx, rt, js, log, cat, &calls)
		})
		return codeModeResult(out, calls, err)
	})
}

// registerDirectTools registers what code mode cannot carry: each provider's
// MCP app resources (ui:// HTML read through the federation client), and a
// proxy tool for every catalog entry whose declared action requires consent.
// A human has to approve those, so they are called directly, where the
// client can show the prompt; railgrid.call refuses them.
func registerDirectTools(ctx context.Context, srv *mcp.Server, log logr.Logger, found []*providerTools, cat *toolCatalog, readSlots chan struct{}) {
	for _, r := range found {
		if r == nil || len(r.tools) == 0 {
			continue
		}
		registerAppResources(ctx, srv, r, log, readSlots)
	}
	for _, ct := range cat.tools {
		if !ct.ConsentRequired {
			continue
		}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Info("code mode: AddTool panic recovered", "provider", ct.Provider, "tool", ct.tool.Name, "panic", fmt.Sprint(rec))
				}
			}()
			registerOneProxyTool(srv, ct.client, ct.provider, ct.tool)
		}()
	}
}

// decodeCodeArgument reads the one argument both tools take.
func decodeCodeArgument(raw json.RawMessage) (string, error) {
	var args struct {
		Code string `json:"code"`
	}
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&args); err != nil {
			return "", scriptErrorf("arguments: %v (expected {\"code\": \"<javascript function expression>\"})", err)
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return "", scriptErrorf("arguments: expected one JSON object")
		}
	}
	return args.Code, nil
}

// installSpec sets the `spec` global from its JSON encoding.
func installSpec(rt *goja.Runtime, js *jsBridge, specJSON []byte) error {
	v, err := js.fromJSON(specJSON)
	if err != nil {
		return err
	}
	return rt.Set("spec", v)
}

// installRailgrid sets the `railgrid` global with its `call` binding.
//
// The binding is synchronous from the script's point of view (the provider
// round trip happens inside the Go call), which is what lets an async
// function that awaits it settle without an event loop. It throws a JS
// Error — catchable — for every refusal and failure, and records every
// attempt that reached a provider in calls so the client sees what the
// script did.
func installRailgrid(ctx context.Context, rt *goja.Runtime, js *jsBridge, log logr.Logger, cat *toolCatalog, calls *[]codeModeCall) error {
	throw := func(err error) {
		panic(rt.NewGoError(err))
	}
	obj := rt.NewObject()
	if err := obj.Set("call", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		if len(*calls) >= codeModeMaxCalls {
			throw(fmt.Errorf("railgrid.call(%q): the %d-call limit for one execute run is reached", name, codeModeMaxCalls))
		}
		ct, ok := cat.byName[name]
		if !ok {
			throw(fmt.Errorf("railgrid.call(%q): unknown tool; pick a name from spec.tools (use search to find it)", name))
		}
		if ct.ConsentRequired {
			throw(fmt.Errorf("railgrid.call(%q): this tool backs an action that requires human consent and cannot run inside a script; call it directly as an MCP tool", name))
		}
		args, err := exportArgs(js, call.Argument(1))
		if err != nil {
			throw(fmt.Errorf("railgrid.call(%q): %w", name, err))
		}
		if err := ctx.Err(); err != nil {
			throw(fmt.Errorf("railgrid.call(%q): run is over: %w", name, err))
		}

		start := time.Now()
		res, err := ct.client.callTool(ctx, ct.provider.MCPURL, ct.tool.Name, args)
		rec := codeModeCall{Tool: name, DurationMs: time.Since(start).Milliseconds()}
		if err != nil {
			rec.Error = err.Error()
			*calls = append(*calls, rec)
			log.V(1).Info("code mode: provider call failed", "provider", ct.Provider, "tool", ct.tool.Name, "err", err.Error())
			throw(fmt.Errorf("railgrid.call(%q): provider %q tool %q: %w", name, ct.Provider, ct.tool.Name, err))
		}
		rec.OK = !res.IsError
		*calls = append(*calls, rec)
		log.V(1).Info("code mode: provider call", "provider", ct.Provider, "tool", ct.tool.Name, "ok", rec.OK, "ms", rec.DurationMs)

		raw, err := json.Marshal(callPayload(res))
		if err != nil {
			throw(fmt.Errorf("railgrid.call(%q): encoding result: %w", name, err))
		}
		v, err := js.fromJSON(raw)
		if err != nil {
			throw(fmt.Errorf("railgrid.call(%q): %w", name, err))
		}
		return v
	}); err != nil {
		return err
	}
	return rt.Set("railgrid", obj)
}

// exportArgs turns the script's args value into the tool's argument map
// through JSON, with UseNumber so integers reach the provider as integers,
// exactly as the proxy tool decodes arguments off the wire. (A JavaScript
// number is a float64, so anything above 2^53 has to be a string in the
// script; that is the language, not the bridge.)
func exportArgs(js *jsBridge, v goja.Value) (map[string]any, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return map[string]any{}, nil
	}
	raw, err := js.toJSON(v)
	if err != nil {
		return nil, fmt.Errorf("args are not JSON-serialisable: %w", err)
	}
	if raw == nil {
		return map[string]any{}, nil
	}
	var args map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&args); err != nil || args == nil {
		return nil, fmt.Errorf("args must be a JSON object")
	}
	return args, nil
}

// callResultPayload is what railgrid.call returns to the script. Raw is the
// structured result's exact JSON text: JSON.parse turns an integer above
// 2^53 into a rounded double, so a script that must forward such an ID
// verbatim takes it from Raw (or from Text) rather than from Data.
type callResultPayload struct {
	OK      bool          `json:"ok"`
	IsError bool          `json:"isError"`
	Text    string        `json:"text"`
	Data    any           `json:"data,omitempty"`
	Raw     string        `json:"raw,omitempty"`
	Content []mcp.Content `json:"content"`
	// Meta is the tool result's own _meta (a provider's source references
	// and presentation hints), carried through untouched so a consumer
	// calling through execute sees exactly what a direct call returns.
	Meta mcp.Meta `json:"_meta,omitempty"`
}

func callPayload(res *mcp.CallToolResult) callResultPayload {
	p := callResultPayload{OK: !res.IsError, IsError: res.IsError, Content: res.Content, Data: res.StructuredContent, Meta: res.Meta}
	if p.Content == nil {
		p.Content = []mcp.Content{}
	}
	if raw, ok := res.StructuredContent.(json.RawMessage); ok {
		p.Raw = string(raw)
	}
	var texts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	p.Text = strings.Join(texts, "\n")
	return p
}

// codeModeResult renders a run as a tool result. A sandboxError (the
// script's own failure) is an IsError result carrying the message and
// whatever the script logged and called before failing, so the model can
// correct itself; anything else is an internal error and fails the call.
func codeModeResult(out sandboxOutcome, calls []codeModeCall, err error) (*mcp.CallToolResult, error) {
	env := codeModeEnvelope{Result: out.Result, Logs: out.Logs, LogsTruncated: out.LogsTruncated, Calls: calls}
	if env.Result == nil {
		env.Result = json.RawMessage("null")
	}
	isErr := false
	if err != nil {
		var se *sandboxError
		if !errors.As(err, &se) {
			return nil, err
		}
		env.Error = se.Error()
		isErr = true
	}
	text, mErr := json.Marshal(env)
	if mErr != nil {
		return nil, fmt.Errorf("code mode: encoding result: %w", mErr)
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(text)}},
		StructuredContent: json.RawMessage(text),
		IsError:           isErr,
	}, nil
}
