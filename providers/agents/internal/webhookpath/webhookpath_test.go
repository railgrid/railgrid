// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package webhookpath

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// The derivation is the credential: every trigger URL already pasted into a
// GitHub webhook setting, a cron runner or a colleague's notes was minted by
// it. These vectors are not "what the code does" — they are what api/
// (webhookKeyBytes / webhookToken / webhookPath, which this package
// deliberately mirrors) has always produced. A change that breaks them
// invalidates every existing trigger, so it must be a deliberate one.
func TestDerivationIsStable(t *testing.T) {
	const wantToken = "3911bb4fbbc12f0cea162edc2321a32a"
	got := Token(Key("test-key", ""), "tenant-a", "deploys")
	if got != wantToken {
		t.Fatalf("token = %q, want %q — this changes every existing trigger URL", got, wantToken)
	}
	wantPath := "/services/providers/agents/webhooks/triggers/tenant-a/deploys/" + wantToken
	if p := For(Key("test-key", ""), "tenant-a", "deploys"); p != wantPath {
		t.Fatalf("path = %q, want %q", p, wantPath)
	}
}

// With no explicit key the provider derives one from its kubeconfig's bytes,
// so every replica of every restart signs identically without shared state.
func TestKeyDerivedFromKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const wantKey = "e64ad55f907a72750518cfd4fc91fc4732e008f1bc512e08dfe9ab478814641a"
	key := Key("", path)
	if hex.EncodeToString(key) != wantKey {
		t.Fatalf("key = %s, want %s", hex.EncodeToString(key), wantKey)
	}
	if tok := Token(key, "tenant-a", "deploys"); tok != "12d435e6e3a993812a714c011d6e4bf8" {
		t.Fatalf("token from derived key = %q", tok)
	}
	// An explicit key wins over the file.
	if hex.EncodeToString(Key("override", path)) != hex.EncodeToString([]byte("override")) {
		t.Fatal("an explicit key must take precedence over the kubeconfig")
	}
}

// No key configured means no URL can be minted. Callers must be able to tell
// that apart from "the path is empty", so they leave a stored path alone
// instead of clearing a working one.
func TestNoKeyMintsNothing(t *testing.T) {
	if tok := Token(Key("", ""), "tenant-a", "deploys"); tok != "" {
		t.Fatalf("token without a key = %q, want empty", tok)
	}
	if p := For(nil, "tenant-a", "deploys"); p != "" {
		t.Fatalf("path without a key = %q, want empty", p)
	}
	// A kubeconfig path that does not exist is the same as none.
	if k := Key("", filepath.Join(t.TempDir(), "absent")); k != nil {
		t.Fatalf("key from a missing file = %v, want nil", k)
	}
}
