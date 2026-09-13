// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package serviceaccounts

import (
	"context"
	"fmt"
	"strings"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// VerifyProviderActionServiceAccount verifies an ordinary tenant identity online.
// Authorization remains the provider's responsibility. Call only for the exact
// Provider Action route; this does not grant access to other provider endpoints.
func VerifyProviderActionServiceAccount(ctx context.Context, cfg *rest.Config, token string) (string, error) {
	if cfg == nil || token == "" {
		return "", fmt.Errorf("tenant action identity required")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", err
	}
	review, err := client.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token, Audiences: []string{WorkloadIdentityTokenAudience}}}, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	if !review.Status.Authenticated || !containsString(review.Status.Audiences, WorkloadIdentityTokenAudience) {
		return "", fmt.Errorf("tenant action token audience rejected")
	}
	parts := strings.Split(review.Status.User.Username, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" || parts[2] != Namespace || parts[3] == "" {
		return "", fmt.Errorf("tenant action requires a default-namespace ServiceAccount")
	}
	sa, err := client.CoreV1().ServiceAccounts(Namespace).Get(ctx, parts[3], metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if sa.DeletionTimestamp != nil || review.Status.User.UID == "" || string(sa.UID) != review.Status.User.UID {
		return "", fmt.Errorf("tenant action ServiceAccount identity changed")
	}
	// Managed identities must pass their existing scoped/proof verification.
	if IsDelegatedUserServiceAccount(sa) || strings.HasPrefix(sa.Name, "faros-du-") || strings.HasPrefix(sa.Name, workloadIdentityNamePrefix) || sa.Labels[LabelWorkloadIdentity] != "" || sa.Annotations[AnnotationWorkloadIdentityTenantPath] != "" {
		return "", fmt.Errorf("managed workload requires scoped identity verification")
	}
	return review.Status.User.Username, nil
}
