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

package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg     Config
		enabled bool
		wantErr bool
	}{
		"off by default":           {cfg: Config{}},
		"on":                       {cfg: Config{URL: "https://telemetry.example.com", WebsiteID: "w"}, enabled: true},
		"url without website":      {cfg: Config{URL: "https://telemetry.example.com"}, enabled: true, wantErr: true},
		"website without url":      {cfg: Config{WebsiteID: "w"}, wantErr: true},
		"not an http url":          {cfg: Config{URL: "telemetry.example.com", WebsiteID: "w"}, enabled: true, wantErr: true},
		"scheme that is not http":  {cfg: Config{URL: "file:///etc/passwd", WebsiteID: "w"}, enabled: true, wantErr: true},
		"hostname alone stays off": {cfg: Config{Hostname: "console.example.com"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.cfg.Enabled(); got != tc.enabled {
				t.Errorf("Enabled() = %v, want %v", got, tc.enabled)
			}
			if err := tc.cfg.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestSanitize(t *testing.T) {
	for name, tc := range map[string]struct {
		in   Event
		want Event
		ok   bool
	}{
		"page": {
			in:   Event{Type: "page", Name: "provider-frame", Route: "/:orgID/:workspaceID/providers/agents"},
			want: Event{Type: "page", Name: "provider-frame", Route: "/:orgID/:workspaceID/providers/agents"},
			ok:   true,
		},
		"optional fields that fail are dropped, the event survives": {
			in: Event{
				Type: "event", Name: "enable_provider", Route: "/:orgID/:workspaceID/providers",
				Props:    map[string]string{"placement": "card", "Bad Key": "x", "long": strings.Repeat("a", 101), "multi": "a\nb", "empty": ""},
				Referrer: "javascript:alert(1)", Screen: "wide", Language: "en US",
			},
			want: Event{Type: "event", Name: "enable_provider", Route: "/:orgID/:workspaceID/providers", Props: map[string]string{"placement": "card"}},
			ok:   true,
		},
		"referrer keeps the origin only": {
			in:   Event{Type: "page", Name: "dashboard", Route: "/:orgID/:workspaceID", Referrer: "https://railgrid.ai/pricing/?utm_source=x#plans", Screen: "1440x900", Language: "en-GB"},
			want: Event{Type: "page", Name: "dashboard", Route: "/:orgID/:workspaceID", Referrer: "https://railgrid.ai", Screen: "1440x900", Language: "en-GB"},
			ok:   true,
		},
		"unknown type":             {in: Event{Type: "identify", Name: "x", Route: "/"}},
		"name with spaces":         {in: Event{Type: "event", Name: "Create Workspace", Route: "/"}},
		"route with a query":       {in: Event{Type: "page", Name: "dashboard", Route: "/a?token=secret"}},
		"route that is not a path": {in: Event{Type: "page", Name: "dashboard", Route: "https://evil.example/"}},
		"route too long":           {in: Event{Type: "page", Name: "dashboard", Route: "/" + strings.Repeat("a", maxRouteBytes)}},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := sanitize(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

type recordingSink struct{ got chan queued }

func (s recordingSink) Send(_ context.Context, client Client, event Event) error {
	s.got <- queued{client: client, event: event}
	return nil
}

func TestRelayQueuesValidEventsWithTheBrowsersIdentity(t *testing.T) {
	sink := recordingSink{got: make(chan queued, 4)}
	relay := NewRelay(sink, func(*http.Request) string { return "203.0.113.7" })
	go relay.Run(t.Context())

	req := httptest.NewRequest(http.MethodPost, "/api/telemetry/events", strings.NewReader(
		`{"events":[{"type":"page","name":"dashboard","route":"/:orgID/:workspaceID"},{"type":"nope","name":"x","route":"/"}]}`))
	req.Header.Set("User-Agent", "Mozilla/5.0 test")
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	select {
	case q := <-sink.got:
		want := queued{client: Client{IP: "203.0.113.7", UserAgent: "Mozilla/5.0 test"}, event: Event{Type: "page", Name: "dashboard", Route: "/:orgID/:workspaceID"}}
		if !reflect.DeepEqual(q, want) {
			t.Errorf("got %+v\nwant %+v", q, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event was not delivered")
	}
	select {
	case q := <-sink.got:
		t.Fatalf("invalid event was delivered: %+v", q)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRelayRejectsMalformedAndOversizedBatches(t *testing.T) {
	relay := NewRelay(recordingSink{got: make(chan queued, 1)}, func(*http.Request) string { return "" })
	many := `{"events":[` + strings.TrimSuffix(strings.Repeat(`{"type":"page","name":"a","route":"/"},`, maxBatch+1), ",") + `]}`
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"not json":        {body: "nope", want: http.StatusBadRequest},
		"too many events": {body: many, want: http.StatusRequestEntityTooLarge},
		"body too large":  {body: `{"events":[{"type":"page","name":"a","route":"/` + strings.Repeat("a", maxBodyBytes) + `"}]}`, want: http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			relay.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/telemetry/events", strings.NewReader(tc.body)))
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestRelayDropsWhenTheQueueIsFull(t *testing.T) {
	// No worker: the queue only fills. The request must still return at once.
	relay := NewRelay(recordingSink{}, func(*http.Request) string { return "" })
	batch := `{"events":[` + strings.TrimSuffix(strings.Repeat(`{"type":"page","name":"a","route":"/"},`, maxBatch), ",") + `]}`
	for i := range queueSize/maxBatch + 2 {
		rec := httptest.NewRecorder()
		relay.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/telemetry/events", strings.NewReader(batch)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d: status = %d, want 204", i, rec.Code)
		}
	}
	if got := len(relay.queue); got != queueSize {
		t.Errorf("queue holds %d events, want it capped at %d", got, queueSize)
	}
}

func TestUmamiSinkPayload(t *testing.T) {
	type received struct {
		path, userAgent string
		body            map[string]any
	}
	got := make(chan received, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got <- received{path: r.URL.Path, userAgent: r.UserAgent(), body: body}
		_, _ = w.Write([]byte(`{"cache":"x"}`))
	}))
	defer srv.Close()

	sink := NewUmamiSink(Config{URL: srv.URL + "/", WebsiteID: "site-1", Hostname: "console.example.com"})
	client := Client{IP: "203.0.113.7", UserAgent: "Mozilla/5.0 test"}

	if err := sink.Send(context.Background(), client, Event{Type: "page", Name: "dashboard", Route: "/:orgID/:workspaceID", Referrer: "https://railgrid.ai", Screen: "1440x900", Language: "en"}); err != nil {
		t.Fatal(err)
	}
	page := <-got
	if page.path != "/api/send" || page.userAgent != "Mozilla/5.0 test" {
		t.Errorf("request went to %q as %q", page.path, page.userAgent)
	}
	wantPage := map[string]any{"type": "event", "payload": map[string]any{
		"website": "site-1", "hostname": "console.example.com", "url": "/:orgID/:workspaceID", "title": "dashboard",
		"referrer": "https://railgrid.ai", "screen": "1440x900", "language": "en", "ip": "203.0.113.7", "userAgent": "Mozilla/5.0 test",
	}}
	if !reflect.DeepEqual(page.body, wantPage) {
		t.Errorf("page view\n got %v\nwant %v", page.body, wantPage)
	}

	if err := sink.Send(context.Background(), client, Event{Type: "event", Name: "enable_provider", Route: "/:orgID/:workspaceID/providers", Props: map[string]string{"provider": "agents"}}); err != nil {
		t.Fatal(err)
	}
	wantEvent := map[string]any{"type": "event", "payload": map[string]any{
		"website": "site-1", "hostname": "console.example.com", "url": "/:orgID/:workspaceID/providers", "name": "enable_provider",
		"data": map[string]any{"provider": "agents"}, "ip": "203.0.113.7", "userAgent": "Mozilla/5.0 test",
	}}
	if event := <-got; !reflect.DeepEqual(event.body, wantEvent) {
		t.Errorf("named event\n got %v\nwant %v", event.body, wantEvent)
	}
}

func TestUmamiSinkReportsARejectedEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Website not found.", http.StatusBadRequest)
	}))
	defer srv.Close()
	err := NewUmamiSink(Config{URL: srv.URL, WebsiteID: "w"}).Send(context.Background(), Client{}, Event{Type: "page", Name: "a", Route: "/"})
	if err == nil {
		t.Fatal("a 400 from the backend must be an error")
	}
}
