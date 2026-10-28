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

// The code-mode sandbox: one fresh JavaScript runtime per execution, with
// nothing in it but ECMAScript, `console`, and the globals the caller
// installs (`spec`, `railgrid`). goja is a pure-Go interpreter that ships no
// filesystem, network, timers, module loader or process object, so there is
// nothing to switch off: the only way out of a script is a binding we put
// there, and the only credential is the one the binding already holds on the
// Go side. Scripts see no bearer, no header and no URL.
//
// Every run is bounded: code size, wall-clock (goja's Interrupt, which also
// cancels any provider call in flight through the context), call-stack depth,
// result size and console output. A limit violation or a thrown error is a
// *sandboxError: the model's mistake, reported back as a tool result it can
// read and fix, never a protocol failure.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// sandboxLimits bounds one script execution.
type sandboxLimits struct {
	// MaxCodeBytes caps the script source.
	MaxCodeBytes int
	// MaxDuration caps wall-clock time for the whole run, provider calls
	// included.
	MaxDuration time.Duration
	// MaxResultBytes caps the JSON encoding of the returned value.
	MaxResultBytes int
	// MaxLogBytes caps captured console output; anything past it is dropped
	// and the outcome is flagged truncated.
	MaxLogBytes int
	// MaxCallStack is goja's call-stack depth limit (runaway recursion).
	MaxCallStack int
}

var defaultSandboxLimits = sandboxLimits{
	MaxCodeBytes:   64 << 10,
	MaxDuration:    60 * time.Second,
	MaxResultBytes: 256 << 10,
	MaxLogBytes:    32 << 10,
	MaxCallStack:   512,
}

// sandboxOutcome is a completed run.
type sandboxOutcome struct {
	// Result is the JSON encoding of the value the function returned (the
	// settled value when it returned a promise); "null" for undefined.
	Result json.RawMessage
	// Logs is captured console output, one entry per call.
	Logs []string
	// LogsTruncated is set when console output hit MaxLogBytes.
	LogsTruncated bool
}

// sandboxError is a failure the script's author can act on: a syntax error,
// a thrown exception, a limit. It is distinct from an internal error so the
// tool can report it as an IsError result rather than failing the call.
type sandboxError struct {
	msg string
}

func (e *sandboxError) Error() string { return e.msg }

func scriptErrorf(format string, args ...any) error {
	return &sandboxError{msg: fmt.Sprintf(format, args...)}
}

// sandboxBindings installs globals into the runtime before the code runs.
// ctx carries the run's deadline, so a binding that leaves the process (a
// provider call) must use it.
type sandboxBindings func(ctx context.Context, rt *goja.Runtime, js *jsBridge) error

// jsBridge moves values between Go and the runtime through JSON, which is
// the one encoding both sides agree on: numbers stay JS numbers, provider
// results stay plain data, and a cyclic object fails the way JSON.stringify
// fails rather than looping.
type jsBridge struct {
	rt        *goja.Runtime
	stringify goja.Callable
	parse     goja.Callable
}

func newJSBridge(rt *goja.Runtime) (*jsBridge, error) {
	s, err := rt.RunString("JSON.stringify")
	if err != nil {
		return nil, err
	}
	p, err := rt.RunString("JSON.parse")
	if err != nil {
		return nil, err
	}
	stringify, ok1 := goja.AssertFunction(s)
	parse, ok2 := goja.AssertFunction(p)
	if !ok1 || !ok2 {
		return nil, errors.New("sandbox: JSON is not callable")
	}
	return &jsBridge{rt: rt, stringify: stringify, parse: parse}, nil
}

// fromJSON turns Go-side JSON into a JS value.
func (b *jsBridge) fromJSON(raw []byte) (goja.Value, error) {
	v, err := b.parse(goja.Undefined(), b.rt.ToValue(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("sandbox: JSON.parse: %w", err)
	}
	return v, nil
}

// toJSON encodes a JS value; undefined (and functions) encode to nil.
func (b *jsBridge) toJSON(v goja.Value) ([]byte, error) {
	if v == nil || goja.IsUndefined(v) {
		return nil, nil
	}
	s, err := b.stringify(goja.Undefined(), v)
	if err != nil {
		return nil, err
	}
	if goja.IsUndefined(s) {
		return nil, nil
	}
	return []byte(s.String()), nil
}

// logSink captures console output up to a byte budget.
type logSink struct {
	max       int
	used      int
	lines     []string
	truncated bool
}

func (l *logSink) add(line string) {
	if l.truncated {
		return
	}
	if l.used+len(line) > l.max {
		l.truncated = true
		return
	}
	l.used += len(line)
	l.lines = append(l.lines, line)
}

// installConsole provides console.log/info/warn/error/debug, each capturing
// its arguments space-joined: strings as-is, everything else as JSON.
func installConsole(rt *goja.Runtime, js *jsBridge, sink *logSink) {
	console := rt.NewObject()
	logFn := func(call goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			if a == nil || goja.IsUndefined(a) {
				parts = append(parts, "undefined")
				continue
			}
			if _, isObj := a.(*goja.Object); !isObj {
				parts = append(parts, a.String())
				continue
			}
			raw, err := js.toJSON(a)
			if err != nil || raw == nil {
				parts = append(parts, a.String())
				continue
			}
			parts = append(parts, string(raw))
		}
		sink.add(strings.Join(parts, " "))
		return goja.Undefined()
	}
	for _, name := range []string{"log", "info", "warn", "error", "debug"} {
		_ = console.Set(name, logFn)
	}
	_ = rt.Set("console", console)
}

// runSandbox evaluates code — a JavaScript function expression — in a fresh
// runtime with bind's globals installed, calls it with no arguments, awaits
// the result if it is a promise, and returns the settled value as JSON.
//
// The job queue (promise reactions) drains when control returns to Go, so an
// async function that only awaits synchronous bindings settles before this
// returns; a promise that is still pending afterwards can never settle (there
// are no timers or I/O to resolve it) and is reported as such.
func runSandbox(ctx context.Context, code string, lim sandboxLimits, bind sandboxBindings) (out sandboxOutcome, err error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return out, scriptErrorf("code is empty: pass a JavaScript function expression, for example `async () => { ... }`")
	}
	if len(code) > lim.MaxCodeBytes {
		return out, scriptErrorf("code is %d bytes, over the %d-byte limit", len(code), lim.MaxCodeBytes)
	}

	rt := goja.New()
	rt.SetMaxCallStackSize(lim.MaxCallStack)
	js, err := newJSBridge(rt)
	if err != nil {
		return out, err
	}
	sink := &logSink{max: lim.MaxLogBytes}
	installConsole(rt, js, sink)

	ctx, cancel := context.WithTimeout(ctx, lim.MaxDuration)
	defer cancel()
	if bind != nil {
		if err := bind(ctx, rt, js); err != nil {
			return out, err
		}
	}

	// Interrupt the interpreter when the deadline passes or the caller goes
	// away. A provider call in flight shares ctx, so it ends at the same
	// moment; the interrupt then stops the script from doing anything else.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			rt.Interrupt(ctx.Err())
		case <-finished:
		}
	}()

	defer func() {
		out.Logs = sink.lines
		out.LogsTruncated = sink.truncated
		if r := recover(); r != nil {
			err = fmt.Errorf("sandbox: runtime panic: %v", r)
		}
	}()

	// Evaluate the source as one expression so a bare arrow function is
	// accepted as written; a trailing semicolon is tolerated.
	fnVal, jsErr := rt.RunString("(" + strings.TrimRight(code, "; \t\r\n") + "\n)")
	if jsErr != nil {
		return out, describeJSError(ctx, lim, jsErr, "code did not compile")
	}
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return out, scriptErrorf("code must evaluate to a function (got %s): write it as `async () => { ...; return value }`", fnVal.ExportType())
	}

	ret, jsErr := fn(goja.Undefined())
	if jsErr != nil {
		return out, describeJSError(ctx, lim, jsErr, "script threw")
	}
	if p, isPromise := ret.Export().(*goja.Promise); isPromise {
		switch p.State() {
		case goja.PromiseStatePending:
			return out, scriptErrorf("the returned promise never settled: the sandbox has no timers or I/O, so only awaits on railgrid.call (which is synchronous) and on already-settled promises resolve")
		case goja.PromiseStateRejected:
			return out, scriptErrorf("script rejected: %s", describeJSValue(p.Result()))
		default:
			ret = p.Result()
		}
	}

	raw, err := js.toJSON(ret)
	if err != nil {
		return out, describeJSError(ctx, lim, err, "result is not JSON-serialisable")
	}
	if raw == nil {
		raw = []byte("null")
	}
	if len(raw) > lim.MaxResultBytes {
		return out, scriptErrorf("result is %d bytes, over the %d-byte limit: return less (filter, slice, or map to the fields you need)", len(raw), lim.MaxResultBytes)
	}
	out.Result = raw
	return out, nil
}

// describeJSError renders a goja failure for the script's author. An
// interrupt is reported as the limit that caused it; a JS exception carries
// its message and stack so the model can see which line failed.
func describeJSError(ctx context.Context, lim sandboxLimits, err error, what string) error {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return scriptErrorf("script exceeded the %s time limit (provider calls count towards it)", lim.MaxDuration)
		}
		return scriptErrorf("script was interrupted: %v", interrupted.Value())
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return scriptErrorf("%s: %s", what, strings.TrimSpace(ex.String()))
	}
	return scriptErrorf("%s: %v", what, err)
}

// describeJSValue renders a thrown or rejected value: Error objects by their
// message and stack, anything else by String().
func describeJSValue(v goja.Value) string {
	if v == nil {
		return "undefined"
	}
	if obj, ok := v.(*goja.Object); ok {
		if stack := obj.Get("stack"); stack != nil && !goja.IsUndefined(stack) {
			return stack.String()
		}
	}
	return v.String()
}
