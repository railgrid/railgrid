// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
)

// admissionServer sees one Repository; visible says whether the stamped
// caller may see it. No Connection and no Secret are present, so any action
// that gets past the gate fails at the binding, never at a credential.
func admissionServer(t *testing.T, visible bool) *Server {
	t.Helper()
	callers := newCallers(func(a conformance.Attributes) bool {
		return visible && allowGet("repositories", "product")(a)
	}, actionObject(t, testRepository()))
	return New(callers, backend.NewRegistry())
}

type observedBody struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (b *observedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	return b.ReadCloser.Read(p)
}

func admissionRequest(ctx context.Context, action string, body io.Reader) *http.Request {
	return actionRequest(http.MethodPost, kubePath(testCluster, "repositories", "product", action), body, testUser).WithContext(
		// Keep the route and identity the helper set; only the cancellation
		// comes from the test.
		withParent(ctx, actionRequest(http.MethodPost, kubePath(testCluster, "repositories", "product", action), nil, testUser).Context()),
	)
}

// withParent returns a context that is cancelled with parent but carries
// values' values: what a handler sees when the server's request context is
// cancelled underneath the adapter's.
func withParent(parent, values context.Context) context.Context {
	ctx, cancel := context.WithCancel(parent)
	context.AfterFunc(parent, cancel)
	return valuesContext{Context: ctx, values: values}
}

type valuesContext struct {
	context.Context
	values context.Context
}

func (c valuesContext) Value(key any) any {
	if v := c.values.Value(key); v != nil {
		return v
	}
	return c.Context.Value(key)
}

func TestActionBodyAdmissionAndCancellation(t *testing.T) {
	s := admissionServer(t, true)
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	body := &observedBody{ReadCloser: reader, started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ServeHTTP(httptest.NewRecorder(), admissionRequest(ctx, "stage-snapshot", body))
	}()
	select {
	case <-body.started:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not start")
	}
	for _, action := range []string{"stage-snapshot", "prepare-snapshot", "publish-snapshot", "branch-head"} {
		probe := &observedBody{ReadCloser: io.NopCloser(strings.NewReader("{}")), started: make(chan struct{})}
		response := httptest.NewRecorder()
		s.ServeHTTP(response, admissionRequest(context.Background(), action, probe))
		if action == "branch-head" {
			// Admitted alongside the upload; it then fails at the binding
			// because the body names no repository.
			if response.Code != 403 {
				t.Fatalf("ordinary action blocked: %d", response.Code)
			}
			continue
		}
		if response.Code != 503 {
			t.Fatalf("%s admitted concurrently: %d", action, response.Code)
		}
		select {
		case <-probe.started:
			t.Fatal("rejected upload body was read")
		default:
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not interrupt body read")
	}
	if len(s.slots) != 0 || len(s.snapshotSlots) != 0 {
		t.Fatal("cancellation leaked admission capacity")
	}
	// A caller who cannot see the Repository is refused before the body is
	// read, with the contract's non-disclosing 404.
	denied := admissionServer(t, false)
	probe := &observedBody{ReadCloser: io.NopCloser(strings.NewReader("{}")), started: make(chan struct{})}
	response := httptest.NewRecorder()
	denied.ServeHTTP(response, admissionRequest(context.Background(), "stage-snapshot", probe))
	if response.Code != 404 {
		t.Fatalf("denied caller status: %d", response.Code)
	}
	select {
	case <-probe.started:
		t.Fatal("unauthorized body was read")
	default:
	}
}
