// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package webtools

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchRejectsNonPublicDestinations(t *testing.T) {
	// A URL the MODEL chose must never reach the platform's own network: a
	// prompt injection would otherwise turn the studio into a network probe.
	srv := httptest.NewServer(nil)
	defer srv.Close()

	if _, err := Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("fetching a loopback address should be refused")
	} else if !strings.Contains(err.Error(), "non-public") {
		t.Errorf("error = %v, want it to name the reason", err)
	}
}

func TestFetchRequiresAnAbsoluteHTTPURL(t *testing.T) {
	for _, raw := range []string{"", "example.com", "file:///etc/passwd", "ftp://example.com"} {
		if _, err := Fetch(context.Background(), raw); err == nil {
			t.Errorf("Fetch(%q) should be refused", raw)
		}
	}
}

func TestSearchWithoutABackendExplainsItself(t *testing.T) {
	_, err := Search(context.Background(), nil, SearchRef{}, "faros")
	if err == nil || !strings.Contains(err.Error(), "search backend") {
		t.Errorf("err = %v, want it to say the project has no search backend yet", err)
	}
}

func TestParseResultsReadsSearXNGAndCaps(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"results":[`)
	for i := 0; i < 12; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"title":"t","url":"https://example.com","content":"c"}`)
	}
	b.WriteString(`]}`)

	got, err := parseResults([]byte(b.String()))
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if len(got) != searchResultLimit {
		t.Errorf("results = %d, want capped at %d", len(got), searchResultLimit)
	}
	if got[0].URL != "https://example.com" || got[0].Snippet != "c" {
		t.Errorf("result[0] = %+v", got[0])
	}
}

func TestHTMLToTextStripsMarkupAndScripts(t *testing.T) {
	got := htmlToText(`<html><head><style>a{}</style><script>var x=1</script></head>
		<body><h1>Faros</h1><p>A platform&nbsp;&amp; more</p></body></html>`)
	if strings.Contains(got, "var x") || strings.Contains(got, "<h1>") {
		t.Errorf("markup survived: %q", got)
	}
	if !strings.Contains(got, "Faros") || !strings.Contains(got, "A platform & more") {
		t.Errorf("text lost: %q", got)
	}
}
