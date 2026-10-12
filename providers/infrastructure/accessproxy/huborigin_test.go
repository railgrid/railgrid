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

package accessproxy

import (
	"testing"
	"time"
)

func TestRailgridAuthCanonicalizesHubIssuerOrigin(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newIdentityFakeHub(t, time.Now())
	config := identityConfig(upstream.URL, hub)
	config.HubURL = "HTTPS://HUB.INTERNAL.TEST:443/"
	config.HubPublicURL = "HTTPS://CONSOLE.AUTH-TEST.EXAMPLE:443/"
	config.AuthIssuer = "https://console.auth-test.example/auth/apps/identity"

	p := newIdentityProxy(t, config)
	wantPublicURL := "https://console.auth-test.example"
	if p.config.hubURL != testHubURL || p.config.hubPublicURL != wantPublicURL {
		t.Fatalf("normalized hub origins = %q / %q, want %q / %q", p.config.hubURL, p.config.hubPublicURL, testHubURL, wantPublicURL)
	}
	wantIssuer := wantPublicURL + identityIssuerPath
	if p.config.authIssuer != wantIssuer {
		t.Fatalf("normalized auth issuer = %q, want %q", p.config.authIssuer, wantIssuer)
	}
}

func TestHTTPHubOriginsRemainValidWhenRailgridAuthIsDisabled(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	config := Config{
		Host:         testHost,
		Mode:         ModePrivate,
		Instance:     testInstance(),
		HubURL:       "HTTP://HUB.INTERNAL.TEST:80/",
		HubPublicURL: "HTTP://CONSOLE.AUTH-TEST.EXAMPLE:80/",
		Routes:       []Route{{Prefix: "/", Target: upstream.URL}},
	}
	config.AllowNonClusterTargets = true
	p, err := New(config)
	if err != nil {
		t.Fatalf("New with HTTP hub origins and auth disabled: %v", err)
	}
	if p.config.hubURL != "http://hub.internal.test" || p.config.hubPublicURL != "http://console.auth-test.example" {
		t.Fatalf("normalized HTTP hub origins = %q / %q", p.config.hubURL, p.config.hubPublicURL)
	}
}

func TestRailgridAuthStillRequiresHTTPSHubIssuer(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newIdentityFakeHub(t, time.Now())
	config := identityConfig(upstream.URL, hub)
	config.HubURL = "http://hub.internal.test:80"
	config.HubPublicURL = "http://console.auth-test.example:80"
	config.AuthIssuer = "http://console.auth-test.example/auth/apps/identity"
	config.AllowNonClusterTargets = true
	if _, err := New(config); err == nil {
		t.Fatal("Railgrid Auth accepted an HTTP hub issuer")
	}
}
