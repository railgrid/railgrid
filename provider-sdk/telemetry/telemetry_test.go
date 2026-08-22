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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		AllowInsecure: true,
		QueueSize:     2,
		SendTimeout:   time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer func() { _ = client.Close() }()

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

func TestHTTPClientAppendsPrivateCAFromDataAndFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*Config, string)
	}{
		{name: "data", set: func(cfg *Config, pemData string) { cfg.CAData = []byte(pemData) }},
		{name: "file", set: func(cfg *Config, pemData string) {
			path := filepath.Join(t.TempDir(), "hub-ca.pem")
			if err := os.WriteFile(path, []byte(pemData), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg.CAFile = path
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan struct{}, 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests <- struct{}{}
				w.WriteHeader(http.StatusAccepted)
			}))
			defer server.Close()

			caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			cfg := Config{
				Enabled:       true,
				ProviderName:  "app-studio",
				HubURL:        server.URL,
				ProviderToken: "provider-token",
				MaxRetries:    1,
			}
			tc.set(&cfg, string(caPEM))
			tracker, err := NewClient(cfg)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			if err := tracker.Track(context.Background(), Event{Action: "project_created"}); err != nil {
				t.Fatalf("Track() error = %v", err)
			}
			if err := tracker.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			select {
			case <-requests:
			default:
				t.Fatal("private-CA telemetry request did not reach hub")
			}
		})
	}
}

func TestHTTPClientPreservesExistingRootsWhenAppendingPrivateCA(t *testing.T) {
	existingRootServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer existingRootServer.Close()
	privateCAServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer privateCAServer.Close()

	existingRoots := x509.NewCertPool()
	existingRootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: existingRootServer.Certificate().Raw})
	if !existingRoots.AppendCertsFromPEM(existingRootPEM) {
		t.Fatal("append existing test root")
	}
	previousDefaultTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: existingRoots}}
	t.Cleanup(func() { http.DefaultTransport = previousDefaultTransport })

	privateCAPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: privateCAServer.Certificate().Raw})
	client, err := NewHTTPClient(HTTPClientConfig{CAData: privateCAPEM})
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}
	for _, serverURL := range []string{existingRootServer.URL, privateCAServer.URL} {
		response, err := client.Get(serverURL)
		if err != nil {
			t.Fatalf("GET %s: %v", serverURL, err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatalf("close response body: %v", err)
		}
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("GET %s status = %d, want %d", serverURL, response.StatusCode, http.StatusAccepted)
		}
	}
}

func TestHTTPClientRejectsUntrustedAndInvalidConfiguredCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	caPEM := testSelfSignedCertificate(t)
	tracker, err := NewClient(Config{
		Enabled:        true,
		ProviderName:   "app-studio",
		HubURL:         server.URL,
		ProviderToken:  "provider-token",
		CAData:         caPEM,
		MaxRetries:     1,
		InitialBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("untrusted CA NewClient() error = %v", err)
	}
	if err := tracker.Track(context.Background(), Event{Action: "project_created"}); err != nil {
		t.Fatalf("Track() error = %v", err)
	}
	if err := tracker.Close(); !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("untrusted CA Close() error = %v, want ErrDeliveryFailed", err)
	}

	if _, err := NewClient(Config{
		Enabled:       true,
		ProviderName:  "app-studio",
		HubURL:        server.URL,
		ProviderToken: "provider-token",
		CAData:        []byte("not a PEM certificate"),
	}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid CA NewClient() error = %v, want ErrInvalidConfig", err)
	}

	if _, err := NewClient(Config{
		Enabled:       true,
		ProviderName:  "app-studio",
		HubURL:        server.URL,
		ProviderToken: "provider-token",
		CAFile:        filepath.Join(t.TempDir(), "missing-ca.pem"),
	}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unreadable CA file NewClient() error = %v, want ErrInvalidConfig", err)
	}

	validCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	invalidCAPath := filepath.Join(t.TempDir(), "invalid-ca.pem")
	if err := os.WriteFile(invalidCAPath, []byte("not a PEM certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(Config{
		Enabled:       true,
		ProviderName:  "app-studio",
		HubURL:        server.URL,
		ProviderToken: "provider-token",
		CAData:        validCA,
		CAFile:        invalidCAPath,
	}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("mixed valid and invalid CA sources NewClient() error = %v, want ErrInvalidConfig", err)
	}
}

func testSelfSignedCertificate(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "untrusted-test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create test CA certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
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
		AllowInsecure:  true,
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
		AllowInsecure: true,
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
	if err := tracker.Close(); !errors.Is(err, ErrCloseTimeout) {
		t.Fatalf("Close() error = %v, want ErrCloseTimeout", err)
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
		AllowInsecure: true,
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
	defer func() { _ = client.Close() }()

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
	if err := tracker.Track(context.Background(), Event{}); err != nil {
		t.Fatalf("noop Track() error = %v", err)
	}
}

func TestEnabledClientRequiresSecureTransportByDefault(t *testing.T) {
	if _, err := NewClient(Config{Enabled: true, ProviderName: "code", HubURL: "http://hub.example", ProviderToken: "token"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewClient() error = %v, want insecure transport rejection", err)
	}
	tracker, err := NewClient(Config{Enabled: true, ProviderName: "code", HubURL: "http://hub.example", ProviderToken: "token", AllowInsecure: true})
	if err != nil {
		t.Fatalf("explicit development transport rejected: %v", err)
	}
	if err := tracker.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestClientDoesNotForwardBearerOrPayloadAcrossRedirect(t *testing.T) {
	var redirectedCalls atomic.Int32
	redirected := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedCalls.Add(1)
	}))
	defer redirected.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", redirected.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	tracker, err := NewClient(Config{Enabled: true, ProviderName: "code", HubURL: source.URL, ProviderToken: "token", AllowInsecure: true, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := tracker.Track(context.Background(), Event{Action: "code_event"}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Close(); !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("Close() error = %v, want delivery failure", err)
	}
	if got := redirectedCalls.Load(); got != 0 {
		t.Fatalf("redirect target received %d telemetry requests", got)
	}
}

func TestClientRetriesTransientStatusesAndReportsPermanentFailure(t *testing.T) {
	var transientCalls atomic.Int32
	transient, err := NewClient(Config{
		Enabled: true, ProviderName: "code", HubURL: "https://hub.example", ProviderToken: "token", InitialBackoff: time.Millisecond,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			call := transientCalls.Add(1)
			status := http.StatusServiceUnavailable
			if call == 3 {
				status = http.StatusAccepted
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transient.Track(context.Background(), Event{Action: "code_event"}); err != nil {
		t.Fatal(err)
	}
	if err := transient.Close(); err != nil {
		t.Fatalf("transient delivery Close() error = %v", err)
	}
	if got := transientCalls.Load(); got != 3 {
		t.Fatalf("transient attempts = %d, want 3", got)
	}

	var permanentCalls atomic.Int32
	permanent, err := NewClient(Config{
		Enabled: true, ProviderName: "code", HubURL: "https://hub.example", ProviderToken: "token",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			permanentCalls.Add(1)
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := permanent.Track(context.Background(), Event{Action: "code_event"}); err != nil {
		t.Fatal(err)
	}
	if err := permanent.Close(); !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("permanent delivery Close() error = %v, want ErrDeliveryFailed", err)
	}
	if got := permanentCalls.Load(); got != 1 {
		t.Fatalf("permanent attempts = %d, want 1", got)
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
