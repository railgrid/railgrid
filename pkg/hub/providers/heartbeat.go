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

// HeartbeatRecorder persists a heartbeat to the provider's CatalogEntry status.
//
// A heartbeat reaches exactly one hub replica — whichever the load balancer
// picked — but every replica routes provider traffic and therefore needs the
// liveness signal. Writing it to CatalogEntry.status is what fans it back out:
// each replica's catalog watch delivers the update and refreshes its own
// registry. Without this, non-receiving replicas would mark a perfectly healthy
// provider stale after HeartbeatTTL and start refusing to proxy to it.
type HeartbeatRecorder func(ctx context.Context, name, version string, at time.Time) error

// heartbeatPersistThreshold suppresses a status write when the recorded
// timestamp is already this fresh. It bounds API churn from a provider that
// heartbeats far more often than intended, while staying well under
// HeartbeatTTL so a normally-paced provider persists every beat.
const heartbeatPersistThreshold = HeartbeatTTL / 6

// NewHeartbeatHandler returns an http.Handler serving
// POST /api/providers/{name}/heartbeat. Auth is enforced by the faros auth
// middleware mounted upstream of this handler — any bearer token faros
// accepts will be accepted as a valid heartbeat sender in Phase 1C. Phase
// 1D will tighten this to "must be the provider's own SA token" once SA
// minting is in place.
//
// record may be nil (no kcp configured), in which case liveness stays local to
// this process and the hub must not be scaled beyond one replica.
func NewHeartbeatHandler(reg *Registry, record HeartbeatRecorder, log logr.Logger) http.Handler {
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
		now := time.Now()
		// Apply locally first so this replica routes to the provider without
		// waiting for the watch to loop the status write back around.
		previous, known := reg.Get(name)
		if !reg.Heartbeat(name, body.Version, now) {
			http.Error(w, "provider not found: "+name, http.StatusNotFound)
			return
		}
		if record != nil && (!known || now.Sub(previous.LastHeartbeat) >= heartbeatPersistThreshold) {
			if err := record(r.Context(), name, body.Version, now); err != nil {
				// Fail the beat rather than silently leaving other replicas to
				// time the provider out; the provider retries on its next tick.
				logger.Error(err, "persisting heartbeat failed", "provider", name)
				http.Error(w, "recording heartbeat failed", http.StatusInternalServerError)
				return
			}
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
// is older than HeartbeatTTL. It derives staleness purely from the timestamps
// already in the registry, so every hub replica runs its own copy and they all
// reach the same verdict — this is deliberately NOT leader-gated. Returns when
// ctx is done.
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
