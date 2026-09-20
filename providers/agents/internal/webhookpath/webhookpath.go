// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package webhookpath derives the inbound URL a Trigger listens on.
//
// The URL is the credential: there is no stored token, only an HMAC over
// cluster/name under a provider-wide key, so anybody holding the URL can fire
// the trigger and nobody can guess one. That makes the derivation itself
// load-bearing — two writers that disagree by a byte mint two different URLs
// for the same trigger, and the one already pasted into GitHub stops working.
//
// Two writers exist. The HTTP layer (api/triggers.go, still serving the MCP
// create_trigger/update_trigger tools) stamps status.webhookPath on the write
// it performs. The Trigger reconciler stamps it for every other writer —
// notably the portal, which creates Triggers with a kube client and never
// passes through the API layer at all. Both reach the derivation here — the
// api layer through Server.webhookKeyBytes / Server.webhookToken, which are
// now thin binds onto Key and Token — so "byte-identical" is the compiler's
// problem rather than a convention anyone has to remember.
//
// The golden vectors in TestDerivationIsStable guard the other half of it:
// not that the two callers agree (they cannot disagree now), but that neither
// this package nor a future refactor CHANGES the derivation. Every trigger URL
// already pasted into a GitHub webhook config depends on it staying put, and
// nothing else in the system would notice if it moved.
package webhookpath

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// PathPrefix is the hub route every trigger webhook lands on.
const PathPrefix = "/services/providers/agents/webhooks/triggers/"

// kubeconfigSalt separates the key derived from the provider kubeconfig from
// any other use of the same file's bytes.
const kubeconfigSalt = "railgrid-agents-webhook"

// Key resolves the signing key: the explicit key when the operator set one
// (AGENTS_WEBHOOK_KEY), else a key derived from the provider kubeconfig's
// contents, which is stable across restarts and identical on every replica.
// Returns nil when neither is available — no key means no URL can be minted,
// which callers must treat as "leave the path alone" rather than "clear it".
func Key(explicit, providerKubeconfig string) []byte {
	if explicit != "" {
		return []byte(explicit)
	}
	if providerKubeconfig != "" {
		if b, err := os.ReadFile(providerKubeconfig); err == nil {
			sum := sha256.Sum256(append(b, []byte(kubeconfigSalt)...))
			return sum[:]
		}
	}
	return nil
}

// Token is the HMAC guarding one trigger's inbound URL. Empty when there is
// no key.
func Token(key []byte, clusterID, name string) string {
	if len(key) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(clusterID + "/" + name))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// Join assembles the hub-relative path from its parts.
func Join(clusterID, name, token string) string {
	return PathPrefix + clusterID + "/" + name + "/" + token
}

// For returns the full inbound path for a trigger, or "" when no key is
// configured.
func For(key []byte, clusterID, name string) string {
	token := Token(key, clusterID, name)
	if token == "" {
		return ""
	}
	return Join(clusterID, name, token)
}
