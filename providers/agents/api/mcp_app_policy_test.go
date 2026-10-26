// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseMCPAppApprovedDigestsDefaultsToDenyAndValidatesPins(t *testing.T) {
	if got, err := ParseMCPAppApprovedDigests(""); err != nil || len(got) != 0 {
		t.Fatalf("empty MCP App allowlist = %#v, err=%v", got, err)
	}
	digest := sha256.Sum256([]byte("reviewed HTML"))
	pin := hex.EncodeToString(digest[:])
	got, err := ParseMCPAppApprovedDigests(`{"reports":["` + pin + `"]}`)
	if err != nil {
		t.Fatalf("parse valid allowlist: %v", err)
	}
	if !mcpAppDigestApproved(got, "reports", pin) || mcpAppDigestApproved(got, "other", pin) || mcpAppDigestApproved(got, "reports", strings.ToUpper(pin)) {
		t.Fatalf("allowlist did not require the exact provider and lowercase digest: %#v", got)
	}
	got["reports"][0] = strings.Repeat("0", 64)
	if !mcpAppDigestApproved(map[string][]string{"reports": {pin}}, "reports", pin) {
		t.Fatal("parsed allowlist unexpectedly retained caller-owned slice storage")
	}
}

func TestParseMCPAppApprovedDigestsRejectsMalformedConfiguration(t *testing.T) {
	valid := strings.Repeat("a", 64)
	for name, raw := range map[string]string{
		"not JSON":           "{",
		"null":               "null",
		"bad provider":       `{"Reports":["` + valid + `"]}`,
		"bad digest":         `{"reports":["` + strings.Repeat("A", 64) + `"]}`,
		"empty pin list":     `{"reports":[]}`,
		"duplicate digest":   `{"reports":["` + valid + `","` + valid + `"]}`,
		"duplicate provider": `{"reports":["` + valid + `"],"reports":["` + strings.Repeat("b", 64) + `"]}`,
		"too large":          `{"reports":["` + strings.Repeat("a", 64) + `"]}` + strings.Repeat(" ", mcpAppApprovedDigestsConfigMaxBytes),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMCPAppApprovedDigests(raw); err == nil {
				t.Fatal("malformed MCP App allowlist was accepted")
			}
		})
	}
}
