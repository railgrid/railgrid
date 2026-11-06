// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/tenant"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestResourceReferencesRecheckCallerAndRetainOnlyCoordinates(t *testing.T) {
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer caller" {
			t.Error("catalog did not use current caller")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"name":"example","ready":true,"export":{"resources":[{"name":"widgets","apiVersion":"example.railgrid.ai/v1alpha1","kind":"Widget"}]}}]}`))
	}))
	defer catalog.Close()
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.railgrid.ai/v1alpha1", "kind": "Widget", "metadata": map[string]any{"name": "demo", "uid": "uid-1", "resourceVersion": "1"}, "spec": map[string]any{"privateValue": "must-never-enter-prompt"}}}
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), object)
	c := agentsclient.NewFromScope(tenant.NewScopeFromDynamic("cluster-a", dyn))
	s := &Server{cfg: Config{HubURL: catalog.URL}}
	id := identity{token: "caller", clusterID: "cluster-a"}
	ref := agentContextResource{Provider: "example", ResourceRef: agentResourceReference{APIVersion: "example.railgrid.ai/v1alpha1", Kind: "Widget", Resource: "widgets", Name: "demo"}}
	receipts, prompt, err := s.resolveAgentContextResources(context.Background(), c, id, []agentContextResource{ref})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].UID != "uid-1" || strings.Contains(prompt, "must-never-enter-prompt") {
		t.Fatalf("resource context = %#v %q", receipts, prompt)
	}
	changed := receipts[0]
	changed.UID = "replaced"
	if _, _, err := s.resolveAgentContextResources(context.Background(), c, id, []agentContextResource{changed}); err == nil {
		t.Fatal("replaced object accepted")
	}
	if _, _, err := s.resolveAgentContextResources(context.Background(), c, identity{}, receipts); err == nil {
		t.Fatal("provider/background access reopened caller selections")
	}
	dyn.PrependReactor("get", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "example.railgrid.ai", Resource: "widgets"}, "demo", nil)
	})
	if _, _, err := s.resolveAgentContextResources(context.Background(), c, id, receipts); err == nil {
		t.Fatal("revoked caller read was accepted")
	}
}

func TestResourceReferenceNormalizationRejectsPathsAndCanonicalizesDuplicates(t *testing.T) {
	ref := agentContextResource{Provider: "example", ResourceRef: agentResourceReference{APIVersion: "example.railgrid.ai/v1alpha1", Kind: "Widget", Resource: "widgets", Name: "demo"}}
	got, err := normalizeAgentContextResources([]agentContextResource{ref, ref})
	if err != nil || len(got) != 1 {
		t.Fatalf("canonical references = %#v, %v", got, err)
	}
	for _, mutate := range []func(*agentContextResource){
		func(r *agentContextResource) { r.ResourceRef.Resource = "widgets/secret" },
		func(r *agentContextResource) { r.ResourceRef.Name = "../secret" },
		func(r *agentContextResource) { r.ResourceRef.Name = "demo%2Fsecret" },
		func(r *agentContextResource) { r.ResourceRef.APIVersion = "v1" },
		func(r *agentContextResource) { r.ResourceRef.Kind = "Widget?key" },
		func(r *agentContextResource) { r.Provider = "other/provider" },
	} {
		candidate := ref
		mutate(&candidate)
		if _, err := normalizeAgentContextResources([]agentContextResource{candidate}); err == nil {
			t.Fatalf("invalid coordinate accepted: %#v", candidate)
		}
	}
}

func TestAcceptedResourceCoordinatesSurviveBearerlessResumeWithoutGrantingReads(t *testing.T) {
	refs := []agentContextResource{{
		Provider: "example",
		ResourceRef: agentResourceReference{
			APIVersion: "example.railgrid.ai/v1alpha1", Kind: "Widget", Resource: "widgets", Name: "demo",
		},
		UID: "uid-1", ResourceVersion: "7",
	}}
	checkpoint := runCheckpoint{ContextResourceOwner: "browser-user-uuid", ContextResources: refs}

	// A canonical KCP identity on approval, or no caller during accepted-run
	// recovery, does not erase prompt-only coordinates already authorized at
	// initial admission. Tool reads continue through their normal authorization.
	for _, tc := range []struct {
		name     string
		caller   string
		recovery bool
	}{
		{name: "approval uses canonical KCP identity", caller: "system:authenticated:alice"},
		{name: "background recovery", recovery: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, owner, got, err := resumeAgentInputReferences(checkpoint, tc.caller, "", tc.recovery)
			if err != nil {
				t.Fatalf("resume accepted pointer context: %v", err)
			}
			if owner != checkpoint.ContextResourceOwner || len(got) != 1 || got[0] != refs[0] {
				t.Fatalf("resumed resource pointer = owner %q refs %#v", owner, got)
			}
		})
	}
}
