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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
)

type sessionModelCredentialResolver struct {
	credential *agentsv1alpha1.ModelCredential
	getErr     error
	getNames   []string
}

func (r *sessionModelCredentialResolver) GetModelCredential(_ context.Context, name string) (*agentsv1alpha1.ModelCredential, error) {
	r.getNames = append(r.getNames, name)
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.credential != nil {
		return r.credential.DeepCopy(), nil
	}
	return &agentsv1alpha1.ModelCredential{}, nil
}

func (*sessionModelCredentialResolver) GetSecret(context.Context, string, string) (*corev1.Secret, error) {
	return &corev1.Secret{}, nil
}

func sessionModelCredentialObject(name, model string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.ModelCredentialGVR.GroupVersion().String(), "kind": "ModelCredential",
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"provider": "openai-compatible", "baseURL": "https://api.openai.com/v1", "model": model,
			"secretRef": map[string]any{"name": "key-" + name},
		},
	}}
}

func sessionModelAgentObject(name, defaultCredential string) *unstructured.Unstructured {
	obj := agentObject(name)
	if defaultCredential != "" {
		obj.Object["spec"] = map[string]any{
			"backend": map[string]any{"model": map[string]any{
				"credentials": map[string]any{"chat": defaultCredential},
			}},
		}
	}
	return obj
}

func sessionModelTestServer(t *testing.T, callers *conformance.FakeCallers) (*Server, http.Handler, store.Scope) {
	t.Helper()
	s := newGatedTestServer(t, callers)
	return s, gatedHandler(t, s), store.Scope{
		OrgUUID: unmappedOrg, WorkspaceUUID: conformanceCluster,
		ClusterID: conformanceCluster, AgentName: conformanceAgent,
	}
}

func serveSessionModelRequest(handler http.Handler, method, session, body string) *httptest.ResponseRecorder {
	path := verbBase(conformanceCluster, "agents", conformanceAgent) + "/session/" + session
	recorder := httptest.NewRecorder()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var requestBody interface{ Read([]byte) (int, error) }
	if reader != nil {
		requestBody = reader
	}
	handler.ServeHTTP(recorder, stamped(method, path, requestBody, conformanceUser))
	return recorder
}

type sessionModelLimitStore struct{ store.Store }

func (sessionModelLimitStore) PutSessionModelSettings(context.Context, store.Scope, string, store.SessionModelSettings) error {
	return store.ErrSessionModelLimit
}

func TestSessionModelExplicitCredentialAuthorizationHidesAndDoesNotSave(t *testing.T) {
	credential := sessionModelCredentialObject("visible-only-to-provider", "gpt-6-astra")
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent), credential},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList", agentsclient.ModelCredentialGVR: "ModelCredentialList",
		},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Resource == "agents" && a.Name == conformanceAgent
		},
	}
	s, handler, scope := sessionModelTestServer(t, callers)
	for _, tc := range []struct{ session, credential string }{
		{session: "known", credential: "visible-only-to-provider"},
		{session: "guessed", credential: "does-not-exist"},
	} {
		if err := s.store.PutSessionModelSettings(t.Context(), scope, tc.session, store.SessionModelSettings{ModelCredential: tc.credential}); err != nil {
			t.Fatal(err)
		}
	}

	var firstBody string
	for _, session := range []string{"known", "guessed"} {
		recorder := serveSessionModelRequest(handler, http.MethodGet, session, "")
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET session %s → %d, want 404: %s", session, recorder.Code, recorder.Body.String())
		}
		if session == "known" {
			firstBody = recorder.Body.String()
		} else if recorder.Body.String() != firstBody {
			t.Errorf("denied existing and absent credentials differ: existing %q, absent %q", firstBody, recorder.Body.String())
		}
	}

	before := store.SessionModelSettings{ModelCredential: "previous", ReasoningEffortByModel: map[string]string{"previous": "high"}}
	if err := s.store.PutSessionModelSettings(t.Context(), scope, "patch", before); err != nil {
		t.Fatal(err)
	}
	patch := `{"modelCredential":"visible-only-to-provider","reasoningEffortByModel":{"visible-only-to-provider":"high"}}`
	recorder := serveSessionModelRequest(handler, http.MethodPatch, "patch", patch)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("denied PATCH → %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	after, err := s.store.GetSessionModelSettings(t.Context(), scope, "patch")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("denied PATCH mutated preferences: got %+v, want %+v", after, before)
	}
	provider, err := callers.AsProvider(conformanceCluster)
	if err != nil {
		t.Fatal(err)
	}
	fake, ok := provider.(*dynamicfake.FakeDynamicClient)
	if !ok {
		t.Fatalf("provider client is %T, want the routed fake", provider)
	}
	for _, action := range fake.Actions() {
		if action.GetVerb() == "get" && action.GetResource() == agentsclient.ModelCredentialGVR {
			t.Fatalf("denied selector read modelcredential before authorization: %+v", action)
		}
	}
}

func TestSessionModelLimitReturnsTooManyRequests(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects:   []*unstructured.Unstructured{agentObject(conformanceAgent)},
		ListKinds: map[schema.GroupVersionResource]string{agentsclient.AgentGVR: "AgentList"},
		Allow: func(a conformance.Attributes) bool {
			return a.Resource == "agents" && a.Name == conformanceAgent && a.Verb == "get"
		},
	}
	s, handler, _ := sessionModelTestServer(t, callers)
	s.store = sessionModelLimitStore{Store: s.store}

	recorder := serveSessionModelRequest(handler, http.MethodPatch, "new-chat", `{}`)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("PATCH at session preference limit → %d, want 429: %s", recorder.Code, recorder.Body.String())
	}
	var response statusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Reason != "TooManyRequests" || response.Message != "Chat model preference limit reached; delete an unused chat and try again" {
		t.Fatalf("limit response = %+v", response)
	}
}

func TestSessionModelExplicitCredentialAuthorizationAllowsGetAndSave(t *testing.T) {
	credential := sessionModelCredentialObject("chat-model", "gpt-6-astra")
	var reviewedCredential bool
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent), credential},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList", agentsclient.ModelCredentialGVR: "ModelCredentialList",
		},
		Allow: func(a conformance.Attributes) bool {
			if a.Resource == "modelcredentials" {
				reviewedCredential = a.Group == agentsv1alpha1.GroupName && a.Version == agentsv1alpha1.Version &&
					a.Name == "chat-model" && a.Verb == "get"
				return reviewedCredential
			}
			return a.Resource == "agents" && a.Name == conformanceAgent && a.Verb == "get"
		},
	}
	s, handler, scope := sessionModelTestServer(t, callers)

	patch := `{"modelCredential":"chat-model","reasoningEffortByModel":{"chat-model":"high"}}`
	recorder := serveSessionModelRequest(handler, http.MethodPatch, "chat", patch)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authorized PATCH → %d: %s", recorder.Code, recorder.Body.String())
	}
	if !reviewedCredential {
		t.Fatal("explicit choice succeeded without the exact credential get review")
	}
	saved, err := s.store.GetSessionModelSettings(t.Context(), scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if saved.ModelCredential != "chat-model" || !reflect.DeepEqual(saved.ReasoningEffortByModel, map[string]string{"chat-model": "high"}) {
		t.Fatalf("saved preferences = %+v, want chat-model with high effort", saved)
	}

	recorder = serveSessionModelRequest(handler, http.MethodGet, "chat", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("authorized GET → %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestSessionModelResponseOnlyExposesSelectedCredentialEffort(t *testing.T) {
	modelA := sessionModelCredentialObject("A", "gpt-6-astra")
	modelB := sessionModelCredentialObject("B", "gpt-6-astra")
	authorizedCredential := "B"
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent), modelA, modelB},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList", agentsclient.ModelCredentialGVR: "ModelCredentialList",
		},
		Allow: func(a conformance.Attributes) bool {
			if a.Resource == "modelcredentials" {
				return a.Verb == "get" && a.Name == authorizedCredential
			}
			return a.Resource == "agents" && a.Name == conformanceAgent && a.Verb == "get"
		},
	}
	s, handler, scope := sessionModelTestServer(t, callers)
	initial := store.SessionModelSettings{
		ModelCredential:        "A",
		ReasoningEffortByModel: map[string]string{"A": "high", "B": "low"},
	}
	if err := s.store.PutSessionModelSettings(t.Context(), scope, "chat", initial); err != nil {
		t.Fatal(err)
	}

	// The caller cannot inspect A while it is selected, then switches to B
	// using only B's preference in the PATCH payload.
	denied := serveSessionModelRequest(handler, http.MethodGet, "chat", "")
	if denied.Code != http.StatusNotFound {
		t.Fatalf("GET unauthorized A → %d, want 404: %s", denied.Code, denied.Body.String())
	}
	patch := serveSessionModelRequest(handler, http.MethodPatch, "chat", `{"modelCredential":"B","reasoningEffortByModel":{"B":"low"}}`)
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH selected B → %d: %s", patch.Code, patch.Body.String())
	}
	if strings.Contains(patch.Body.String(), `"A"`) {
		t.Fatalf("response exposed hidden credential A: %s", patch.Body.String())
	}
	var response struct {
		store.SessionModelSettings
		SelectedCredential string `json:"selectedCredential"`
	}
	if err := json.Unmarshal(patch.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SelectedCredential != "B" || !reflect.DeepEqual(response.ReasoningEffortByModel, map[string]string{"B": "low"}) {
		t.Fatalf("PATCH response exposed preferences outside selected B: %+v", response)
	}

	get := serveSessionModelRequest(handler, http.MethodGet, "chat", "")
	if get.Code != http.StatusOK {
		t.Fatalf("GET selected B → %d: %s", get.Code, get.Body.String())
	}
	if strings.Contains(get.Body.String(), `"A"`) {
		t.Fatalf("GET response exposed hidden credential A: %s", get.Body.String())
	}
	if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SelectedCredential != "B" || !reflect.DeepEqual(response.ReasoningEffortByModel, map[string]string{"B": "low"}) {
		t.Fatalf("GET response exposed preferences outside selected B: %+v", response)
	}

	// JSON unmarshalling the singleton B map into the cloned persisted map did
	// not replace the hidden A entry. Once the caller is authorized for A again,
	// selecting it recovers its remembered effort.
	stored, err := s.store.GetSessionModelSettings(t.Context(), scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.ReasoningEffortByModel, initial.ReasoningEffortByModel) {
		t.Fatalf("PATCH with selected-only map dropped stored preferences: got %v, want %v", stored.ReasoningEffortByModel, initial.ReasoningEffortByModel)
	}
	authorizedCredential = "A"
	patch = serveSessionModelRequest(handler, http.MethodPatch, "chat", `{"modelCredential":"A","reasoningEffortByModel":{"B":"low"}}`)
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH selected A after authorization → %d: %s", patch.Code, patch.Body.String())
	}
	// json.Unmarshal merges object keys into an existing non-nil map, so reset
	// this response value before checking that only A was returned.
	response = struct {
		store.SessionModelSettings
		SelectedCredential string `json:"selectedCredential"`
	}{}
	if err := json.Unmarshal(patch.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SelectedCredential != "A" || !reflect.DeepEqual(response.ReasoningEffortByModel, map[string]string{"A": "high"}) {
		t.Fatalf("return to A did not recover its selected preference: %+v", response)
	}
}

func TestSessionModelPatchOnlyMutatesSelectedCredentialEffort(t *testing.T) {
	modelA := sessionModelCredentialObject("A", "gpt-6-astra")
	modelB := sessionModelCredentialObject("B", "gpt-6-astra")
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent), modelA, modelB},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList", agentsclient.ModelCredentialGVR: "ModelCredentialList",
		},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && ((a.Resource == "agents" && a.Name == conformanceAgent) || a.Resource == "modelcredentials")
		},
	}
	s, handler, scope := sessionModelTestServer(t, callers)
	initial := store.SessionModelSettings{
		ModelCredential:        "A",
		ReasoningEffortByModel: map[string]string{"A": "high", "B": "low", "other": "low"},
	}
	if err := s.store.PutSessionModelSettings(t.Context(), scope, "before-first-message", initial); err != nil {
		t.Fatal(err)
	}

	// Model selection and its preferences are persisted before any transcript
	// exists. A client may submit stale known keys and arbitrary additions, but
	// only the newly selected B entry is allowed to change.
	patch := serveSessionModelRequest(handler, http.MethodPatch, "before-first-message", `{"modelCredential":"B","reasoningEffortByModel":{"A":"low","B":"medium","other":"high","unregistered":"max"}}`)
	if patch.Code != http.StatusOK {
		t.Fatalf("select B → %d: %s", patch.Code, patch.Body.String())
	}
	settings, err := s.store.GetSessionModelSettings(t.Context(), scope, "before-first-message")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "high", "B": "medium", "other": "low"}
	if !reflect.DeepEqual(settings.ReasoningEffortByModel, want) {
		t.Fatalf("selected B patch changed a non-selected preference: got %v, want %v", settings.ReasoningEffortByModel, want)
	}

	// When switching back, a stale B entry in the payload is ignored and A's
	// remembered effort is restored from storage.
	patch = serveSessionModelRequest(handler, http.MethodPatch, "before-first-message", `{"modelCredential":"A","reasoningEffortByModel":{"B":"high","unregistered":"low"}}`)
	if patch.Code != http.StatusOK {
		t.Fatalf("return to A → %d: %s", patch.Code, patch.Body.String())
	}
	var response struct {
		store.SessionModelSettings
		SelectedCredential string `json:"selectedCredential"`
	}
	if err := json.Unmarshal(patch.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SelectedCredential != "A" || !reflect.DeepEqual(response.ReasoningEffortByModel, map[string]string{"A": "high"}) {
		t.Fatalf("switch back did not restore only A's remembered effort: %+v", response)
	}
	settings, err = s.store.GetSessionModelSettings(t.Context(), scope, "before-first-message")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(settings.ReasoningEffortByModel, want) {
		t.Fatalf("switching back mutated non-selected preferences: got %v, want %v", settings.ReasoningEffortByModel, want)
	}
}

func TestSessionModelInheritedAgentDefaultDoesNotRequireCredentialGet(t *testing.T) {
	credential := sessionModelCredentialObject("agent-default", "gpt-6-astra")
	credentialReviews := 0
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{sessionModelAgentObject(conformanceAgent, "agent-default"), credential},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList", agentsclient.ModelCredentialGVR: "ModelCredentialList",
		},
		Allow: func(a conformance.Attributes) bool {
			if a.Resource == "modelcredentials" {
				credentialReviews++
			}
			return a.Resource == "agents" && a.Name == conformanceAgent && a.Verb == "get"
		},
	}
	_, handler, _ := sessionModelTestServer(t, callers)
	recorder := serveSessionModelRequest(handler, http.MethodGet, "default", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET inherited default → %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		SelectedCredential string `json:"selectedCredential"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SelectedCredential != "agent-default" {
		t.Fatalf("selectedCredential = %q, want agent-default", response.SelectedCredential)
	}
	if credentialReviews != 0 {
		t.Fatalf("inherited default made %d credential reviews, want none", credentialReviews)
	}
}

func TestResolveChatModelSettingsRechecksExplicitCredentialOnFreshAdmission(t *testing.T) {
	allowCredential := false
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects:   []*unstructured.Unstructured{sessionModelCredentialObject("chosen", "gpt-6-astra")},
		ListKinds: map[schema.GroupVersionResource]string{agentsclient.ModelCredentialGVR: "ModelCredentialList"},
		Allow: func(a conformance.Attributes) bool {
			return allowCredential && a.Resource == "modelcredentials" && a.Name == "chosen" && a.Verb == "get"
		},
	}
	provider, err := callers.AsProvider(conformanceCluster)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "analyst"}
	if err := s.store.PutSessionModelSettings(t.Context(), scope, "session", store.SessionModelSettings{ModelCredential: "chosen"}); err != nil {
		t.Fatal(err)
	}
	ctx := dataplane.WithProxiedIdentity(t.Context(), dataplane.ProxiedIdentity{User: conformanceUser, Groups: []string{"system:authenticated"}})
	ctx = withGate(ctx, &gateInfo{provider: provider})
	newRun := func() taskRun {
		return taskRun{Scope: scope, SessionID: "session", Trigger: agentsv1alpha1.RunTriggerChat, Agent: &agentsv1alpha1.Agent{}}
	}

	denied := newRun()
	err = s.resolveChatModelSettings(ctx, &denied)
	requestErr, isRequestErr := err.(*requestError)
	if !isRequestErr || requestErr.code != http.StatusNotFound {
		t.Fatalf("revoked explicit preference error = %v, want generic 404", err)
	}
	if denied.ModelSettings != nil {
		t.Fatal("denied fresh admission pinned an explicit credential")
	}

	allowCredential = true
	allowed := newRun()
	if err := s.resolveChatModelSettings(ctx, &allowed); err != nil {
		t.Fatalf("authorized fresh admission: %v", err)
	}
	if allowed.ModelSettings == nil || allowed.ModelSettings.ModelCredential != "chosen" {
		t.Fatalf("authorized admission did not pin chosen credential: %+v", allowed.ModelSettings)
	}

	// A resumed run carries its admission-time immutable snapshot and does not
	// re-read the newer session preference or require a fresh selector grant.
	if err := s.store.PutSessionModelSettings(t.Context(), scope, "session", store.SessionModelSettings{ModelCredential: "later"}); err != nil {
		t.Fatal(err)
	}
	resumed := newRun()
	resumed.ModelSettings = allowed.ModelSettings
	allowCredential = false
	if err := s.resolveChatModelSettings(ctx, &resumed); err != nil {
		t.Fatalf("resolve pinned resumed run: %v", err)
	}
	if resumed.ModelSettings.ModelCredential != "chosen" {
		t.Fatalf("resumed snapshot changed to %q", resumed.ModelSettings.ModelCredential)
	}
}

func TestResolveMCPChatModelSettingsReadsExplicitCredentialAsCaller(t *testing.T) {
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "analyst"}
	if err := s.store.PutSessionModelSettings(t.Context(), scope, "session", store.SessionModelSettings{ModelCredential: "chosen"}); err != nil {
		t.Fatal(err)
	}
	resolver := &sessionModelCredentialResolver{getErr: apierrors.NewForbidden(agentsclient.ModelCredentialGVR.GroupResource(), "chosen", errors.New("private"))}
	run := taskRun{
		Creds: resolver, HubToken: "caller-token", Scope: scope, SessionID: "session",
		Trigger: agentsv1alpha1.RunTriggerChat, Agent: &agentsv1alpha1.Agent{},
	}
	err := s.resolveChatModelSettings(t.Context(), &run)
	requestErr, isRequestErr := err.(*requestError)
	if !isRequestErr || requestErr.code != http.StatusNotFound {
		t.Fatalf("MCP caller denial = %v, want generic 404", err)
	}
	if !reflect.DeepEqual(resolver.getNames, []string{"chosen"}) {
		t.Fatalf("MCP caller credential reads = %v, want [chosen]", resolver.getNames)
	}

	resolver = &sessionModelCredentialResolver{credential: &agentsv1alpha1.ModelCredential{}}
	run.Creds = resolver
	if err := s.resolveChatModelSettings(t.Context(), &run); err != nil {
		t.Fatalf("authorized MCP caller: %v", err)
	}
	if !reflect.DeepEqual(resolver.getNames, []string{"chosen"}) {
		t.Fatalf("authorized MCP admission did not verify caller get: %v", resolver.getNames)
	}
}

func TestSessionModelSwitchClearsOnlyStaleRememberedEffort(t *testing.T) {
	modelA := sessionModelCredentialObject("A", "gpt-6-astra")
	modelB := sessionModelCredentialObject("B", "gpt-6-astra")
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent), modelA, modelB},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList", agentsclient.ModelCredentialGVR: "ModelCredentialList",
		},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && ((a.Resource == "agents" && a.Name == conformanceAgent) || a.Resource == "modelcredentials")
		},
	}
	s, handler, scope := sessionModelTestServer(t, callers)
	initial := store.SessionModelSettings{
		ModelCredential:        "A",
		ReasoningEffortByModel: map[string]string{"A": "high", "B": "high", "other": "low"},
	}
	if err := s.store.PutSessionModelSettings(t.Context(), scope, "chat", initial); err != nil {
		t.Fatal(err)
	}

	// Switching to B keeps its supported remembered value. The request carries
	// only B's selected preference; decoding into the cloned non-nil map merges
	// that entry instead of dropping A or the unrelated entry.
	recorder := serveSessionModelRequest(handler, http.MethodPatch, "chat", `{"modelCredential":"B","reasoningEffortByModel":{"B":"high"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("switch to B → %d: %s", recorder.Code, recorder.Body.String())
	}
	var selectedB struct {
		store.SessionModelSettings
		SelectedCredential string `json:"selectedCredential"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &selectedB); err != nil {
		t.Fatal(err)
	}
	if selectedB.SelectedCredential != "B" || !reflect.DeepEqual(selectedB.ReasoningEffortByModel, map[string]string{"B": "high"}) {
		t.Fatalf("switch response should expose only B: %+v", selectedB)
	}
	settings, err := s.store.GetSessionModelSettings(t.Context(), scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if settings.ReasoningEffortByModel["B"] != "high" || settings.ReasoningEffortByModel["A"] != "high" || settings.ReasoningEffortByModel["other"] != "low" {
		t.Fatalf("switch to supported B lost remembered effort: %+v", settings.ReasoningEffortByModel)
	}

	// The target's metadata changes while B is active. Returning to A leaves
	// the exact stale remembered value in place, so the server clears just A
	// and lets the model use its default.
	dyn, err := callers.AsProvider(conformanceCluster)
	if err != nil {
		t.Fatal(err)
	}
	aObject, err := dyn.Resource(agentsclient.ModelCredentialGVR).Get(t.Context(), "A", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(aObject.Object, "custom-model-with-no-known-efforts", "spec", "model"); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(agentsclient.ModelCredentialGVR).Update(t.Context(), aObject, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	recorder = serveSessionModelRequest(handler, http.MethodPatch, "chat", `{"modelCredential":"A"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("return to A after metadata change → %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		store.SessionModelSettings
		SelectedCredential string `json:"selectedCredential"`
		DefaultEffort      string `json:"defaultEffort"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SelectedCredential != "A" || response.DefaultEffort != "" {
		t.Fatalf("recovered response selected=%q default=%q: %+v", response.SelectedCredential, response.DefaultEffort, response)
	}
	if len(response.ReasoningEffortByModel) != 0 {
		t.Fatalf("response should omit all non-selected and cleared efforts: %+v", response.ReasoningEffortByModel)
	}
	settings, err = s.store.GetSessionModelSettings(t.Context(), scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if settings.ReasoningEffortByModel["B"] != "high" || settings.ReasoningEffortByModel["other"] != "low" {
		t.Fatalf("clearing stale A changed other stored efforts: %+v", settings.ReasoningEffortByModel)
	}

	// A different unsupported value is new user input, not a stale remembered
	// preference, and remains a validation error without mutating the store.
	before := settings
	recorder = serveSessionModelRequest(handler, http.MethodPatch, "chat", `{"modelCredential":"A","reasoningEffortByModel":{"A":"high"}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("new unsupported effort → %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	after, err := s.store.GetSessionModelSettings(t.Context(), scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected new effort mutated settings: got %+v, want %+v", after, before)
	}
}
