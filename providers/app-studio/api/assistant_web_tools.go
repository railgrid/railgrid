/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Web tools for assistant turns: search, and reading a page. Ported from
// vibe-studio's webtools package (itself ported from the agents provider).
//
// The SSRF posture: a URL the MODEL chose must never reach an internal
// address, because a prompt injection would otherwise turn the provider into
// a network probe. The dial guard checks the RESOLVED socket address, not
// the hostname — that is what defeats DNS rebinding.
//
// web_search rides the workspace's shared searxng instance (the Studio
// singleton, controller/studio) through the infrastructure data plane with
// the caller's bearer — the instance has no public URL and no credential of
// its own; the caller's kedge RBAC on the instance is the gate.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	webFetchMaxBody   = 200 * 1024
	webFetchMaxReturn = 12000
	// webSearchResultLimit bounds what reaches the model: it follows up with
	// web_fetch on whatever looks relevant, so a long list only burns context.
	webSearchResultLimit = 5
	webSearchTimeout     = 20 * time.Second
)

// webDialGuard rejects non-public dial targets.
func webDialGuard(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("unparseable dial address %q", host)
	}
	// Link-local carries cloud instance-metadata endpoints; loopback and
	// private ranges are the platform's own network.
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
		return fmt.Errorf("refusing to connect to non-public address %s", ip)
	}
	return nil
}

// webGuardedClient reaches public destinations only. It is the only client a
// model-supplied URL is ever handed to.
var webGuardedClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: webDialGuard}).DialContext,
	},
}

// projectAssistantWebFetch reads a public page and returns its text.
func projectAssistantWebFetch(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url must be an absolute http(s) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "kedge-app-studio/0.1 (+https://github.com/faroshq/kedge)")
	resp, err := webGuardedClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBody))
	if err != nil {
		return "", err
	}
	text := string(body)
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		text = webHTMLToText(text)
	}
	return fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, u.String(), webClip(text, webFetchMaxReturn)), nil
}

// projectAssistantWebSearch queries the workspace's shared search backend
// through the data plane as the caller.
func (s *Server) projectAssistantWebSearch(ctx context.Context, req projectAssistantToolCallRequest, query string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is required")
	}
	c, err := s.clientFor(req.Identity)
	if err != nil {
		return "", err
	}
	resource, name := s.searchBackend(ctx, c)
	if resource == "" || name == "" {
		return "", fmt.Errorf("this workspace has no search backend yet; it is provisioned with the workspace's Studio, so retry shortly")
	}
	q := url.Values{"q": {query}, "format": {"json"}}
	httpReq, err := s.newDataPlaneRequest(ctx, http.MethodGet, req.Identity,
		dataPlaneRef{Resource: resource, Name: name}, "proxy", "/search?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := s.sandboxDataPlaneClient(webSearchTimeout).Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBody))
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusConflict {
		return "", fmt.Errorf("the search backend is not ready yet (HTTP %d) — it is still starting", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("search backend HTTP %d: %s", resp.StatusCode, webClip(string(body), 300))
	}
	results, err := webParseSearchResults(body)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "no results", nil
	}
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, webClip(webHTMLToText(r.Snippet), 300))
	}
	return b.String(), nil
}

type webSearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// webParseSearchResults reads SearXNG's JSON API shape.
func webParseSearchResults(raw []byte) ([]webSearchResult, error) {
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("search backend returned unreadable JSON: %w", err)
	}
	out := make([]webSearchResult, 0, webSearchResultLimit)
	for _, r := range parsed.Results {
		if len(out) == webSearchResultLimit {
			break
		}
		out = append(out, webSearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out, nil
}

var (
	webReScript = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</(script|style|noscript)>`)
	webReTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	webReBlank  = regexp.MustCompile("\n{3,}")
)

// webHTMLToText is a crude readability pass: drop script/style, strip tags,
// collapse whitespace. Good enough for the model to read an article.
func webHTMLToText(s string) string {
	s = webReScript.ReplaceAllString(s, " ")
	s = webReTag.ReplaceAllString(s, "\n")
	s = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s)
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return webReBlank.ReplaceAllString(strings.Join(out, "\n"), "\n\n")
}

func webClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (truncated)"
}
