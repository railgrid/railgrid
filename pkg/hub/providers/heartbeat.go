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

package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// PathProviderHeartbeat is the prefix for the heartbeat endpoint. The handler
// extracts the provider name from the path: POST /api/providers/{name}/heartbeat
const PathProviderHeartbeat = "/api/providers"

// heartbeatRequest is the body the provider pod POSTs. All fields optional;
// the hub only needs to know the request came in, with optional metadata.
type heartbeatRequest struct {
	Version string `json:"version,omitempty"`
	Status  string `json:"status,omitempty"`
}

// NewHeartbeatHandler returns an http.Handler serving
// POST /api/providers/{name}/heartbeat. Auth is enforced by the kedge auth
// middleware mounted upstream of this handler — any bearer token kedge
// accepts will be accepted as a valid heartbeat sender in Phase 1C. Phase
// 1D will tighten this to "must be the provider's own SA token" once SA
// minting is in place.
func NewHeartbeatHandler(reg *Registry, log logr.Logger) http.Handler {
	logger := log.WithName("heartbeat")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name, ok := parseHeartbeatPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		var body heartbeatRequest
		if r.ContentLength > 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
				http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		if !reg.Heartbeat(name, body.Version, time.Now()) {
			http.Error(w, "provider not found: "+name, http.StatusNotFound)
			return
		}
		logger.V(2).Info("heartbeat received", "provider", name, "version", body.Version)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

// parseHeartbeatPath extracts the provider name from
// /api/providers/{name}/heartbeat. Returns ("", false) on mismatch.
func parseHeartbeatPath(p string) (string, bool) {
	const prefix = PathProviderHeartbeat + "/"
	if !strings.HasPrefix(p, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(p, prefix)
	const suffix = "/heartbeat"
	if !strings.HasSuffix(rest, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(rest, suffix)
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

// RunSweeper periodically marks providers stale when their last heartbeat
// is older than HeartbeatTTL. Designed to run as a single goroutine for the
// lifetime of the hub process. Returns when ctx is done.
func RunSweeper(ctx context.Context, reg *Registry, log logr.Logger) {
	logger := log.WithName("heartbeat-sweeper")
	logger.Info("starting", "interval", SweepInterval, "ttl", HeartbeatTTL)
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("stopping")
			return
		case now := <-ticker.C:
			if n := reg.SweepStale(now, HeartbeatTTL); n > 0 {
				logger.V(2).Info("marked providers stale", "count", n)
			}
		}
	}
}
