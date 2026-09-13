// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroshq/provider-linear/internal/linearapi"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestOnboardingAuthorizationAndCredentialBoundary(t *testing.T) {
	for _, tc := range []struct {
		name              string
		identity, allowed bool
		body              string
		upstreamError     bool
		status            int
		calls             int
	}{
		{"no identity", false, false, `{"apiKey":"private-key"}`, false, 401, 0},
		{"denied", true, false, `{"apiKey":"private-key"}`, false, 403, 0},
		{"empty key", true, true, `{"apiKey":" "}`, false, 400, 0},
		{"oversized", true, true, `{"apiKey":"` + strings.Repeat("x", 17000) + `"}`, false, 400, 0},
		{"upstream failure", true, true, `{"apiKey":"private-key"}`, true, 422, 1},
		{"teams", true, true, `{"apiKey":"private-key","after":"next"}`, false, 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleDynamicClient(runtime.NewScheme())
			client.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				obj := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
				attrs, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "resourceAttributes")
				if attrs["verb"] != "create" || attrs["resource"] != "connections" || attrs["group"] != "linear.providers.faros.sh" {
					t.Fatalf("incorrect authorization: %v", attrs)
				}
				return true, &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"allowed": tc.allowed}}}, nil
			})
			calls := 0
			handler := newOnboardingHandler(func(*http.Request) (dynamic.Interface, error) {
				if !tc.identity {
					return nil, errors.New("missing identity")
				}
				return client, nil
			}, func(_ context.Context, key, after string) (linearapi.Page[linearapi.Team], error) {
				calls++
				if key != "private-key" {
					t.Fatal("credential changed")
				}
				if tc.upstreamError {
					return linearapi.Page[linearapi.Team]{}, errors.New("private-key upstream detail")
				}
				if after != "next" {
					t.Fatal("cursor lost")
				}
				return linearapi.Page[linearapi.Team]{Nodes: []linearapi.Team{{ID: "team", Name: "Engineering"}}}, nil
			})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/onboarding/teams", strings.NewReader(tc.body)))
			if w.Code != tc.status || calls != tc.calls {
				t.Fatalf("status %d calls %d", w.Code, calls)
			}
			if strings.Contains(w.Body.String(), "private-key") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("credential disclosure or caching")
			}
			for _, action := range client.Actions() {
				if action.GetResource().Resource != "selfsubjectaccessreviews" {
					t.Fatal("discovery persisted a resource")
				}
			}
		})
	}
}
