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

package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/railgrid/railgrid/pkg/runner"
)

const (
	testCluster  = "tenant-cluster"
	testService  = "runner-service"
	testEdge     = "worker-edge"
	testEdgeKind = EdgeKindMacOSServer
	testRunner   = "runner-1"
)

// proxyBase is the coordinate the edges provider publishes on
// Service.status.url: the kube path of its services/proxy custom subresource.
func proxyBase() string {
	return "/clusters/" + testCluster + "/apis/" + edgesAPIGroup + "/v1alpha1/services/" + testService + "/proxy"
}

func serviceObjectPath() string {
	return "/clusters/" + testCluster + "/apis/" + edgesAPIGroup + "/v1alpha1/services/" + testService
}

func testRef() ServiceRef {
	return ServiceRef{Cluster: testCluster, Service: testService, EdgeKind: testEdgeKind, EdgeName: testEdge, RunnerID: testRunner}
}

type loggedRequest struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   []byte
}

// fixture serves the three things a client talks to: the Service object, the
// runner's capabilities behind the proxy coordinate, and whatever operation the
// test under way installs.
type fixture struct {
	mu       sync.Mutex
	requests []loggedRequest

	service   map[string]any
	caps      runner.Capabilities
	operation func(http.ResponseWriter, *http.Request)
}

func (f *fixture) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, loggedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), Body: body})
		f.mu.Unlock()
		switch r.URL.Path {
		case serviceObjectPath():
			writeJSON(w, http.StatusOK, f.service)
		case proxyBase() + "/runner/v1/capabilities":
			writeJSON(w, http.StatusOK, f.caps)
		default:
			if f.operation != nil && strings.HasPrefix(r.URL.Path, proxyBase()+"/runner/v1/") {
				f.operation(w, r)
				return
			}
			http.NotFound(w, r)
		}
	})
}

func (f *fixture) seen() []loggedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]loggedRequest(nil), f.requests...)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func serviceObject(name, edgeName, edgeKind, publishedURL string) map[string]any {
	return map[string]any{
		"apiVersion": edgesAPIGroup + "/v1alpha1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"edgeRef": map[string]any{"name": edgeName, "kind": edgeKind}},
		"status":     map[string]any{"url": publishedURL},
	}
}

func enrolledCapabilities() runner.Capabilities {
	return runner.Capabilities{
		ProtocolVersion: runner.ProtocolVersion,
		RunnerID:        testRunner,
		OS:              "darwin",
		Architecture:    "arm64",
		Capacity:        runner.Capacity{Maximum: 1},
		Ready:           true,
	}
}

func newFixtureClient(t *testing.T, service map[string]any, caps runner.Capabilities) (*Client, *fixture) {
	t.Helper()
	f := &fixture{service: service, caps: caps}
	server := httptest.NewTLSServer(f.handler())
	t.Cleanup(server.Close)
	client, err := New(tenantConfig(server), testRef())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, f
}

func tenantConfig(server *httptest.Server) *rest.Config {
	return &rest.Config{
		Host:        server.URL + "/clusters/" + testCluster,
		BearerToken: "tenant-bearer",
		TLSClientConfig: rest.TLSClientConfig{
			CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}),
		},
	}
}

func TestNewRequiresTheEnrolledClusterAndAnUnweakenedTransport(t *testing.T) {
	base := &rest.Config{Host: "https://hub.example/clusters/" + testCluster, BearerToken: "tenant-bearer"}
	tests := []struct {
		name   string
		mutate func(*rest.Config, *ServiceRef)
	}{
		{"plaintext scheme", func(c *rest.Config, _ *ServiceRef) { c.Host = "http://hub.example/clusters/" + testCluster }},
		{"another cluster", func(c *rest.Config, _ *ServiceRef) { c.Host = "https://hub.example/clusters/other" }},
		{"deeper path", func(c *rest.Config, _ *ServiceRef) { c.Host += "/apis" }},
		{"query string", func(c *rest.Config, _ *ServiceRef) { c.Host += "?redirect=elsewhere" }},
		{"userinfo", func(c *rest.Config, _ *ServiceRef) { c.Host = "https://user:pw@hub.example/clusters/" + testCluster }},
		{"insecure TLS", func(c *rest.Config, _ *ServiceRef) { c.Insecure = true }},
		{"proxy", func(c *rest.Config, _ *ServiceRef) {
			c.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
		}},
		{"exec provider", func(c *rest.Config, _ *ServiceRef) { c.ExecProvider = &clientcmdapi.ExecConfig{Command: "get-token"} }},
		{"auth provider", func(c *rest.Config, _ *ServiceRef) { c.AuthProvider = &clientcmdapi.AuthProviderConfig{Name: "oidc"} }},
		{"basic auth", func(c *rest.Config, _ *ServiceRef) { c.Username, c.Password = "user", "pw" }},
		{"impersonation", func(c *rest.Config, _ *ServiceRef) { c.Impersonate = rest.ImpersonationConfig{UserName: "someone"} }},
		{"client certificate", func(c *rest.Config, _ *ServiceRef) { c.CertData = []byte("certificate") }},
		{"client key", func(c *rest.Config, _ *ServiceRef) { c.KeyFile = "/tmp/key.pem" }},
		{"no credential", func(c *rest.Config, _ *ServiceRef) { c.BearerToken = "" }},
		{"cluster edge kind", func(_ *rest.Config, ref *ServiceRef) { ref.EdgeKind = "KubernetesCluster" }},
		{"traversal service", func(_ *rest.Config, ref *ServiceRef) { ref.Service = "../secrets" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := rest.CopyConfig(base)
			ref := testRef()
			tt.mutate(cfg, &ref)
			if _, err := New(cfg, ref); err == nil {
				t.Fatalf("New accepted %s", tt.name)
			}
		})
	}
	// The accepted shapes: a pinned bearer, a bearer file, and a wrapper that
	// attaches a hub-minted token per request.
	for _, accepted := range []func(*rest.Config){
		func(c *rest.Config) { c.BearerToken = "tenant-bearer" },
		func(c *rest.Config) { c.BearerToken, c.BearerTokenFile = "", tokenFile(t) },
		func(c *rest.Config) {
			c.BearerToken = ""
			c.WrapTransport = func(rt http.RoundTripper) http.RoundTripper { return rt }
		},
	} {
		cfg := rest.CopyConfig(base)
		accepted(cfg)
		if _, err := New(cfg, testRef()); err != nil {
			t.Fatalf("New refused an accepted credential shape: %v", err)
		}
	}
}

func TestNewDoesNoIO(t *testing.T) {
	f := &fixture{service: serviceObject(testService, testEdge, testEdgeKind, proxyBase()), caps: enrolledCapabilities()}
	server := httptest.NewTLSServer(f.handler())
	defer server.Close()
	if _, err := New(tenantConfig(server), testRef()); err != nil {
		t.Fatalf("New: %v", err)
	}
	if seen := f.seen(); len(seen) != 0 {
		t.Fatalf("New made %d requests: %+v", len(seen), seen)
	}
}

func TestDiscoveryChecksTheServiceTheCoordinateAndTheRunnerIdentity(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	caps, err := client.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.RunnerID != testRunner || !caps.Ready {
		t.Fatalf("capabilities = %+v", caps)
	}
	seen := f.seen()
	// The Service object, the capabilities read that validates the coordinate,
	// and the caller's own capabilities read.
	if len(seen) != 3 {
		t.Fatalf("requests = %+v", seen)
	}
	if seen[0].Path != serviceObjectPath() || seen[1].Path != proxyBase()+"/runner/v1/capabilities" {
		t.Fatalf("discovery walked %q then %q", seen[0].Path, seen[1].Path)
	}
	for _, request := range seen {
		if request.Method != http.MethodGet || request.Auth != "Bearer tenant-bearer" || request.Query != "" {
			t.Fatalf("unexpected discovery request %+v", request)
		}
	}
}

func TestDiscoveryRefusesAServiceThatIsNotThisRunners(t *testing.T) {
	tests := []struct {
		name    string
		service map[string]any
		caps    runner.Capabilities
		want    func(error) bool
	}{
		{
			name:    "another service name",
			service: serviceObject("other-service", testEdge, testEdgeKind, proxyBase()),
			caps:    enrolledCapabilities(),
		},
		{
			name:    "re-pointed at another edge",
			service: serviceObject(testService, "other-edge", testEdgeKind, proxyBase()),
			caps:    enrolledCapabilities(),
		},
		{
			name:    "another edge kind",
			service: serviceObject(testService, testEdge, EdgeKindLinuxServer, proxyBase()),
			caps:    enrolledCapabilities(),
		},
		{
			name:    "status.url is not the rendered coordinate",
			service: serviceObject(testService, testEdge, testEdgeKind, proxyBase()+"/"),
			caps:    enrolledCapabilities(),
		},
		{
			name:    "status.url points at another workspace",
			service: serviceObject(testService, testEdge, testEdgeKind, "/clusters/other/apis/"+edgesAPIGroup+"/v1alpha1/services/"+testService+"/proxy"),
			caps:    enrolledCapabilities(),
		},
		{
			name:    "no status.url at all",
			service: serviceObject(testService, testEdge, testEdgeKind, ""),
			caps:    enrolledCapabilities(),
		},
		{
			name:    "another runner answers",
			service: serviceObject(testService, testEdge, testEdgeKind, proxyBase()),
			caps: runner.Capabilities{
				ProtocolVersion: runner.ProtocolVersion,
				RunnerID:        "someone-elses-runner",
			},
			want: func(err error) bool {
				var identity *IdentityError
				return errors.As(err, &identity) && identity.Reported == "someone-elses-runner" && identity.Enrolled == testRunner
			},
		},
		{
			name:    "another protocol answers",
			service: serviceObject(testService, testEdge, testEdgeKind, proxyBase()),
			caps: runner.Capabilities{
				ProtocolVersion: "runner/v2",
				RunnerID:        testRunner,
			},
			want: func(err error) bool {
				var identity *IdentityError
				return errors.As(err, &identity) && identity.Protocol == "runner/v2" && strings.Contains(err.Error(), "speaks protocol")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newFixtureClient(t, tt.service, tt.caps)
			_, err := client.Capabilities(context.Background())
			if err == nil {
				t.Fatal("discovery accepted a Service that is not this runner's")
			}
			if tt.want != nil && !tt.want(err) {
				t.Fatalf("error = %v (%T)", err, err)
			}
		})
	}
}

func TestRedirectsAreRefusedBeforeTheBearerIsResent(t *testing.T) {
	var followed int
	var target string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == serviceObjectPath() {
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		followed++
	}))
	defer server.Close()
	target = server.URL + "/elsewhere"
	client, err := New(tenantConfig(server), testRef())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Capabilities(context.Background()); err == nil || !strings.Contains(err.Error(), "redirects are forbidden") {
		t.Fatalf("redirect error = %v", err)
	}
	if followed != 0 {
		t.Fatalf("the redirect target was reached %d times", followed)
	}
}

func TestStartRoundTripsTypedRequestAndReceipt(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	accepted := runner.Receipt{
		ProtocolVersion: runner.ProtocolVersion,
		TaskID:          "task-1",
		AttemptID:       "attempt-1",
		AttemptEpoch:    7,
		Phase:           runner.PhaseAccepted,
		Cursor:          1,
		AcceptedAt:      time.Now().UTC().Truncate(time.Second),
	}
	f.operation = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != proxyBase()+"/runner/v1/attempts" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusAccepted, accepted)
	}
	// ProtocolVersion is left unset on purpose: the client stamps the one
	// version it implements, and the runner rejects a request without it.
	receipt, err := client.Start(context.Background(), runner.StartRequest{
		RequestID:    "request-1",
		TaskID:       "task-1",
		AttemptID:    "attempt-1",
		AttemptEpoch: 7,
		WorkspaceID:  "workspace-1",
		Instructions: "approved",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if receipt.AttemptID != "attempt-1" || receipt.Phase != runner.PhaseAccepted || receipt.AttemptEpoch != 7 {
		t.Fatalf("receipt = %+v", receipt)
	}
	// The wire body must be the protocol shape: the runner decodes it with
	// DisallowUnknownFields, so a stray field is a 400.
	seen := f.seen()
	if len(seen) != 3 {
		// Discovery plus exactly one POST: a rejected mutation is never retried.
		t.Fatalf("requests = %+v", seen)
	}
	var sent runner.StartRequest
	decoder := json.NewDecoder(strings.NewReader(string(seen[2].Body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&sent); err != nil {
		t.Fatalf("the runner would refuse this body: %v (%s)", err, seen[2].Body)
	}
	if sent.ProtocolVersion != runner.ProtocolVersion {
		t.Fatalf("sent protocol version = %q", sent.ProtocolVersion)
	}
	if seen[2].Path != proxyBase()+"/runner/v1/attempts" {
		t.Fatalf("start reached %q", seen[2].Path)
	}
}

// tokenFile is a bearer-token file that exists, which is what rest.Config
// requires of one.
func tokenFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("tenant-bearer"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return path
}

func TestStartRefusesAnIncompleteIdentityBeforeDispatch(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	for name, req := range map[string]runner.StartRequest{
		"no request ID": {TaskID: "task-1", AttemptID: "attempt-1", AttemptEpoch: 1},
		"no task ID":    {RequestID: "request-1", AttemptID: "attempt-1", AttemptEpoch: 1},
		"no attempt ID": {RequestID: "request-1", TaskID: "task-1", AttemptEpoch: 1},
		"zero epoch":    {RequestID: "request-1", TaskID: "task-1", AttemptID: "attempt-1"},
		"other version": {ProtocolVersion: "runner/v2", RequestID: "request-1", TaskID: "task-1", AttemptID: "attempt-1", AttemptEpoch: 1},
	} {
		if _, err := client.Start(context.Background(), req); err == nil {
			t.Fatalf("Start accepted %s", name)
		}
	}
	if seen := f.seen(); len(seen) != 0 {
		t.Fatalf("a refused mutation still reached the runner: %+v", seen)
	}
}

func TestProtocolErrorBodyBecomesATypedRunnerError(t *testing.T) {
	client, fix := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	stale := &runner.Error{
		Code:             runner.ErrorStaleAttempt,
		Retryable:        false,
		Message:          "attempt epoch 6 is behind 7",
		SnapshotRequired: true,
		Receipt:          &runner.Receipt{AttemptID: "attempt-1", AttemptEpoch: 7, Phase: runner.PhaseRunning},
	}
	fix.operation = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusConflict, stale)
	}
	_, err := client.Start(context.Background(), runner.StartRequest{RequestID: "request-1", TaskID: "task-1", AttemptID: "attempt-1", AttemptEpoch: 6})
	var protocolErr *runner.Error
	if !errors.As(err, &protocolErr) {
		t.Fatalf("error = %v (%T), want *runner.Error", err, err)
	}
	// A caller branches on Code, never on a message.
	if protocolErr.Code != runner.ErrorStaleAttempt || !protocolErr.SnapshotRequired {
		t.Fatalf("protocol error = %+v", protocolErr)
	}
	if protocolErr.Receipt == nil || protocolErr.Receipt.AttemptEpoch != 7 {
		t.Fatalf("protocol error lost the receipt: %+v", protocolErr)
	}

	// A body that is not the protocol shape stays an HTTPError: it did not come
	// from the runner, so it carries no code to branch on.
	client, fix = newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	fix.operation = func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "<html>gateway timeout</html>", http.StatusBadGateway)
	}
	_, err = client.Inspect(context.Background(), "attempt-1")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("error = %v (%T), want *HTTPError", err, err)
	}
	if !strings.Contains(string(httpErr.Body), "gateway timeout") {
		t.Fatalf("HTTPError lost the body: %q", httpErr.Body)
	}
}

func TestADeniedOrMissingServiceInvalidatesTheCachedCoordinate(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	f.operation = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusForbidden, &runner.Error{Code: runner.ErrorForbidden, Message: "the named Service grant was withdrawn"})
	}
	if _, err := client.Inspect(context.Background(), "attempt-1"); err == nil {
		t.Fatal("Inspect accepted a 403")
	}
	// The second Inspect must re-read the Service and re-check the identity
	// rather than reuse a coordinate a 403 already called into question.
	if _, err := client.Inspect(context.Background(), "attempt-1"); err == nil {
		t.Fatal("Inspect accepted a 403")
	}
	var discoveries int
	for _, request := range f.seen() {
		if request.Path == serviceObjectPath() {
			discoveries++
		}
	}
	if discoveries != 2 {
		t.Fatalf("the Service was read %d times, want one per failed operation", discoveries)
	}
}

func TestTheCoordinateIsReusedWithinItsTTL(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	now := time.Now()
	client.nowFn = func() time.Time { return now }
	f.operation = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, runner.Receipt{AttemptID: "attempt-1", Phase: runner.PhaseRunning})
	}
	for range 3 {
		if _, err := client.Inspect(context.Background(), "attempt-1"); err != nil {
			t.Fatalf("Inspect: %v", err)
		}
	}
	if got := countPath(f.seen(), serviceObjectPath()); got != 1 {
		t.Fatalf("the Service was read %d times inside the TTL, want once", got)
	}
	now = now.Add(discoveryTTL + time.Second)
	if _, err := client.Inspect(context.Background(), "attempt-1"); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := countPath(f.seen(), serviceObjectPath()); got != 2 {
		t.Fatalf("the Service was read %d times across the TTL, want twice", got)
	}
	client.Invalidate()
	if _, err := client.Inspect(context.Background(), "attempt-1"); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := countPath(f.seen(), serviceObjectPath()); got != 3 {
		t.Fatalf("Invalidate did not force a re-check: %d Service reads", got)
	}
}

func countPath(requests []loggedRequest, path string) int {
	var n int
	for _, request := range requests {
		if request.Path == path {
			n++
		}
	}
	return n
}

func TestEventsStreamInOrderAndEndAtEOF(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	released := make(chan struct{})
	f.operation = func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/events") || r.URL.Query().Get("after") != "4" {
			http.Error(w, "unexpected events request "+r.URL.String(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		// Replayed events, then a keep-alive comment, then a live one: the
		// framing is pkg/runner/http.go writeSSE.
		writeSSEFrame(w, runner.Event{Cursor: 5, Type: runner.EventProgress, Message: "building"})
		writeSSEFrame(w, runner.Event{Cursor: 6, Type: runner.EventCheckpoint, Message: "checkpoint", Data: json.RawMessage(`{"commit":"abc"}`)})
		_, _ = io.WriteString(w, ": keep-alive\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		<-released
		writeSSEFrame(w, runner.Event{Cursor: 7, Type: runner.EventCompleted, Message: "done"})
	}
	stream, err := client.Events(context.Background(), "attempt-1", 4)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer func() { _ = stream.Close() }()

	for _, want := range []struct {
		cursor uint64
		kind   string
	}{{5, runner.EventProgress}, {6, runner.EventCheckpoint}} {
		event, err := stream.Next(context.Background())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if event.Cursor != want.cursor || event.Type != want.kind {
			t.Fatalf("event = %+v, want cursor %d type %s", event, want.cursor, want.kind)
		}
		if stream.Cursor() != want.cursor {
			t.Fatalf("stream cursor = %d, want %d", stream.Cursor(), want.cursor)
		}
	}
	close(released)
	event, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Next after the live event: %v", err)
	}
	if event.Cursor != 7 || event.Type != runner.EventCompleted {
		t.Fatalf("live event = %+v", event)
	}
	if _, err := stream.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("end of stream = %v, want io.EOF", err)
	}
	// Once ended, the stream stays ended.
	if _, err := stream.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after EOF = %v", err)
	}
}

func TestAMidStreamCursorGapRequiresReconciliation(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	f.operation = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSEFrame(w, runner.Event{Cursor: 1, Type: runner.EventAccepted})
		// Cursor 2 was dropped: the runner publishes to a subscriber with a
		// non-blocking send, so a consumer that fell behind loses events.
		writeSSEFrame(w, runner.Event{Cursor: 3, Type: runner.EventProgress})
	}
	stream, err := client.Events(context.Background(), "attempt-1", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer func() { _ = stream.Close() }()
	if event, err := stream.Next(context.Background()); err != nil || event.Cursor != 1 {
		t.Fatalf("first event = %+v, %v", event, err)
	}
	_, err = stream.Next(context.Background())
	var protocolErr *runner.Error
	if !errors.As(err, &protocolErr) {
		t.Fatalf("gap error = %v (%T), want *runner.Error", err, err)
	}
	if protocolErr.Code != runner.ErrorCursorExpired || !protocolErr.SnapshotRequired || !protocolErr.Retryable {
		t.Fatalf("gap error = %+v", protocolErr)
	}
}

func TestAnExpiredCursorIsRefusedBeforeTheStreamOpens(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	f.operation = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusGone, &runner.Error{
			Code:             runner.ErrorCursorExpired,
			Retryable:        true,
			SnapshotRequired: true,
			Message:          "event cursor expired; inspect the attempt before replaying",
			Receipt:          &runner.Receipt{AttemptID: "attempt-1", Cursor: 90},
		})
	}
	_, err := client.Events(context.Background(), "attempt-1", 1)
	var protocolErr *runner.Error
	if !errors.As(err, &protocolErr) || protocolErr.Code != runner.ErrorCursorExpired {
		t.Fatalf("error = %v (%T)", err, err)
	}
	if protocolErr.Receipt == nil || protocolErr.Receipt.Cursor != 90 {
		t.Fatalf("the caller cannot reconcile without the receipt: %+v", protocolErr)
	}
}

func TestEventsRefuseAnInvalidAttemptIDWithoutDispatch(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	for _, attemptID := range []string{"", "../secrets", "attempt/1", "attempt 1"} {
		if _, err := client.Events(context.Background(), attemptID, 0); err == nil {
			t.Fatalf("Events accepted attempt ID %q", attemptID)
		}
		if _, err := client.Inspect(context.Background(), attemptID); err == nil {
			t.Fatalf("Inspect accepted attempt ID %q", attemptID)
		}
	}
	if seen := f.seen(); len(seen) != 0 {
		t.Fatalf("an invalid attempt ID still reached the runner: %+v", seen)
	}
}

func TestArtifactVerifiesTheAnnouncedDigest(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	const contents = "the artifact bytes"
	digest := sha256.Sum256([]byte(contents))
	announced := "sha256:" + hex.EncodeToString(digest[:])
	serve := func(value string) {
		f.operation = func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/artifacts/artifact-1") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Digest", value)
			_, _ = io.WriteString(w, contents)
		}
	}
	serve(announced)
	var sink strings.Builder
	artifact, err := client.Artifact(context.Background(), "attempt-1", "artifact-1", &sink)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if sink.String() != contents || artifact.Digest != announced || artifact.Length != int64(len(contents)) || artifact.MediaType != "text/plain" {
		t.Fatalf("artifact = %+v, body %q", artifact, sink.String())
	}
	serve("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	if _, err := client.Artifact(context.Background(), "attempt-1", "artifact-1", io.Discard); err == nil {
		t.Fatal("Artifact accepted a body that does not match the announced digest")
	}
}

func TestOversizedJSONIsRefused(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	f.operation = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, io.LimitReader(filler{}, jsonLimit+1))
	}
	if _, err := client.Inspect(context.Background(), "attempt-1"); err == nil || !strings.Contains(err.Error(), "2MiB") {
		t.Fatalf("oversized receipt error = %v", err)
	}
}

type filler struct{}

func (filler) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestAnOversizedEventStreamIsRefused(t *testing.T) {
	stream := newEventStream(io.NopCloser(io.LimitReader(filler{}, eventStreamLimit+64)), 0)
	defer func() { _ = stream.Close() }()
	if _, err := stream.Next(context.Background()); err == nil || !strings.Contains(err.Error(), "64MiB") {
		t.Fatalf("oversized stream error = %v", err)
	}
}

func TestCancellingTheContextEndsTheStream(t *testing.T) {
	client, f := newFixtureClient(t, serviceObject(testService, testEdge, testEdgeKind, proxyBase()), enrolledCapabilities())
	f.operation = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}
	stream, err := client.Events(context.Background(), "attempt-1", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer func() { _ = stream.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := stream.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next after cancel = %v, want context.Canceled", err)
	}
}

// writeSSEFrame reproduces the runner's framing exactly (pkg/runner/http.go
// writeSSE), so the parser is tested against the wire it will actually meet.
func writeSSEFrame(w http.ResponseWriter, event runner.Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Cursor, event.Type, encoded)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// An enrollment without a RunnerID pins the runner by the Service alone: the
// caller trusts what the edge published under that name, so whichever runner
// answers there is the one. The protocol check is not relaxed with it.
func TestAnEnrollmentWithoutARunnerIDAcceptsTheServicesRunner(t *testing.T) {
	ref := testRef()
	ref.RunnerID = ""
	dial := func(caps runner.Capabilities) *Client {
		t.Helper()
		f := &fixture{service: serviceObject(testService, testEdge, testEdgeKind, proxyBase()), caps: caps}
		server := httptest.NewTLSServer(f.handler())
		t.Cleanup(server.Close)
		client, err := New(tenantConfig(server), ref)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return client
	}
	caps, err := dial(runner.Capabilities{ProtocolVersion: runner.ProtocolVersion, RunnerID: "whatever-the-edge-published", Ready: true}).Capabilities(context.Background())
	if err != nil || caps.RunnerID != "whatever-the-edge-published" {
		t.Fatalf("caps = %+v, err = %v; the Service's runner must be accepted as is", caps, err)
	}
	_, err = dial(runner.Capabilities{ProtocolVersion: "runner/v2", RunnerID: "whatever-the-edge-published"}).Capabilities(context.Background())
	var identity *IdentityError
	if !errors.As(err, &identity) || identity.Protocol != "runner/v2" {
		t.Fatalf("err = %v, want the protocol mismatch still refused", err)
	}
}
