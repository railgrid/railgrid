// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Inbound channel webhook verification and de-duplication.
//
// The HMAC token in the webhook URL only proves the caller knows the URL — and
// the URL is displayed in the portal and pasted into third-party dashboards.
// Every platform that POSTs to us also signs (Slack) or tags (Telegram) each
// delivery with a per-app secret that never leaves the app config, so the
// handler additionally requires that proof before a message can drive a
// tool-using agent. The secret lives in the connection Secret under
// signingSecretKey next to the bot token.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/dynamic"

	"github.com/faroshq/provider-agents/llm"
)

// signingSecretKey is the connection Secret key holding the platform-side
// verification secret: the Slack app signing secret, or the Telegram webhook
// secret_token the provider generated.
const signingSecretKey = "signing_secret"

// slackSignatureMaxSkew bounds how old a signed Slack request may be. Slack
// documents five minutes; anything older is a replay of a captured request.
const slackSignatureMaxSkew = 5 * time.Minute

// connectionSigningSecretMissingMessage is the one wording for "this connection
// has no verification secret stored". It is both the errSigningSecretMissing
// text returned when a delivery cannot be verified and the
// Connection.Status.Message the reconcile loop writes, so the API response and
// what the portal shows cannot drift apart.
//
// The wording is platform-neutral on purpose: this path also serves Telegram,
// whose secret is a webhook secret_token rather than a signing secret, and a
// Telegram 401 that talks about a "signing secret" sends the reader looking for
// a Slack setting that does not exist on their connection.
const connectionSigningSecretMissingMessage = "webhook verification secret required; update the connection"

var (
	errSigningSecretMissing = errors.New(connectionSigningSecretMissingMessage)
	errSignatureInvalid     = errors.New("invalid signature")
	errSignatureStale       = errors.New("request timestamp outside the accepted window")
)

// newSigningSecret returns a fresh random secret (32 bytes, hex). Used for
// Telegram, whose secret_token is chosen by the webhook owner — us — and must
// match ^[A-Za-z0-9_-]{1,256}$.
func newSigningSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// slackSignature computes Slack's v0 request signature over the raw body:
// "v0=" + hex(HMAC-SHA256(secret, "v0:" + timestamp + ":" + body)).
func slackSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// verifySlackSignature checks the X-Slack-Request-Timestamp /
// X-Slack-Signature pair against the app signing secret. The timestamp is
// bound first so an attacker who captured a valid request cannot replay it
// after the window, and the comparison is constant-time.
func verifySlackSignature(secret, timestamp, signature string, body []byte, now time.Time) error {
	if strings.TrimSpace(secret) == "" {
		return errSigningSecretMissing
	}
	timestamp = strings.TrimSpace(timestamp)
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errSignatureStale
	}
	if d := now.Sub(time.Unix(ts, 0)); d > slackSignatureMaxSkew || d < -slackSignatureMaxSkew {
		return errSignatureStale
	}
	want := slackSignature(secret, timestamp, body)
	if !hmac.Equal([]byte(want), []byte(strings.TrimSpace(signature))) {
		return errSignatureInvalid
	}
	return nil
}

// verifyTelegramSecret checks the X-Telegram-Bot-Api-Secret-Token header
// Telegram attaches to every update once the webhook was registered with a
// secret_token.
func verifyTelegramSecret(secret, header string) error {
	if strings.TrimSpace(secret) == "" {
		return errSigningSecretMissing
	}
	if !hmac.Equal([]byte(secret), []byte(strings.TrimSpace(header))) {
		return errSignatureInvalid
	}
	return nil
}

// connectionSigningSecret reads a connection's signing secret through the
// APIExport virtual workspace (the inbound path has no user; it acts as the
// provider).
//
// It returns ("", nil) only when the secret is genuinely absent — no Secret, or
// no such key in it — and a non-nil error when the read itself failed. The
// distinction matters because "" is what callers act on: collapsing a failed
// read into it answers a verifiable delivery with 401 "no verification secret",
// telling the user to fix a secret that is already there, and parks a healthy
// Slack connection in Error.
func connectionSigningSecret(ctx context.Context, dyn dynamic.Interface, connName string) (string, error) {
	sec, err := vwSecrets{dyn}.GetSecret(ctx, llm.SecretNamespace, connectionSecretName(connName))
	switch {
	case apierrors.IsNotFound(err):
		return "", nil
	case err != nil:
		return "", err
	}
	return strings.TrimSpace(string(sec.Data[signingSecretKey])), nil
}

// inboundDedup is a bounded in-memory set of recently accepted event keys, so
// a platform redelivery (Slack retries on any non-2xx or slow response;
// Telegram redelivers until it sees a 2xx) does not start a second run of the
// same message.
//
// It is per process: a multi-replica deployment can still double-run an event
// that lands on two replicas. Making this durable (the run idempotency index
// in the store, keyed before the executor accepts the job) is a follow-up;
// the executor itself is single-replica today.
type inboundDedup struct {
	mu   sync.Mutex
	ttl  time.Duration
	max  int
	seen map[string]time.Time
	now  func() time.Time
}

// Slack retries at 1 and 5 minutes; keep keys comfortably longer than that.
const (
	inboundDedupTTL = time.Hour
	inboundDedupMax = 20000
)

func newInboundDedup(ttl time.Duration, max int) *inboundDedup {
	if ttl <= 0 {
		ttl = inboundDedupTTL
	}
	if max <= 0 {
		max = inboundDedupMax
	}
	return &inboundDedup{ttl: ttl, max: max, seen: map[string]time.Time{}, now: time.Now}
}

// claim records key and reports whether it was new. A key already present
// (and not expired) means the event was handled before.
func (d *inboundDedup) claim(key string) bool {
	if d == nil || key == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if at, ok := d.seen[key]; ok && now.Sub(at) < d.ttl {
		return false
	}
	if len(d.seen) >= d.max {
		d.evict(now)
	}
	d.seen[key] = now
	return true
}

// release forgets key so the event can be accepted again — used when the job
// could not be queued and the platform should retry it.
func (d *inboundDedup) release(key string) {
	if d == nil || key == "" {
		return
	}
	d.mu.Lock()
	delete(d.seen, key)
	d.mu.Unlock()
}

// evict drops expired keys; if that leaves the set at capacity, it drops the
// oldest quarter so a burst cannot grow memory without bound. Caller holds mu.
//
// The ordering is explicit rather than incidental. Go randomises map iteration,
// so evicting "whatever comes out of the range first" discards an arbitrary
// quarter: keys claimed seconds ago can go while hour-old ones survive. This
// set is replay protection, not a cache — a discarded key stops being a
// duplicate and its delivery gets run a second time — so the entries most worth
// keeping are the newest, exactly the ones a random sweep may take. Eviction
// also runs at the tail of a burst, when redeliveries of that burst are still
// arriving. Sorting a set this size (inboundDedupMax, 20k) is well under the
// cost of the agent run a missed duplicate would start.
func (d *inboundDedup) evict(now time.Time) {
	for k, at := range d.seen {
		if now.Sub(at) >= d.ttl {
			delete(d.seen, k)
		}
	}
	if len(d.seen) < d.max {
		return
	}
	// At least one, or a max below 4 would make this a no-op and let claim()
	// grow the map without bound while believing it was capped.
	drop := d.max / 4
	if drop < 1 {
		drop = 1
	}
	if drop > len(d.seen) {
		drop = len(d.seen)
	}
	type seenAt struct {
		key string
		at  time.Time
	}
	entries := make([]seenAt, 0, len(d.seen))
	for k, at := range d.seen {
		entries = append(entries, seenAt{key: k, at: at})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	for _, e := range entries[:drop] {
		delete(d.seen, e.key)
	}
}

// inboundEventKey namespaces a platform event id by connection so two tenants'
// bots cannot collide on the same platform id.
func inboundEventKey(cluster, conn, id string) string {
	if id == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/%s", cluster, conn, id)
}
