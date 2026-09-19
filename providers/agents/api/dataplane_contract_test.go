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
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"github.com/railgrid/provider-sdk/tenantaccess"

	agentsclient "github.com/railgrid/provider-agents/client"
)

const (
	conformanceCluster = "aaaaaaaaaaaaaaaa"
	conformanceToken   = "caller-token"
	conformanceAgent   = "scout"
)

// TestDataPlaneConformance drives the provider's whole data-plane mux through
// the contract's observable behaviour: granted verb 200, missing bearer 401,
// path/header cluster mismatch 400, workspace A's token cannot reach workspace
// B, an ungranted verb denied without disclosing the object, and a malformed
// path refused where the grammar says it is.
//
// It is the one test that proves the gates are actually wired rather than
// merely imported, and it is the same suite every other provider is held to.
func TestDataPlaneConformance(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster,
		Token:   conformanceToken,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent)},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList",
		},
		// One granted verb and one refused one, on the same object, so the
		// difference the suite observes is the grant and nothing else.
		Allow: func(a conformance.Attributes) bool {
			return a.Resource == "agents" && a.Name == conformanceAgent && a.Subresource == "sessions"
		},
	}

	server := newGatedTestServer(t, callers)
	base := "/dataplane/clusters/" + conformanceCluster + "/agents/" + conformanceAgent

	conformance.Test(t, server.DataPlane(), conformance.Fixtures{
		Callers:     callers,
		Method:      "GET",
		GrantedPath: base + "/sessions",
		DeniedPath:  base + "/messages",
		MalformedPaths: []string{
			// A traversal segment, which must be refused rather than cleaned
			// into a path addressing a different object.
			"/dataplane/clusters/" + conformanceCluster + "/agents/../" + conformanceAgent + "/sessions",
			// An empty segment.
			"/dataplane/clusters/" + conformanceCluster + "//" + conformanceAgent + "/sessions",
			// A workspace path where a logical-cluster ID belongs.
			"/dataplane/clusters/root:railgrid:tenants:acme/agents/" + conformanceAgent + "/sessions",
			// A verb this provider does not serve.
			base + "/delegate",
		},
		// These are plain REST verbs, not actions: no {"input": …} envelope
		// and no declared input limit.
		Body:           "",
		SkipStrictBody: true,
	})
}

// TestRunVerbsGateOnTheRunObject: the run verbs address a Run, so the two
// gates run against the Run — and the agent whose store rows they then read
// comes off that object's spec, never off the request.
//
// That is the whole reason Run became a kind. While a run was reachable only
// as a tail segment on its agent, the gates could see the agent and not the
// run: a caller granted one agent's runs was granted all of them, and nothing
// stopped a request naming run X while claiming agent Y.
func TestRunVerbsGateOnTheRunObject(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster,
		Token:   conformanceToken,
		Objects: []*unstructured.Unstructured{
			agentObject(conformanceAgent),
			runObject("r1", conformanceAgent),
		},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList",
			agentsclient.RunGVR:   "RunList",
		},
		Allow: func(a conformance.Attributes) bool {
			return a.Resource == "runs" && a.Subresource == "trace"
		},
	}
	handler := newGatedTestServer(t, callers).DataPlane()
	base := "/dataplane/clusters/" + conformanceCluster + "/runs"

	for _, tc := range []struct {
		method, path string
		want         int
		why          string
	}{
		{"GET", base + "/r1/trace", 404, "the run object exists and the verb is granted, but the store has no rows for it"},
		{"GET", base + "/r1/wait", 404, "the verb is not granted, and a denial must not disclose that the run exists"},
		{"POST", base + "/r1/cancel", 404, "same"},
		{"GET", base + "/r2/trace", 404, "no such run object"},
		{"GET", base + "/r1/unknown", 400, "not a verb this provider serves"},
		{"GET", base + "/r1/trace/extra", 400, "trace takes no tail"},
	} {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		request.Header.Set("Authorization", "Bearer "+conformanceToken)
		request.Header.Set("X-Railgrid-Cluster", conformanceCluster)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != tc.want {
			t.Errorf("%s %s → %d, want %d (%s): %s", tc.method, tc.path, recorder.Code, tc.want, tc.why, recorder.Body.String())
		}
	}
}

// newGatedTestServer builds a Server whose data plane can actually complete a
// request: an in-memory store, a tenant client (so requireClient does not
// answer 501), a stubbed workspace lookup (so it does not answer 400 for want
// of an org/workspace scope), and the caller factory under test.
func newGatedTestServer(t *testing.T, callers *conformance.FakeCallers) *Server {
	t.Helper()
	s, err := New(t.Context(), Config{InMemoryStore: true, HubURL: "https://hub.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	s.callers = callers
	s.workspaces = func(_ context.Context, clusterID, _ string) (tenantaccess.Workspace, error) {
		return tenantaccess.Workspace{
			Path:          "root:railgrid:tenants:org:ws",
			OrgUUID:       "org-" + clusterID,
			WorkspaceUUID: "ws-" + clusterID,
		}, nil
	}
	return s
}

func runObject(name, agent string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.RunGVR.GroupVersion().String(),
		"kind":       "Run",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"agentRef": agent, "trigger": "api"},
		"status":     map[string]any{"phase": "Running"},
	}}
}

func agentObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.AgentGVR.GroupVersion().String(),
		"kind":       "Agent",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{},
	}}
}

// TestDataPlaneVerbsMatchManifest keeps the three copies of the verb surface in
// step: the route table this package serves, the CatalogEntry in manifest.yaml,
// and the chart's copy of it (the one that actually reaches production).
//
// A verb that is served but not declared cannot be granted to a workload
// identity — the hub's scoped-identity policy refuses to mint a capability for
// a coordinate no CatalogEntry claims. A verb that is declared but not served
// is worse: the hub will happily mint the capability and the call 404s.
func TestDataPlaneVerbsMatchManifest(t *testing.T) {
	manifest := readVerbBlock(t, "../manifest.yaml")
	chart := readVerbBlock(t, "../deploy/chart/templates/catalogentry.yaml")
	if manifest != chart {
		t.Fatalf("manifest.yaml and the chart's catalogentry.yaml declare different verbs.\nmanifest:\n%s\nchart:\n%s", manifest, chart)
	}

	declared := parseVerbBlock(manifest)
	served := map[string]verbFacts{}
	for resource, routes := range (&Server{}).routes() {
		for verb, route := range routes.verbs {
			served[resource+"/"+verb] = verbFacts{stream: route.stream, readOnly: route.readOnly}
		}
	}

	for coordinate, want := range declared {
		got, ok := served[coordinate]
		if !ok {
			t.Errorf("%s is declared in the CatalogEntry but not served", coordinate)
			continue
		}
		if got != want {
			t.Errorf("%s: declared %+v, served %+v", coordinate, want, got)
		}
	}
	for coordinate := range served {
		if _, ok := declared[coordinate]; !ok {
			t.Errorf("%s is served but not declared in the CatalogEntry, so no workload identity can be granted it", coordinate)
		}
	}
}

type verbFacts struct {
	stream   bool
	readOnly bool
}

// readVerbBlock returns the spec.dataPlane.verbs block of a CatalogEntry
// verbatim. Comparing the two files as TEXT is deliberate: "identical" is the
// rule the contract states, and a textual comparison also catches a difference
// in description or ordering that a parse would normalise away.
func readVerbBlock(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "  dataPlane:\n    verbs:\n"
	start := strings.Index(string(raw), marker)
	if start < 0 {
		t.Fatalf("%s has no spec.dataPlane.verbs block", path)
	}
	rest := string(raw)[start+len(marker):]
	var block []string
	for line := range strings.SplitSeq(rest, "\n") {
		// The block ends at the next key or comment at spec-member indentation.
		if line != "" && !strings.HasPrefix(line, "      ") {
			break
		}
		block = append(block, line)
	}
	return strings.TrimRight(strings.Join(block, "\n"), "\n")
}

// parseVerbBlock reads the fixed shape writeVerbBlock produces. It is a hand
// parser rather than a YAML unmarshal so this test adds no dependency to a
// provider binary's module graph for the sake of nine lines of structure.
func parseVerbBlock(block string) map[string]verbFacts {
	out := map[string]verbFacts{}
	var resource, verb string
	facts := verbFacts{}
	flush := func() {
		if resource != "" && verb != "" {
			out[resource+"/"+verb] = facts
		}
		resource, verb, facts = "", "", verbFacts{}
	}
	for line := range strings.SplitSeq(block, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "- resource:"):
			flush()
			resource = strings.TrimSpace(strings.TrimPrefix(trimmed, "- resource:"))
		case strings.HasPrefix(trimmed, "verb:"):
			verb = strings.TrimSpace(strings.TrimPrefix(trimmed, "verb:"))
		case trimmed == "stream: true":
			facts.stream = true
		case trimmed == "readOnly: true":
			facts.readOnly = true
		}
	}
	flush()
	return out
}

// TestDataPlaneVerbNamesAreGrantable asserts every served verb is a legal RBAC
// subresource half — the CatalogEntry's own pattern for it — because the verb
// name IS the coordinate a tenant writes into a ClusterRole.
func TestDataPlaneVerbNamesAreGrantable(t *testing.T) {
	names := []string{}
	for resource, routes := range (&Server{}).routes() {
		for verb := range routes.verbs {
			names = append(names, resource+"/"+verb)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		resource, verb, _ := strings.Cut(name, "/")
		for _, part := range []string{resource, verb} {
			if part == "" || strings.ContainsAny(part, "/ \t") {
				t.Errorf("%q is not a usable {resource}/{verb} coordinate", name)
			}
		}
		if strings.ToLower(verb) != verb {
			t.Errorf("verb %q must be lowercase: it is the subresource half of an RBAC rule", verb)
		}
	}
}
