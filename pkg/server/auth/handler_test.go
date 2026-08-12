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

package auth

import (
	"testing"

	"golang.org/x/oauth2"
)

func TestOAuth2EndpointWithBrowserAuthURLOverridesOnlyAuthorization(t *testing.T) {
	discovered := oauth2.Endpoint{
		AuthURL:  "https://dex.faros-system.svc.cluster.local:5554/dex/auth",
		TokenURL: "https://dex.faros-system.svc.cluster.local:5554/dex/token",
	}

	got, err := oauth2EndpointWithBrowserAuthURL(discovered, "https://login.example.test/dex/auth")
	if err != nil {
		t.Fatal(err)
	}
	if got.AuthURL != "https://login.example.test/dex/auth" {
		t.Fatalf("authorization URL = %q", got.AuthURL)
	}
	if got.TokenURL != discovered.TokenURL {
		t.Fatalf("token URL = %q, want discovery-derived %q", got.TokenURL, discovered.TokenURL)
	}
	if discovered.AuthURL != "https://dex.faros-system.svc.cluster.local:5554/dex/auth" {
		t.Fatalf("discovered endpoint was mutated: %#v", discovered)
	}
}

func TestOAuth2EndpointWithBrowserAuthURLRejectsUnsafeValues(t *testing.T) {
	for _, browserAuthURL := range []string{
		"localhost:5554/dex/auth",
		"http://login.example.test/dex/auth",
		"ftp://login.example.test/dex/auth",
		"https://user@login.example.test/dex/auth",
		"https://login.example.test/dex/auth#fragment",
	} {
		t.Run(browserAuthURL, func(t *testing.T) {
			if _, err := oauth2EndpointWithBrowserAuthURL(oauth2.Endpoint{}, browserAuthURL); err == nil {
				t.Fatal("expected invalid browser authorization URL")
			}
		})
	}
}

func TestOAuth2EndpointWithBrowserAuthURLDefaultsToDiscovery(t *testing.T) {
	want := oauth2.Endpoint{AuthURL: "https://idp.example.test/auth", TokenURL: "https://idp.example.test/token"}
	got, err := oauth2EndpointWithBrowserAuthURL(want, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("endpoint = %#v, want %#v", got, want)
	}
}
