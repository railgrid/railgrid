/*
Copyright 2026 The Railgrid Authors.

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

package tunnel

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"
)

// A browser cannot set an Authorization header on a WebSocket upgrade. The
// old answer was "?token=<the caller's kcp bearer>", which put a
// full-lifetime, workspace-wide credential into every access log, proxy log
// and Referer between the browser and this process.
//
// The answer now is a ticket:
//
//  1. The page POSTs to the ordinary, fully gated verb
//     /dataplane/clusters/{id}/{resource}/{name}/ticket with its bearer in
//     the Authorization header, like any other fetch. Both gates run: the
//     caller must be able to GET the object and must hold
//     "create" on {resource}/ticket for that name.
//  2. It gets back an opaque, single-use, TicketTTL-lived string that names
//     exactly that (cluster, resource, name) and nothing else.
//  3. It opens the WebSocket with that string as a Sec-WebSocket-Protocol
//     subprotocol — the one header a browser WebSocket CAN set — and the
//     upgrade handler redeems it.
//
// A leaked ticket is therefore worth one session on one object for at most
// TicketTTL, instead of the caller's whole identity for its whole lifetime.
//
// Tickets are held in memory. That is deliberate and it is not a Pillar 1
// violation: a ticket is not desired state and not durable status, it is a
// sixty-second capability. The cost of the in-memory store on N replicas is
// that mint and redeem must land on the same replica; both are issued by the
// same page within a second or two through the same hub connection, and a
// redeem that misses is a clean 401 the page retries by minting again.

const (
	// TicketTTL bounds a minted ticket. Long enough for a page to open the
	// socket it just asked for, short enough that a ticket in a log is
	// already dead by the time anyone reads it.
	TicketTTL = 60 * time.Second

	// ticketSubprotocolPrefix namespaces the subprotocol so a ticket is never
	// confused with a real WebSocket subprotocol the client also offers.
	ticketSubprotocolPrefix = "railgrid.ticket."

	// maxTickets bounds the store. A mint past the cap evicts expired entries
	// first, then refuses: an unbounded map keyed by a secret is a memory
	// leak with a credential-shaped key.
	maxTickets = 4096
)

// ticketScope is the exact coordinate a ticket authorizes, and nothing wider.
type ticketScope struct {
	ClusterID string
	Resource  string
	Name      string
}

type ticketEntry struct {
	scope   ticketScope
	user    string
	bearer  string
	expires time.Time
}

// ticketStore holds unredeemed tickets. Redeeming removes the entry, so a
// ticket is single-use.
type ticketStore struct {
	mu sync.Mutex
	m  map[string]ticketEntry
}

func newTicketStore() *ticketStore { return &ticketStore{m: map[string]ticketEntry{}} }

// mint returns a fresh ticket for scope, carrying the bearer that was gated
// for it. The bearer rides along because the verb handler behind the socket
// still acts as the caller (SSH credential reads, service token reads); the
// ticket is the transport, not a second identity.
func (s *ticketStore) mint(scope ticketScope, user, bearer string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("minting ticket: %w", err)
	}
	// URL-safe and unpadded so it is a legal Sec-WebSocket-Protocol token.
	ticket := base64.RawURLEncoding.EncodeToString(raw)

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked(now)
	if len(s.m) >= maxTickets {
		return "", fmt.Errorf("ticket store is full")
	}
	s.m[ticket] = ticketEntry{scope: scope, user: user, bearer: bearer, expires: now.Add(TicketTTL)}
	return ticket, nil
}

// redeem consumes ticket and reports the bearer it was minted for, but only
// when it names exactly this scope. A ticket for another object, another
// workspace or another resource is no ticket at all.
func (s *ticketStore) redeem(ticket string, scope ticketScope) (bearer string, ok bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, found := s.m[ticket]
	if !found {
		return "", false
	}
	delete(s.m, ticket)
	if now.After(entry.expires) || entry.scope != scope {
		return "", false
	}
	return entry.bearer, true
}

func (s *ticketStore) evictLocked(now time.Time) {
	for key, entry := range s.m {
		if now.After(entry.expires) {
			delete(s.m, key)
		}
	}
}

// ticketFromRequest returns the ticket a browser offered as a subprotocol, or
// "". Only the railgrid-namespaced value is considered.
func ticketFromRequest(r *http.Request) string {
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, offered := range strings.Split(header, ",") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(offered), ticketSubprotocolPrefix); ok && v != "" {
				return v
			}
		}
	}
	return ""
}

// serveTicket handles the "ticket" verb. The request is already gated, so
// reaching here means the caller may open this object's socket.
func (p *Server) serveTicket(w http.ResponseWriter, r *http.Request, token string, req dataplane.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, _, user, _ := dataplane.Identity(r)
	ticket, err := p.tickets.mint(ticketScope{ClusterID: req.ClusterID, Resource: req.Resource, Name: req.Name}, user, token)
	if err != nil {
		p.logger.Error(err, "minting websocket ticket", "cluster", req.ClusterID, "resource", req.Resource, "name", req.Name)
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ticket":      ticket,
		"subprotocol": ticketSubprotocolPrefix + ticket,
		"expiresIn":   int(TicketTTL / time.Second),
	})
}

// redeemTicket resolves the caller's bearer for a request that carried a
// ticket subprotocol instead of an Authorization header. It returns "" when
// there is no ticket, or the ticket does not name this exact object.
func (p *Server) redeemTicket(r *http.Request, req dataplane.Request) string {
	ticket := ticketFromRequest(r)
	if ticket == "" {
		return ""
	}
	bearer, ok := p.tickets.redeem(ticket, ticketScope{ClusterID: req.ClusterID, Resource: req.Resource, Name: req.Name})
	if !ok {
		return ""
	}
	return bearer
}

// acceptTicketSubprotocol echoes the ticket subprotocol back on the upgrade
// response. A browser aborts a WebSocket whose server does not select one of
// the subprotocols it offered, so this is not optional.
func acceptTicketSubprotocol(h http.Header, r *http.Request) {
	if ticket := ticketFromRequest(r); ticket != "" {
		h.Set("Sec-WebSocket-Protocol", ticketSubprotocolPrefix+ticket)
	}
}
