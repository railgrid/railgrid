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
	"crypto/tls"
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
	webFetchMaxBody = 1024 * 1024
	// webFetchMaxReturn is the default readable-text budget: enough to answer a
	// question from a page, small enough not to dominate a chat context.
	webFetchMaxReturn = 12000
	// webFetchHardMaxReturn is the ceiling a caller may raise the budget to with
	// maxChars. Research work summarizes a long source into a small finding, so
	// reading it in full is worth the tokens once; chat is not, which is why the
	// default stays low and this is opt-in per call.
	webFetchHardMaxReturn = 64 * 1024
	braveSearchURL        = "https://api.search.brave.com/res/v1/web/search"

	// Search backends a websearch Connection can speak, selected by
	// spec.config["provider"].
	searchProviderBrave   = "brave"
	searchProviderSearXNG = "searxng"
)

// dialGuard rejects non-public dial targets. Checking the resolved socket
// address (not the pre-resolution hostname) is what defeats DNS rebinding.
//
// allowPrivate distinguishes the two kinds of destination this package reaches:
// a URL the MODEL chose (web_fetch) must never reach internal addresses, since
// a prompt injection would otherwise turn the provider into a network probe;
// a URL the USER configured (a websearch Connection's baseURL) is ordinary
// configuration, at the same trust level as an mcp Connection's endpoint, and
// blocking it only stops people running their own search backend.
//
// Link-local stays blocked either way: that range carries cloud
// instance-metadata endpoints, which no configuration should be able to reach.
func dialGuard(allowPrivate bool) func(string, string, syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("unparseable dial address %q", host)
		}
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("refusing to connect to link-local address %s", ip)
		}
		if allowPrivate {
			return nil
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
			return fmt.Errorf("refusing to connect to non-public address %s", ip)
		}
		return nil
	}
}

func newHTTPClient(allowPrivate bool) *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: dialGuard(allowPrivate)}).DialContext,
		},
	}
}

// guardedHTTPClient is the default: public destinations only.
var guardedHTTPClient = newHTTPClient(false)

// ConfiguredEndpointHTTPClient returns the shared client for destinations a
// CALLER configured, as opposed to ones a model chose: private addresses are
// reachable, link-local (cloud instance metadata) is not. See dialGuard for why
// the two trust levels differ.
//
// Exported for run-completion callbacks, whose URL comes from an authenticated,
// authorized caller — the same trust level as a Connection's baseURL, and
// commonly an in-cluster Service, which the public-only client would refuse.
// Exported rather than reimplemented so there stays exactly one dial guard.
func ConfiguredEndpointHTTPClient() *http.Client { return configuredHTTPClient }

// configuredHTTPClient serves endpoints the user configured on a Connection —
// a self-hosted search backend, which is commonly an in-cluster Service or a
// loopback address in local development.
var configuredHTTPClient = newHTTPClient(true)

// insecureConfiguredHTTPClient is the same, without TLS verification, for the
// hub's own self-signed certificate in local development. Only ever selected
// from the operator-set hub-insecure flag — never from anything a tenant or a
// model can influence.
var insecureConfiguredHTTPClient = func() *http.Client {
	c := newHTTPClient(true)
	t := c.Transport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev hubs use self-signed certs; opt-in
	c.Transport = t
	return c
}()

// Web returns the web family: SSRF-guarded fetch and search. Search needs a
// websearch Connection, which speaks either a self-hosted SearXNG instance
// (no API key — see the searxng infrastructure Template) or the Brave API.
func Web(d Deps) []engine.Tool {
	return []engine.Tool{
		{
			Name: "web_fetch",
			Desc: "Fetch a public web page over HTTP(S) and return its readable text (truncated). Private/internal addresses are blocked.",
			Params: map[string]engine.Param{
				"url": {Type: "string", Desc: "absolute http(s) URL", Required: true},
				"maxChars": {Type: "integer", Desc: fmt.Sprintf(
					"how much readable text to return (default %d, max %d). Raise it when you need the whole source — a long article or reference page you are going to summarize.",
					webFetchMaxReturn, webFetchHardMaxReturn)},
			},
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				args, err := parseArgs(argsJSON)
				if err != nil {
					return "", err
				}
				return webFetch(ctx, argString(args, "url"), argInt(args, "maxChars"))
			},
		},
		{
			Name: "web_search",
			Desc: "Search the web and return the top results (title, URL, snippet). Follow up with web_fetch to read a result in full. Requires a websearch connection in this workspace." +
				searchFanOutHint(d),
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

// fetchReturnBudget resolves how much readable text one fetch returns: the
// caller's request, defaulted and capped.
func fetchReturnBudget(maxChars int) int {
	if maxChars <= 0 {
		return webFetchMaxReturn
	}
	return min(maxChars, webFetchHardMaxReturn)
}

// searchFanOutHint steers a multi-part request toward the spawn tool when the
// caller has it. Without this the two tools compete for the same job and the
// model reliably picks the cheaper-looking one — a sequence of searches in a
// single turn, which is both slower and shallower than a fan-out.
func searchFanOutHint(d Deps) string {
	if d.Spawn == nil {
		return ""
	}
	return " If the request has several independent parts, do NOT search them one after another here: " +
		"spawn a worker per part and join the results instead."
}

func webFetch(ctx context.Context, raw string, maxChars int) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url must be an absolute http(s) URL")
	}
	maxChars = fetchReturnBudget(maxChars)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "faros-agents/0.1 (+https://github.com/faroshq/faros)")
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
	return fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, u.String(), clip(text, maxChars)), nil
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
		return "", fmt.Errorf("no websearch connection in this workspace — add one on the Connections tab (a self-hosted SearXNG instance, or a Brave API key)")
	}
	token := d.connToken(ctx, search.Name)
	req, err := searchRequest(ctx, search, d.DataPlane, token, query)
	if err != nil {
		return "", err
	}
	// The endpoint came from the Connection (or from the platform's own data
	// plane), not from the model, so private and loopback destinations are
	// allowed here — see dialGuard.
	client := configuredHTTPClient
	if d.DataPlane.Insecure {
		client = insecureConfiguredHTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("search API HTTP %d: %s", resp.StatusCode, clip(string(raw), 300))
	}
	results, err := parseSearchResults(searchProvider(search), raw)
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

// searchProvider resolves which backend a websearch Connection speaks.
// Brave is the default so a connection that only carries an API token keeps
// working without extra configuration.
func searchProvider(conn *agentsv1alpha1.Connection) string {
	if p := strings.ToLower(strings.TrimSpace(conn.Spec.Config["provider"])); p != "" {
		return p
	}
	return searchProviderBrave
}

// searchRequest builds the backend-specific query.
//
// A searxng connection addresses its backend one of two ways. Preferred is an
// INSTANCE reference: a searxng Template provisioned in this workspace, reached
// over the platform's internal data plane, with no public hostname and no
// instance credential — kcp RBAC on the instance is the gate. A baseURL is the
// fallback for a SearXNG the tenant runs somewhere else. Brave is a fixed
// hosted endpoint authenticated with its own header.
func searchRequest(ctx context.Context, conn *agentsv1alpha1.Connection, dp DataPlane, token, query string) (*http.Request, error) {
	base := strings.TrimSpace(conn.Spec.BaseURL)
	switch searchProvider(conn) {
	case searchProviderSearXNG:
		if instance := strings.TrimSpace(conn.Spec.Config["instance"]); instance != "" {
			return dataPlaneSearchRequest(ctx, conn, dp, instance, query)
		}
		if base == "" {
			return nil, fmt.Errorf("websearch connection %q is searxng but names neither an instance nor a baseURL — point it at a searxng instance in this workspace, or at an external instance's URL", conn.Name)
		}
		// Accept either the instance root or the /search endpoint: pointing a
		// connection at the URL the template reports is the obvious thing to do.
		endpoint := strings.TrimRight(base, "/")
		if !strings.HasSuffix(endpoint, "/search") {
			endpoint += "/search"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			endpoint+"?q="+url.QueryEscape(query)+"&format=json", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return req, nil

	case searchProviderBrave:
		if token == "" {
			return nil, fmt.Errorf("websearch connection %q has no API token", conn.Name)
		}
		if base == "" {
			base = braveSearchURL
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			base+"?q="+url.QueryEscape(query)+"&count=5", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Subscription-Token", token)
		return req, nil

	default:
		return nil, fmt.Errorf("websearch connection %q has unknown provider %q (want %q or %q)",
			conn.Name, searchProvider(conn), searchProviderBrave, searchProviderSearXNG)
	}
}

// searxngResource is the plural the searxng Template publishes its instances
// under. Overridable per connection for a differently-named template.
const searxngResource = "searxngs"

// dataPlaneSearchRequest addresses a searxng instance through the
// infrastructure provider's data plane:
//
//	{hub}/services/providers/infrastructure/dataplane/clusters/{cluster}/{resource}/{name}/proxy/search?…
//
// The call is authenticated as the CALLER, and the data plane authorizes it by
// re-reading the instance with that same token — so kcp RBAC on the instance is
// the whole gate and the workload needs no credential of its own.
func dataPlaneSearchRequest(ctx context.Context, conn *agentsv1alpha1.Connection, dp DataPlane, instance, query string) (*http.Request, error) {
	_, resource := instanceRef(conn, searxngResource)
	base, err := dp.ProxyURL("websearch", conn.Name, resource, instance)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/search?q="+url.QueryEscape(query)+"&format=json", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+dp.Token)
	return req, nil
}

// searchResult is one hit, normalized across backends.
type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

// searchResultLimit bounds how many hits reach the model — the agent follows up
// with web_fetch on whichever look relevant, so a long list only burns context.
const searchResultLimit = 5

func parseSearchResults(provider string, raw []byte) ([]searchResult, error) {
	switch provider {
	case searchProviderSearXNG:
		var parsed struct {
			Results []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Content string `json:"content"`
			} `json:"results"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("parsing searxng response: %w", err)
		}
		out := make([]searchResult, 0, min(len(parsed.Results), searchResultLimit))
		for _, r := range parsed.Results {
			if len(out) == searchResultLimit {
				break
			}
			out = append(out, searchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
		}
		return out, nil

	default:
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
			return nil, fmt.Errorf("parsing search response: %w", err)
		}
		out := make([]searchResult, 0, len(parsed.Web.Results))
		for _, r := range parsed.Web.Results {
			out = append(out, searchResult{Title: r.Title, URL: r.URL, Snippet: r.Description})
		}
		return out, nil
	}
}
