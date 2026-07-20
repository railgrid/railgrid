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

package events

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorilla/websocket"

	"github.com/faroshq/provider-edges/internal/haclient"
)

// wsPathUniFiEvents is the UniFi Protect Integration API events subscription,
// reached through the agent's /svc reverse proxy (which bridges the upgrade to
// the console, doing the TLS itself). The leading /svc prefix routes to the
// agent's service proxy; the remainder is the console-local path.
const wsPathUniFiEvents = "/svc/proxy/protect/integration/v1/subscribe/events"

// SubscriberConfig configures one long-lived event subscription. The dialer is
// resolved lazily on each (re)connect so an edge tunnel that flaps and
// re-registers is picked up without restarting the subscriber.
type SubscriberConfig struct {
	// Key is the tenant+service scope events are stored under.
	Key Key
	// ResolveDialer returns the current tunnel dialer for the Service's edge, or
	// (nil,false) when the edge isn't connected right now.
	ResolveDialer func() (haclient.Dialer, bool)
	// Target is the console address the agent dials (scheme/host/port).
	Target haclient.Target
	// Header carries the auth applied by the caller (svccatalog.Apply) — for
	// UniFi that is X-API-KEY. It is copied onto the WS handshake request.
	Header http.Header
	// Store receives decoded events.
	Store Store
	// Logger is optional.
	Logger logr.Logger
}

// runSubscriber holds the WS subscription open, decoding UniFi Protect event
// frames into the store, and reconnecting with backoff until ctx is cancelled.
// It returns only when ctx is done.
func runSubscriber(ctx context.Context, cfg SubscriberConfig) {
	const (
		minBackoff = 1 * time.Second
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		connected := cfg.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = minBackoff // a successful session resets the backoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// connectOnce opens one WS session and pumps frames until it errors or ctx is
// cancelled. It reports whether the session connected (used to reset backoff).
func (cfg SubscriberConfig) connectOnce(ctx context.Context) bool {
	dialer, ok := cfg.ResolveDialer()
	if !ok || dialer == nil {
		return false // edge not connected right now; caller backs off and retries
	}

	wsDialer := &websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		// Every connection rides a fresh tunnel dial to the agent; the agent
		// bridges the upgrade to the console (and does the TLS), so no client
		// TLS here. The addr is ignored — the tunnel decides the destination.
		NetDialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return dialer.Dial(dialCtx)
		},
	}
	header := http.Header{}
	header.Set(haclient.SvcTargetHeader, cfg.Target.SvcTarget())
	for k, vals := range cfg.Header {
		for _, v := range vals {
			header.Add(k, v)
		}
	}

	conn, resp, err := wsDialer.DialContext(ctx, "ws://edge-agent"+wsPathUniFiEvents, header)
	if err != nil {
		if resp != nil {
			cfg.Logger.V(2).Info("events subscribe handshake failed", "service", cfg.Key.Service, "status", resp.StatusCode)
		} else {
			cfg.Logger.V(2).Info("events subscribe dial failed", "service", cfg.Key.Service, "err", err.Error())
		}
		return false
	}
	defer conn.Close() //nolint:errcheck
	cfg.Logger.V(2).Info("events subscription connected", "service", cfg.Key.Service, "cluster", cfg.Key.Cluster)

	// Close the conn when ctx is cancelled so the read loop unblocks.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return true // connected then dropped; reconnect
		}
		evs, derr := decodeUniFiFrame(data)
		if derr != nil {
			continue
		}
		for _, ev := range evs {
			if err := cfg.Store.Append(ctx, cfg.Key, ev); err != nil {
				cfg.Logger.V(3).Info("events store append failed", "service", cfg.Key.Service, "err", err.Error())
			}
		}
	}
}
