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

package identity

import "testing"

// The derivation is baked into running hubs: User CR names, their sub
// labels, kcp token-auth files and --admin-users entries all hold these
// values, so changing it silently orphans every static-token user.
func TestNewStaticTokenIsStable(t *testing.T) {
	got := NewStaticToken("dev-token")
	want := StaticToken{
		Sub:          "47b9dce0e91570a1e1fff9c4c57d664279c2a0a755bf3db1f374bf64d75cf66",
		UID:          "47b9dce0e91570a1",
		RBACIdentity: "faros:static:47b9dce0e91570a1",
		UserName:     "static-user-47b9dce0e91570a1",
	}
	if got != want {
		t.Fatalf("NewStaticToken(dev-token) = %+v, want %+v", got, want)
	}
}

// Tokens sharing a prefix must not collapse to one user: --admin-users
// matches on the RBAC identity, so a collision would share admin.
func TestNewStaticTokenDistinguishesSharedPrefixes(t *testing.T) {
	if a, b := NewStaticToken("dev-token"), NewStaticToken("dev-token2"); a.RBACIdentity == b.RBACIdentity {
		t.Fatalf("dev-token and dev-token2 share RBAC identity %q", a.RBACIdentity)
	}
}
