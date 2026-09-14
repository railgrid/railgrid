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

import (
	"crypto/sha256"
	"encoding/hex"
)

// StaticToken is everything faros derives from a static bearer token. Every
// field is a hash of the token, never the token text: the User CR and its
// email/display name are readable by fellow org members, so anything built
// from the token itself would hand them a working credential.
//
// The hub proxy (pkg/server/proxy), the embedded kcp token-auth file
// (pkg/hub/kcp) and the dev setup (pkg/cli/cmd/dev/plugin) MUST all use this
// so the User CR, the kcp-authenticated username and --admin-users agree.
type StaticToken struct {
	// Sub is the 63-char value stored in the User CR's
	// tenants.faros.sh/sub label (a label value's maximum length).
	Sub string
	// UID is the short hash kcp's token-auth file records as the uid.
	UID string
	// RBACIdentity is the kcp username the token authenticates as,
	// "faros:static:<uid>". It is also the User's display name.
	RBACIdentity string
	// UserName is the User CR name, "static-user-<uid>".
	UserName string
}

// NewStaticToken derives the identity for a static bearer token.
func NewStaticToken(token string) StaticToken {
	h := sha256.Sum256([]byte("static-token/" + token))
	sub := hex.EncodeToString(h[:])[:63]
	uid := sub[:16]
	return StaticToken{
		Sub:          sub,
		UID:          uid,
		RBACIdentity: "faros:static:" + uid,
		UserName:     "static-user-" + uid,
	}
}
