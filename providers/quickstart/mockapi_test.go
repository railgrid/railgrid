/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// The fingerprint is what lets an e2e tell "the caller's own credential arrived"
// from "a credential arrived". On the edge path those differ: a Service left on
// the default auth=secret substitutes its own token, which a length check can
// easily still accept.
func TestTokenFingerprintDistinguishesTokens(t *testing.T) {
	caller := tokenFingerprint("Bearer dev-token")
	// Same length, different value — exactly the case TokenLength cannot catch.
	substituted := tokenFingerprint("Bearer xxx-token")

	if caller == "" || substituted == "" {
		t.Fatal("fingerprint empty for a present credential")
	}
	if caller == substituted {
		t.Fatal("two different tokens of equal length share a fingerprint; the probe cannot detect substitution")
	}
	if len("Bearer dev-token") != len("Bearer xxx-token") {
		t.Fatal("test premise broken: the two tokens must be the same length")
	}
}

// A caller that knows what it sent must be able to recompute the value, or the
// assertion cannot be written.
func TestTokenFingerprintIsReproducible(t *testing.T) {
	const auth = "Bearer dev-token"
	sum := sha256.Sum256([]byte(auth))
	if got, want := tokenFingerprint(auth), hex.EncodeToString(sum[:])[:12]; got != want {
		t.Errorf("tokenFingerprint = %q, want %q", got, want)
	}
}

// Absent must not look present: an empty fingerprint is how a test detects that
// the hop dropped the credential entirely.
func TestTokenFingerprintEmptyForNoCredential(t *testing.T) {
	if got := tokenFingerprint(""); got != "" {
		t.Errorf("tokenFingerprint(\"\") = %q, want empty", got)
	}
}

// The value is echoed back over the same hop it describes, so it must not be
// reversible to the token.
func TestTokenFingerprintDoesNotLeakTheToken(t *testing.T) {
	const auth = "Bearer super-secret-value"
	fp := tokenFingerprint(auth)
	if len(fp) != 12 {
		t.Errorf("fingerprint length = %d, want 12", len(fp))
	}
	for _, secret := range []string{auth, "super-secret-value", "secret"} {
		if fp == secret || len(fp) >= len(secret) && fp[:min(len(fp), len(secret))] == secret {
			t.Errorf("fingerprint %q contains the credential", fp)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
