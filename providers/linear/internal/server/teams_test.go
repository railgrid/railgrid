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

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	"github.com/faroshq/provider-linear/internal/linearapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestTeamDiscoveryAuthorizationAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		name                                                string
		identity, allowed, visible, replaced, upstreamError bool
		status, resolves, reads                             int
	}{
		{name: "no identity", status: 401},
		{name: "no registration permission", identity: true, status: 403},
		{name: "hidden connection", identity: true, allowed: true, status: 403},
		{name: "replaced connection", identity: true, allowed: true, visible: true, replaced: true, status: 409, resolves: 1},
		{name: "registered discovery", identity: true, allowed: true, visible: true, status: 200, resolves: 1, reads: 1},
		{name: "upstream error is private", identity: true, allowed: true, visible: true, upstreamError: true, status: 422, resolves: 1, reads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleDynamicClient(runtime.NewScheme())
			client.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
				obj := a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
				attrs, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "resourceAttributes")
				if attrs["resource"] != "teams" || attrs["verb"] != "create" || attrs["group"] != api.GroupName || attrs["namespace"] != "" {
					t.Fatalf("wrong authority: %v", attrs)
				}
				return true, &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"allowed": tc.allowed}}}, nil
			})
			client.PrependReactor("get", "connections", func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.(ktesting.GetAction).GetName() != "main" {
					t.Fatal("wrong connection")
				}
				if !tc.visible {
					return true, nil, errors.New("denied")
				}
				return true, &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"uid": "current"}}}, nil
			})
			resolves, reads := 0, 0
			handler := newTeamDiscoveryHandler(func(*http.Request) (dynamic.Interface, error) {
				if !tc.identity {
					return nil, errors.New("missing")
				}
				return client, nil
			}, func(_ context.Context, cluster, name string) (api.Connection, string, error) {
				resolves++
				if cluster != "workspace" || name != "main" {
					t.Fatal("tenant scope lost")
				}
				conn := api.Connection{ObjectMeta: metav1.ObjectMeta{UID: "current"}}
				if tc.replaced {
					conn.UID = "replacement"
				}

				return conn, "private-key", nil
			}, func(_ context.Context, key, after string) (linearapi.Page[linearapi.Team], error) {
				reads++
				if key != "private-key" || after != "next" {
					t.Fatal("credential or cursor changed")
				}
				if tc.upstreamError {
					return linearapi.Page[linearapi.Team]{}, errors.New("private-key")
				}
				return linearapi.Page[linearapi.Team]{Nodes: []linearapi.Team{{ID: "eng"}, {ID: "outside"}}}, nil
			})
			r := httptest.NewRequest("GET", "/api/connections/main/teams?after=next", nil)
			r.SetPathValue("connection", "main")
			r.Header.Set("X-Faros-Cluster", "workspace")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || resolves != tc.resolves || reads != tc.reads {
				t.Fatalf("status=%d resolves=%d reads=%d", w.Code, resolves, reads)
			}
			if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private-key") {
				t.Fatal("credential disclosure or cache")
			}
			if tc.status == 200 && !strings.Contains(w.Body.String(), "outside") {
				t.Fatal("discovery policy mismatch")
			}
			for _, a := range client.Actions() {
				if a.GetVerb() != "get" && a.GetResource().Resource != "selfsubjectaccessreviews" {
					t.Fatal("discovery persisted resources")
				}
			}
		})
	}
}
