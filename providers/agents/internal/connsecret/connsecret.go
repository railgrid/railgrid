// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package connsecret is the one place that knows how a Connection's credential
// Secret is named and which keys the messaging paths share. Both the HTTP layer
// (inbound webhooks, OAuth callbacks) and the Connection reconciler read and
// write these Secrets, and they must agree on the names or a secret the
// reconciler stores is one the webhook handler never finds.
package connsecret

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Name is the Secret holding a Connection's credentials (token, refresh token,
// signing secret, ...), in llm.SecretNamespace of the tenant workspace.
func Name(conn string) string { return "railgrid-agents-conn-" + conn }

// SigningSecretKey is the Secret key holding the platform-side verification
// secret: the Slack app signing secret, or the Telegram webhook secret_token
// the provider generated.
const SigningSecretKey = "signing_secret"

// SigningSecretMissingMessage is the one wording for "this connection has no
// verification secret stored". It is both the error text an inbound delivery
// gets when it cannot be verified and the Connection.Status.Message the
// reconciler writes, so the API response and what the portal shows cannot
// drift apart.
//
// The wording is platform-neutral on purpose: this path also serves Telegram,
// whose secret is a webhook secret_token rather than a signing secret, and a
// Telegram 401 that talks about a "signing secret" sends the reader looking
// for a Slack setting that does not exist on their connection.
const SigningSecretMissingMessage = "webhook verification secret required; update the connection"

// TelegramRegisterFailedPrefix starts the status message the reconciler
// writes when it could not re-register a Telegram webhook with a secret.
const TelegramRegisterFailedPrefix = "telegram webhook re-registration failed: "

// NewSigningSecret returns a fresh random secret (32 bytes, hex). Used for
// Telegram, whose secret_token is chosen by the webhook owner — us — and must
// match ^[A-Za-z0-9_-]{1,256}$.
func NewSigningSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ConnectionName is the inverse of Name: the Connection a credential Secret
// belongs to, and whether the name is one of ours at all. The Connection
// reconciler maps Secret events back to their Connection with it.
func ConnectionName(secret string) (string, bool) {
	return strings.CutPrefix(secret, "railgrid-agents-conn-")
}
