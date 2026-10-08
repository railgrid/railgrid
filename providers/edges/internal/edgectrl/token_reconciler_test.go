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

package edgectrl

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	edgeapi "github.com/railgrid/provider-edges/internal/edgeapi"
)

// Every issued bootstrap token is stored twice: the plaintext, which
// registration clears, and its digest, which outlives it so the agent that
// joined with the token can enroll again after its durable credential expires.
// A token issued without the digest would re-create the lockout it fixes.
func TestIssueTokenStoresTheDigestBesideThePlaintext(t *testing.T) {
	c := newLifecycleClient(t, linuxServer())
	edge := getLinuxServer(t, c)
	cs := edge.GetConnectionStatus()
	r := &TokenReconciler{}

	if _, err := r.issueToken(context.Background(), c, edge, cs, "TestRequested", "issued by the test", klog.Background()); err != nil {
		t.Fatalf("issueToken: %v", err)
	}

	stored := getLinuxServer(t, c).GetConnectionStatus()
	if stored.JoinToken == "" {
		t.Fatal("issueToken stored no join token")
	}
	if stored.JoinTokenHash != edgeapi.HashJoinToken(stored.JoinToken) {
		t.Fatalf("joinTokenHash = %q, want the digest of the issued token", stored.JoinTokenHash)
	}
}

// Rotation must invalidate the previous token: the digest is replaced, so an
// agent still holding the old one is refused rather than re-enrolled.
func TestIssueTokenRotationReplacesTheDigest(t *testing.T) {
	c := newLifecycleClient(t, linuxServer())
	r := &TokenReconciler{}

	edge := getLinuxServer(t, c)
	if _, err := r.issueToken(context.Background(), c, edge, edge.GetConnectionStatus(), "First", "first", klog.Background()); err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	first := getLinuxServer(t, c).GetConnectionStatus()

	rotated := getLinuxServer(t, c)
	if _, err := r.issueToken(context.Background(), c, rotated, rotated.GetConnectionStatus(), "RegenerateRequested", "rotated", klog.Background()); err != nil {
		t.Fatalf("issueToken (rotate): %v", err)
	}
	second := getLinuxServer(t, c).GetConnectionStatus()

	if second.JoinToken == first.JoinToken {
		t.Fatal("rotation reissued the same token")
	}
	if second.JoinTokenHash == first.JoinTokenHash {
		t.Fatal("rotation left the previous digest in place; the old token would still enroll")
	}
	if second.JoinTokenHash != edgeapi.HashJoinToken(second.JoinToken) {
		t.Fatalf("joinTokenHash = %q, want the digest of the rotated token", second.JoinTokenHash)
	}
}

// The hub registers an agent in two steps by two different writers: it clears
// status.joinToken with a MergePatch, and the lifecycle reconciler marks the
// edge Registered afterwards. Between the two, the edge has a hash, no
// plaintext token, and no Registered=True -- and the token controller must not
// mint a replacement, because the agent in the field still holds the token that
// hash belongs to. Regenerating here orphans it: the new token's plaintext is
// destroyed by the hub's follow-up clear, so nobody holds a token matching the
// stored hash, and the agent is refused on every reconnect.
func TestNoTokenIsMintedBetweenTheClearAndRegistered(t *testing.T) {
	agentToken := "the-token-the-agent-holds"
	cs := &edgeapi.ConnectionStatus{
		JoinToken:     "", // cleared by the hub on registration
		JoinTokenHash: edgeapi.HashJoinToken(agentToken),
		// Registered has NOT been written yet.
	}

	if needsJoinToken(cs) {
		t.Fatal("a token would be minted after the clear but before Registered, orphaning the agent's token")
	}
}

// The ordinary paths around that window must keep working.
func TestNeedsJoinToken(t *testing.T) {
	registered := func(status metav1.ConditionStatus) []metav1.Condition {
		return []metav1.Condition{{
			Type:   edgeapi.ConnectionConditionRegistered,
			Status: status,
			Reason: "Test",
		}}
	}

	for name, tc := range map[string]struct {
		cs   *edgeapi.ConnectionStatus
		want bool
	}{
		"fresh edge, nothing issued yet": {
			cs:   &edgeapi.ConnectionStatus{},
			want: true,
		},
		"token outstanding, agent has not used it": {
			cs:   &edgeapi.ConnectionStatus{JoinToken: "t", JoinTokenHash: edgeapi.HashJoinToken("t")},
			want: false,
		},
		"cleared after registration, hash retained": {
			cs:   &edgeapi.ConnectionStatus{JoinTokenHash: edgeapi.HashJoinToken("t")},
			want: false,
		},
		"registered": {
			cs:   &edgeapi.ConnectionStatus{Conditions: registered(metav1.ConditionTrue)},
			want: false,
		},
		"registration failed and nothing was ever issued": {
			cs:   &edgeapi.ConnectionStatus{Conditions: registered(metav1.ConditionFalse)},
			want: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := needsJoinToken(tc.cs); got != tc.want {
				t.Fatalf("needsJoinToken = %v, want %v", got, tc.want)
			}
		})
	}
}
