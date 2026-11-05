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

package proxy

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/util/identity"
)

// resolveUser must find an account by any identity bound to it: the one it
// was created with and those linked later by verified email.
func TestResolveUserFindsPrimaryAndLinkedIdentities(t *testing.T) {
	const issuer, primarySub, linkedSub = "https://auth.example.com", "github-sub", "google-sub"
	p := newStaticTokenTestProxy(t)
	ctx := context.Background()
	if _, err := p.railgridClient.Users().Create(ctx, &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{
			Name: "user-alice",
			Labels: map[string]string{
				identity.LabelSubject: identity.OIDCSubjectHash(issuer, primarySub),
				identity.LinkedIdentityLabel(identity.OIDCSubjectHash(issuer, linkedSub)): "true",
			},
		},
		Spec: tenancyv1alpha1.UserSpec{Email: "alice@example.com", RBACIdentity: "railgrid:alice@example.com"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	for _, sub := range []string{primarySub, linkedSub} {
		user, err := p.resolveUser(ctx, issuer, sub)
		if err != nil {
			t.Fatalf("resolveUser(%s): %v", sub, err)
		}
		if user.Name != "user-alice" {
			t.Fatalf("resolveUser(%s) = %s, want user-alice", sub, user.Name)
		}
	}
	if _, err := p.resolveUser(ctx, issuer, "nobody"); err == nil {
		t.Fatal("resolveUser for an unbound subject must fail")
	}
	// The linked subject under another issuer is a different identity.
	if _, err := p.resolveUser(ctx, "https://other.example", linkedSub); err == nil {
		t.Fatal("resolveUser must not match a subject across issuers")
	}
}
