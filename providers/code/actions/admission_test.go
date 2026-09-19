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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func admissionServer(t *testing.T, allowed bool) *Server {
	t.Helper()
	repo := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "code.railgrid.ai/v1alpha1", "kind": "Repository", "metadata": map[string]any{"name": "product", "uid": "repo-uid"}}}
	caller := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), repo)
	caller.PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"allowed": allowed}}}, nil
	})
	return New(callerFixture{client: caller, t: t}, func(context.Context, string, string) (dynamic.Interface, error) {
		t.Error("unexpected credential authority lookup")
		return nil, context.Canceled
	}, backend.NewRegistry())
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
	r := httptest.NewRequest("POST", "/actions/clusters/tenant-id/repositories/product/"+action+"/v1", body).WithContext(ctx)
	r.Header.Set("X-Railgrid-Cluster", "tenant-id")
	r.Header.Set("Authorization", "Bearer caller-token")
	return r
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
		s.ServeHTTP(httptest.NewRecorder(), admissionRequest(ctx, "stage_snapshot", body))
	}()
	select {
	case <-body.started:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not start")
	}
	for _, action := range []string{"stage_snapshot", "prepare_snapshot", "publish_snapshot", "branch_head"} {
		probe := &observedBody{ReadCloser: io.NopCloser(strings.NewReader("{}")), started: make(chan struct{})}
		response := httptest.NewRecorder()
		s.ServeHTTP(response, admissionRequest(context.Background(), action, probe))
		if action == "branch_head" {
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
	denied := admissionServer(t, false)
	probe := &observedBody{ReadCloser: io.NopCloser(strings.NewReader("{}")), started: make(chan struct{})}
	response := httptest.NewRecorder()
	denied.ServeHTTP(response, admissionRequest(context.Background(), "stage_snapshot", probe))
	if response.Code != 403 {
		t.Fatalf("denied caller status: %d", response.Code)
	}
	select {
	case <-probe.started:
		t.Fatal("unauthorized body was read")
	default:
	}
}
