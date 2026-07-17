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

package tunnel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/faroshq/provider-edges/internal/identity"
	"github.com/faroshq/provider-edges/internal/kcpurl"
)

// saTokenClaims holds the claims extracted from a kcp ServiceAccount JWT.
type saTokenClaims struct {
	Issuer      string `json:"iss"`
	ClusterName string `json:"kubernetes.io/serviceaccount/clusterName"`
}

// parseServiceAccountToken decodes a JWT without signature verification and
// checks whether it is a kcp ServiceAccount token. kcp verifies the actual
// signature when the request is forwarded.
func parseServiceAccountToken(token string) (saTokenClaims, bool) {
	if token == "" {
		return saTokenClaims{}, false
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return saTokenClaims{}, false
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return saTokenClaims{}, false
	}

	var claims saTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return saTokenClaims{}, false
	}

	if claims.Issuer != "kubernetes/serviceaccount" || claims.ClusterName == "" {
		return saTokenClaims{}, false
	}

	return claims, true
}

// extractBearerToken extracts the bearer token from the Authorization header
// or, as a fallback, the "token" query parameter.  The query-parameter path
// exists because browsers cannot set headers on WebSocket connections.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Fallback for WebSocket upgrades from the browser terminal.
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	return ""
}

// authorize performs delegated authentication and authorization against kcp.
// It follows the same pattern as kcp-dev/kcp's delegated.NewDelegatedAuthorizer:
//  1. TokenReview — authenticates the bearer token and extracts user identity.
//  2. SubjectAccessReview — checks if the authenticated user is allowed to
//     perform the given verb on the resource in the target workspace.
//
// Both calls use admin credentials scoped to the target workspace, with one
// exception: kcp ServiceAccount tokens only authenticate in their home
// logical cluster, so for SA tokens the TokenReview runs against the token's
// clusterName claim. The home cluster is still VERIFIED — kcp checks the
// token signature there, and a forged claim just fails the review. The
// SubjectAccessReview always runs in the target workspace (clusterName from
// the request URL — the #68 invariant), under a cluster-qualified synthetic
// identity (see pkg/util/identity) with the SA's groups dropped, so a
// foreign SA passes only when the target workspace explicitly bound that
// exact qualified identity (e.g. the provider edges-proxy grant created on
// tenant Enable).
func authorize(ctx context.Context, kcpConfig *rest.Config, token, clusterName, verb, group, resource, name string) error {
	reviewCluster := clusterName
	saClaims, isForeignSA := parseServiceAccountToken(token)
	if isForeignSA && saClaims.ClusterName == clusterName {
		// Same-cluster SA: the plain path below already handles it.
		isForeignSA = false
	}
	if isForeignSA {
		reviewCluster = saClaims.ClusterName
	}

	trCfg := rest.CopyConfig(kcpConfig)
	trCfg.Host = kcpurl.ClusterURL(trCfg.Host, reviewCluster)
	trClient, err := kubernetes.NewForConfig(trCfg)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	// 1. Authenticate: TokenReview with admin creds.
	tr, err := trClient.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("token review: %w", err)
	}
	if !tr.Status.Authenticated {
		return fmt.Errorf("token not authenticated")
	}

	sarUser := tr.Status.User.Username
	sarGroups := tr.Status.User.Groups
	if isForeignSA {
		qualified, ok := identity.QualifyServiceAccount(reviewCluster, tr.Status.User.Username)
		if !ok {
			// Token claimed to be an SA token but the home cluster resolved
			// it to a non-SA identity — refuse rather than authorize an
			// identity we can't encode unambiguously.
			return fmt.Errorf("token review: expected ServiceAccount identity, got %q", tr.Status.User.Username)
		}
		sarUser = qualified
		// Drop groups: system:serviceaccounts et al. would match
		// group-targeted bindings the tenant wrote for their OWN SAs.
		sarGroups = nil
	}

	client := trClient
	if reviewCluster != clusterName {
		sarCfg := rest.CopyConfig(kcpConfig)
		sarCfg.Host = kcpurl.ClusterURL(sarCfg.Host, clusterName)
		client, err = kubernetes.NewForConfig(sarCfg)
		if err != nil {
			return fmt.Errorf("creating kubernetes client: %w", err)
		}
	}

	// 2. Authorize: SubjectAccessReview with the resolved identity, always
	// in the target workspace.
	sar, err := client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   sarUser,
			Groups: sarGroups,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb:     verb,
				Group:    group,
				Version:  "v1alpha1",
				Resource: resource,
				Name:     name,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("subject access review: %w", err)
	}
	if !sar.Status.Allowed {
		return fmt.Errorf("access denied: %s", sar.Status.Reason)
	}

	return nil
}
