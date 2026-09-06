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
	"fmt"
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
// POST /api/providers/{name}/heartbeat.
//
// The handler is mounted on the root router with no auth middleware in front
// of it, so it authenticates the beat itself: authenticate must accept the
// request as coming from provider {name}'s own service account (the one the
// hub minted its kubeconfig for) before the beat counts. Without that check
// anyone able to reach the hub could keep a dead provider Ready and forge its
// reportedVersion. In HeartbeatAuthWarn mode a failed check is logged and the
// beat is still recorded; in HeartbeatAuthEnforce it is rejected with 401
// (no or unrecognised token), 403 (some other identity) or 503 (could not
// verify). A nil authenticate means the hub cannot verify anything (no kcp)
// and behaves like a check that always fails with ErrHeartbeatAuthUnavailable.
//
// A rejection says only which of those three classes it was: the reason goes to
// the log, never to the caller. In enforce mode a beat for a name that is not
// registered is refused with exactly the 401 a bad credential gets, so the
// endpoint cannot be used to enumerate provider names.
//
// record may be nil (no kcp configured), in which case liveness stays local to
// this process and the hub must not be scaled beyond one replica.
func NewHeartbeatHandler(reg *Registry, record HeartbeatRecorder, authenticate HeartbeatAuthenticator, mode HeartbeatAuthMode, log logr.Logger) http.Handler {
	logger := log.WithName("heartbeat")
	if authenticate == nil {
		authenticate = func(context.Context, *http.Request, string) error {
			return fmt.Errorf("%w: no kcp configured", ErrHeartbeatAuthUnavailable)
		}
	}
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
		// The registry lookup comes before authentication on purpose. Verifying
		// a beat for a name the hub has never heard of costs a TokenReview round
		// trip against kcp and, in warn mode, one log line per beat — work an
		// unauthenticated caller can drive at will.
		//
		// Note that this is a different lookup from the authenticator's
		// CatalogEntryCluster: a provider that is registered but whose
		// CatalogEntry has not been observed yet is known here and unresolvable
		// there. That transient startup state must stay a 503 the provider
		// retries, so only a name absent from the registry short-circuits.
		previous, known := reg.Get(name)
		if !known {
			if mode == HeartbeatAuthEnforce {
				// Deliberately indistinguishable from a bad credential: same
				// status, same body. This endpoint has no auth middleware in
				// front of it, so answering 404 here — or letting the
				// authenticator answer 503 because it cannot resolve a cluster
				// for an unknown name — would let anyone who can reach the hub
				// enumerate provider names by status code. The real reason is
				// logged.
				logger.V(1).Info("heartbeat rejected", "provider", name, "reason", "provider is not registered")
				http.Error(w, heartbeatAuthFailureBody(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
			// Warn mode accepts unauthenticated beats by design, so a caller can
			// tell known from unknown regardless of what this returns; there is
			// no oracle left to close and 404 is the honest answer.
			logger.V(1).Info("heartbeat for unknown provider", "provider", name)
			http.Error(w, "provider not found", http.StatusNotFound)
			return
		}
		if err := authenticate(r.Context(), r, name); err != nil {
			if mode == HeartbeatAuthEnforce {
				logger.V(1).Info("heartbeat rejected", "provider", name, "reason", err.Error())
				// The body says only which class of failure it was. The
				// authenticator's messages carry logical cluster IDs and the
				// expected service account username, and this endpoint answers
				// unauthenticated callers.
				status := heartbeatAuthStatus(err)
				http.Error(w, heartbeatAuthFailureBody(status), status)
				return
			}
			// Warn mode exists so an operator can see which providers will
			// break when enforce becomes the default, so a rejected identity
			// is worth a line per beat. "Cannot verify" is not: it says
			// nothing about this provider, repeats for every provider on
			// every tick, and both ways it happens are already reported
			// elsewhere — a hub with no kcp says so once at startup, and a
			// hub whose kcp is unreachable fails the heartbeat recorder,
			// which logs at error level.
			warn := logger.V(0)
			if heartbeatAuthStatus(err) == http.StatusServiceUnavailable {
				warn = logger.V(1)
			}
			warn.Info("heartbeat failed authentication; accepting because --provider-heartbeat-auth=warn (enforce becomes the default next release)",
				"provider", name, "reason", err.Error())
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
		// waiting for the watch to loop the status write back around. The
		// provider was in the registry above, so this only fails if it was
		// deleted in between.
		if !reg.Heartbeat(name, body.Version, now) {
			http.Error(w, "provider not found", http.StatusNotFound)
			return
		}
		// previous.LastHeartbeat is zero for a provider that has never beaten,
		// which is far enough in the past to persist.
		if record != nil && now.Sub(previous.LastHeartbeat) >= heartbeatPersistThreshold {
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
