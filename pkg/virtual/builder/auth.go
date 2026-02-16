package builder

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// saTokenClaims holds the claims extracted from a KCP ServiceAccount JWT.
type saTokenClaims struct {
	Issuer      string `json:"iss"`
	ClusterName string `json:"kubernetes.io/serviceaccount/clusterName"`
}

// parseServiceAccountToken decodes a JWT without signature verification and
// checks whether it is a KCP ServiceAccount token. KCP verifies the actual
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

// extractBearerToken extracts the bearer token from the Authorization header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

// authorize performs delegated authentication and authorization against KCP.
// It follows the same pattern as kcp-dev/kcp's delegated.NewDelegatedAuthorizer:
//  1. TokenReview — authenticates the bearer token and extracts user identity.
//  2. SubjectAccessReview — checks if the authenticated user is allowed to
//     perform the given verb on the resource in the target workspace.
//
// Both calls use admin credentials scoped to the target workspace.
func authorize(ctx context.Context, kcpConfig *rest.Config, token, clusterName, verb, resource, name string) error {
	cfg := rest.CopyConfig(kcpConfig)
	cfg.Host = kcp.AppendClusterPath(cfg.Host, clusterName)

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	// 1. Authenticate: TokenReview with admin creds.
	tr, err := client.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("token review: %w", err)
	}
	if !tr.Status.Authenticated {
		return fmt.Errorf("token not authenticated")
	}

	// 2. Authorize: SubjectAccessReview with the resolved identity.
	sar, err := client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   tr.Status.User.Username,
			Groups: tr.Status.User.Groups,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb:     verb,
				Group:    "kedge.faros.sh",
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
