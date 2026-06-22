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

// Command frontend is the public-facing tier of the infrastructure provider's
// "application" 3-tier demo template. It serves a tiny single-page UI on "/"
// that talks to the backend from the browser at "/api/*" on the SAME origin —
// the exposure layer (Ingress for oidc.mode=none, oauth2-proxy otherwise)
// routes "/api/" to the backend and "/" here, so the frontend never proxies to
// the backend itself and needs no BACKEND_URL.
//
//	GET  /         the SPA (HTML + a little JS that calls /api/messages)
//	GET  /healthz  liveness/readiness
//
// Standard library only. Demo workload, not production code.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := getenv("PORT", "8080")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/", index)

	log.Printf("frontend listening on :%s (UI only; calls /api/* same-origin)", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

// index serves the SPA for "/". Anything that isn't the root is 404'd so a
// stray "/api/*" that somehow reached the frontend (it shouldn't — the exposure
// layer routes those to the backend) doesn't get the HTML page back.
func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(pageHTML))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

const pageHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>kedge · 3-tier demo</title>
  <style>
    :root { color-scheme: light dark; }
    body { font-family: system-ui, sans-serif; max-width: 40rem; margin: 3rem auto; padding: 0 1rem; line-height: 1.5; }
    h1 { font-size: 1.4rem; }
    .sub { color: #888; font-size: .9rem; margin-top: -.5rem; }
    form { display: flex; gap: .5rem; margin: 1.5rem 0; }
    input[type=text] { flex: 1; padding: .5rem .6rem; border: 1px solid #8884; border-radius: .4rem; }
    button { padding: .5rem 1rem; border: 0; border-radius: .4rem; background: #4f46e5; color: #fff; cursor: pointer; }
    ul { list-style: none; padding: 0; }
    li { padding: .6rem .8rem; border: 1px solid #8883; border-radius: .4rem; margin-bottom: .5rem; }
    li time { color: #888; font-size: .8rem; display: block; }
    .err { background: #f43f5e22; border: 1px solid #f43f5e88; padding: .6rem .8rem; border-radius: .4rem; }
    .empty { color: #888; }
  </style>
</head>
<body>
  <h1>kedge 3-tier demo</h1>
  <p class="sub">UI (served at <code>/</code>) → API (<code>/api</code>) → Postgres. The list below is fetched from the backend in your browser; the exposure layer routes <code>/api</code> to the backend service.</p>

  <form id="form">
    <input id="text" type="text" placeholder="Leave a message…" autofocus maxlength="200">
    <button type="submit">Add</button>
  </form>

  <div id="out"></div>

  <script>
    const out = document.getElementById('out');
    const form = document.getElementById('form');
    const input = document.getElementById('text');

    function esc(s) {
      return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
    }

    async function load() {
      try {
        const r = await fetch('/api/messages');
        if (!r.ok) throw new Error('backend returned ' + r.status);
        const msgs = await r.json();
        if (!msgs.length) { out.innerHTML = '<p class="empty">No messages yet — add the first one above.</p>'; return; }
        out.innerHTML = '<ul>' + msgs.map(m =>
          '<li>' + esc(m.text) + '<time>' + esc(m.createdAt) + '</time></li>'
        ).join('') + '</ul>';
      } catch (e) {
        out.innerHTML = '<p class="err">Backend unavailable: ' + esc(e.message) + '<br><small>GET /api/messages</small></p>';
      }
    }

    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const text = input.value.trim();
      if (!text) return;
      try {
        const r = await fetch('/api/messages', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ text }),
        });
        if (!r.ok) throw new Error('backend returned ' + r.status);
        input.value = '';
        load();
      } catch (e) {
        out.innerHTML = '<p class="err">Could not post: ' + esc(e.message) + '</p>';
      }
    });

    load();
  </script>
</body>
</html>
`
