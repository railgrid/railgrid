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

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/mcpcode"
)

// --- sandbox -------------------------------------------------------------

func runJS(t *testing.T, code string, lim sandboxLimits) (sandboxOutcome, error) {
	t.Helper()
	return runSandbox(context.Background(), code, lim, nil)
}

func TestSandboxReturnsValue(t *testing.T) {
	out, err := runJS(t, `() => ({a: 1, b: [1, 2], s: "x"})`, defaultSandboxLimits)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := string(out.Result); got != `{"a":1,"b":[1,2],"s":"x"}` {
		t.Fatalf("result = %s", got)
	}
}

func TestSandboxAsyncFunctionAndConsole(t *testing.T) {
	out, err := runJS(t, `async () => {
		console.log("hi", {x: 1}, 3);
		const v = await Promise.resolve(42);
		return v;
	};`, defaultSandboxLimits)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(out.Result) != "42" {
		t.Fatalf("result = %s, want 42", out.Result)
	}
	if len(out.Logs) != 1 || out.Logs[0] != `hi {"x":1} 3` {
		t.Fatalf("logs = %q", out.Logs)
	}
}

func TestSandboxUndefinedResultIsNull(t *testing.T) {
	out, err := runJS(t, `() => {}`, defaultSandboxLimits)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(out.Result) != "null" {
		t.Fatalf("result = %s, want null", out.Result)
	}
}

func TestSandboxRejectsNonFunction(t *testing.T) {
	_, err := runJS(t, `1 + 1`, defaultSandboxLimits)
	var se *sandboxError
	if !errors.As(err, &se) || !strings.Contains(se.Error(), "must evaluate to a function") {
		t.Fatalf("err = %v, want a sandboxError about a function expression", err)
	}
	_, err = runJS(t, `const x = 1; return x;`, defaultSandboxLimits)
	if !errors.As(err, &se) || !strings.Contains(se.Error(), "did not compile") {
		t.Fatalf("err = %v, want a compile sandboxError", err)
	}
}

func TestSandboxThrownErrorIsReported(t *testing.T) {
	out, err := runJS(t, `() => { console.log("before"); throw new Error("boom"); }`, defaultSandboxLimits)
	var se *sandboxError
	if !errors.As(err, &se) || !strings.Contains(se.Error(), "boom") {
		t.Fatalf("err = %v, want a sandboxError containing boom", err)
	}
	if len(out.Logs) != 1 || out.Logs[0] != "before" {
		t.Fatalf("logs before the throw were lost: %q", out.Logs)
	}
	_, err = runJS(t, `async () => { throw new Error("async boom"); }`, defaultSandboxLimits)
	if !errors.As(err, &se) || !strings.Contains(se.Error(), "async boom") {
		t.Fatalf("async err = %v, want a sandboxError containing async boom", err)
	}
}

func TestSandboxTimeLimitInterruptsScript(t *testing.T) {
	lim := defaultSandboxLimits
	lim.MaxDuration = 100 * time.Millisecond
	start := time.Now()
	_, err := runJS(t, `() => { for (;;) {} }`, lim)
	var se *sandboxError
	if !errors.As(err, &se) || !strings.Contains(se.Error(), "time limit") {
		t.Fatalf("err = %v, want a time-limit sandboxError", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("interrupt took %s", took)
	}
}

func TestSandboxHasNoHostAccess(t *testing.T) {
	out, err := runJS(t, `() => [typeof fetch, typeof require, typeof process, typeof XMLHttpRequest, typeof setTimeout, typeof globalThis.railgrid]`, defaultSandboxLimits)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := string(out.Result); got != `["undefined","undefined","undefined","undefined","undefined","undefined"]` {
		t.Fatalf("host globals leaked into the sandbox: %s", got)
	}
}

func TestSandboxResultCap(t *testing.T) {
	lim := defaultSandboxLimits
	lim.MaxResultBytes = 1024
	_, err := runJS(t, `() => "x".repeat(4096)`, lim)
	var se *sandboxError
	if !errors.As(err, &se) || !strings.Contains(se.Error(), "over the 1024-byte limit") {
		t.Fatalf("err = %v, want a result-size sandboxError", err)
	}
}

func TestSandboxLogCapTruncates(t *testing.T) {
	lim := defaultSandboxLimits
	lim.MaxLogBytes = 10
	out, err := runJS(t, `() => { console.log("12345"); console.log("67890"); console.log("overflow"); return 1 }`, lim)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out.Logs) != 2 || !out.LogsTruncated {
		t.Fatalf("logs = %q truncated=%v, want 2 lines and truncated", out.Logs, out.LogsTruncated)
	}
}

func TestSandboxPendingPromiseIsAnError(t *testing.T) {
	_, err := runJS(t, `() => new Promise(() => {})`, defaultSandboxLimits)
	var se *sandboxError
	if !errors.As(err, &se) || !strings.Contains(se.Error(), "never settled") {
		t.Fatalf("err = %v, want a never-settled sandboxError", err)
	}
}

// --- aggregate -----------------------------------------------------------

// codeModeProvider is a fake provider with two tools: provision (plain) and
// branches, which claims the declared action branches/v1 — one the catalog
// marks consent-required. tools/call echoes the tool name and arguments.
type codeModeProvider struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []struct {
		Name string
		Args json.RawMessage
	}
}

func newCodeModeProvider(t *testing.T) *codeModeProvider {
	t.Helper()
	p := &codeModeProvider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[
				{"name":"provision","description":"make a thing","inputSchema":{"type":"object","properties":{"size":{"type":"integer"}}}},
				{"name":"branches","description":"list branches","inputSchema":{"type":"object"},"_meta":{"railgrid":{"action":"branches/v1"}}},
				{"name":"items","description":"page of items","inputSchema":{"type":"object","properties":{"page":{"type":"integer"}}}}
			]}}`))
		case "tools/call":
			p.mu.Lock()
			p.calls = append(p.calls, struct {
				Name string
				Args json.RawMessage
			}{req.Params.Name, req.Params.Arguments})
			n := len(p.calls)
			p.mu.Unlock()
			var page struct {
				Page int `json:"page"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &page)
			body, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{
					"content":           []map[string]any{{"type": "text", "text": "ran " + req.Params.Name}},
					"structuredContent": map[string]any{"tool": req.Params.Name, "args": req.Params.Arguments, "call": n, "nextPage": page.Page + 1, "bigID": json.Number("9007199254740993")},
					"_meta":             map[string]any{"railgrid": map[string]any{"sources": []any{map[string]any{"object": map[string]any{"uid": "u1"}}}}},
				},
			})
			_, _ = w.Write(body)
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *codeModeProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func codeModeHandler(t *testing.T, p *codeModeProvider) http.Handler {
	t.Helper()
	return New(Options{
		Providers: func(context.Context, Caller) []ProviderTarget {
			return []ProviderTarget{{
				Name: "infra", DisplayName: "Infrastructure", MCPURL: p.srv.URL,
				Actions: []DeclaredAction{{
					ID: "branches/v1", Name: "branches", Version: "v1", ReadOnly: true, Risk: "low",
					Consent: DeclaredConsent{Required: true, Prompt: "Really list?"},
				}},
				Verbs: []DeclaredVerb{{Coordinate: "instances/log", Resource: "instances", Verb: "log", Stream: true, ReadOnly: true}},
			}}
		},
		Verifier: allowAll,
	})
}

func listToolNames(t *testing.T, h http.Handler) []string {
	t.Helper()
	result, code := jsonrpc(t, h, "tools/list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("tools/list status = %d", code)
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	names := make([]string, 0, len(out.Tools))
	for _, tl := range out.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	return names
}

// runCode calls search or execute with code and decodes the envelope.
func runCode(t *testing.T, h http.Handler, tool, code string) (env codeModeEnvelope, isError bool) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": tool, "arguments": map[string]any{"code": code}})
	result, status := jsonrpc(t, h, "tools/call", string(params))
	if status != http.StatusOK {
		t.Fatalf("%s status = %d", tool, status)
	}
	var res struct {
		IsError           bool             `json:"isError"`
		StructuredContent codeModeEnvelope `json:"structuredContent"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &res); err != nil {
		t.Fatalf("decode %s result: %v (%s)", tool, err, result)
	}
	if len(res.Content) != 1 || res.Content[0].Text == "" {
		t.Fatalf("%s result has no text content: %s", tool, result)
	}
	return res.StructuredContent, res.IsError
}

// The aggregate lists search, execute, and — directly — only the tools whose
// declared action requires consent. Those are called as ordinary tools.
func TestAggregateListsSearchExecuteAndConsentTools(t *testing.T) {
	p := newCodeModeProvider(t)
	h := codeModeHandler(t, p)
	names := listToolNames(t, h)
	if strings.Join(names, ",") != "execute,infra__branches,search" {
		t.Fatalf("tools = %v, want execute, the consent-gated infra__branches, and search", names)
	}
	callResult, code := jsonrpc(t, h, "tools/call", `{"name":"infra__branches","arguments":{}}`)
	if code != http.StatusOK || !strings.Contains(string(callResult), "ran branches") {
		t.Fatalf("direct call of the consent-gated tool: status %d, result %s", code, callResult)
	}
	if p.count() != 1 {
		t.Fatalf("provider saw %d calls, want 1", p.count())
	}
	if _, code := jsonrpc(t, h, "tools/call", `{"name":"infra__provision","arguments":{}}`); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if p.count() != 1 {
		t.Fatal("a non-consent tool was reachable by direct tools/call; it must go through execute")
	}
}

// --- shared helpers used by the other aggregate tests ------------------

// searchJS runs `search` with code as bearer and returns the result value.
func searchJS(t *testing.T, h http.Handler, bearer, code string) json.RawMessage {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": codeModeToolSearch, "arguments": map[string]any{"code": code}})
	var res struct {
		IsError           bool             `json:"isError"`
		StructuredContent codeModeEnvelope `json:"structuredContent"`
	}
	if err := json.Unmarshal(mcpCall(t, h, bearer, "tools/call", string(params)), &res); err != nil {
		t.Fatalf("decode search result: %v", err)
	}
	if res.IsError {
		t.Fatalf("search failed: %s", res.StructuredContent.Error)
	}
	return res.StructuredContent.Result
}

// catalogNames is the sorted tool catalog as `search` shows it to bearer.
func catalogNames(t *testing.T, h http.Handler, bearer string) []string {
	t.Helper()
	var names []string
	if err := json.Unmarshal(searchJS(t, h, bearer, `() => spec.tools.map(t => t.name)`), &names); err != nil {
		t.Fatalf("decode catalog names: %v", err)
	}
	sort.Strings(names)
	return names
}

// callViaExecute invokes one tool through `execute` and returns what
// railgrid.call returned to the script, or {"thrown": "<message>"}.
func callViaExecute(t *testing.T, h http.Handler, bearer, tool, argsJSON string) json.RawMessage {
	t.Helper()
	code := "() => { try { return railgrid.call(" + strconv.Quote(tool) + ", " + argsJSON + ") } catch (e) { return {thrown: String(e)} } }"
	params, _ := json.Marshal(map[string]any{"name": codeModeToolExecute, "arguments": map[string]any{"code": code}})
	var res struct {
		IsError           bool             `json:"isError"`
		StructuredContent codeModeEnvelope `json:"structuredContent"`
	}
	if err := json.Unmarshal(mcpCall(t, h, bearer, "tools/call", string(params)), &res); err != nil {
		t.Fatalf("decode execute result: %v", err)
	}
	if res.IsError {
		t.Fatalf("execute failed: %s", res.StructuredContent.Error)
	}
	return res.StructuredContent.Result
}

func TestCodeModeSearchSeesCatalogAndCapabilities(t *testing.T) {
	h := codeModeHandler(t, newCodeModeProvider(t))
	env, isErr := runCode(t, h, "search", `() => ({
		providers: spec.providers.map(p => p.provider),
		verbs: spec.providers[0].verbs.map(v => v.coordinate),
		tools: spec.tools.filter(t => t.provider === 'infra').map(t => t.name),
		schema: spec.tools.find(t => t.name === 'infra__provision').inputSchema.properties.size.type,
		branches: spec.tools.find(t => t.name === 'infra__branches'),
		directOnly: spec.tools.filter(t => t.consentRequired).map(t => t.name),
		tenant: spec.tenant,
	})`)
	if isErr {
		t.Fatalf("search failed: %s", env.Error)
	}
	var got struct {
		Providers []string `json:"providers"`
		Verbs     []string `json:"verbs"`
		Tools     []string `json:"tools"`
		Schema    string   `json:"schema"`
		Tenant    string   `json:"tenant"`
		Direct    []string `json:"directOnly"`
		Branches  struct {
			ConsentRequired bool `json:"consentRequired"`
			Meta            struct {
				Railgrid struct {
					Provider string `json:"provider"`
					Action   struct {
						ID string `json:"id"`
					} `json:"action"`
				} `json:"railgrid"`
			} `json:"_meta"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, env.Result)
	}
	if strings.Join(got.Providers, ",") != "infra" || strings.Join(got.Verbs, ",") != "instances/log" {
		t.Fatalf("providers/verbs = %v / %v", got.Providers, got.Verbs)
	}
	if strings.Join(got.Tools, ",") != "infra__provision,infra__branches,infra__items" {
		t.Fatalf("tools = %v (want provider tool order preserved)", got.Tools)
	}
	if got.Schema != "integer" || got.Tenant != "some-cluster" {
		t.Fatalf("schema = %q tenant = %q", got.Schema, got.Tenant)
	}
	if !got.Branches.ConsentRequired || got.Branches.Meta.Railgrid.Provider != "infra" || got.Branches.Meta.Railgrid.Action.ID != "branches/v1" {
		t.Fatalf("branches entry = %+v, want the registry-resolved _meta and consentRequired", got.Branches)
	}
	if strings.Join(got.Direct, ",") != "infra__branches" {
		t.Fatalf("consentRequired tools = %v", got.Direct)
	}
	if len(env.Calls) != 0 {
		t.Fatalf("search recorded calls: %+v", env.Calls)
	}
}

func TestCodeModeSearchHasNoRailgridBinding(t *testing.T) {
	h := codeModeHandler(t, newCodeModeProvider(t))
	env, isErr := runCode(t, h, "search", `() => typeof railgrid`)
	if isErr || string(env.Result) != `"undefined"` {
		t.Fatalf("search exposes railgrid: %s (err=%v %s)", env.Result, isErr, env.Error)
	}
}

func TestCodeModeExecuteCallsProviderAsProxyWould(t *testing.T) {
	p := newCodeModeProvider(t)
	h := codeModeHandler(t, p)
	env, isErr := runCode(t, h, "execute", `async () => {
		const r = await railgrid.call('infra__provision', {size: 3, id: 4294967297});
		console.log("ok", r.ok);
		return {text: r.text, tool: r.data.tool, ok: r.ok, bigID: typeof r.data.bigID};
	}`)
	if isErr {
		t.Fatalf("execute failed: %s", env.Error)
	}
	if got := string(env.Result); got != `{"text":"ran provision","tool":"provision","ok":true,"bigID":"number"}` {
		t.Fatalf("result = %s", got)
	}
	if len(env.Logs) != 1 || env.Logs[0] != "ok true" {
		t.Fatalf("logs = %q", env.Logs)
	}
	if len(env.Calls) != 1 || env.Calls[0].Tool != "infra__provision" || !env.Calls[0].OK {
		t.Fatalf("calls = %+v", env.Calls)
	}
	if p.count() != 1 || p.calls[0].Name != "provision" {
		t.Fatalf("provider saw %+v", p.calls)
	}
	// Arguments reach the provider as the script wrote them: an integer above
	// 32 bits stays an integer literal, not 4.294967297e+09. (JavaScript
	// numbers are float64, so anything above 2^53 must be passed as a string;
	// that is a property of the language, not of the bridge.)
	if got := string(p.calls[0].Args); got != `{"id":4294967297,"size":3}` {
		t.Fatalf("provider args = %s", got)
	}
}

func TestCodeModeExecuteChainsCalls(t *testing.T) {
	p := newCodeModeProvider(t)
	h := codeModeHandler(t, p)
	env, isErr := runCode(t, h, "execute", `async () => {
		let page = 0, seen = [];
		while (page < 3) {
			const r = await railgrid.call('infra__items', {page});
			seen.push(r.data.call);
			page = r.data.nextPage;
		}
		return seen;
	}`)
	if isErr {
		t.Fatalf("execute failed: %s", env.Error)
	}
	if string(env.Result) != `[1,2,3]` || len(env.Calls) != 3 || p.count() != 3 {
		t.Fatalf("result = %s calls = %d provider = %d", env.Result, len(env.Calls), p.count())
	}
}

func TestCodeModeExecuteRefusesUnknownAndConsentTools(t *testing.T) {
	p := newCodeModeProvider(t)
	h := codeModeHandler(t, p)
	env, isErr := runCode(t, h, "execute", `() => {
		const out = [];
		try { railgrid.call('nope', {}) } catch (e) { out.push(String(e)) }
		try { railgrid.call('infra__branches', {}) } catch (e) { out.push(String(e)) }
		try { railgrid.call('infra__provision', 42) } catch (e) { out.push(String(e)) }
		return out;
	}`)
	if isErr {
		t.Fatalf("execute failed: %s", env.Error)
	}
	var msgs []string
	if err := json.Unmarshal(env.Result, &msgs); err != nil || len(msgs) != 3 {
		t.Fatalf("result = %s", env.Result)
	}
	if !strings.Contains(msgs[0], "unknown tool") || !strings.Contains(msgs[1], "requires human consent") || !strings.Contains(msgs[2], "must be a JSON object") {
		t.Fatalf("messages = %q", msgs)
	}
	if p.count() != 0 {
		t.Fatalf("refused calls reached the provider: %d", p.count())
	}
	if len(env.Calls) != 0 {
		t.Fatalf("refused calls were recorded as calls: %+v", env.Calls)
	}
}

func TestCodeModeExecuteCallCap(t *testing.T) {
	p := newCodeModeProvider(t)
	h := codeModeHandler(t, p)
	env, isErr := runCode(t, h, "execute", `() => {
		let refused = 0;
		for (let i = 0; i < 40; i++) {
			try { railgrid.call('infra__items', {page: i}) } catch (e) { refused++ }
		}
		return refused;
	}`)
	if isErr {
		t.Fatalf("execute failed: %s", env.Error)
	}
	if string(env.Result) != "8" || p.count() != codeModeMaxCalls || len(env.Calls) != codeModeMaxCalls {
		t.Fatalf("refused = %s, provider calls = %d, recorded = %d (cap %d)", env.Result, p.count(), len(env.Calls), codeModeMaxCalls)
	}
}

func TestCodeModeScriptErrorIsToolError(t *testing.T) {
	p := newCodeModeProvider(t)
	h := codeModeHandler(t, p)
	env, isErr := runCode(t, h, "execute", `async () => {
		await railgrid.call('infra__provision', {});
		throw new Error("after one call");
	}`)
	if !isErr || !strings.Contains(env.Error, "after one call") {
		t.Fatalf("isError = %v error = %q, want the script's error", isErr, env.Error)
	}
	if len(env.Calls) != 1 {
		t.Fatalf("the call made before the throw was not reported: %+v", env.Calls)
	}
	env, isErr = runCode(t, h, "search", `not a function`)
	if !isErr || !strings.Contains(env.Error, "did not compile") {
		t.Fatalf("search isError = %v error = %q", isErr, env.Error)
	}
}

func TestCodeModeRejectsUnknownArguments(t *testing.T) {
	h := codeModeHandler(t, newCodeModeProvider(t))
	result, status := jsonrpc(t, h, "tools/call", `{"name":"execute","arguments":{"code":"() => 1","language":"python"}}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(string(result), `"isError":true`) || !strings.Contains(string(result), "unknown field") {
		t.Fatalf("result = %s", result)
	}
}

func TestCodeModeInstructionsMentionTools(t *testing.T) {
	h := codeModeHandler(t, newCodeModeProvider(t))
	result, status := jsonrpc(t, h, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`)
	if status != http.StatusOK {
		t.Fatalf("initialize status = %d", status)
	}
	if !strings.Contains(string(result), "`search`") || !strings.Contains(string(result), "railgrid.call") {
		t.Fatalf("instructions do not describe code mode: %s", result)
	}
}

// The provider-sdk helper every machine consumer uses (mcpcode) must speak
// exactly this aggregate's wire: catalog through search, one call through
// execute, refusals classified, _meta and exact structured bytes carried.
func TestCodeModeWireMatchesSDKHelper(t *testing.T) {
	p := newCodeModeProvider(t)
	h := codeModeHandler(t, p)

	params, _ := json.Marshal(mcpcode.CatalogParams())
	result, code := jsonrpc(t, h, "tools/call", string(params))
	if code != http.StatusOK {
		t.Fatalf("search status = %d", code)
	}
	cat, err := mcpcode.DecodeCatalog(result)
	if err != nil {
		t.Fatalf("DecodeCatalog: %v", err)
	}
	var names []string
	for _, c := range cat {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "infra__provision,infra__branches,infra__items" || !cat[1].ConsentRequired || cat[1].Meta["railgrid"] == nil {
		t.Fatalf("catalog = %+v", cat)
	}

	callParams, err := mcpcode.CallParams("infra__provision", map[string]any{"size": 3})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(callParams)
	result, code = jsonrpc(t, h, "tools/call", string(raw))
	if code != http.StatusOK {
		t.Fatalf("execute status = %d", code)
	}
	call, err := mcpcode.DecodeCallResult(result)
	if err != nil {
		t.Fatalf("DecodeCallResult: %v", err)
	}
	if !call.OK || call.Text != "ran provision" || !strings.Contains(string(call.Data), `"bigID":9007199254740993`) || call.Meta["railgrid"] == nil {
		t.Fatalf("call = %+v", call)
	}
	if got := string(p.calls[0].Args); got != `{"size":3}` {
		t.Fatalf("provider args = %s", got)
	}

	var ce *mcpcode.CallError
	callParams, _ = mcpcode.CallParams("infra__branches", nil)
	raw, _ = json.Marshal(callParams)
	result, _ = jsonrpc(t, h, "tools/call", string(raw))
	if _, err := mcpcode.DecodeCallResult(result); !errors.As(err, &ce) || !ce.ConsentRequired() {
		t.Fatalf("consent refusal not classified: %v", err)
	}
	callParams, _ = mcpcode.CallParams("nope", nil)
	raw, _ = json.Marshal(callParams)
	result, _ = jsonrpc(t, h, "tools/call", string(raw))
	if _, err := mcpcode.DecodeCallResult(result); !errors.As(err, &ce) || !ce.UnknownTool() {
		t.Fatalf("unknown tool not classified: %v", err)
	}
}
