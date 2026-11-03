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

package providers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	nativeSA "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/endpoints/request"
	nativeJWT "k8s.io/kubernetes/pkg/serviceaccount"

	"github.com/kcp-dev/logicalcluster/v3"
)

// Exercise the pinned native JWT authenticator, not a fake that infers issuer
// from the endpoint. A signed foreign SA remains authenticated through another
// cluster's request context with the same username, while its verified Extra
// retains the real issuer. TokenReview copies this UserInfo into its status.
func TestNativeServiceAccountJWTNeedsVerifiedIssuerBoundary(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := nativeJWT.JWTTokenGenerator(nativeJWT.LegacyIssuer, key)
	if err != nil {
		t.Fatal(err)
	}
	account := corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: ProviderSAName, Namespace: ProviderSANamespace, UID: "foreign-provider-uid", Annotations: map[string]string{"kcp.io/cluster": "foreign-org"}}}
	public, private := nativeJWT.LegacyClaims(account, corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "provider-token"}})
	token, err := generator.GenerateToken(context.Background(), public, private)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := nativeJWT.StaticPublicKeysGetter([]interface{}{&key.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := nativeJWT.NewLegacyValidator(false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	authenticate := nativeJWT.JWTTokenAuthenticator([]string{nativeJWT.LegacyIssuer}, keys, authenticator.Audiences{"api"}, validator)
	for _, endpoint := range []string{"foreign-org", "platform-provider"} {
		ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.Name(endpoint)})
		response, ok, err := authenticate.AuthenticateToken(ctx, token)
		if err != nil || !ok {
			t.Fatalf("native authentication at %s: ok=%v err=%v", endpoint, ok, err)
		}
		status := authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: response.User.GetName(), UID: response.User.GetUID(), Extra: map[string]authnv1.ExtraValue{}}}
		for key, values := range response.User.GetExtra() {
			status.User.Extra[key] = authnv1.ExtraValue(values)
		}
		if status.User.Username != ProviderSAUsername {
			t.Fatalf("unexpected native subject %q", status.User.Username)
		}
		if !TokenReviewMatchesCluster(status, "foreign-org") {
			t.Fatal("native verified issuer missing")
		}
		if TokenReviewMatchesCluster(status, "platform-provider") {
			t.Fatal("request endpoint laundered a foreign provider into platform authority")
		}
	}
}

func TestTokenReviewIssuerMustBeSingleExactTrustedValue(t *testing.T) {
	for _, issuers := range [][]string{nil, {}, {""}, {"foreign"}, {"expected", "foreign"}, {"expected", "expected"}} {
		status := authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: ProviderSAUsername, Extra: map[string]authnv1.ExtraValue{nativeSA.ClusterNameKey: issuers}}}
		if TokenReviewMatchesCluster(status, "expected") {
			t.Fatalf("accepted ambiguous/missing/foreign issuer %v", issuers)
		}
	}
}
