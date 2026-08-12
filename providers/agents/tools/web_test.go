// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
)

func searchConn(name string, spec agentsv1alpha1.ConnectionSpec) *agentsv1alpha1.Connection {
	spec.Type = agentsv1alpha1.ConnectionTypeWebSearch
	return &agentsv1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
}

func TestSearchRequest(t *testing.T) {
	ctx := context.Background()

	t.Run("brave is the default and keeps its own auth header", func(t *testing.T) {
		req, err := searchRequest(ctx, searchConn("brave", agentsv1alpha1.ConnectionSpec{}), DataPlane{}, "tok", "faros agents")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(req.URL.String(), braveSearchURL) {
			t.Fatalf("url = %s, want the hosted Brave endpoint", req.URL)
		}
		if req.Header.Get("X-Subscription-Token") != "tok" {
			t.Fatalf("missing Brave subscription header: %v", req.Header)
		}
		if req.URL.Query().Get("q") != "faros agents" {
			t.Fatalf("query not passed through: %s", req.URL)
		}
	})

	t.Run("brave without a token is a clear error", func(t *testing.T) {
		_, err := searchRequest(ctx, searchConn("brave", agentsv1alpha1.ConnectionSpec{}), DataPlane{}, "", "x")
		if err == nil || !strings.Contains(err.Error(), "token") {
			t.Fatalf("want a missing-token error, got %v", err)
		}
	})

	t.Run("searxng builds the JSON API query", func(t *testing.T) {
		conn := searchConn("local", agentsv1alpha1.ConnectionSpec{
			BaseURL: "https://searxng-abc.apps.example.com",
			Config:  map[string]string{"provider": "searxng"},
		})
		req, err := searchRequest(ctx, conn, DataPlane{}, "t0ken", "who is ada lovelace")
		if err != nil {
			t.Fatal(err)
		}
		if got := req.URL.Path; got != "/search" {
			t.Fatalf("path = %q, want /search appended to the instance root", got)
		}
		if req.URL.Query().Get("format") != "json" {
			t.Fatalf("format=json is what makes SearXNG return the API shape: %s", req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer t0ken" {
			t.Fatalf("token should ride as a bearer for the template's auth gate: %v", req.Header)
		}
	})

	t.Run("searxng accepts a baseURL that already ends in /search", func(t *testing.T) {
		conn := searchConn("local", agentsv1alpha1.ConnectionSpec{
			BaseURL: "https://s.example.com/search",
			Config:  map[string]string{"provider": "searxng"},
		})
		req, err := searchRequest(ctx, conn, DataPlane{}, "", "x")
		if err != nil {
			t.Fatal(err)
		}
		if req.URL.Path != "/search" {
			t.Fatalf("path = %q, want no doubled /search", req.URL.Path)
		}
		// A bare instance needs no credential at all.
		if req.Header.Get("Authorization") != "" {
			t.Fatal("no token configured, so no Authorization header should be sent")
		}
	})

	t.Run("searxng without a baseURL is a clear error", func(t *testing.T) {
		conn := searchConn("local", agentsv1alpha1.ConnectionSpec{Config: map[string]string{"provider": "searxng"}})
		_, err := searchRequest(ctx, conn, DataPlane{}, "", "x")
		if err == nil || !strings.Contains(err.Error(), "baseURL") {
			t.Fatalf("want a missing-baseURL error, got %v", err)
		}
	})

	t.Run("an unknown provider names the valid options", func(t *testing.T) {
		conn := searchConn("x", agentsv1alpha1.ConnectionSpec{Config: map[string]string{"provider": "google"}})
		_, err := searchRequest(ctx, conn, DataPlane{}, "", "x")
		if err == nil || !strings.Contains(err.Error(), "searxng") {
			t.Fatalf("want an error listing the supported providers, got %v", err)
		}
	})
}

func TestParseSearchResults(t *testing.T) {
	t.Run("searxng shape", func(t *testing.T) {
		raw := []byte(`{"results":[
			{"title":"Ada","url":"https://a.example","content":"first programmer"},
			{"title":"Babbage","url":"https://b.example","content":"engine"}
		],"number_of_results":2}`)
		out, err := parseSearchResults(searchProviderSearXNG, raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 2 || out[0].Title != "Ada" || out[0].URL != "https://a.example" || out[0].Snippet != "first programmer" {
			t.Fatalf("got %+v", out)
		}
	})

	t.Run("searxng results are capped so a long list can't flood the context", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(`{"results":[`)
		for i := range 40 {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"title":"t","url":"https://x.example","content":"c"}`)
		}
		b.WriteString(`]}`)
		out, err := parseSearchResults(searchProviderSearXNG, []byte(b.String()))
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != searchResultLimit {
			t.Fatalf("got %d results, want the cap of %d", len(out), searchResultLimit)
		}
	})

	t.Run("brave shape", func(t *testing.T) {
		raw := []byte(`{"web":{"results":[{"title":"Ada","url":"https://a.example","description":"first <b>programmer</b>"}]}}`)
		out, err := parseSearchResults(searchProviderBrave, raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 1 || out[0].Title != "Ada" {
			t.Fatalf("got %+v", out)
		}
	})

	t.Run("a malformed body reports which backend failed", func(t *testing.T) {
		if _, err := parseSearchResults(searchProviderSearXNG, []byte("not json")); err == nil ||
			!strings.Contains(err.Error(), "searxng") {
			t.Fatalf("want a searxng-specific parse error, got %v", err)
		}
	})
}

// A configured endpoint (a websearch Connection's baseURL) may be private or
// loopback; a model-supplied one may not. Neither may reach link-local, where
// cloud instance-metadata lives.
func TestDialGuard(t *testing.T) {
	tests := []struct {
		name         string
		addr         string
		allowPrivate bool
		wantErr      bool
	}{
		{"public is allowed", "93.184.216.34:443", false, false},
		{"loopback blocked by default", "127.0.0.1:8080", false, true},
		{"private blocked by default", "10.1.2.3:8080", false, true},
		{"loopback allowed for a configured endpoint", "127.0.0.1:8080", true, false},
		{"private allowed for a configured endpoint", "10.1.2.3:8080", true, false},
		{"link-local blocked even for a configured endpoint", "169.254.169.254:80", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := dialGuard(tc.allowPrivate)("tcp", tc.addr, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("dialGuard(%v)(%s) err = %v, wantErr %v", tc.allowPrivate, tc.addr, err, tc.wantErr)
			}
		})
	}
}

// A searxng connection that names an instance is addressed over the
// infrastructure provider's data plane — no public hostname, no instance
// credential, authorized by the caller's own RBAC on the instance.
func TestDataPlaneSearchRequest(t *testing.T) {
	ctx := context.Background()
	dp := DataPlane{HubBase: "https://hub.example.com", ClusterID: "23qp2e0jwjeqwp2i", Token: "user-token"}
	instanceConn := func(cfg map[string]string) *agentsv1alpha1.Connection {
		cfg["provider"] = "searxng"
		return searchConn("search", agentsv1alpha1.ConnectionSpec{Config: cfg})
	}

	t.Run("addresses the instance through the hub data plane", func(t *testing.T) {
		req, err := searchRequest(ctx, instanceConn(map[string]string{"instance": "search"}), dp, "", "ada lovelace")
		if err != nil {
			t.Fatal(err)
		}
		want := "https://hub.example.com/services/providers/infrastructure/dataplane/clusters/23qp2e0jwjeqwp2i/searxngs/search/proxy/search"
		if got := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path; got != want {
			t.Fatalf("url = %s\nwant %s", got, want)
		}
		if req.URL.Query().Get("format") != "json" || req.URL.Query().Get("q") != "ada lovelace" {
			t.Fatalf("query not composed: %s", req.URL.RawQuery)
		}
		// The caller's token is what the data plane authorizes with; the
		// instance itself holds no credential.
		if req.Header.Get("Authorization") != "Bearer user-token" {
			t.Fatalf("headers = %v", req.Header)
		}
	})

	t.Run("an instance reference wins over a stale baseURL", func(t *testing.T) {
		conn := instanceConn(map[string]string{"instance": "search"})
		conn.Spec.BaseURL = "https://leftover-public-url.example.com"
		req, err := searchRequest(ctx, conn, dp, "", "x")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(req.URL.Host, "leftover") {
			t.Fatalf("baseURL should not be used when an instance is named: %s", req.URL)
		}
	})

	t.Run("a run with no identity gets a precise error, not a bare 401 two hops away", func(t *testing.T) {
		noToken := DataPlane{HubBase: dp.HubBase, ClusterID: dp.ClusterID}
		_, err := searchRequest(ctx, instanceConn(map[string]string{"instance": "search"}), noToken, "", "x")
		if err == nil {
			t.Fatal("want an error explaining the run has no identity to authorize as")
		}
		if !strings.Contains(err.Error(), "no identity") {
			t.Fatalf("error should name the missing identity: %v", err)
		}
	})

	t.Run("a custom template resource is honoured", func(t *testing.T) {
		req, err := searchRequest(ctx, instanceConn(map[string]string{"instance": "s", "instanceResource": "mysearches"}), dp, "", "x")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(req.URL.Path, "/mysearches/s/proxy/") {
			t.Fatalf("path = %s", req.URL.Path)
		}
	})

	t.Run("names neither an instance nor a baseURL", func(t *testing.T) {
		_, err := searchRequest(ctx, instanceConn(map[string]string{}), dp, "", "x")
		if err == nil || !strings.Contains(err.Error(), "instance") {
			t.Fatalf("want an error naming both options, got %v", err)
		}
	})
}

// A research worker reads a whole source once and summarizes it; chat does not.
// The default therefore stays small and raising it is per-call and capped.
func TestFetchReturnBudget(t *testing.T) {
	tests := []struct {
		name    string
		request int
		want    int
	}{
		{"unset uses the chat-sized default", 0, webFetchMaxReturn},
		{"negative is treated as unset", -1, webFetchMaxReturn},
		{"a request within the cap is honored", 40000, 40000},
		{"an over-large request is capped", 10_000_000, webFetchHardMaxReturn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := fetchReturnBudget(tc.request); got != tc.want {
				t.Fatalf("fetchReturnBudget(%d) = %d, want %d", tc.request, got, tc.want)
			}
		})
	}
}

func TestWebFetchAdvertisesMaxChars(t *testing.T) {
	for _, tool := range Web(Deps{}) {
		if tool.Name != "web_fetch" {
			continue
		}
		if _, ok := tool.Params["maxChars"]; !ok {
			t.Fatalf("web_fetch must advertise maxChars so a worker can ask for a full read; params = %v", tool.Params)
		}
		return
	}
	t.Fatal("web family has no web_fetch tool")
}

// web_search and spawn compete for the same job on a multi-part request, and a
// model picks the cheaper-looking one. The search tool therefore points at spawn
// — but only when the caller actually has it.
func TestSearchFanOutHint(t *testing.T) {
	withSpawn := Deps{Spawn: func(context.Context, SpawnRequest) (string, error) { return "", nil }}
	if got := searchFanOutHint(withSpawn); !strings.Contains(got, "spawn a worker per part") {
		t.Fatalf("expected a fan-out hint, got %q", got)
	}
	if got := searchFanOutHint(Deps{}); got != "" {
		t.Fatalf("no spawn tool means no hint to give, got %q", got)
	}

	// And the hint has to reach the tool description the model reads.
	for _, tool := range Web(withSpawn) {
		if tool.Name == "web_search" {
			if !strings.Contains(tool.Desc, "spawn a worker per part") {
				t.Fatalf("web_search description is missing the hint: %s", tool.Desc)
			}
			return
		}
	}
	t.Fatal("web family has no web_search tool")
}
