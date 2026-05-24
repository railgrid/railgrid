// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// quickstart is a minimal kedge provider used to prove the platform's
// extension surface end-to-end. It serves two routes on the same port:
//
//   - /                     a tiny HTML page that uses the postMessage
//                           handshake to receive identity context from the
//                           kedge portal shell. Mounted in the portal via
//                           /ui/providers/quickstart/.
//   - /healthz, /api/hello  the provider's "backend HTTP API". Mounted via
//                           /services/providers/quickstart/.
//
// The two are split only by URL in production — a single Service exposes
// the port and the ProviderCatalogEntry routes the same URL to both the
// UI proxy and the backend proxy. For local dev, both run on PORT.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// The kedge portal loads providers as **custom elements**, not iframes:
// the provider serves /main.js, the portal injects a <script> tag, then
// renders <kedge-provider-{name}> directly into its own DOM tree. The
// element shares the portal's stylesheet (so CSS tokens cascade in via
// :root) and dispatches/receives normal DOM events — no iframe boundary,
// no postMessage shuttle.
//
// The /  endpoint serves a small fallback page for direct browser visits
// (useful for debugging the provider outside the portal). The real entry
// point is /main.js.
// indexHTML is the standalone fallback served at "/". When you point a
// browser at the quickstart provider directly (outside the kedge portal)
// you get a one-paragraph explanation pointing at the real flow. We do NOT
// render the full UI here — that's the custom element's job, and the
// custom element is meant to mount inside the portal's DOM.
const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Quickstart provider · kedge</title>
<style>
  body { font-family: ui-sans-serif, system-ui, sans-serif; max-width: 540px; margin: 4em auto; padding: 0 1em; line-height: 1.6; color: #0f172a; background: #f5f7fa; }
  @media (prefers-color-scheme: dark) { body { color: #eeeef3; background: #06060b; } code { background: #14141f; } a { color: #7c5bf5; } }
  h1 { font-size: 16px; }
  code, a { color: #6d4fe0; }
  code { padding: 1px 5px; border-radius: 4px; background: #eef1f6; font-family: ui-monospace, Menlo, monospace; font-size: 12px; }
</style>
</head>
<body>
<h1>kedge quickstart provider</h1>
<p>This binary is a kedge <em>provider</em>. It's designed to mount inside
the kedge portal as a custom element (<code>&lt;kedge-provider-quickstart&gt;</code>),
loaded from <code>/main.js</code>. You're seeing this fallback page because
you opened the provider URL directly.</p>
<p>Visit the portal at <a href="/ui/providers">/ui/providers</a> to see it
mounted natively, or fetch <a href="/main.js">/main.js</a> to inspect the
custom-element bundle.</p>
</body>
</html>
`

// mainJS is the entry point the kedge portal loads at runtime via
// <script src="/ui/providers/quickstart/main.js">. It defines the
// <kedge-provider-quickstart> custom element. Rendering happens in
// **light DOM** (no Shadow DOM) so the portal's :root CSS variables
// cascade in unchanged — palette parity for free.
//
// Provider contract:
//   - portal calls customElements.define BEFORE this script's side-effect
//     (we do it in the script itself).
//   - portal sets `element.kedgeContext = { token, user, tenant, theme,
//     basePath, tokens }` as a property after mount; setter triggers render.
//   - portal listens for `kedge-navigate` CustomEvents bubbled from the
//     element to push browser history.
//
// No framework dependency; plain JS keeps the bundle ~3KB.
const mainJS = `(() => {
  const TAG = 'kedge-provider-quickstart';
  if (customElements.get(TAG)) return; // hot-reload safe

  const CSS = ` + "`" + `
    :host, ${TAG} { display: block; }
    ${TAG} .grid {
      display: grid; gap: 12px;
      grid-template-columns: 1fr;
    }
    @media (min-width: 720px) { ${TAG} .grid { grid-template-columns: 1fr 1fr; } }
    ${TAG} .panel {
      background: var(--color-surface-raised, #0d0d14);
      border: 1px solid var(--color-border-subtle, rgba(255,255,255,0.04));
      border-radius: 12px;
      padding: 14px 16px;
      color: var(--color-text-primary, inherit);
    }
    ${TAG} .panel-head {
      display: flex; align-items: center; justify-content: space-between;
      gap: 8px; margin-bottom: 10px;
    }
    ${TAG} .panel-title {
      margin: 0; font-size: 11px; font-weight: 600; letter-spacing: 0.04em;
      color: var(--color-text-secondary, currentColor); text-transform: uppercase;
    }
    ${TAG} .badge {
      display: inline-flex; align-items: center; gap: 4px;
      font-size: 9px; font-weight: 600; letter-spacing: 0.08em;
      text-transform: uppercase;
      padding: 2px 8px; border-radius: 999px;
      border: 1px solid var(--color-border-default, transparent);
      background: var(--color-surface-overlay, transparent);
      color: var(--color-text-muted, inherit);
    }
    ${TAG} .badge.ok   { border-color: var(--color-success); color: var(--color-success); background: var(--color-success-subtle); }
    ${TAG} .badge.warn { border-color: var(--color-warning); color: var(--color-warning); background: transparent; }
    ${TAG} pre {
      margin: 0;
      background: var(--color-surface-overlay, rgba(0,0,0,0.04));
      color: var(--color-text-primary, inherit);
      border: 1px solid var(--color-border-subtle, transparent);
      border-radius: 8px;
      padding: 10px 12px;
      font-family: ui-monospace, "JetBrains Mono", "SF Mono", Menlo, monospace;
      font-size: 11px;
      overflow-x: auto;
    }
    ${TAG} code {
      background: var(--color-surface-overlay, rgba(0,0,0,0.04));
      color: var(--color-text-secondary, currentColor);
      padding: 1px 5px; border-radius: 4px;
      font-family: ui-monospace, "JetBrains Mono", "SF Mono", Menlo, monospace;
      font-size: 11px;
    }
    ${TAG} .meta { margin: 0 0 8px; font-size: 11px; color: var(--color-text-muted, inherit); }
    ${TAG} .muted { color: var(--color-text-muted, inherit); }
  ` + "`" + `;

  // Stylesheet attached once to the document. Light DOM means we share the
  // host page's stylesheet; namespacing every selector under our tag keeps
  // us from leaking into the portal.
  const styleId = 'kedge-provider-quickstart-css';
  if (!document.getElementById(styleId)) {
    const s = document.createElement('style');
    s.id = styleId;
    s.textContent = CSS;
    document.head.appendChild(s);
  }

  class Element extends HTMLElement {
    constructor() {
      super();
      this._ctx = null;
      this._calledAPI = false;
    }

    // Portal sets this property after appending; setter triggers render.
    set kedgeContext(v) { this._ctx = v; this._render(); }
    get kedgeContext() { return this._ctx; }

    connectedCallback() { this._render(); this._callAPI(); }

    _render() {
      const ctx = this._ctx;
      this.innerHTML = ` + "`" + `
        <div class="grid">
          <div class="panel">
            <div class="panel-head">
              <h2 class="panel-title">Portal handshake</h2>
              <span class="badge ${ctx ? 'ok' : 'warn'}">${ctx ? 'Connected' : 'Waiting'}</span>
            </div>
            <p class="meta">Context delivered by the shell as a JS property — no postMessage shuttle.</p>
            <pre class="${ctx ? '' : 'muted'}" id="ctx-dump">${ctx ? JSON.stringify({ user: ctx.user, tenant: ctx.tenant, theme: ctx.theme, basePath: ctx.basePath }, null, 2) : '(no context yet)'}</pre>
          </div>

          <div class="panel">
            <div class="panel-head">
              <h2 class="panel-title">Backend proxy</h2>
              <span class="badge warn" id="api-status">${this._apiState ? this._apiState.label : 'Calling…'}</span>
            </div>
            <p class="meta">GET <code id="api-url">${this._apiURL()}</code></p>
            <pre class="${this._apiState ? '' : 'muted'}" id="api-dump">${this._apiState ? this._apiState.dump : '(no response yet)'}</pre>
          </div>
        </div>
      ` + "`" + `;
      // Restore status badge classes if the api call resolved earlier
      if (this._apiState) {
        const s = this.querySelector('#api-status');
        if (s) s.className = 'badge ' + this._apiState.cls;
      }
    }

    _apiURL() {
      const base = (this._ctx?.basePath || '').replace(/^\/ui\/providers\//, '/services/providers/');
      return base + '/api/hello';
    }

    _callAPI() {
      if (this._calledAPI) return;
      this._calledAPI = true;
      // Defer until we have a basePath so the constructed URL is correct.
      const tryFetch = () => {
        if (!this._ctx?.basePath) { setTimeout(tryFetch, 50); return; }
        const url = this._apiURL();
        fetch(url, { credentials: 'same-origin' })
          .then(r => r.json().then(j => ({ ok: r.ok, code: r.status, j })))
          .then(({ ok, code, j }) => {
            this._apiState = ok
              ? { cls: 'ok',   label: code + ' OK', dump: JSON.stringify(j, null, 2) }
              : { cls: 'warn', label: code + ' ' + (j.reason || 'Error'), dump: JSON.stringify(j, null, 2) };
            this._render();
          })
          .catch(e => {
            this._apiState = { cls: 'warn', label: 'Error', dump: 'fetch error: ' + e.message };
            this._render();
          });
      };
      tryFetch();
    }
  }

  customElements.define(TAG, Element);
})();
`

// iconSVG is a small lucide-style line icon for the provider's tile in the
// kedge side-nav. Matches the portal's stroke conventions: 24×24 viewBox,
// 2px stroke, rounded joins/caps. The stroke is the kedge accent purple
// (--color-accent in main.css) because the portal renders icons inside an
// <img> tag which does NOT inherit text color from its parent — using
// currentColor would render invisibly against the dark surface.
const iconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none"
  stroke="#7c5bf5" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
  <path d="M3 7l9-4 9 4-9 4-9-4z"/>
  <path d="M3 12l9 4 9-4"/>
  <path d="M3 17l9 4 9-4"/>
</svg>
`

type helloResponse struct {
	Message     string    `json:"message"`
	Provider    string    `json:"provider"`
	ServedAt    time.Time `json:"servedAt"`
	UserHeader  string    `json:"userHeader,omitempty"`
	TokenLength int       `json:"tokenLength,omitempty"`
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	mux := http.NewServeMux()

	// Health: gates Ready=true in the hub when wired via spec.backend.healthPath.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Icon used by the kedge portal's side-nav tile (referenced by
	// CatalogEntry.spec.iconURL = "/ui/providers/quickstart/icon.svg").
	// Must be registered BEFORE the "/" catch-all that serves indexHTML —
	// otherwise the catch-all would return the HTML page for every request
	// and the portal's <img> would silently fail to render an icon.
	mux.HandleFunc("/icon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte(iconSVG))
	})

	// /main.js is the custom-element entry point the kedge portal loads at
	// runtime. Defines <kedge-provider-quickstart> in the global registry.
	// Short cache to avoid serving stale bundles during dev; cache-bust
	// is also driven by ?v= from the portal.
	mux.HandleFunc("/main.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(mainJS))
	})

	// Sample backend API. Echoes which user header arrived (proves the
	// hub forwarded Authorization) and how long the token was (without
	// echoing the token itself).
	mux.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := helloResponse{
			Message:    "hello from the quickstart provider",
			Provider:   "quickstart",
			ServedAt:   time.Now().UTC(),
			UserHeader: r.Header.Get("X-Kedge-User"),
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			resp.TokenLength = len(auth)
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// UI: anything not matched above falls through to the static index page.
	// The portal mounts this at /ui/providers/quickstart/ so we don't care
	// about the request path — any GET serves the same HTML for now.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(indexHTML))
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           logMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("quickstart provider listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	// Heartbeat goroutine — POSTs to the hub every 30s so the catalog
	// controller's TTL doesn't flip us to NotReady. Configurable via env:
	//   KEDGE_HUB_URL   - base URL of the hub (e.g. http://localhost:19443)
	//   KEDGE_HUB_TOKEN - bearer token for the heartbeat request
	//   KEDGE_PROVIDER_NAME - this provider's CatalogEntry name (default: quickstart)
	// All empty → heartbeats disabled (useful for tests / dry-run).
	go runHeartbeat(ctx)

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
		_ = fmt.Sprintf
	})
}

const (
	heartbeatVersion  = "0.1.0" // align with manifest.yaml spec.version
	heartbeatInterval = 30 * time.Second
)

// runHeartbeat POSTs to /api/providers/{name}/heartbeat every 30s. Skips
// silently when KEDGE_HUB_URL is empty so test invocations don't need a hub.
// Logs errors but keeps trying — losing a beat just means the hub flips us
// to NotReady until the next successful POST.
//
// Env:
//
//	KEDGE_HUB_URL        - hub base URL (https://localhost:9443 in dev)
//	KEDGE_HUB_TOKEN      - bearer token for the heartbeat request
//	KEDGE_PROVIDER_NAME  - this provider's CatalogEntry name (default: quickstart)
//	KEDGE_HUB_INSECURE   - "true" → skip TLS verification (dev with self-signed certs)
func runHeartbeat(ctx context.Context) {
	hub := os.Getenv("KEDGE_HUB_URL")
	token := os.Getenv("KEDGE_HUB_TOKEN")
	name := os.Getenv("KEDGE_PROVIDER_NAME")
	if name == "" {
		name = "quickstart"
	}
	if hub == "" {
		log.Printf("heartbeat disabled (set KEDGE_HUB_URL to enable)")
		return
	}
	url := hub + "/api/providers/" + name + "/heartbeat"
	body, _ := json.Marshal(map[string]string{"version": heartbeatVersion, "status": "healthy"})

	client := &http.Client{Timeout: 5 * time.Second}
	if os.Getenv("KEDGE_HUB_INSECURE") == "true" {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev-only; opt-in via KEDGE_HUB_INSECURE
		}
	}

	// First beat immediately so the hub sees us as healthy as soon as the
	// CatalogEntry exists; subsequent beats on the ticker.
	send := func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			log.Printf("heartbeat build req: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("heartbeat send: %v", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			log.Printf("heartbeat %s: %d %s", url, resp.StatusCode, resp.Status)
		}
	}
	send()

	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			send()
		}
	}
}
