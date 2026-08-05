// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Package webtools gives a studio turn the internet: search, and reading a
// page. Both are always available — a builder that cannot look anything up
// has to ask the user to paste documentation into the chat, which is the one
// thing the wizard was supposed to stop.
//
// The SSRF posture is ported from the agents provider (providers/agents/
// tools/web.go), which had to solve the same problem: a URL the MODEL chose
// must never reach an internal address, because a prompt injection would
// otherwise turn the provider into a network probe.
package webtools

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

	"github.com/faroshq/provider-vibe-studio/engine"
	"github.com/faroshq/provider-vibe-studio/provision"
)

const (
	fetchMaxBody   = 200 * 1024
	fetchMaxReturn = 12000
	// searchResultLimit bounds what reaches the model: it follows up with
	// web_fetch on whatever looks relevant, so a long list only burns context.
	searchResultLimit = 5
)

// dialGuard rejects non-public dial targets. Checking the RESOLVED socket
// address, not the hostname, is what defeats DNS rebinding.
func dialGuard(_, address string, _ syscall.RawConn) error {
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

// guardedClient reaches public destinations only. It is the only client a
// model-supplied URL is ever handed to.
var guardedClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: dialGuard}).DialContext,
	},
}

// SearchRef addresses the project's private search backend: a searxng
// instance reached over the infrastructure data plane, with no public URL and
// no credential of its own — the caller's own kedge RBAC on the instance is
// the gate.
type SearchRef struct {
	Resource string // "searxngs"
	Name     string
}

// Tools returns the web family for one studio turn. pc carries the caller's
// bearer; search is empty when the project has no search instance bound, in
// which case web_search explains that instead of failing obscurely.
func Tools(pc *provision.Client, search SearchRef) []engine.Tool {
	return []engine.Tool{
		{
			Name: "web_search",
			Desc: "Search the web and return the top results (title, URL, snippet). Follow up with web_fetch to read one in full.",
			Params: map[string]engine.Param{
				"query": {Type: "string", Desc: "search query", Required: true},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					Query string `json:"query"`
				}
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					return "", err
				}
				return Search(ctx, pc, search, args.Query)
			},
		},
		{
			Name: "web_fetch",
			Desc: "Fetch a public web page over HTTP(S) and return its readable text (truncated). Use it to read documentation, a README, or a page found with web_search. Internal addresses are blocked.",
			Params: map[string]engine.Param{
				"url": {Type: "string", Desc: "absolute http(s) URL", Required: true},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				var args struct {
					URL string `json:"url"`
				}
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					return "", err
				}
				return Fetch(ctx, args.URL)
			},
		},
	}
}

// Fetch reads a public page and returns its text.
func Fetch(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url must be an absolute http(s) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "kedge-vibe-studio/0.1 (+https://github.com/faroshq/kedge)")
	resp, err := guardedClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBody))
	if err != nil {
		return "", err
	}
	text := string(body)
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		text = htmlToText(text)
	}
	return fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, u.String(), clip(text, fetchMaxReturn)), nil
}

// Search queries the project's searxng instance through the data plane.
func Search(ctx context.Context, pc *provision.Client, ref SearchRef, query string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is required")
	}
	if ref.Name == "" || ref.Resource == "" {
		return "", fmt.Errorf("this project has no search backend yet; it is provisioned with the project, so retry shortly")
	}
	if !pc.Ready() {
		return "", fmt.Errorf("no runtime credential on this turn")
	}
	q := url.Values{"q": {query}, "format": {"json"}}
	body, status, err := pc.GetPath(ctx, provision.Ref{Resource: ref.Resource, Name: ref.Name}, provision.VerbProxy, "search", q)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound || status == http.StatusConflict {
		return "", fmt.Errorf("the search backend is not ready yet (HTTP %d) — it is still starting", status)
	}
	if status/100 != 2 {
		return "", fmt.Errorf("search backend HTTP %d: %s", status, clip(string(body), 300))
	}
	results, err := parseResults(body)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "no results", nil
	}
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, clip(htmlToText(r.Snippet), 300))
	}
	return b.String(), nil
}

type result struct {
	Title   string
	URL     string
	Snippet string
}

// parseResults reads SearXNG's JSON API shape.
func parseResults(raw []byte) ([]result, error) {
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
	out := make([]result, 0, searchResultLimit)
	for _, r := range parsed.Results {
		if len(out) == searchResultLimit {
			break
		}
		out = append(out, result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out, nil
}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</(script|style|noscript)>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlank  = regexp.MustCompile(`\n{3,}`)
)

// htmlToText is a crude readability pass: drop script/style, strip tags,
// collapse whitespace. Good enough for the model to read an article.
func htmlToText(s string) string {
	s = reScript.ReplaceAllString(s, " ")
	s = reTag.ReplaceAllString(s, "\n")
	s = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s)
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return reBlank.ReplaceAllString(strings.Join(out, "\n"), "\n\n")
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (truncated)"
}
