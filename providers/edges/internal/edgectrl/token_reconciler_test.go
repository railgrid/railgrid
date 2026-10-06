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
