/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

type nativeBrowserSnapshotGate struct {
	arrivals chan int
	release  chan struct{}
	once     sync.Once
	mu       sync.Mutex
	calls    int
}

func newNativeBrowserSnapshotGate() *nativeBrowserSnapshotGate {
	return &nativeBrowserSnapshotGate{
		arrivals: make(chan int, 8),
		release:  make(chan struct{}),
	}
}

func (gate *nativeBrowserSnapshotGate) releaseFirst() {
	gate.once.Do(func() { close(gate.release) })
}

type nativeBrowserConcurrentCallCounts struct {
	mu    sync.Mutex
	tools map[string]int
}

func installNativeBrowserConcurrencyTransport(server *Server, gate *nativeBrowserSnapshotGate, oversizedTool string) *nativeBrowserConcurrentCallCounts {
	counts := &nativeBrowserConcurrentCallCounts{tools: map[string]int{}}
	var nextSession atomic.Int64
	server.sandboxDataPlaneClientFactory = nativeBrowserTestClient(func(request *http.Request) (*http.Response, error) {
		var envelope nativeBrowserTestEnvelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			return nil, err
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "initialize":
			recorder.Header().Set("Mcp-Session-Id", "concurrent-session-"+strconv.FormatInt(nextSession.Add(1), 10))
			_ = json.NewEncoder(recorder).Encode(map[string]any{
				"jsonrpc": "2.0", "id": envelope.ID,
				"result": map[string]any{"protocolVersion": browserMCPProtocolVersion},
			})
		case "notifications/initialized":
			recorder.WriteHeader(http.StatusAccepted)
		case "tools/call":
			counts.mu.Lock()
			counts.tools[envelope.Params.Name]++
			counts.mu.Unlock()
			if envelope.Params.Name == oversizedTool {
				_, _ = recorder.WriteString(strings.Repeat("x", projectAssistantPreviewInspectionMaxResponse+1))
				return recorder.Result(), nil
			}
			if gate != nil && envelope.Params.Name == browserMCPToolSnapshot {
				gate.mu.Lock()
				gate.calls++
				call := gate.calls
				gate.mu.Unlock()
				gate.arrivals <- call
				if call == 1 {
					<-gate.release
				}
			}
			content := "ok"
			switch envelope.Params.Name {
			case browserMCPToolNavigate, browserMCPToolSnapshot:
				content = "- Page URL: https://demo.preview.example/\n- Page Title: Demo\n- Page Snapshot:\n- generic [ref=e1]:\n"
			case "browser_tabs":
				content = "- 0: https://demo.preview.example/\n"
			}
			_ = json.NewEncoder(recorder).Encode(map[string]any{
				"jsonrpc": "2.0", "id": envelope.ID,
				"result": map[string]any{
					"isError": false,
					"content": []map[string]string{{"type": "text", "text": content}},
				},
			})
		default:
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": map[string]any{}})
		}
		return recorder.Result(), nil
	})
	return counts
}

func nativeBrowserConcurrencyRequest(threadID, runID string) projectAssistantToolCallRequest {
	return projectAssistantToolCallRequest{
		Identity: identity{
			tenant:    "root:railgrid:tenants:org-a:ws-a",
			clusterID: "cluster-native-browser-concurrency",
			user:      "alice",
		},
		Project: &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{
			Name: "demo", UID: types.UID("project-uid"),
		}},
		AssistantThreadID: threadID,
		AssistantRunID:    runID,
	}
}

type nativeBrowserCallResult struct {
	result string
	err    error
}

func startNativeBrowserSnapshotCall(server *Server, request projectAssistantToolCallRequest, ctx context.Context) <-chan nativeBrowserCallResult {
	result := make(chan nativeBrowserCallResult, 1)
	go func() {
		value, err := server.callProjectAssistantNativeBrowserTool(ctx, request, browserMCPToolSnapshot, projectAssistantToolRiskRead)
		result <- nativeBrowserCallResult{result: value, err: err}
	}()
	return result
}

func waitForNativeBrowserSnapshot(t *testing.T, gate *nativeBrowserSnapshotGate, want int) {
	t.Helper()
	select {
	case got := <-gate.arrivals:
		if got != want {
			t.Fatalf("browser_snapshot arrival = %d, want %d", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("browser_snapshot call %d did not reach the test transport", want)
	}
}

func waitForNativeBrowserOwnerGateRefs(t *testing.T, manager *projectAssistantBrowserSessionManager, owner browserSessionOwner, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		gate := manager.ownerGates[owner.key()]
		refs := 0
		if gate != nil {
			refs = gate.refs
		}
		manager.mu.Unlock()
		if refs == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("owner gate references did not reach %d", want)
}

func TestProjectAssistantNativeBrowserDistinctOwnersOverlap(t *testing.T) {
	server := newNativeBrowserTestServer(t)
	gate := newNativeBrowserSnapshotGate()
	t.Cleanup(gate.releaseFirst)
	installNativeBrowserConcurrencyTransport(server, gate, "")
	requestA := nativeBrowserConcurrencyRequest("thread-a", "run-a")
	requestB := nativeBrowserConcurrencyRequest("thread-b", "run-b")

	callA := startNativeBrowserSnapshotCall(server, requestA, context.Background())
	waitForNativeBrowserSnapshot(t, gate, 1)
	callB := startNativeBrowserSnapshotCall(server, requestB, context.Background())
	select {
	case arrival := <-gate.arrivals:
		if arrival != 2 {
			gate.releaseFirst()
			t.Fatalf("second owner snapshot arrival = %d, want 2", arrival)
		}
	case <-time.After(2 * time.Second):
		gate.releaseFirst()
		<-callA
		<-callB
		t.Fatal("second isolated owner did not reach the browser while the first owner was active")
	}
	gate.releaseFirst()
	for index, result := range []<-chan nativeBrowserCallResult{callA, callB} {
		select {
		case call := <-result:
			if call.err != nil || call.result == "" {
				t.Fatalf("owner %d native browser call = %q, err %v", index+1, call.result, call.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("owner %d native browser call did not finish", index+1)
		}
	}
	server.browserSessions.mu.Lock()
	defer server.browserSessions.mu.Unlock()
	if got := len(server.browserSessions.sessions); got != 2 {
		t.Fatalf("managed sessions after overlapping owners = %d, want two isolated owner sessions", got)
	}
}

func TestProjectAssistantNativeBrowserPreparationForOneOwnerDoesNotBlockAnother(t *testing.T) {
	server := newNativeBrowserTestServer(t)
	installNativeBrowserConcurrencyTransport(server, nil, "")
	alicePreparing := make(chan struct{})
	releaseAlice := make(chan struct{})
	bobResolved := make(chan struct{}, 1)
	var releaseOnce sync.Once
	releasePreparation := func() { releaseOnce.Do(func() { close(releaseAlice) }) }
	t.Cleanup(releasePreparation)
	var aliceOnce sync.Once
	server.previewInspectionResolveURL = func(_ context.Context, id identity, _ *aiv1alpha1.Project) (string, error) {
		switch id.user {
		case "alice":
			aliceOnce.Do(func() { close(alicePreparing) })
			<-releaseAlice
		case "bob":
			bobResolved <- struct{}{}
		}
		return "https://demo.preview.example/", nil
	}
	requestA := nativeBrowserConcurrencyRequest("thread-a", "run-a")
	callA := startNativeBrowserSnapshotCall(server, requestA, context.Background())
	select {
	case <-alicePreparing:
	case <-time.After(2 * time.Second):
		releasePreparation()
		<-callA
		t.Fatal("first owner did not reach preview preparation")
	}
	requestB := nativeBrowserConcurrencyRequest("thread-b", "run-b")
	requestB.Identity.user = "bob"
	callB := startNativeBrowserSnapshotCall(server, requestB, context.Background())
	select {
	case <-bobResolved:
		releasePreparation()
	case <-time.After(2 * time.Second):
		releasePreparation()
		<-callA
		<-callB
		t.Fatal("one owner's preview preparation blocked another isolated owner")
	}
	for index, result := range []<-chan nativeBrowserCallResult{callA, callB} {
		select {
		case call := <-result:
			if call.err != nil || call.result == "" {
				t.Fatalf("owner %d native browser call = %q, err %v", index+1, call.result, call.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("owner %d native browser call did not finish", index+1)
		}
	}
}

func TestProjectAssistantNativeBrowserSameOwnerSerializesAndWaitHonorsCancellation(t *testing.T) {
	server := newNativeBrowserTestServer(t)
	gate := newNativeBrowserSnapshotGate()
	t.Cleanup(gate.releaseFirst)
	installNativeBrowserConcurrencyTransport(server, gate, "")
	requestA := nativeBrowserConcurrencyRequest("thread-a", "run-a")
	callA := startNativeBrowserSnapshotCall(server, requestA, context.Background())
	waitForNativeBrowserSnapshot(t, gate, 1)

	requestB := requestA
	requestB.AssistantRunID = "run-b"
	ctx, cancel := context.WithCancel(context.Background())
	callB := startNativeBrowserSnapshotCall(server, requestB, ctx)
	owner := server.nativeBrowserOwner(requestA)
	waitForNativeBrowserOwnerGateRefs(t, server.browserSessions, owner, 2)
	select {
	case arrival := <-gate.arrivals:
		gate.releaseFirst()
		t.Fatalf("same owner reached snapshot concurrently at arrival %d", arrival)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case call := <-callB:
		if !errors.Is(call.err, context.Canceled) {
			t.Fatalf("canceled same-owner wait error = %v, want context.Canceled", call.err)
		}
	case <-time.After(time.Second):
		gate.releaseFirst()
		t.Fatal("same-owner lock wait did not return after cancellation")
	}

	gate.releaseFirst()
	select {
	case call := <-callA:
		if call.err != nil || call.result == "" {
			t.Fatalf("first same-owner call = %q, err %v", call.result, call.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first same-owner browser call did not finish")
	}
	waitForNativeBrowserOwnerGateRefs(t, server.browserSessions, owner, 0)

	requestC := requestA
	requestC.AssistantRunID = "run-c"
	callC := startNativeBrowserSnapshotCall(server, requestC, context.Background())
	waitForNativeBrowserSnapshot(t, gate, 2)
	if call := <-callC; call.err != nil || call.result == "" {
		t.Fatalf("same-owner call after releasing the gate = %q, err %v", call.result, call.err)
	}
}

func TestProjectAssistantBrowserOwnerGateFencesIncarnationAndShutdown(t *testing.T) {
	t.Run("incarnation replacement waits for the owner call", func(t *testing.T) {
		manager := newProjectAssistantBrowserSessionManager()
		defer manager.closeAll()
		owner := browserSessionOwner{
			Identity:   identity{tenant: "tenant", clusterID: "cluster", user: "alice"},
			ProjectUID: "project", AssistantThreadID: "thread-a", AssistantRunID: "run-a",
		}
		oldRef := dataPlaneRef{Resource: "instances", Name: "browser", IsolatedMCPContexts: true, InstanceUID: "old"}
		newRef := oldRef
		newRef.InstanceUID = "new"
		oldEntry, err := manager.entryWithPolicy(owner, oldRef)
		if err != nil {
			t.Fatalf("create old-incarnation entry: %v", err)
		}
		releaseOld, err := manager.lockOwnerContext(context.Background(), owner)
		if err != nil {
			t.Fatalf("lock old-incarnation owner: %v", err)
		}
		transition := make(chan error, 1)
		go func() {
			releaseNew, waitErr := manager.lockOwnerContext(context.Background(), owner)
			if waitErr != nil {
				transition <- waitErr
				return
			}
			defer releaseNew()
			newEntry, entryErr := manager.entryWithPolicy(owner, newRef)
			if entryErr != nil {
				transition <- entryErr
				return
			}
			if newEntry == oldEntry {
				transition <- errors.New("Browser incarnation transition reused the prior entry")
				return
			}
			transition <- nil
		}()
		waitForNativeBrowserOwnerGateRefs(t, manager, owner, 2)
		select {
		case err := <-transition:
			releaseOld()
			t.Fatalf("incarnation transition passed the active owner gate: %v", err)
		case <-time.After(30 * time.Millisecond):
		}
		releaseOld()
		select {
		case err := <-transition:
			if err != nil {
				t.Fatalf("incarnation transition: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("incarnation transition did not proceed after the prior owner call")
		}
	})

	t.Run("shutdown wakes owner waiters", func(t *testing.T) {
		manager := newProjectAssistantBrowserSessionManager()
		owner := browserSessionOwner{
			Identity:   identity{tenant: "tenant", clusterID: "cluster", user: "alice"},
			ProjectUID: "project", AssistantThreadID: "thread-a", AssistantRunID: "run-a",
		}
		releaseHolder, err := manager.lockOwnerContext(context.Background(), owner)
		if err != nil {
			t.Fatalf("lock owner: %v", err)
		}
		waiter := make(chan error, 1)
		go func() {
			release, waitErr := manager.lockOwnerContext(context.Background(), owner)
			if release != nil {
				release()
			}
			waiter <- waitErr
		}()
		waitForNativeBrowserOwnerGateRefs(t, manager, owner, 2)
		manager.closeAll()
		select {
		case waitErr := <-waiter:
			if !errors.Is(waitErr, errProjectAssistantBrowserSessionManagerClosed) {
				t.Fatalf("shutdown waiter error = %v, want manager-closed", waitErr)
			}
		case <-time.After(time.Second):
			releaseHolder()
			t.Fatal("manager shutdown did not wake an owner-gate waiter")
		}
		releaseHolder()
		manager.mu.Lock()
		gateCount := len(manager.ownerGates)
		manager.mu.Unlock()
		if gateCount != 0 {
			t.Fatalf("owner gates retained after shutdown = %d, want zero", gateCount)
		}
	})
}

func TestProjectAssistantNativeBrowserOversizedActionIsUnknownAndNotReplayed(t *testing.T) {
	server := newNativeBrowserTestServer(t)
	counts := installNativeBrowserConcurrencyTransport(server, nil, "browser_click")
	request := nativeBrowserConcurrencyRequest("thread-oversized", "run-oversized")
	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolSnapshot, projectAssistantToolRiskRead); err != nil {
		t.Fatalf("establish managed preview before oversized action: %v", err)
	}

	result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_click", projectAssistantToolRiskRuntime)
	if err != nil {
		t.Fatalf("oversized browser action returned a transport error: %v", err)
	}
	var outcome map[string]any
	if err := json.Unmarshal([]byte(result), &outcome); err != nil {
		t.Fatalf("oversized browser action outcome %q: %v", result, err)
	}
	if outcome["status"] != "outcome_unknown" || outcome["actionDispatchStatus"] != "uncertain" || outcome["sessionStatus"] != "active" || outcome["replayed"] != false {
		t.Fatalf("oversized browser action outcome = %#v, want uncertain, active, unreplayed outcome_unknown", outcome)
	}
	counts.mu.Lock()
	clickCalls := counts.tools["browser_click"]
	counts.mu.Unlock()
	if clickCalls != 1 {
		t.Fatalf("oversized browser_click dispatches = %d, want exactly one", clickCalls)
	}
}

func TestProjectAssistantBrowserCatalogIncludesTemplateUID(t *testing.T) {
	manager := newProjectAssistantBrowserSessionManager()
	defer manager.closeAll()
	id := identity{tenant: "tenant", clusterID: "cluster", user: "alice"}
	ref := dataPlaneRef{
		Resource: "instances", Name: "browser", InstanceUID: "instance",
		TemplateUID: "template-old", TemplateVersion: "0.2.0", TemplateGeneration: 1,
		IsolatedMCPContexts: true,
	}
	catalog := []projectMCPTool{{Name: browserMCPToolSnapshot, InputSchema: json.RawMessage(`{"type":"object"}`)}}
	manager.setBrowserCatalog(id, ref, catalog)
	ref.TemplateUID = "template-new"
	if _, cached := manager.browserCatalog(id, ref); cached {
		t.Fatal("browser catalog survived replacement of its Template UID")
	}
	if got := len(manager.catalogs); got != 0 {
		t.Fatalf("catalogs after Template UID replacement = %d, want zero", got)
	}
}
