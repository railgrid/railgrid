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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
)

func TestCaptureAgentArtifactRequiresCurrentSourceAuthorization(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "analyst"}
	now := time.Now().UTC()
	data := []byte(`{"rows":[{"id":7}]}`)
	input := `{"query":"latest"}`
	source := store.SourceReference{
		Object: store.SourceObject{Version: "v1", Resource: "reports", Name: "latest", UID: "uid-current"},
		Access: []store.SourceAccess{{Version: "v1", Resource: "reports", Name: "latest", Verb: "get"}},
	}
	html := "<main>latest report</main>"
	htmlDigest := sha256.Sum256([]byte(html))
	presentation := &store.ProviderPresentation{
		Version: 1, Title: "Latest report",
		MCPApp: &store.MCPAppResource{
			ResourceURI: "ui://railgrid-provider/report/abc", MIMEType: store.MCPAppMIMEType,
			HTML: html, SHA256: hex.EncodeToString(htmlDigest[:]),
		},
	}
	runID, callID := "run-1", "call-1"
	if err := st.SaveRun(ctx, scope, store.Run{ID: runID, AgentName: scope.AgentName, SessionID: "chat", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendToolCall(ctx, scope, store.ToolCall{
		ID: "audit-1", AgentName: scope.AgentName, RunID: runID, CallID: callID, SessionID: "chat",
		Tool: "edges__report", Args: input, Result: string(data), Outcome: "ok",
		Sources: []store.SourceReference{source}, Presentation: presentation, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendMessage(ctx, scope, store.Message{
		ID: "message-1", AgentName: scope.AgentName, RunID: runID, SessionID: "chat", Role: "tool",
		Content: string(data), Metadata: map[string]any{historyToolCallIDKey: callID}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	req := createAgentArtifactRequest{RunID: runID, SessionID: "chat", ToolCallID: callID}
	if _, err := s.captureAgentArtifact(ctx, scope, "agent-uid", "caller", req, nil); err == nil {
		t.Fatal("source-backed capture succeeded without current source authorization")
	}
	called := false
	artifact, err := s.captureAgentArtifact(ctx, scope, "agent-uid", "caller", req, func(_ context.Context, got []store.SourceReference) error {
		called = true
		if len(got) != 1 || got[0].Object.UID != source.Object.UID {
			t.Fatalf("sources passed for revalidation = %+v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || string(artifact.Data) != string(data) || string(artifact.Input) != input || artifact.Digest == "" || artifact.Presentation == nil || artifact.Presentation.MCPApp == nil {
		t.Fatalf("captured artifact = %+v, callback called=%t", artifact, called)
	}
	metadata := agentArtifactViewOf(artifact, false, false)
	if metadata.Data != "" || metadata.Input != "" || metadata.Presentation != nil || metadata.Sources[0].Object.UID != source.Object.UID {
		t.Fatalf("artifact metadata view leaked payload: %+v", metadata)
	}
	opened := agentArtifactViewOf(artifact, true, true)
	if opened.Data != string(data) || opened.Presentation == nil || opened.Presentation.MCPApp == nil || opened.EvidenceStatus != "source_identity_and_access_revalidated" {
		t.Fatalf("authorized artifact view = %+v", opened)
	}
	if _, found, err := st.GetAgentArtifact(ctx, scope, "agent-uid", "other-caller", artifact.ID); err != nil || found {
		t.Fatalf("artifact crossed owner boundary: found=%t err=%v", found, err)
	}
}

func TestAgentArtifactCapturePointerAndSafePresentation(t *testing.T) {
	sum := sha256.Sum256([]byte("saved result"))
	run := taskRun{ArtifactRefs: []store.AgentArtifactReference{{ArtifactID: "artifact-1", Digest: hex.EncodeToString(sum[:])}}}
	event := backend.ToolEvent{Name: "open_analysis_artifact", ID: "call-open", Args: `{"artifactID":"artifact-1","digest":"` + run.ArtifactRefs[0].Digest + `"}`}
	got := savedCaptureForToolEvent(run, event)
	if got == nil || got.ArtifactID != "artifact-1" || got.Digest != run.ArtifactRefs[0].Digest {
		t.Fatalf("saved capture pointer = %+v", got)
	}
	var presentation *store.ProviderPresentation
	if safeToolPresentation(presentation) != nil {
		t.Fatal("nil presentation was not preserved as nil")
	}
	presentation = &store.ProviderPresentation{Version: 1, Title: "Report", MCPApp: &store.MCPAppResource{HTML: "secret"}}
	safe := safeToolPresentation(presentation)
	if safe == nil || safe.Title != "Report" || safe.MCPApp != nil {
		t.Fatalf("safe event presentation = %+v", safe)
	}

	const html = "<html>private app resource</html>"
	htmlDigest := sha256.Sum256([]byte(html))
	event.Sources = []store.SourceReference{{
		Object: store.SourceObject{Version: "v1", Resource: "documents", Name: "report", UID: "uid-1"},
		Access: []store.SourceAccess{{Version: "v1", Resource: "documents", Name: "report", Verb: "get"}},
	}}
	event.Presentation = &store.ProviderPresentation{Version: 1, Title: "Report", MCPApp: &store.MCPAppResource{
		ResourceURI: "ui://railgrid-provider/report/resource",
		MIMEType:    store.MCPAppMIMEType,
		HTML:        html,
		SHA256:      hex.EncodeToString(htmlDigest[:]),
	}}
	metadata := persistedToolMetadata(run, event)
	if metadata["hasMcpApp"] != true {
		t.Fatalf("history metadata omitted validated App availability: %+v", metadata)
	}
	citation, ok := metadata["savedCapture"].(*savedAgentArtifactReference)
	if !ok || citation.ArtifactID != "artifact-1" || citation.Digest != run.ArtifactRefs[0].Digest {
		t.Fatalf("history metadata saved-capture pointer = %#v", metadata["savedCapture"])
	}
	historyPresentation, ok := metadata["presentation"].(*store.ProviderPresentation)
	if !ok || historyPresentation.MCPApp != nil {
		t.Fatalf("history metadata did not strip hosted HTML: %#v", metadata["presentation"])
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedMetadata), html) {
		t.Fatalf("history metadata contains hosted App HTML: %s", encodedMetadata)
	}
	event.Err = true
	if got := savedCaptureForToolEvent(run, event); got != nil {
		t.Fatalf("failed open emitted a citation pointer: %+v", got)
	}
	if got := persistedToolMetadata(run, event); got["hasMcpApp"] == true || got["savedCapture"] != nil {
		t.Fatalf("failed tool metadata advertised a protected result: %+v", got)
	}
}

func TestHubAggregateSourceResultFailsClosedWithoutCaller(t *testing.T) {
	source := store.SourceReference{
		Object: store.SourceObject{Version: "v1", Resource: "reports", Name: "latest", UID: "uid-current"},
		Access: []store.SourceAccess{{Version: "v1", Resource: "reports", Name: "latest", Verb: "get"}},
	}
	tool := backend.Tool{ExecRich: func(context.Context, string) (backend.Observation, error) {
		return backend.Observation{Text: "protected result", Sources: []store.SourceReference{source}}, nil
	}}
	s := &Server{}
	got, err := s.revalidateHubAggregateTool(taskRun{}, tool).ExecRich(t.Context(), `{}`)
	if err == nil || got.Text != "" {
		t.Fatalf("source-backed result without caller = %+v, err=%v", got, err)
	}
	plain := backend.Tool{ExecRich: func(context.Context, string) (backend.Observation, error) {
		return backend.Observation{Text: "plain MCP result"}, nil
	}}
	got, err = s.revalidateHubAggregateTool(taskRun{}, plain).ExecRich(t.Context(), `{}`)
	if err != nil || got.Text != "plain MCP result" {
		t.Fatalf("ordinary aggregate result changed without a source claim = %+v, err=%v", got, err)
	}
}

func TestRevalidateAgentArtifactSourcesChecksCurrentActionsAndObjectUID(t *testing.T) {
	objectGVR := schema.GroupVersionResource{Group: "reports.example", Version: "v1", Resource: "reports"}
	source := store.SourceReference{
		Object: store.SourceObject{Group: objectGVR.Group, Version: objectGVR.Version, Resource: objectGVR.Resource, Namespace: "team-a", Name: "weekly", UID: "uid-current"},
		Access: []store.SourceAccess{{Group: objectGVR.Group, Version: objectGVR.Version, Resource: objectGVR.Resource, Namespace: "team-a", Name: "weekly", Verb: "get"}},
	}
	object := func(uid string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": objectGVR.Group + "/" + objectGVR.Version, "kind": "Report",
			"metadata": map[string]any{"name": "weekly", "namespace": "team-a", "uid": uid},
		}}
	}

	t.Run("allows exact get and current UID", func(t *testing.T) {
		dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object("uid-current"))
		var review *unstructured.Unstructured
		dyn.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
			review = action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
			return true, selfSubjectAccessReview(true), nil
		})
		if err := revalidateAgentArtifactSources(t.Context(), dyn, []store.SourceReference{source}); err != nil {
			t.Fatalf("revalidate allowed source: %v", err)
		}
		group, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "group")
		resource, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "resource")
		verb, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "verb")
		namespace, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "namespace")
		name, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "name")
		if group != objectGVR.Group || resource != objectGVR.Resource || verb != "get" || namespace != "team-a" || name != "weekly" {
			t.Fatalf("SSAR resourceAttributes = group %q resource %q verb %q namespace %q name %q", group, resource, verb, namespace, name)
		}
		if got := len(dyn.Actions()); got != 2 {
			t.Fatalf("caller actions = %d, want SSAR plus exact object GET", got)
		}
	})

	t.Run("denies when current action is no longer allowed", func(t *testing.T) {
		dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object("uid-current"))
		dyn.PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, selfSubjectAccessReview(false), nil
		})
		if err := revalidateAgentArtifactSources(t.Context(), dyn, []store.SourceReference{source}); err == nil {
			t.Fatal("source survived a denied current caller action")
		}
		if got := len(dyn.Actions()); got != 1 {
			t.Fatalf("caller actions = %d, want only the denied SSAR", got)
		}
	})

	t.Run("denies a replacement object UID", func(t *testing.T) {
		dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object("uid-replacement"))
		dyn.PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, selfSubjectAccessReview(true), nil
		})
		if err := revalidateAgentArtifactSources(t.Context(), dyn, []store.SourceReference{source}); err == nil {
			t.Fatal("source survived an object replacement at the same name")
		}
		if got := len(dyn.Actions()); got != 2 {
			t.Fatalf("caller actions = %d, want SSAR plus UID check GET", got)
		}
	})

	t.Run("rejects invalid source and treats an empty source list as public", func(t *testing.T) {
		dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
		invalid := source
		invalid.Access = nil
		if err := revalidateAgentArtifactSources(t.Context(), dyn, []store.SourceReference{invalid}); err == nil {
			t.Fatal("invalid source passed validation")
		}
		if err := revalidateAgentArtifactSources(t.Context(), dyn, nil); err != nil {
			t.Fatalf("empty source list: %v", err)
		}
		if got := len(dyn.Actions()); got != 0 {
			t.Fatalf("invalid/empty sources caused %d caller API actions", got)
		}
	})
}

func selfSubjectAccessReview(allowed bool) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview",
		"status": map[string]any{"allowed": allowed},
	}}
}

func TestArtifactPointersSurviveCheckpointWithoutBytesAndRequireSameCaller(t *testing.T) {
	sum := sha256.Sum256([]byte("result bytes must never be checkpointed"))
	run := taskRun{
		ArtifactOwner: "alice",
		ArtifactRefs:  []store.AgentArtifactReference{{ArtifactID: "artifact-1", Digest: hex.EncodeToString(sum[:])}},
	}
	owner, refs := checkpointArtifactReferences(run)
	checkpoint := runCheckpoint{ArtifactOwner: owner, ArtifactRefs: refs}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"result bytes must never be checkpointed", "Bearer ", "authorizationHeader"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("checkpoint included forbidden payload %q: %s", forbidden, payload)
		}
	}
	var restored runCheckpoint
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatal(err)
	}
	gotOwner, gotRefs, err := resumeArtifactReferences(restored, "alice")
	if err != nil || gotOwner != "alice" || len(gotRefs) != 1 || gotRefs[0] != run.ArtifactRefs[0] {
		t.Fatalf("same-caller checkpoint restore = owner %q refs %+v err %v", gotOwner, gotRefs, err)
	}
	if _, _, err := resumeArtifactReferences(restored, "bob"); err == nil {
		t.Fatal("a different current caller restored another user's artifact references")
	}
	if _, _, err := resumeArtifactReferences(restored, ""); err == nil {
		t.Fatal("a caller-less recovery restored attended artifact references")
	}
	if _, _, err := resumeArtifactReferences(runCheckpoint{}, "alice"); err != nil {
		t.Fatalf("legacy checkpoint without artifact references: %v", err)
	}
}

func TestSourceBackedSavedResultResumeNeverFallsBackToProviderIdentity(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "analyst"}
	data := []byte(`{"rows":[{"id":7}]}`)
	sum := sha256.Sum256(data)
	source := store.SourceReference{
		Object: store.SourceObject{Version: "v1", Resource: "reports", Name: "latest", UID: "uid-current"},
		Access: []store.SourceAccess{{Version: "v1", Resource: "reports", Name: "latest", Verb: "get"}},
	}
	artifact := store.AgentArtifact{
		ID: "artifact-1", AgentName: scope.AgentName, AgentUID: "agent-uid", Owner: "alice",
		RunID: "run-1", SessionID: "chat", ToolCallID: "call-1", ToolName: "edges__report",
		MediaType: "application/json", Digest: hex.EncodeToString(sum[:]), Size: int64(len(data)), Data: data,
		Sources: []store.SourceReference{source}, CreatedAt: time.Now().UTC(),
	}
	if err := st.SaveAgentArtifact(ctx, scope, artifact); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	run := taskRun{
		Scope: scope, Agent: &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: scope.AgentName, UID: "agent-uid"}},
		ArtifactOwner: "alice", ArtifactRefs: []store.AgentArtifactReference{{ArtifactID: artifact.ID, Digest: artifact.Digest}},
		ClusterID: "cluster-1", // no HubToken exists on a data-plane approval resume
	}
	tool := s.openAgentArtifactTool(run)
	args, _ := json.Marshal(openAgentArtifactInput{ArtifactID: artifact.ID, Digest: artifact.Digest})
	if _, err := tool.ExecRich(ctx, string(args)); !errors.Is(err, backend.ErrToolResultReferenceCallerAuthorizationUnavailable) {
		t.Fatalf("source-backed read without caller bearer = %v, want caller authorization unavailable", err)
	}

	plainData := []byte("caller-owned result without source claims")
	plainDigest := sha256.Sum256(plainData)
	plainArtifact := artifact
	plainArtifact.ID = "artifact-plain"
	plainArtifact.ToolCallID = "call-plain"
	plainArtifact.Sources = nil
	plainArtifact.Digest = hex.EncodeToString(plainDigest[:])
	plainArtifact.Size = int64(len(plainData))
	plainArtifact.Data = plainData
	if err := st.SaveAgentArtifact(ctx, scope, plainArtifact); err != nil {
		t.Fatal(err)
	}
	plainRun := run
	plainRun.ArtifactRefs = []store.AgentArtifactReference{{ArtifactID: plainArtifact.ID, Digest: plainArtifact.Digest}}
	plainTool := s.openAgentArtifactTool(plainRun)
	plainArgs, _ := json.Marshal(openAgentArtifactInput{ArtifactID: plainArtifact.ID, Digest: plainArtifact.Digest})
	opened, err := plainTool.ExecRich(ctx, string(plainArgs))
	if err != nil || opened.Text != string(plainData) {
		t.Fatalf("same-owner source-free result read = %+v, err %v", opened, err)
	}
}
