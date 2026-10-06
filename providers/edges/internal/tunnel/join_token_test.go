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
	"strings"
	"testing"

	edgeapi "github.com/railgrid/provider-edges/internal/edgeapi"
)

// An agent's durable credential is minted by this provider and cannot be
// renewed once expired. An edge offline longer than that lifetime returns with
// a credential the provider refuses and — before this — a join token the
// provider had cleared, so it was locked out until an operator regenerated the
// token by hand. The digest kept beside the cleared plaintext is what lets the
// agent enroll again with the token it already has.
func TestMatchJoinToken(t *testing.T) {
	const token = "bootstrap-token"
	hash := edgeapi.HashJoinToken(token)

	for _, tc := range []struct {
		name      string
		joinToken string
		hash      string
		presented string
		wantErr   string
	}{
		{name: "plaintext matches before registration", joinToken: token, presented: token},
		{name: "plaintext rejects another token", joinToken: token, presented: "other", wantErr: "mismatch"},
		// The case this fixes: registered, plaintext cleared, agent re-enrolls.
		{name: "hash matches after registration", hash: hash, presented: token},
		{name: "hash rejects another token", hash: hash, presented: "other", wantErr: "mismatch"},
		// A rotated token replaces the hash, so the previous one stops working.
		{name: "rotated hash rejects the previous token", hash: edgeapi.HashJoinToken("rotated"), presented: token, wantErr: "mismatch"},
		{name: "neither set", wantErr: "has no join token set"},
		{name: "empty presented against a hash", hash: hash, presented: "", wantErr: "mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := matchJoinToken(tc.joinToken, tc.hash, tc.presented)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("matchJoinToken = %v, want accepted", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("matchJoinToken = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// The plaintext still wins while it is present, so a pre-registration edge
// whose hash belongs to an older token is not authenticated by that older one.
func TestMatchJoinTokenPrefersThePlaintext(t *testing.T) {
	if err := matchJoinToken("current", edgeapi.HashJoinToken("previous"), "previous"); err == nil {
		t.Fatal("a stale hash authenticated while a fresh plaintext token was set")
	}
	if err := matchJoinToken("current", edgeapi.HashJoinToken("previous"), "current"); err != nil {
		t.Fatalf("the current plaintext token was refused: %v", err)
	}
}

func TestHashJoinTokenIsStableAndEmptyForEmpty(t *testing.T) {
	if got := edgeapi.HashJoinToken(""); got != "" {
		t.Fatalf("HashJoinToken(\"\") = %q, want empty so an unset token cannot match", got)
	}
	a, b := edgeapi.HashJoinToken("x"), edgeapi.HashJoinToken("x")
	if a != b || len(a) != 64 {
		t.Fatalf("HashJoinToken not a stable hex sha256: %q / %q", a, b)
	}
}
