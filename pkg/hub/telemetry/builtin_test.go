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
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestDestinations(t *testing.T) {
	builtinConsole := Destination{URL: BuiltinURL, WebsiteID: BuiltinConsoleWebsiteID, Anonymous: true}
	builtinInstances := Destination{URL: BuiltinURL, WebsiteID: BuiltinInstancesWebsiteID, Anonymous: true}
	own := Destination{URL: "https://telemetry.example.com", WebsiteID: "w", Hostname: "console.example.com"}
	ownCfg := Config{URL: own.URL, WebsiteID: own.WebsiteID, Hostname: own.Hostname}

	for name, tc := range map[string]struct {
		cfg       Config
		console   *Destination
		instances *Destination
	}{
		"a release reports to the built-in destination": {
			cfg: Config{Version: "v0.3.0", Hostname: "console.example.com"}, console: &builtinConsole, instances: &builtinInstances,
		},
		"a development build reports nowhere":  {cfg: Config{Version: "dev"}},
		"a build between tags reports nowhere": {cfg: Config{Version: "v0.3.0-4-gabc1234"}},
		"--telemetry=false sends nothing, whatever else is set": {
			cfg: Config{Disabled: true, Version: "v0.3.0", URL: own.URL, WebsiteID: own.WebsiteID},
		},
		"the instance report can be turned off alone": {
			cfg: Config{InstanceReportDisabled: true, Version: "v0.3.0"}, console: &builtinConsole,
		},
		"an own backend takes the console events away from the built-in one": {
			cfg: func() Config { c := ownCfg; c.Version = "v0.3.0"; return c }(), console: &own, instances: &builtinInstances,
		},
		"an own backend works on a development build": {cfg: ownCfg, console: &own},
	} {
		t.Run(name, func(t *testing.T) {
			check := func(what string, got Destination, on bool, want *Destination) {
				t.Helper()
				if on != (want != nil) {
					t.Fatalf("%s on = %v, want %v", what, on, want != nil)
				}
				if want != nil && got != *want {
					t.Errorf("%s = %+v, want %+v", what, got, *want)
				}
			}
			got, on := tc.cfg.Console()
			check("Console()", got, on, tc.console)
			got, on = tc.cfg.Instances()
			check("Instances()", got, on, tc.instances)
		})
	}
}

func TestIsRelease(t *testing.T) {
	for version, want := range map[string]bool{
		"v0.2.38": true, "v1.0.0-rc.1": true, "v1.0.0-beta.2": true,
		"dev": false, "": false, "v0.2.38-4-gabc1234": false, "v0.2.38-dirty": false,
		"v0.2.38-4-gabc1234-dirty": false, "abc1234": false, "0.2.38": false,
	} {
		if got := IsRelease(version); got != want {
			t.Errorf("IsRelease(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestInstanceIDIsStableAndDoesNotCarryItsSeed(t *testing.T) {
	const seed = "5e0e8a55-3a43-4d7b-9a0c-6f2b3c1d9e77"
	id := InstanceID(seed)
	if id != InstanceID(seed) {
		t.Error("the same seed must give the same ID")
	}
	if id == InstanceID(seed+"x") {
		t.Error("different seeds must give different IDs")
	}
	if len(id) != 36 || strings.Contains(id, seed[:8]) {
		t.Errorf("ID %q must be a UUID-shaped hash, not the seed", id)
	}
}

func TestAnonymousRelaySendsNoAddressAndNoChosenName(t *testing.T) {
	sink := recordingSink{got: make(chan queued, 4)}
	relay := NewRelay(sink, func(*http.Request) string { return "203.0.113.7" }).Anonymous("instance-1")
	go relay.Run(t.Context())

	req := httptest.NewRequest(http.MethodPost, "/api/telemetry/events", strings.NewReader(`{"events":[
		{"type":"page","name":"provider-frame","route":"/:orgID/:workspaceID/providers/acme-billing","referrer":"https://intranet.acme.example","screen":"1440x900"},
		{"type":"event","name":"enable_provider","route":"/:orgID/:workspaceID/providers","props":{"provider":"acme-billing","flow":"enable"}},
		{"type":"page","name":"provider-frame","route":"/:orgID/:workspaceID/providers/agents"}]}`))
	req.Header.Set("User-Agent", "Mozilla/5.0 test")
	relay.ServeHTTP(httptest.NewRecorder(), req)
	relay.Record(req, "provider_enabled", map[string]string{"provider": "edges"})

	client := Client{ID: sessionID("instance-1", "Mozilla/5.0 test"), UserAgent: "Mozilla/5.0 test"}
	for i, want := range []Event{
		{Type: "page", Name: "provider-frame", Route: "/:orgID/:workspaceID/providers/other", Screen: "1440x900"},
		{Type: "event", Name: "enable_provider", Route: "/:orgID/:workspaceID/providers", Props: map[string]string{"provider": "other", "flow": "enable"}},
		{Type: "page", Name: "provider-frame", Route: "/:orgID/:workspaceID/providers/agents"},
		{Type: "event", Name: "provider_enabled", Route: "/hub", Props: map[string]string{"provider": "edges"}},
	} {
		select {
		case q := <-sink.got:
			if !reflect.DeepEqual(q, queued{client: client, event: want}) {
				t.Errorf("event %d\n got %+v\nwant %+v", i, q, queued{client: client, event: want})
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d was not delivered", i)
		}
	}
	if client.ID == sessionID("instance-2", "Mozilla/5.0 test") || client.ID == sessionID("instance-1", "Mozilla/5.0 other") {
		t.Error("the session must differ per installation and per browser")
	}
}

func TestReporterBuildsTheWholeReport(t *testing.T) {
	r := NewReporter(nil, "instance-1", "v0.3.0", nil)
	client, event := r.build(Report{
		KCP: "embedded", Auth: "oidc", Organizations: 7, Users: 143,
		Providers: []string{"edges", "acme-billing", "agents", "org-provider"},
	})
	wantClient := Client{ID: "instance-1", UserAgent: "Mozilla/5.0 (X11; Linux x86_64) railgrid-hub/v0.3.0"}
	if client != wantClient {
		t.Errorf("client = %+v, want %+v", client, wantClient)
	}
	want := Event{Type: "event", Name: "instance_report", Route: "/hub", Props: map[string]string{
		"version": "v0.3.0", "kcp": "embedded", "auth": "oidc",
		"organizations": "6-20", "users": "101-500",
		"providers": "agents,edges", "other_providers": "2-5",
	}}
	if !reflect.DeepEqual(event, want) {
		t.Errorf("event\n got %+v\nwant %+v", event, want)
	}

	// What the hub could not read is left out, not reported as zero.
	_, event = r.build(Report{Organizations: -1, Users: -1})
	if want := map[string]string{"version": "v0.3.0", "other_providers": "0"}; !reflect.DeepEqual(event.Props, want) {
		t.Errorf("unknowns: got %v, want %v", event.Props, want)
	}
}

func TestBucket(t *testing.T) {
	for n, want := range map[int]string{-1: "0", 0: "0", 1: "1", 2: "2-5", 5: "2-5", 6: "6-20", 20: "6-20", 21: "21-100", 100: "21-100", 101: "101-500", 500: "101-500", 501: "500+"} {
		if got := Bucket(n); got != want {
			t.Errorf("Bucket(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestReporterWaitsThenReportsDaily(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := recordingSink{got: make(chan queued, 8)}
		r := NewReporter(sink, "instance-1", "v0.3.0", func(context.Context) Report { return Report{KCP: "external"} })
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go r.Run(ctx)

		time.Sleep(instanceReportDelay - time.Second)
		synctest.Wait()
		if len(sink.got) != 0 {
			t.Fatal("a hub that has only just started must not have reported")
		}
		time.Sleep(instanceReportDelay + 2*time.Second)
		synctest.Wait()
		if len(sink.got) != 1 {
			t.Fatalf("reports after the delay = %d, want 1", len(sink.got))
		}
		time.Sleep(2*instanceReportInterval + time.Second)
		synctest.Wait()
		if len(sink.got) != 3 {
			t.Fatalf("reports after two more days = %d, want 3", len(sink.got))
		}
	})
}

func TestUmamiSinkSendsADistinctIDInPlaceOfTheAddress(t *testing.T) {
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		got <- body
	}))
	defer srv.Close()
	sink := NewUmamiSink(Destination{URL: srv.URL, WebsiteID: "site-1", Hostname: "instance-1"})
	if err := sink.Send(context.Background(), Client{ID: "session-1", UserAgent: "Mozilla/5.0 test"}, Event{Type: "page", Name: "dashboard", Route: "/:orgID/:workspaceID"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"type": "event", "payload": map[string]any{
		"website": "site-1", "hostname": "instance-1", "url": "/:orgID/:workspaceID", "title": "dashboard",
		"userAgent": "Mozilla/5.0 test", "id": "session-1",
	}}
	if body := <-got; !reflect.DeepEqual(body, want) {
		t.Errorf("payload\n got %v\nwant %v", body, want)
	}
}

func TestUmamiSinkReportsAnEventDiscardedAsABot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"beep":"boop"}`))
	}))
	defer srv.Close()
	err := NewUmamiSink(Destination{URL: srv.URL, WebsiteID: "w"}).Send(context.Background(), Client{UserAgent: "Go-http-client/2.0"}, Event{Type: "page", Name: "a", Route: "/"})
	if err == nil {
		t.Fatal("a 200 that recorded nothing must be an error")
	}
}
