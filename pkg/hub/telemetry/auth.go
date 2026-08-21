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

package telemetry

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/faroshq/faros/pkg/apiurl"
	"github.com/faroshq/faros/pkg/hub/providers"
)

type WorkspaceClusterResolver func(string) (string, bool)

type TokenReviewAuthenticator struct {
	base    *rest.Config
	resolve WorkspaceClusterResolver
}

func NewTokenReviewAuthenticator(base *rest.Config, resolve WorkspaceClusterResolver) (*TokenReviewAuthenticator, error) {
	if base == nil || resolve == nil {
		return nil, fmt.Errorf("provider TokenReview configuration is incomplete: %w", ErrInvalidConfig)
	}
	return &TokenReviewAuthenticator{base: rest.CopyConfig(base), resolve: resolve}, nil
}

func (a *TokenReviewAuthenticator) Authenticate(ctx context.Context, r *http.Request, provider string) error {
	cluster, ok := a.resolve(provider)
	if !ok || strings.TrimSpace(cluster) == "" {
		return ErrUnauthorized
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		return ErrUnauthorized
	}
	cfg := rest.CopyConfig(a.base)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, cluster)
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return ErrUnauthorized
	}
	review, err := client.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token}}, metav1.CreateOptions{})
	if err != nil || !review.Status.Authenticated {
		return ErrUnauthorized
	}
	want := "system:serviceaccount:" + providers.ProviderSANamespace + ":" + providers.ProviderSAName
	if subtle.ConstantTimeCompare([]byte(review.Status.User.Username), []byte(want)) != 1 {
		return ErrUnauthorized
	}
	return nil
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return token, token != "" && !strings.ContainsAny(token, "\r\n")
}
