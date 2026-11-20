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

// Package telemetry relays the portal's product telemetry (which page a user
// opened, which named control they clicked) to an analytics backend the
// operator runs.
//
// It is off unless the operator configures it. A hub without --telemetry-url
// registers no route, tells the portal nothing is enabled, and makes no
// outbound call; see docs/telemetry.md for what is sent when it is on.
//
// The portal cannot post to the backend itself: its Content-Security-Policy
// is connect-src 'self'. It posts neutral events to the hub, and the hub is
// the only place that knows which backend receives them, so a backend change
// touches Sink and nothing in the portal or the providers.
package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"k8s.io/klog/v2"
)

const (
	// maxBodyBytes bounds one POST. A batch is a handful of short events.
	maxBodyBytes = 16 << 10
	// maxBatch is the most events one POST may carry.
	maxBatch = 20
	// queueSize is how many events wait for delivery. Telemetry is best
	// effort: when the backend is slow or down, new events are dropped, never
	// queued without bound and never allowed to slow a request.
	queueSize = 512

	maxProps      = 10
	maxPropValue  = 100
	maxRouteBytes = 200
)

// Config is the operator's telemetry configuration. The zero value is "off".
type Config struct {
	// URL is the analytics backend's base URL, e.g. https://telemetry.example.com.
	URL string
	// WebsiteID is the backend's identifier for the property events are
	// recorded under. Using the same one as the marketing site lets the
	// backend follow a visitor from the site into the console.
	WebsiteID string
	// Hostname is recorded as the host the events happened on: the hub's
	// external hostname.
	Hostname string
}

// Enabled reports whether the operator turned telemetry on.
func (c Config) Enabled() bool { return c.URL != "" }

// Validate rejects a half-configured relay, so a typo cannot silently drop
// every event or point the hub at something that is not an http endpoint.
func (c Config) Validate() error {
	if c.URL == "" {
		if c.WebsiteID != "" {
			return errors.New("--telemetry-website-id is set without --telemetry-url")
		}
		return nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("--telemetry-url %q must be an http(s) URL", c.URL)
	}
	if c.WebsiteID == "" {
		return errors.New("--telemetry-url is set without --telemetry-website-id")
	}
	return nil
}

// Event is one thing a user did in the portal, in the hub's own vocabulary.
// It deliberately has no field for a raw URL, a page title or free text: the
// portal's URLs carry organization and workspace IDs, and titles and labels
// can carry tenant data.
type Event struct {
	// Type is "page" (a route was opened) or "event" (a named interaction).
	Type string `json:"type"`
	// Name is the route name for a page, the event name otherwise.
	Name string `json:"name"`
	// Route is the route pattern the user is on, parameters left as
	// placeholders: /:orgID/:workspaceID/providers/agents.
	Route string `json:"route"`
	// Props are short, bounded labels (where a button sits, which provider).
	Props map[string]string `json:"props,omitempty"`
	// Referrer is the origin the user arrived from, on the first page only.
	Referrer string `json:"referrer,omitempty"`
	Screen   string `json:"screen,omitempty"`
	Language string `json:"language,omitempty"`
}

// Client is what the hub knows about the browser that sent a batch. The
// backend derives its anonymous session from these, which is also what joins a
// console session to the same visitor's session on the marketing site.
type Client struct {
	IP        string
	UserAgent string
}

// Sink delivers events to one analytics backend.
type Sink interface {
	Send(ctx context.Context, client Client, event Event) error
}

var (
	nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	// A route pattern: path segments of names, :params and the few
	// characters vue-router patterns use. No query, no fragment.
	routeRE    = regexp.MustCompile(`^/[A-Za-z0-9/_:.\-]*$`)
	propKeyRE  = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]{0,31}$`)
	screenRE   = regexp.MustCompile(`^[0-9]{1,5}x[0-9]{1,5}$`)
	languageRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,35}$`)
)

// sanitize returns the event with everything outside the contract removed, or
// false when the event itself is not acceptable. Optional fields that fail
// are dropped rather than failing the event.
func sanitize(e Event) (Event, bool) {
	if e.Type != "page" && e.Type != "event" {
		return Event{}, false
	}
	if !nameRE.MatchString(e.Name) {
		return Event{}, false
	}
	if len(e.Route) > maxRouteBytes || !routeRE.MatchString(e.Route) {
		return Event{}, false
	}
	out := Event{Type: e.Type, Name: e.Name, Route: e.Route}
	for k, v := range e.Props {
		if len(out.Props) == maxProps {
			break
		}
		if !propKeyRE.MatchString(k) || v == "" || len(v) > maxPropValue || strings.ContainsAny(v, "\r\n\t") {
			continue
		}
		if out.Props == nil {
			out.Props = map[string]string{}
		}
		out.Props[k] = v
	}
	// Only where the user came from, never the page they were on.
	if u, err := url.Parse(e.Referrer); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && len(u.Host) <= 253 {
		out.Referrer = u.Scheme + "://" + u.Host
	}
	if screenRE.MatchString(e.Screen) {
		out.Screen = e.Screen
	}
	if languageRE.MatchString(e.Language) {
		out.Language = e.Language
	}
	return out, true
}

type queued struct {
	client Client
	event  Event
}

// Relay accepts event batches from the portal and delivers them to the sink
// from one background worker.
type Relay struct {
	sink     Sink
	clientIP func(*http.Request) string
	queue    chan queued
}

// NewRelay returns a relay delivering to sink. clientIP resolves the browser's
// address the same way the hub's rate limiters do (--trusted-proxy-cidrs).
func NewRelay(sink Sink, clientIP func(*http.Request) string) *Relay {
	return &Relay{sink: sink, clientIP: clientIP, queue: make(chan queued, queueSize)}
}

// Run delivers queued events until ctx is cancelled. Events still queued at
// shutdown are dropped.
func (r *Relay) Run(ctx context.Context) {
	logger := klog.FromContext(ctx).WithName("telemetry")
	for {
		select {
		case <-ctx.Done():
			return
		case q := <-r.queue:
			if err := r.sink.Send(ctx, q.client, q.event); err != nil && ctx.Err() == nil {
				// V(2): a backend outage would otherwise log once per click.
				logger.V(2).Info("Dropping telemetry event", "err", err.Error())
			}
		}
	}
}

// Recorder records an outcome the hub itself confirmed (an organization was
// created, a provider was enabled). It is what the hub's own handlers hold;
// a nil *Relay is a valid Recorder that records nothing, so a handler never
// has to ask whether telemetry is on.
type Recorder interface {
	Record(req *http.Request, name string, props map[string]string)
}

// serverRoute is the route recorded for a hub-side outcome. The request path
// carries organization and workspace IDs, so it is never used.
const serverRoute = "/hub"

// Record queues a hub-side outcome under the caller's browser, so it lands in
// the same backend session as the clicks that led to it. It is authoritative
// where a browser event is not: it also fires for the CLI and the API, and
// only when the operation succeeded. Labels are fixed strings chosen in the
// handler, never tenant data.
func (r *Relay) Record(req *http.Request, name string, props map[string]string) {
	if r == nil {
		return
	}
	event, ok := sanitize(Event{Type: "event", Name: name, Route: serverRoute, Props: props})
	if !ok {
		return
	}
	select {
	case r.queue <- queued{client: Client{IP: r.clientIP(req), UserAgent: req.UserAgent()}, event: event}:
	default: // full: drop
	}
}

// What a browser may report before anyone has signed in. The sign-in page is
// the one step of the funnel the authenticated route cannot see, so exactly
// that is accepted here and nothing else: this route takes no credentials, and
// an open one would let anyone write arbitrary events into the backend.
var (
	publicPages  = map[string]string{"login": "/login", "auth-callback": "/auth/callback"}
	publicEvents = map[string]bool{"sign_in": true}
	// sign_in's one label: which button.
	publicProps = map[string]map[string]bool{"method": {"oidc": true, "token": true}}
)

const (
	maxPublicBodyBytes = 2 << 10
	maxPublicBatch     = 4
)

// publicEvent narrows a sanitized event to the pre-sign-in contract.
func publicEvent(e Event) (Event, bool) {
	switch e.Type {
	case "page":
		if route, ok := publicPages[e.Name]; !ok || e.Route != route {
			return Event{}, false
		}
		e.Props = nil
	case "event":
		if !publicEvents[e.Name] || e.Route != publicPages["login"] {
			return Event{}, false
		}
		for k, v := range e.Props {
			if !publicProps[k][v] {
				delete(e.Props, k)
			}
		}
	}
	return e, true
}

// PublicHandler handles POST /api/telemetry/public: the sign-in page's own
// page views and its sign-in click, from a browser with no session. allow is
// the per-address limiter; a caller over its budget gets 204 like everyone
// else, because the portal never acts on the answer.
func (r *Relay) PublicHandler(allow func(clientIP string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ip := r.clientIP(req)
		if !allow(ip) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var body struct {
			Events []Event `json:"events"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxPublicBodyBytes)).Decode(&body); err != nil {
			http.Error(w, "invalid telemetry batch", http.StatusBadRequest)
			return
		}
		if len(body.Events) > maxPublicBatch {
			http.Error(w, "telemetry batch too large", http.StatusRequestEntityTooLarge)
			return
		}
		client := Client{IP: ip, UserAgent: req.UserAgent()}
		for _, e := range body.Events {
			event, ok := sanitize(e)
			if !ok {
				continue
			}
			if event, ok = publicEvent(event); !ok {
				continue
			}
			select {
			case r.queue <- queued{client: client, event: event}:
			default: // full: drop
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// ServeHTTP handles POST /api/telemetry/events. It answers 204 as soon as the
// batch is queued: the portal never waits on, or learns about, the backend.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Events []Event `json:"events"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid telemetry batch", http.StatusBadRequest)
		return
	}
	if len(body.Events) > maxBatch {
		http.Error(w, "telemetry batch too large", http.StatusRequestEntityTooLarge)
		return
	}
	client := Client{IP: r.clientIP(req), UserAgent: req.UserAgent()}
	for _, e := range body.Events {
		event, ok := sanitize(e)
		if !ok {
			continue
		}
		select {
		case r.queue <- queued{client: client, event: event}:
		default: // full: drop
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
