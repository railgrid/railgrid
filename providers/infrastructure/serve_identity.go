/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	authenticationv1client "k8s.io/client-go/kubernetes/typed/authentication/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

const (
	workloadIdentityReviewPath = "/workload-identities/review"
	workloadIdentityAudience   = "faros-provider-actions-bootstrap"
	identityReviewBodyLimit    = 1 << 20
)

type workloadIdentityReviewRequest struct {
	TenantPath  string `json:"tenantPath"`
	Project     string `json:"project"`
	ProjectUID  string `json:"projectUID"`
	Environment string `json:"environment"`
	Instance    string `json:"instance"`
}

// workloadIdentityReviewResponse is intentionally a narrow attestation
// contract. It never returns the reviewed token or any Kubernetes object.
type workloadIdentityReviewResponse struct {
	Authenticated  bool   `json:"authenticated"`
	Subject        string `json:"subject"`
	Namespace      string `json:"namespace"`
	ServiceAccount string `json:"serviceAccount"`
}

type workloadIdentityReviewer interface {
	Review(context.Context, string, []string) (authenticationv1.TokenReviewStatus, error)
	GetPod(context.Context, string, string) (*corev1.Pod, error)
}

type kubernetesWorkloadIdentityReviewer struct {
	auth authenticationv1client.AuthenticationV1Interface
	core corev1client.CoreV1Interface
}

func (r *kubernetesWorkloadIdentityReviewer) Review(ctx context.Context, token string, audiences []string) (authenticationv1.TokenReviewStatus, error) {
	review, err := r.auth.TokenReviews().Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: audiences},
	}, metav1.CreateOptions{})
	if err != nil {
		return authenticationv1.TokenReviewStatus{}, err
	}
	return review.Status, nil
}

func (r *kubernetesWorkloadIdentityReviewer) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	return r.core.Pods(namespace).Get(ctx, name, metav1.GetOptions{})
}

// buildWorkloadIdentityReviewHandler wires the attestation endpoint to the
// runtime cluster where development workloads run. It remains a non-nil 503
// handler when runtime credentials are unavailable so the route is explicit
// in REST-only/development mode instead of falling through to the portal.
func buildWorkloadIdentityReviewHandler() http.Handler {
	runtimeConfig, _ := loadDataPlaneRuntimeConfig()
	if runtimeConfig == nil {
		return unavailableWorkloadIdentityHandler("runtime cluster credentials are unavailable")
	}
	clients, err := kubernetes.NewForConfig(runtimeConfig)
	if err != nil {
		return unavailableWorkloadIdentityHandler("runtime cluster client is unavailable")
	}
	return newWorkloadIdentityReviewHandler(&kubernetesWorkloadIdentityReviewer{
		auth: clients.AuthenticationV1(),
		core: clients.CoreV1(),
	})
}

func unavailableWorkloadIdentityHandler(reason string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != workloadIdentityReviewPath {
			http.NotFound(w, r)
			return
		}
		http.Error(w, reason, http.StatusServiceUnavailable)
	})
}

func newWorkloadIdentityReviewHandler(reviewer workloadIdentityReviewer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != workloadIdentityReviewPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if reviewer == nil {
			http.Error(w, "workload identity reviewer is unavailable", http.StatusServiceUnavailable)
			return
		}
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			http.Error(w, "authorization bearer token is required", http.StatusUnauthorized)
			return
		}
		var request workloadIdentityReviewRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, identityReviewBodyLimit))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid workload identity request", http.StatusBadRequest)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			http.Error(w, "workload identity request must contain one JSON object", http.StatusBadRequest)
			return
		}
		if err := validateWorkloadIdentityRequest(request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		status, err := reviewer.Review(r.Context(), token, []string{workloadIdentityAudience})
		if err != nil {
			http.Error(w, "token review unavailable", http.StatusServiceUnavailable)
			return
		}
		response := workloadIdentityReviewResponse{}
		if status.Authenticated && containsIdentityAudience(status.Audiences, workloadIdentityAudience) {
			subject, namespace, serviceAccount, parseErr := parseServiceAccountSubject(status.User.Username)
			if parseErr == nil {
				podName := identityExtra(status.User.Extra, "authentication.kubernetes.io/pod-name")
				podUID := identityExtra(status.User.Extra, "authentication.kubernetes.io/pod-uid")
				if podName != "" {
					pod, podErr := reviewer.GetPod(r.Context(), namespace, podName)
					if podErr == nil && workloadPodMatchesRequest(pod, request, podUID, serviceAccount) {
						response = workloadIdentityReviewResponse{
							Authenticated:  true,
							Subject:        subject,
							Namespace:      namespace,
							ServiceAccount: serviceAccount,
						}
					} else if podErr != nil && !apierrors.IsNotFound(podErr) {
						http.Error(w, "workload identity pod lookup unavailable", http.StatusServiceUnavailable)
						return
					}
				}
			}
		}
		writeWorkloadIdentityReview(w, response)
	})
}

func containsIdentityAudience(audiences []string, expected string) bool {
	for _, audience := range audiences {
		if audience == expected {
			return true
		}
	}
	return false
}

func bearerToken(raw string) (string, bool) {
	parts := strings.Fields(raw)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return parts[1], true
}

func validateWorkloadIdentityRequest(request workloadIdentityReviewRequest) error {
	for name, value := range map[string]string{
		"tenantPath":  request.TenantPath,
		"project":     request.Project,
		"projectUID":  request.ProjectUID,
		"environment": request.Environment,
		"instance":    request.Instance,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

func parseServiceAccountSubject(subject string) (string, string, string, error) {
	parts := strings.Split(subject, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" || parts[2] == "" || parts[3] == "" {
		return "", "", "", errors.New("token review subject is not a service account")
	}
	return subject, parts[2], parts[3], nil
}

func identityExtra(extra map[string]authenticationv1.ExtraValue, key string) string {
	values := extra[key]
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func workloadPodMatchesRequest(pod *corev1.Pod, request workloadIdentityReviewRequest, podUID, serviceAccount string) bool {
	if pod == nil || pod.Spec.ServiceAccountName != serviceAccount {
		return false
	}
	if strings.TrimSpace(podUID) == "" || string(pod.UID) != podUID {
		return false
	}
	annotations := pod.GetAnnotations()
	for key, expected := range map[string]string{
		"faros.sh/actions-tenant":      request.TenantPath,
		"faros.sh/actions-project":     request.Project,
		"faros.sh/actions-project-uid": request.ProjectUID,
		"faros.sh/actions-environment": request.Environment,
		"faros.sh/actions-instance":    request.Instance,
	} {
		actual := strings.TrimSpace(annotations[key])
		if actual == "" || actual != expected {
			return false
		}
	}
	return true
}

func writeWorkloadIdentityReview(w http.ResponseWriter, response workloadIdentityReviewResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
