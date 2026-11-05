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

package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
)

const (
	// LabelSubject holds the hash of the identity a User was created with
	// (OIDCSubjectHash, or StaticToken.Sub). It is the User's primary IdP
	// binding and never changes once set.
	LabelSubject = "tenants.railgrid.ai/sub"

	// LinkedIdentityLabelPrefix prefixes one label per additional OIDC
	// identity linked to a User after creation: the key is the prefix plus
	// the identity's OIDCSubjectHash, the value is always "true". A User can
	// carry any number of them, so one account survives a change of IdP
	// connector (Dex's sub is connector-scoped) or of the IdP itself.
	LinkedIdentityLabelPrefix = "identities.tenants.railgrid.ai/"
)

// OIDCSubjectHash is the label-safe key of an OIDC identity: the first 63 hex
// characters (a label value's and a label name's maximum length) of
// sha256(issuer + "/" + sub).
func OIDCSubjectHash(issuer, sub string) string {
	h := sha256.Sum256([]byte(issuer + "/" + sub))
	return hex.EncodeToString(h[:])[:63]
}

// LinkedIdentityLabel is the label key that links the identity with the given
// OIDCSubjectHash to a User.
func LinkedIdentityLabel(subjectHash string) string {
	return LinkedIdentityLabelPrefix + subjectHash
}

// HasOIDCIdentity reports whether the User is bound to any IdP identity,
// primary or linked.
func HasOIDCIdentity(user *tenancyv1alpha1.User) bool {
	if user.Labels[LabelSubject] != "" {
		return true
	}
	for k := range user.Labels {
		if strings.HasPrefix(k, LinkedIdentityLabelPrefix) {
			return true
		}
	}
	return false
}

// FindUserByOIDCSubject returns the User bound to the identity with the given
// OIDCSubjectHash, as its primary identity or as a linked one, or nil when no
// User is bound to it.
func FindUserByOIDCSubject(ctx context.Context, client *railgridclient.Client, subjectHash string) (*tenancyv1alpha1.User, error) {
	for _, selector := range []string{
		LabelSubject + "=" + subjectHash,
		LinkedIdentityLabel(subjectHash),
	} {
		users, err := client.Users().List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return nil, fmt.Errorf("listing users: %w", err)
		}
		if len(users.Items) > 0 {
			return &users.Items[0], nil
		}
	}
	return nil, nil
}

// IdentityEntry returns the User's spec.oidcProviders entry for the identity
// with the given issuer and subject, or nil. An entry written before the
// issuer was recorded has none and matches on the subject alone, but only
// when no entry names the issuer: a Dex that moved hostname keeps its
// connectors' subjects, so the same subject can be bound under two issuers.
func IdentityEntry(user *tenancyv1alpha1.User, issuer, sub string) *tenancyv1alpha1.OIDCProvider {
	var legacy *tenancyv1alpha1.OIDCProvider
	for i := range user.Spec.OIDCProviders {
		entry := &user.Spec.OIDCProviders[i]
		if entry.ProviderID != sub {
			continue
		}
		if entry.Issuer == issuer {
			return entry
		}
		if entry.Issuer == "" && legacy == nil {
			legacy = entry
		}
	}
	return legacy
}

// EmailLinkable reports whether a new identity presenting email may be linked
// onto the User: one of the User's own identities presents that email
// (case-insensitively) and is not recorded as having it unverified. An entry
// written before EmailVerified was recorded carries no verdict and is trusted
// as it always was.
func EmailLinkable(user *tenancyv1alpha1.User, email string) bool {
	for _, entry := range user.Spec.OIDCProviders {
		if !strings.EqualFold(entry.Email, email) {
			continue
		}
		if entry.EmailVerified == nil || *entry.EmailVerified {
			return true
		}
	}
	return false
}
