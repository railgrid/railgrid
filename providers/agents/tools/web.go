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

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/engine"
)

const (
	webFetchMaxBody   = 200 * 1024
	webFetchMaxReturn = 12000
	braveSearchURL    = "https://api.search.brave.com/res/v1/web/search"
)

// guardedHTTPClient blocks connections to private, loopback, and link-local
// addresses at dial time (checking the actual socket address defeats DNS
// rebinding, not just the pre-resolved name).
var guardedHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				ip := net.ParseIP(host)
				if ip == nil {
					return fmt.Errorf("unparseable dial address %q", host)
				}
				if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
					return fmt.Errorf("refusing to connect to non-public address %s", ip)
				}
				return nil
			},
		}).DialContext,
	},
}

// Web returns the web family: SSRF-guarded fetch and search (search needs a
// websearch Connection with a Brave-compatible API token).
func Web(d Deps) []engine.Tool {
	return []engine.Tool{
		{
			Name: "web_fetch",
			Desc: "Fetch a public web page over HTTP(S) and return its readable text (truncated). Private/internal addresses are blocked.",
			Params: map[string]engine.Param{
				"url": {Type: "string", Desc: "absolute http(s) URL", Required: true},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				return webFetch(ctx, argString(args, "url"))
			},
		},
		{
			Name: "web_search",
			Desc: "Search the web and return the top results (title, URL, snippet). Requires a websearch connection in this workspace.",
			Params: map[string]engine.Param{
				"query": {Type: "string", Desc: "search query", Required: true},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				return webSearch(ctx, d, argString(args, "query"))
			},
		},
	}
}

func webFetch(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url must be an absolute http(s) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "kedge-agents/0.1 (+https://github.com/faroshq/kedge)")
	resp, err := guardedHTTPClient.Do(req)
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
		text = htmlToText(text)
	}
	return fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, u.String(), clip(text, webFetchMaxReturn)), nil
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
	lines := strings.Split(s, "\n")
	var out []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return reBlank.ReplaceAllString(strings.Join(out, "\n"), "\n\n")
}

func webSearch(ctx context.Context, d Deps, query string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is required")
	}
	conns, err := d.CR.ListConnections(ctx)
	if err != nil {
		return "", err
	}
	var search *agentsv1alpha1.Connection
	for i := range conns {
		if conns[i].Spec.Type == agentsv1alpha1.ConnectionTypeWebSearch {
			search = &conns[i]
			break
		}
	}
	if search == nil {
		return "", fmt.Errorf("no websearch connection in this workspace — create one (Brave-compatible API) on the Connections tab")
	}
	token := d.connToken(ctx, search.Name)
	if token == "" {
		return "", fmt.Errorf("websearch connection %q has no API token", search.Name)
	}
	base := strings.TrimSpace(search.Spec.BaseURL)
	if base == "" {
		base = braveSearchURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?q="+url.QueryEscape(query)+"&count=5", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", token)
	resp, err := guardedHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("search API HTTP %d: %s", resp.StatusCode, clip(string(raw), 300))
	}
	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("parsing search response: %w", err)
	}
	if len(parsed.Web.Results) == 0 {
		return "no results", nil
	}
	var b strings.Builder
	for i, r := range parsed.Web.Results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, clip(htmlToText(r.Description), 300))
	}
	return b.String(), nil
}
