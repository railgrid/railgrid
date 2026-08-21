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
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDisabledTrackerMakesNoNetworkCalls(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	tracker, err := NewClient(Config{
		Enabled:      false,
		ProviderName: "not a provider",
		HubURL:       "://invalid",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("disabled tracker used its HTTP client")
			return nil, nil
		})},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := tracker.Track(context.Background(), Event{Action: "bad Action"}); err != nil {
		t.Fatalf("disabled Track() error = %v", err)
	}
	if err := tracker.Close(); err != nil {
		t.Fatalf("disabled Close() error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("disabled tracker calls = %d, want 0", calls.Load())
	}
}

func TestHTTPClientPostsBoundedEventToProviderEndpoint(t *testing.T) {
	request := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		request <- capturedRequest{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), provider: r.Header.Get("X-Faros-Provider"), body: body}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		Enabled:       true,
		ProviderName:  "app-studio",
		HubURL:        server.URL + "/hub/",
		ProviderToken: "provider-secret",
		QueueSize:     2,
		SendTimeout:   time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	occurredAt := time.Date(2026, 8, 21, 15, 0, 0, 123000000, time.FixedZone("UTC+2", 2*60*60))
	if err := client.Track(context.Background(), Event{
		Action:        "project_created",
		OccurredAt:    occurredAt,
		OrgID:         "org-1",
		WorkspaceID:   "workspace-1",
		ProjectID:     "project-1",
		ResourceID:    "resource-1",
		Actor:         "actor-1",
		CorrelationID: "request-1",
		Properties:    map[string]any{"outcome": "success", "count": int64(1)},
	}); err != nil {
		t.Fatalf("Track() error = %v", err)
	}

	select {
	case got := <-request:
		if got.method != http.MethodPost {
			t.Errorf("method = %q, want POST", got.method)
		}
		if got.path != "/hub/api/providers/app-studio/telemetry" {
			t.Errorf("path = %q, want provider endpoint", got.path)
		}
		if got.auth != "Bearer provider-secret" {
			t.Errorf("authorization = %q", got.auth)
		}
		if got.provider != "app-studio" {
			t.Errorf("provider header = %q", got.provider)
		}
		if strings.Contains(string(got.body), "provider-secret") {
			t.Fatal("provider token leaked into request body")
		}
		var event Event
		if err := json.Unmarshal(got.body, &event); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if event.Action != "project_created" || event.OrgID != "org-1" || event.WorkspaceID != "workspace-1" || event.ProjectID != "project-1" || event.ResourceID != "resource-1" || event.CorrelationID != "request-1" {
			t.Fatalf("decoded event identity = %#v", event)
		}
		if event.OccurredAt.IsZero() || event.OccurredAt.Location() != time.UTC {
			t.Fatalf("occurred_at = %v, want normalized UTC timestamp", event.OccurredAt)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP client did not send queued event")
	}
}

func TestTrackDoesNotWaitForSlowNetworkBeyondEnqueueTimeout(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-release
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		Enabled:        true,
		ProviderName:   "code",
		HubURL:         server.URL,
		ProviderToken:  "token",
		QueueSize:      1,
		EnqueueTimeout: 20 * time.Millisecond,
		SendTimeout:    100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer func() {
		close(release)
		_ = client.Close()
	}()

	start := time.Now()
	if err := client.Track(context.Background(), Event{Action: "code_event"}); err != nil {
		t.Fatalf("first Track() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sender did not start")
	}
	if err := client.Track(context.Background(), Event{Action: "code_event"}); err != nil {
		t.Fatalf("second Track() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Track blocked for %v, want bounded enqueue behavior", elapsed)
	}
}

func TestCloseCancelsBlockedSendAndRejectsLaterEvents(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	var calls atomic.Int32

	tracker, err := NewClient(Config{
		Enabled:       true,
		ProviderName:  "code",
		HubURL:        "http://hub.example",
		ProviderToken: "token",
		CloseTimeout:  30 * time.Millisecond,
		SendTimeout:   10 * time.Second,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			close(started)
			<-r.Context().Done()
			close(canceled)
			return nil, r.Context().Err()
		})},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := tracker.Track(context.Background(), Event{Action: "code_event"}); err != nil {
		t.Fatalf("Track() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sender did not start")
	}

	startedClose := time.Now()
	if err := tracker.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if elapsed := time.Since(startedClose); elapsed > 500*time.Millisecond {
		t.Fatalf("Close blocked for %v, want bounded shutdown", elapsed)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("blocked request was not canceled")
	}
	if err := tracker.Track(context.Background(), Event{Action: "code_event"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Track after Close() error = %v, want ErrClosed", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("send count after close = %d, want 1", got)
	}
}

func TestTrackRejectsInvalidAndOversizedEventsBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	client, err := NewClient(Config{
		Enabled:       true,
		ProviderName:  "code",
		HubURL:        "http://hub.example",
		ProviderToken: "token",
		MaxEventBytes: 256,
		MaxProperties: 2,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("unexpected network call")
		})},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	for _, event := range []Event{
		{Action: "Invalid Action"},
		{Action: "valid_action", Properties: map[string]any{"token": "secret"}},
		{Action: "valid_action", Properties: map[string]any{"one": "1", "two": "2", "three": "3"}},
		{Action: "valid_action", Properties: map[string]any{"large": strings.Repeat("x", DefaultMaxPropertyBytes+1)}},
	} {
		if err := client.Track(context.Background(), event); err == nil {
			t.Errorf("Track(%#v) accepted invalid event", event)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid events made %d network calls", calls.Load())
	}
}

func TestNewClientRequiresEnabledConfigurationAndNoopIsSafe(t *testing.T) {
	if _, err := NewClient(Config{Enabled: true, ProviderName: "code", HubURL: "https://hub.example"}); err == nil {
		t.Fatal("enabled client accepted missing provider token")
	}
	tracker, err := NewClient(Config{Enabled: false})
	if err != nil {
		t.Fatalf("disabled NewClient() error = %v", err)
	}
	if err := tracker.Track(nil, Event{}); err != nil {
		t.Fatalf("noop Track() error = %v", err)
	}
}

type capturedRequest struct {
	method   string
	path     string
	auth     string
	provider string
	body     []byte
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
