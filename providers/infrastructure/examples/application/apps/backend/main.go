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

// Command backend is the internal API tier of the infrastructure provider's
// "application" 3-tier demo template. It is never exposed publicly — only the
// frontend (behind the optional OIDC gate) reaches it over cluster DNS.
//
// It speaks to Postgres via DATABASE_URL (the full postgres:// connection
// string the template's credentials Job mints) and serves a tiny guestbook API:
//
//	GET  /healthz       liveness/readiness
//	GET  /api/messages  list guestbook messages (newest first), JSON
//	POST /api/messages  append a message ({"text": "..."}), JSON
//
// It is intentionally dependency-light (one pure-Go pg driver) so the image
// builds fast and small. Not production code — a demo workload.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
)

type message struct {
	ID        int       `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

type server struct {
	db *sql.DB
}

func main() {
	port := getenv("PORT", "8080")

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required (the template injects it from the db credentials Secret)")
	}

	db, err := openDB(dsn)
	if err != nil {
		log.Fatalf("connect to postgres: %v", err)
	}
	defer db.Close()

	s := &server{db: db}
	if err := s.initSchema(); err != nil {
		log.Fatalf("init schema: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/api/messages", s.messages)

	log.Printf("backend listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

// openDB dials Postgres, retrying for up to ~60s so the backend tolerates the
// database StatefulSet still coming up alongside it (no ordering guarantees in
// the RGD).
func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(5)

	var lastErr error
	for i := range 30 {
		if lastErr = db.Ping(); lastErr == nil {
			log.Printf("connected to postgres")
			return db, nil
		}
		log.Printf("waiting for postgres (%d/30): %v", i+1, lastErr)
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("postgres not reachable after retries: %w", lastErr)
}

func (s *server) initSchema() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id         SERIAL PRIMARY KEY,
			text       TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	return err
}

func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	// Report unhealthy if the DB connection has dropped so the readiness probe
	// pulls the pod from rotation until it recovers.
	if err := s.db.Ping(); err != nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

func (s *server) messages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listMessages(w, r)
	case http.MethodPost:
		s.addMessage(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) listMessages(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, text, created_at FROM messages ORDER BY id DESC LIMIT 100`)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	out := []message{}
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.ID, &m.Text, &m.CreatedAt); err != nil {
			httpError(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) addMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	if in.Text == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}

	var m message
	err := s.db.QueryRowContext(r.Context(),
		`INSERT INTO messages (text) VALUES ($1) RETURNING id, text, created_at`, in.Text).
		Scan(&m.ID, &m.Text, &m.CreatedAt)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, err error) {
	log.Printf("error: %v", err)
	http.Error(w, http.StatusText(code), code)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
