// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/actionwire"
)

func testEnvelope(r *http.Request) actionwire.Envelope {
	return actionwire.New(r, "example", "greet", actionwire.ResourceRef{
		APIVersion: "example.railgrid.ai/v1alpha1", Kind: "Greeting", Resource: "greetings", Name: "hello",
	})
}

func serve(t *testing.T, method, body string, lim Limits, exec Executor, opts ...ServeOption) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/actions/clusters/"+testCluster+"/greetings/hello/greet/v1", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	Serve(recorder, r, testEnvelope(r), lim, exec, opts...)
	return recorder
}

func decodeEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) actionwire.Envelope {
	t.Helper()
	var envelope actionwire.Envelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("response is not an actionwire envelope: %v (body %q)", err, recorder.Body.String())
	}
	return envelope
}

func echoInput(_ context.Context, input json.RawMessage) (any, *actionwire.Error) {
	return map[string]any{"echo": json.RawMessage(input)}, nil
}

func TestServeSuccess(t *testing.T) {
	got := serve(t, http.MethodPost, `{"input":{"name":"world"}}`, Limits{}, echoInput)
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", got.Code, got.Body.String())
	}
	if ct := got.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if got.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("action response is cacheable")
	}
	envelope := decodeEnvelope(t, got)
	if envelope.RequestID == "" || envelope.Provider != "example" || envelope.Action != "greet" {
		t.Fatalf("envelope = %+v", envelope)
	}
	if envelope.Error != nil {
		t.Fatalf("envelope carries an error: %+v", envelope.Error)
	}
	if string(envelope.Result) != `{"echo":{"name":"world"}}` {
		t.Fatalf("result = %s", envelope.Result)
	}
	if got.Header().Get("X-Request-ID") != envelope.RequestID {
		t.Fatal("X-Request-ID does not match the envelope")
	}
}

func TestServeOmittedInputIsNull(t *testing.T) {
	got := serve(t, http.MethodPost, `{}`, Limits{}, func(_ context.Context, input json.RawMessage) (any, *actionwire.Error) {
		if string(input) != "null" {
			t.Errorf("input = %q, want null", input)
		}
		return map[string]any{}, nil
	})
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", got.Code)
	}
}

func TestServeRejectsNonPost(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		got := serve(t, method, "", Limits{}, echoInput)
		if got.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, got.Code)
		}
		if got.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s: Allow = %q", method, got.Header().Get("Allow"))
		}
	}
}

func TestServeInputLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		lim  Limits
		want int
		code string
	}{
		{"unknown field", `{"input":{},"stowaway":1}`, Limits{}, http.StatusBadRequest, "invalid_action_input"},
		{"not json", `not json`, Limits{}, http.StatusBadRequest, "invalid_action_input"},
		{"trailing content", `{"input":{}}{"input":{}}`, Limits{}, http.StatusBadRequest, "invalid_action_input"},
		{"oversized", `{"input":{"pad":"` + strings.Repeat("x", 4096) + `"}}`, Limits{MaxInputBytes: 512}, http.StatusRequestEntityTooLarge, "input_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := serve(t, http.MethodPost, tc.body, tc.lim, echoInput)
			if got.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", got.Code, tc.want, got.Body.String())
			}
			envelope := decodeEnvelope(t, got)
			if envelope.Error == nil || envelope.Error.Code != tc.code {
				t.Fatalf("envelope error = %+v, want code %q", envelope.Error, tc.code)
			}
		})
	}
}

func TestServeOutputLimits(t *testing.T) {
	big := make([]int, 100)
	for _, tc := range []struct {
		name   string
		lim    Limits
		result any
		want   int
	}{
		{"array over the item limit", Limits{MaxResultItems: 10}, big, http.StatusInternalServerError},
		{"array under the item limit", Limits{MaxResultItems: 1000}, big, http.StatusOK},
		{"items array over the item limit", Limits{MaxResultItems: 10}, map[string]any{"items": big}, http.StatusInternalServerError},
		{"items array under the item limit", Limits{MaxResultItems: 1000}, map[string]any{"items": big}, http.StatusOK},
		{"item limit ignores a non-list result", Limits{MaxResultItems: 1}, map[string]any{"count": 5000}, http.StatusOK},
		{"over the byte limit", Limits{MaxOutputBytes: 64}, map[string]any{"pad": strings.Repeat("x", 4096)}, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := serve(t, http.MethodPost, `{"input":{}}`, tc.lim, func(context.Context, json.RawMessage) (any, *actionwire.Error) {
				return tc.result, nil
			})
			if got.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", got.Code, tc.want, got.Body.String())
			}
			if tc.want != http.StatusOK {
				if envelope := decodeEnvelope(t, got); envelope.Error == nil || envelope.Error.Code != "result_limit" {
					t.Fatalf("envelope error = %+v, want code result_limit", envelope.Error)
				}
			}
		})
	}
}

func TestServeTimeout(t *testing.T) {
	got := serve(t, http.MethodPost, `{"input":{}}`, Limits{Timeout: 20 * time.Millisecond},
		func(ctx context.Context, _ json.RawMessage) (any, *actionwire.Error) {
			<-ctx.Done()
			return nil, nil
		})
	if got.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (body %q)", got.Code, got.Body.String())
	}
	envelope := decodeEnvelope(t, got)
	if envelope.Error == nil || envelope.Error.Code != "action_timeout" || !envelope.Error.Retryable {
		t.Fatalf("envelope error = %+v", envelope.Error)
	}
}

func TestServeExecutorError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		aerr  *actionwire.Error
		opts  []ServeOption
		want  int
		code  string
		retry bool
	}{
		{"non-retryable defaults to 422", &actionwire.Error{Code: "no_such_branch", Message: "no such branch"}, nil, http.StatusUnprocessableEntity, "no_such_branch", false},
		{"retryable defaults to 502", &actionwire.Error{Code: "upstream_unconfirmed", Message: "upstream unconfirmed", Retryable: true}, nil, http.StatusBadGateway, "upstream_unconfirmed", true},
		{"status override", &actionwire.Error{Code: "identity_conflict", Message: "identity conflict"},
			[]ServeOption{WithErrorStatus(func(*actionwire.Error) int { return http.StatusConflict })}, http.StatusConflict, "identity_conflict", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := serve(t, http.MethodPost, `{"input":{}}`, Limits{}, func(context.Context, json.RawMessage) (any, *actionwire.Error) {
				return nil, tc.aerr
			}, tc.opts...)
			if got.Code != tc.want {
				t.Fatalf("status = %d, want %d", got.Code, tc.want)
			}
			envelope := decodeEnvelope(t, got)
			if envelope.Error == nil || envelope.Error.Code != tc.code || envelope.Error.Retryable != tc.retry {
				t.Fatalf("envelope error = %+v", envelope.Error)
			}
			if len(envelope.Result) != 0 {
				t.Fatalf("failure envelope carries a result: %s", envelope.Result)
			}
		})
	}
}

func TestServeWithoutExecutor(t *testing.T) {
	got := serve(t, http.MethodPost, `{"input":{}}`, Limits{}, nil)
	if got.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", got.Code)
	}
}

func TestCountResultItems(t *testing.T) {
	for _, tc := range []struct {
		data  string
		count int64
		ok    bool
	}{
		{`[1,2,3]`, 3, true},
		{`[]`, 0, true},
		{`{"items":[1,2]}`, 2, true},
		{`{"items":[]}`, 0, true},
		{`{"count":3}`, 0, false},
		{`{"items":3}`, 0, false},
		{`"a string"`, 0, false},
		{`null`, 0, false},
		{``, 0, false},
	} {
		count, ok := countResultItems([]byte(tc.data))
		if count != tc.count || ok != tc.ok {
			t.Errorf("countResultItems(%q) = (%d, %t), want (%d, %t)", tc.data, count, ok, tc.count, tc.ok)
		}
	}
}
