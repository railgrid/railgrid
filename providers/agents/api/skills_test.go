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

	agents "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant"
	"github.com/railgrid/provider-sdk/skills"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

func skillRuntimeFixture(t *testing.T) (*Server, taskRun, agentSkillSelection) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := agents.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	packageObject := &agents.Skill{TypeMeta: metav1.TypeMeta{APIVersion: agents.SchemeGroupVersion.String(), Kind: "Skill"}, ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: agents.SkillSpec{Document: "---\nname: demo\ndescription: verify work\n---\nInspect evidence before answering.", Resources: []agents.SkillResource{{Name: "checklist.md", Content: "Recheck permissions."}}}}
	dyn := fake.NewSimpleDynamicClient(scheme, packageObject)
	c := agentsclient.NewFromScope(tenant.NewScopeFromDynamic("cluster-a", dyn))
	run := taskRun{Agent: &agents.Agent{ObjectMeta: metav1.ObjectMeta{Name: "helper"}, Spec: agents.AgentSpec{Skills: []agents.AgentSkillReference{{Name: "workspace:demo"}}}}, Scope: store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "helper"}, ClusterID: "cluster-a", CR: clientCR{c}}
	s := &Server{}
	snapshot, err := s.buildAgentSkillSnapshot(context.Background(), c, run.Agent, identity{})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := snapshot.Find("workspace:demo")
	if !ok {
		t.Fatal("skill is missing")
	}
	return s, run, agentSkillSelection{ID: entry.QualifiedName, SHA256: entry.Digest}
}

func TestAgentSkillRuntimePinsSelectionsAndRequiresLoadingResources(t *testing.T) {
	s, run, selection := skillRuntimeFixture(t)
	_, prompt, tools, err := s.agentSkillRuntime(context.Background(), run, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "Inspect evidence before answering.") {
		t.Fatal("unselected skill body was injected eagerly")
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %d", len(tools))
	}
	if _, err := tools[1].Exec(context.Background(), `{"id":"workspace:demo","path":"checklist.md"}`); err == nil {
		t.Fatal("resource was readable before skill load")
	}
	if _, err := tools[0].Exec(context.Background(), `{"id":"workspace:demo"}`); err != nil {
		t.Fatal(err)
	}
	if got, err := tools[1].Exec(context.Background(), `{"id":"workspace:demo","path":"checklist.md"}`); err != nil || !strings.Contains(got, "Recheck permissions.") {
		t.Fatalf("resource = %q, %v", got, err)
	}
	_, prompt, _, err = s.agentSkillRuntime(context.Background(), run, []agentSkillSelection{selection}, false)
	if err != nil || !strings.Contains(prompt, "Inspect evidence before answering.") {
		t.Fatalf("selected guidance = %q, %v", prompt, err)
	}
	selection.SHA256 = "sha256:" + strings.Repeat("0", 64)
	if _, _, _, err := s.agentSkillRuntime(context.Background(), run, []agentSkillSelection{selection}, false); err == nil {
		t.Fatal("changed selection was accepted")
	}
	run.Agent.Spec.Skills = nil
	if _, _, _, err := s.agentSkillRuntime(context.Background(), run, []agentSkillSelection{{ID: "workspace:demo", SHA256: selection.SHA256}}, false); err == nil {
		t.Fatal("disabled selection was accepted")
	}
}

func TestResumeSkillCatalogRefreshPreservesAdmittedGuidance(t *testing.T) {
	s, run, selection := skillRuntimeFixture(t)
	ctx := context.Background()
	_, guidance, _, err := s.agentSkillRuntime(ctx, run, []agentSkillSelection{selection}, false)
	if err != nil {
		t.Fatal(err)
	}
	stale := selection
	stale.SHA256 = "sha256:" + strings.Repeat("0", 64)
	snapshot, _, tools, err := s.agentSkillRuntime(ctx, run, []agentSkillSelection{stale}, true)
	if err != nil {
		t.Fatal(err)
	}
	const staleRow = "Skill metadata: stale"
	checkpoint := engine.Checkpoint{Messages: []engine.CheckpointMessage{
		{Role: engine.RoleSystem, Content: guidance + staleRow},
	}}
	refreshAgentSkillCatalog(&checkpoint, agentSkillCatalogPrompt(snapshot))
	if len(checkpoint.Messages) != 2 ||
		!strings.Contains(checkpoint.Messages[0].Content, selection.SHA256) ||
		!strings.Contains(checkpoint.Messages[1].Content, "Inspect evidence before answering.") ||
		strings.Contains(checkpoint.Messages[1].Content, staleRow) {
		t.Fatalf("resumed messages did not preserve guidance and refresh metadata: %+v", checkpoint.Messages)
	}
	if _, err := tools[1].Exec(ctx, `{"id":"workspace:demo","path":"checklist.md"}`); err == nil {
		t.Fatal("a stale selection authorized resources from the current package")
	}
	refreshAgentSkillCatalog(&checkpoint, "")
	if len(checkpoint.Messages) != 1 || !strings.Contains(checkpoint.Messages[0].Content, "Inspect evidence before answering.") {
		t.Fatalf("an unavailable catalog affected selected guidance: %+v", checkpoint.Messages)
	}
}

func TestCatalogSkillDigestSurvivesAdmissionAndCheckpoint(t *testing.T) {
	_, run, selection := skillRuntimeFixture(t)
	parts := []agentChatContentPart{{Type: "skill", SkillID: selection.ID}}
	if _, _, _, _, err := normalizeAgentChatContentParts(parts, []agentSkillSelection{selection}, nil); err != nil {
		t.Fatalf("the catalog's own package digest was rejected at admission: %v", err)
	}
	run.SkillRefs = []agentSkillSelection{selection}
	_, _, pins, _, _ := checkpointAgentInputReferences(run)
	if len(pins) != 1 || pins[0] != selection {
		t.Fatalf("catalog pin lost at checkpoint: %#v", pins)
	}
	_, _, resumed, _, _, err := resumeAgentInputReferences(runCheckpoint{SkillRefs: pins}, "", "", true)
	if err != nil || len(resumed) != 1 || resumed[0] != selection {
		t.Fatalf("catalog pin lost on resume: %#v, %v", resumed, err)
	}
	for _, invalid := range []string{strings.TrimPrefix(selection.SHA256, "sha256:"), "sha256:" + strings.Repeat("A", 64), "sha256:123"} {
		bad := selection
		bad.SHA256 = invalid
		if _, _, _, _, err := normalizeAgentChatContentParts(parts, []agentSkillSelection{bad}, nil); err == nil {
			t.Fatalf("noncanonical skill digest accepted: %q", invalid)
		}
	}
}

func TestAgentSkillDetailsProjectDoesNotExposeBytesAsBase64(t *testing.T) {
	entry := skills.Entry{QualifiedName: "workspace:demo", PackagePath: "actual-cr-name", Name: "demo", Resources: []skills.Resource{{Path: "guide.md", Size: 5, Digest: "sha256:test"}}}
	view := skillView(entry)
	if view.PackageName != "actual-cr-name" || len(view.Resources) != 1 || view.Resources[0].Content != "" {
		t.Fatalf("public view = %#v", view)
	}
}

func TestSkillCatalogRequiresTenantIdentityInsteadOfProviderCredential(t *testing.T) {
	calls := 0
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer tenant-workload" || r.Header.Get("X-Railgrid-Workspace") != "workspace" || r.Header.Get("X-Railgrid-Tenant") != "cluster-a" || r.Header.Get("X-Railgrid-Cluster") != "cluster-a" {
			t.Error("catalog did not use the scoped tenant identity")
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer hub.Close()
	s := &Server{cfg: Config{HubURL: hub.URL}, providerBase: &rest.Config{BearerToken: "provider-workspace"}}
	if _, err := s.agentProviderCatalog(context.Background(), identity{}); err == nil || calls != 0 {
		t.Fatal("unscoped provider credential was used for tenant catalog discovery")
	}
	if _, err := s.agentProviderCatalog(context.Background(), identity{token: "tenant-workload", clusterID: "cluster-a", orgUUID: "org", workspaceUUID: "workspace"}); err != nil || calls != 1 {
		t.Fatalf("tenant catalog discovery: calls=%d, error=%v", calls, err)
	}
}
