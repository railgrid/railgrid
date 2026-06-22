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
// "application" 3-tier demo template. It is the only tier exposed (through the
// Ingress, and — unless oidc.mode=none — the oauth2-proxy gate). It never talks
// to Postgres directly: it calls the internal backend over cluster DNS via
// BACKEND_URL and renders a tiny guestbook so the whole frontend → backend →
// database path is visibly exercised in one click.
//
//	GET  /         render the guestbook (messages fetched from the backend)
//	POST /         submit a message (proxied to the backend), then redirect
//	GET  /healthz  liveness/readiness
//
// Standard library only — keeps the image tiny and the build fast. Demo
// workload, not production code.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"
)

type message struct {
	ID        int       `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

type server struct {
	backend string
	client  *http.Client
	tmpl    *template.Template
}

func main() {
	port := getenv("PORT", "8080")

	backend := os.Getenv("BACKEND_URL")
	if backend == "" {
		log.Fatal("BACKEND_URL is required (the template injects the in-cluster backend URL)")
	}

	s := &server{
		backend: backend,
		client:  &http.Client{Timeout: 5 * time.Second},
		tmpl:    template.Must(template.New("page").Parse(pageHTML)),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/", s.handle)

	log.Printf("frontend listening on :%s (backend=%s)", port, backend)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

func (s *server) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.render(w, r)
	case http.MethodPost:
		s.submit(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// render fetches the guestbook from the backend and renders the page. A backend
// error is shown inline rather than failing the request, so the demo still
// loads (and visibly reports the wiring problem) while tiers are coming up.
func (s *server) render(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Messages []message
		Backend  string
		Err      string
	}{Backend: s.backend}

	msgs, err := s.fetchMessages(r.Context())
	if err != nil {
		data.Err = err.Error()
	} else {
		data.Messages = msgs
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.Execute(w, data); err != nil {
		log.Printf("render: %v", err)
	}
}

func (s *server) submit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	text := r.FormValue("text")
	if text != "" {
		if err := s.postMessage(r.Context(), text); err != nil {
			log.Printf("submit: %v", err)
			http.Error(w, "backend unavailable", http.StatusBadGateway)
			return
		}
	}
	// Post/Redirect/Get so a refresh doesn't resubmit.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) fetchMessages(ctx context.Context) ([]message, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.backend+"/api/messages", nil)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching backend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("backend returned %s", resp.Status)
	}
	var msgs []message
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, fmt.Errorf("decoding backend response: %w", err)
	}
	return msgs, nil
}

func (s *server) postMessage(ctx context.Context, text string) error {
	body, _ := json.Marshal(map[string]string{"text": text})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.backend+"/api/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("reaching backend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("backend returned %s", resp.Status)
	}
	return nil
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
  <p class="sub">frontend → backend → postgres. This page is the frontend; the list comes from the backend, which reads it from Postgres.</p>

  <form method="post" action="/">
    <input type="text" name="text" placeholder="Leave a message…" autofocus maxlength="200">
    <button type="submit">Add</button>
  </form>

  {{if .Err}}
    <p class="err">Backend unavailable: {{.Err}}<br><small>backend = {{.Backend}}</small></p>
  {{else if not .Messages}}
    <p class="empty">No messages yet — add the first one above.</p>
  {{else}}
    <ul>
      {{range .Messages}}
        <li>{{.Text}}<time>{{.CreatedAt.Format "2006-01-02 15:04:05 MST"}}</time></li>
      {{end}}
    </ul>
  {{end}}
</body>
</html>
`
