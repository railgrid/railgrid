/*
Copyright 2026 The Faros Authors.

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

package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func enabledConfig() Config {
	return Config{Mode: ModeSaaS, Endpoint: "https://receiver.example/v1/events", SinkToken: "0123456789abcdef", HMACSecret: "0123456789abcdef0123456789abcdef", InstallationID: "install-1", QueueSize: 8, BatchSize: 2, FlushInterval: 30 * time.Second, EnqueueTimeout: 5 * time.Millisecond, SendTimeout: time.Second, ShutdownTimeout: time.Second, MaxRequestBytes: 4096, MaxRetries: 2, InitialBackoff: time.Millisecond}
}

func agentEvent() Event {
	return Event{Action: "agents_agent_created", OrgID: "raw-org", WorkspaceID: "raw-workspace", Actor: "raw-actor", ResourceID: "raw-resource", Properties: map[string]any{"outcome": "success"}}
}

type recordingSink struct {
	mu       sync.Mutex
	calls    int
	records  []Record
	failures int
	entered  chan struct{}
	release  chan struct{}
}

func (s *recordingSink) Send(ctx context.Context, batch []Record) error {
	s.mu.Lock()
	s.calls++
	if s.failures > 0 {
		s.failures--
		s.mu.Unlock()
		return errors.New("retry")
	}
	s.records = append(s.records, batch...)
	entered, release := s.entered, s.release
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestDefaultOffCreatesNoNetworkActivity(t *testing.T) {
	var calls atomic.Int32
	r, err := NewRuntime(Config{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, errors.New("unexpected") })}}, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if r.Enabled() {
		t.Fatal("default runtime enabled")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("network calls=%d", calls.Load())
	}
}

func TestSaaSConfigRequiresAllSecretsAndBounds(t *testing.T) {
	for _, mutate := range []func(*Config){func(c *Config) { c.Endpoint = "" }, func(c *Config) { c.Endpoint = "http://receiver.example/v1/events" }, func(c *Config) { c.Endpoint += "?token=bad" }, func(c *Config) { c.SinkToken = "short" }, func(c *Config) { c.SinkToken = "0123456789abcde " }, func(c *Config) { c.HMACSecret = "short" }, func(c *Config) { c.HMACSecret = "0123456789abcdef0123456789abcde\n" }, func(c *Config) { c.InstallationID = "" }, func(c *Config) { c.InstallationID = "not/a/source" }, func(c *Config) { c.BatchSize = 9; c.QueueSize = 8 }} {
		cfg := enabledConfig()
		mutate(&cfg)
		if _, err := NewRuntime(cfg, prometheus.NewRegistry()); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("error=%v, want invalid config", err)
		}
	}
}

func TestCatalogValidationAndPrivacyNormalization(t *testing.T) {
	sink := &recordingSink{}
	cfg := enabledConfig()
	cfg.BatchSize = 1
	r, err := NewRuntimeWithSink(cfg, sink, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	bad := agentEvent()
	bad.Properties["outcome"] = "invented"
	if !errors.Is(r.Track(context.Background(), "agents", bad), ErrInvalidEvent) {
		t.Fatal("invalid enum accepted")
	}
	if !errors.Is(r.Track(context.Background(), "edges", agentEvent()), ErrInvalidEvent) {
		t.Fatal("cross-provider action accepted")
	}
	bad = agentEvent()
	bad.ProjectID = "undeclared-project"
	if !errors.Is(r.Track(context.Background(), "agents", bad), ErrInvalidEvent) {
		t.Fatal("undeclared identifier accepted")
	}
	if err := r.Track(context.Background(), "agents", agentEvent()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.records)
		var got Record
		if n > 0 {
			got = sink.records[0]
		}
		sink.mu.Unlock()
		if n > 0 {
			payload, _ := json.Marshal(got)
			for _, raw := range []string{"raw-org", "raw-workspace", "raw-actor", "raw-resource"} {
				if strings.Contains(string(payload), raw) {
					t.Fatalf("raw identifier %q escaped in %s", raw, payload)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("record not delivered")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPseudonymsAreSeparatedByInstallationAndIdentifierKind(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	first := normalizer{key: key, installationID: "install-1"}
	second := normalizer{key: key, installationID: "install-2"}

	org := first.hash("org", "shared-identifier")
	if org == second.hash("org", "shared-identifier") {
		t.Fatal("pseudonym is linkable across installations that reuse a key")
	}
	if org == first.hash("workspace", "shared-identifier") {
		t.Fatal("pseudonym is linkable across identifier kinds")
	}
	if org != first.hash("org", "shared-identifier") {
		t.Fatal("pseudonym is not stable within one installation and identifier kind")
	}
}

func TestPlatformCatalogEventsUseInternalBoundary(t *testing.T) {
	sink := &recordingSink{}
	cfg := enabledConfig()
	cfg.BatchSize = 1
	r, err := NewRuntimeWithSink(cfg, sink, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	event := Event{Action: "organization_created", OrgID: "org", Actor: "actor", Properties: map[string]any{"outcome": "success"}}
	if err := r.TrackPlatform(context.Background(), event); err != nil {
		t.Fatal(err)
	}
}

func TestRetryReusesCloudEventIDAndNeverSerializesRawIdentifiers(t *testing.T) {
	var mu sync.Mutex
	var payloads [][]byte
	var calls int
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		mu.Lock()
		payloads = append(payloads, body)
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			return nil, errors.New("lost response")
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})
	cfg := enabledConfig()
	cfg.BatchSize = 1
	cfg.HTTPClient = &http.Client{Transport: transport}
	r, err := NewRuntime(cfg, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Track(context.Background(), "agents", agentEvent()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		n := len(payloads)
		mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry not observed")
		}
		time.Sleep(time.Millisecond)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var first, second []map[string]any
	if err := json.Unmarshal(payloads[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payloads[1], &second); err != nil {
		t.Fatal(err)
	}
	if first[0]["id"] == "" || first[0]["id"] != second[0]["id"] {
		t.Fatalf("CloudEvent IDs changed across retry: %v / %v", first[0]["id"], second[0]["id"])
	}
	for _, payload := range payloads {
		for _, raw := range []string{"raw-org", "raw-workspace", "raw-actor", "raw-resource"} {
			if strings.Contains(string(payload), raw) {
				t.Fatalf("raw identifier %q escaped in CloudEvent: %s", raw, payload)
			}
		}
	}
}

func TestHTTPSinkDoesNotForwardCredentialsAcrossRedirect(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.Header.Get("Authorization") != "Bearer 0123456789abcdef" {
			t.Fatal("initial request omitted receiver credential")
		}
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": []string{"https://redirect.example/v1/events"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})}
	sink := NewHTTPSink("https://receiver.example/v1/events", "0123456789abcdef", client)
	err := sink.Send(context.Background(), []Record{{
		EventID: "event-1", InstallationID: "install-1", Provider: "platform",
		Action: "organization_created", OccurredAt: time.Now().UTC(),
		Identifiers: Identifiers{Org: strings.Repeat("a", 64), Actor: strings.Repeat("b", 64)},
		Properties:  map[string]any{"outcome": "success"},
	}})
	if err == nil {
		t.Fatal("redirect response was accepted")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HTTP client made %d requests, want only the original request", got)
	}
}

func TestBatchRetryAndQueueBackpressure(t *testing.T) {
	sink := &recordingSink{failures: 1, entered: make(chan struct{}, 1), release: make(chan struct{})}
	cfg := enabledConfig()
	cfg.QueueSize = 2
	cfg.BatchSize = 1
	r, err := NewRuntimeWithSink(cfg, sink, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Track(context.Background(), "agents", agentEvent()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("sink not entered")
	}
	if err := r.Track(context.Background(), "agents", agentEvent()); err != nil {
		t.Fatal(err)
	}
	if err := r.Track(context.Background(), "agents", agentEvent()); err != nil {
		t.Fatal(err)
	}
	if err := r.Track(context.Background(), "agents", agentEvent()); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("error=%v, want queue full", err)
	}
	close(sink.release)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.calls < 2 {
		t.Fatalf("sink calls=%d, want retry", sink.calls)
	}
	if len(sink.records) != 3 {
		t.Fatalf("records=%d, want 3", len(sink.records))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
