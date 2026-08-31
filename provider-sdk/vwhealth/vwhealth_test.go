/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package vwhealth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A provider that has not finished starting is not broken. Readiness gates
// traffic, so the interesting state is a probe that RAN and failed.
func TestReadyBeforeTheFirstProbe(t *testing.T) {
	var r Readiness
	if err := r.Check(); err != nil {
		t.Errorf("unprobed provider reported unready: %v", err)
	}
}

func TestReportsAFailedProbe(t *testing.T) {
	var r Readiness
	r.set("https://127.0.0.1:6443/services/apiexport/abc/x", errors.New("no such host"))

	err := r.Check()
	if err == nil {
		t.Fatal("a failed probe reported ready; this is the silence the probe exists to break")
	}
	// The message is read by someone looking at a provider that appears fine,
	// so it has to carry the address, the cause, what stops working, and where
	// the address came from.
	for _, want := range []string{"127.0.0.1:6443", "no such host", "will not reconcile", "virtualWorkspaceURL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message omits %q: %s", want, err)
		}
	}
}

// A transient blip must not pin a provider unready until someone notices.
func TestClearsOnRecovery(t *testing.T) {
	var r Readiness
	r.set("https://x/y", errors.New("connection refused"))
	if r.Check() == nil {
		t.Fatal("setup: expected unready")
	}
	r.set("https://x/y", nil)
	if err := r.Check(); err != nil {
		t.Errorf("a recovered probe still reports unready: %v", err)
	}
}

func TestHandlerStatusAndReason(t *testing.T) {
	var r Readiness
	h := Handler(&r)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("ready → %d, want 200", rec.Code)
	}

	r.set("https://x/y", errors.New("no such host"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unready → %d, want 503", rec.Code)
	}
	// The reason has to travel in the body: whoever reads /readyz — the hub,
	// a kubelet, a human with curl — gets the same explanation.
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.Contains(body["reason"], "no such host") {
		t.Errorf("body carries no reason: %v", body)
	}
}

// An empty slice is normal right after install — kcp has not reconciled it yet
// — and must read as "not yet", not as a bad URL.
func TestFirstEndpointURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		obj     map[string]any
		want    string
		wantErr bool
	}{{
		name: "published",
		obj:  map[string]any{"status": map[string]any{"endpoints": []any{map[string]any{"url": "https://h/services/apiexport/c/e"}}}},
		want: "https://h/services/apiexport/c/e",
	}, {
		name: "no endpoints yet", obj: map[string]any{"status": map[string]any{"endpoints": []any{}}}, wantErr: true,
	}, {
		name: "empty url", obj: map[string]any{"status": map[string]any{"endpoints": []any{map[string]any{"url": ""}}}}, wantErr: true,
	}, {
		name: "no status", obj: map[string]any{}, wantErr: true,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FirstEndpointURL(tc.obj, "x.faros.sh")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("got (%q, %v), want %q", got, err, tc.want)
			}
		})
	}
}
