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

// authorize performs delegated authentication and authorization for a caller of
// the provider's own endpoints (consumer egress + agent ingress), following
// kcp's standard auth-delegator pattern:
//  1. TokenReview — authenticate the bearer token in the workspace that issued
//     it, and resolve the caller identity.
//  2. SubjectAccessReview — authorize that identity for verb on the resource,
//     ALWAYS in the consumer workspace (clusterName), served on the provider's
//     APIExport virtual workspace scoped to the engaged cluster (kcp#4279 /
//     kcp#4280 — this is what the edges APIExport claims tokenreviews +
//     subjectaccessreviews for).
//
// tenantCfg targets the consumer workspace through the APIExport VW; it is the
// SAR channel and also the TokenReview channel for tokens issued in the
// consumer workspace (end-user OIDC tokens — shard-wide authenticator — and the
// SA credentials the provider minted there for agents).
//
// kcp ServiceAccount tokens only authenticate in their home logical cluster, so
// a foreign SA (e.g. the provider's own SA, whose home is the provider
// workspace) is TokenReview'd there instead — reached by re-rooting the
// provider's own credential (kcpConfig) at the SA's home cluster, which works
// because a provider can always address its own home workspace. The home
// cluster is VERIFIED (kcp checks the signature there); the resolved identity
// is then re-encoded as the cluster-qualified SA name and authorized against
// the consumer workspace's RBAC via the VW.
//
// The SAR deliberately does NOT re-root kcpConfig at /clusters/<consumer> (the
// old approach), which the production hub proxy rejects with an opaque 404 —
// the failure kcp#4279 documents. It goes through the VW instead.
func authorize(ctx context.Context, tenantCfg, kcpConfig *rest.Config, token, clusterName, verb, group, resource, name string) error {
	saClaims, isForeignSA := parseServiceAccountToken(token)
	if isForeignSA && saClaims.ClusterName == clusterName {
		// SA minted in the consumer workspace (agent credentials): it
		// authenticates natively there via the VW, like a user token.
		isForeignSA = false
	}

	// 1. Authenticate the token in its issuing workspace.
	var trCfg *rest.Config
	if isForeignSA {
		trCfg = rest.CopyConfig(kcpConfig)
		trCfg.Host = kcpurl.ClusterURL(trCfg.Host, saClaims.ClusterName)
	} else {
		trCfg = tenantCfg
	}
	trClient, err := kubernetes.NewForConfig(trCfg)
	if err != nil {
		return fmt.Errorf("creating token-review client: %w", err)
	}
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
		qualified, ok := identity.QualifyServiceAccount(saClaims.ClusterName, tr.Status.User.Username)
		if !ok {
			// Claimed to be an SA token but the home cluster resolved it to a
			// non-SA identity — refuse rather than authorize an identity we
			// can't encode unambiguously.
			return fmt.Errorf("token review: expected ServiceAccount identity, got %q", tr.Status.User.Username)
		}
		sarUser = qualified
		// Drop groups: system:serviceaccounts et al. would match group-targeted
		// bindings the tenant wrote for their OWN SAs.
		sarGroups = nil
	}

	// 2. Authorize the resolved identity against the consumer workspace's RBAC,
	// via the APIExport virtual workspace.
	sarClient, err := kubernetes.NewForConfig(tenantCfg)
	if err != nil {
		return fmt.Errorf("creating subject-access-review client: %w", err)
	}
	sar, err := sarClient.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
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
