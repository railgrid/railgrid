// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/railgrid/provider-sdk/actionwire"
)

// Defaults applied when the corresponding Limits field is zero. A declared
// action states its own limits in the manifest; these are the floor for a
// handler that forgot to pass them on.
const (
	DefaultTimeout        = 30 * time.Second
	DefaultMaxInputBytes  = 64 << 10
	DefaultMaxOutputBytes = 512 << 10
)

// Limits are the declared bounds of one action, enforced around the executor.
//
// MaxResultItems bounds how many elements a list-shaped result may carry. It
// applies to a result that marshals to a JSON array, and to one that marshals
// to a JSON object with a top-level "items" array; any other shape is
// unbounded by it. Exceeding it fails the request rather than truncating the
// result: a silently shortened list is indistinguishable from a complete one,
// and a caller that acts on the difference is the bug this prevents. An
// action that can legitimately produce more must page in its own input.
//
// A zero field takes the corresponding Default; a zero MaxResultItems means
// no item limit.
type Limits struct {
	Timeout        time.Duration
	MaxInputBytes  int64
	MaxOutputBytes int64
	MaxResultItems int64
}

func (l Limits) timeout() time.Duration {
	if l.Timeout <= 0 {
		return DefaultTimeout
	}
	return l.Timeout
}

func (l Limits) maxInput() int64 {
	if l.MaxInputBytes <= 0 {
		return DefaultMaxInputBytes
	}
	return l.MaxInputBytes
}

func (l Limits) maxOutput() int64 {
	if l.MaxOutputBytes <= 0 {
		return DefaultMaxOutputBytes
	}
	return l.MaxOutputBytes
}

// Executor runs one action. input is the raw value of the request body's
// "input" member — it is never nil; a body of {"input":{}} yields "{}" and an
// omitted member yields "null". A returned *actionwire.Error is reported to
// the caller verbatim; any other failure must be turned into one by the
// executor, so no backend message escapes by accident.
type Executor func(ctx context.Context, input json.RawMessage) (result any, aerr *actionwire.Error)

// ServeOption tunes Serve.
type ServeOption func(*serveConfig)

type serveConfig struct {
	errorStatus func(*actionwire.Error) int
}

// WithErrorStatus overrides how an *actionwire.Error from the executor maps
// to an HTTP status. The default answers a retryable failure with 502 (the
// action reached something that did not settle) and a non-retryable one with
// 422 (the request was well formed but the action cannot be applied).
func WithErrorStatus(f func(*actionwire.Error) int) ServeOption {
	return func(c *serveConfig) {
		if f != nil {
			c.errorStatus = f
		}
	}
}

func defaultErrorStatus(e *actionwire.Error) int {
	if e != nil && e.Retryable {
		return http.StatusBadGateway
	}
	return http.StatusUnprocessableEntity
}

// actionRequest is the action body: exactly {"input": …}, with the optional
// requestId some clients send. Anything else is a client bug and is rejected
// rather than ignored.
type actionRequest struct {
	RequestID string          `json:"requestId,omitempty"`
	Input     json.RawMessage `json:"input"`
}

// Serve runs exec under lim and writes the actionwire envelope. It is the
// whole response path of an action: call it after Gate has passed, and write
// nothing to w yourself.
//
// It enforces, in order: POST only (405 — an action mutates, so it is never a
// GET and never reachable by a redirect the browser replays); a body no
// larger than lim.MaxInputBytes (413); a body that is exactly one
// {"input": …} object with no unknown fields and no trailing content (400);
// a deadline of lim.Timeout on the executor (504); lim.MaxResultItems and
// lim.MaxOutputBytes on the encoded envelope (500). Serve never writes a
// redirect and always answers with an envelope.
func Serve(
	w http.ResponseWriter,
	r *http.Request,
	env actionwire.Envelope,
	lim Limits,
	exec Executor,
	opts ...ServeOption,
) {
	cfg := serveConfig{errorStatus: defaultErrorStatus}
	for _, opt := range opts {
		opt(&cfg)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Request-ID", env.RequestID)

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		env.Failure(w, http.StatusMethodNotAllowed, "method_not_allowed", "an action is invoked with POST", false)
		return
	}
	if exec == nil {
		env.Failure(w, http.StatusInternalServerError, "action_unavailable", "action has no executor", false)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), lim.timeout())
	defer cancel()

	input, err := readInput(ctx, w, r, lim.maxInput())
	switch {
	case err == nil:
	case errors.Is(err, errInputTooLarge):
		env.Failure(w, http.StatusRequestEntityTooLarge, "input_too_large", "action input exceeds the declared limit", false)
		return
	default:
		env.Failure(w, http.StatusBadRequest, "invalid_action_input", "action input is not a valid {\"input\": …} body", false)
		return
	}

	result, aerr := exec(ctx, input)
	if ctx.Err() != nil {
		env.Failure(w, http.StatusGatewayTimeout, "action_timeout", "action did not finish within its declared timeout", true)
		return
	}
	if aerr != nil {
		env.Failure(w, cfg.errorStatus(aerr), aerr.Code, aerr.Message, aerr.Retryable)
		return
	}

	encoded, err := encodeResult(env, result, lim)
	if err != nil {
		env.Failure(w, http.StatusInternalServerError, "result_limit", "action result exceeds the declared limit", false)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

var errInputTooLarge = errors.New("dataplane: action input too large")

// readInput decodes the request body strictly and returns the raw "input"
// member. The read is bounded by limit and by ctx: a client that opens a body
// and stops sending must not hold a slot until the server's own idle timeout.
func readInput(ctx context.Context, w http.ResponseWriter, r *http.Request, limit int64) (json.RawMessage, error) {
	body := r.Body
	if deadline, ok := ctx.Deadline(); ok {
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(deadline)
		done := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(done)
			_ = controller.SetReadDeadline(time.Now())
			_ = body.Close()
		})
		defer func() {
			if !stop() {
				<-done
			}
			_ = controller.SetReadDeadline(time.Time{})
		}()
	}

	decoder := json.NewDecoder(http.MaxBytesReader(w, body, limit))
	decoder.DisallowUnknownFields()
	var request actionRequest
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errInputTooLarge
		}
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errInputTooLarge
		}
		return nil, errors.New("dataplane: unexpected trailing action input")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Input == nil {
		request.Input = json.RawMessage("null")
	}
	return request.Input, nil
}

// encodeResult marshals result once, checks the item and byte limits, and
// returns the encoded envelope.
func encodeResult(env actionwire.Envelope, result any, lim Limits) ([]byte, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if lim.MaxResultItems > 0 {
		if count, ok := countResultItems(data); ok && count > lim.MaxResultItems {
			return nil, errors.New("dataplane: action result exceeds the declared item limit")
		}
	}
	// json.RawMessage marshals to itself, so the envelope is built without a
	// second pass over the result.
	encoded, err := env.Success(json.RawMessage(data))
	if err != nil {
		return nil, err
	}
	if int64(len(encoded)) > lim.maxOutput() {
		return nil, errors.New("dataplane: action result exceeds the declared byte limit")
	}
	return encoded, nil
}

// countResultItems reports the number of elements in a list-shaped result:
// the elements of a top-level JSON array, or of a top-level "items" array.
// ok is false for any other shape, which MaxResultItems then does not bound.
func countResultItems(data []byte) (int64, bool) {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return 0, false
	}
	switch trimmed[0] {
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(trimmed, &items) != nil {
			return 0, false
		}
		return int64(len(items)), true
	case '{':
		var probe struct {
			Items *[]json.RawMessage `json:"items"`
		}
		if json.Unmarshal(trimmed, &probe) != nil || probe.Items == nil {
			return 0, false
		}
		return int64(len(*probe.Items)), true
	}
	return 0, false
}
