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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fakeWorkloadIdentityReviewer struct {
	status    authenticationv1.TokenReviewStatus
	pod       *corev1.Pod
	token     string
	audiences []string
}

func (f *fakeWorkloadIdentityReviewer) Review(_ context.Context, token string, audiences []string) (authenticationv1.TokenReviewStatus, error) {
	f.token = token
	f.audiences = append([]string(nil), audiences...)
	return f.status, nil
}

func (f *fakeWorkloadIdentityReviewer) GetPod(context.Context, string, string) (*corev1.Pod, error) {
	return f.pod, nil
}

func TestWorkloadIdentityReviewAttestsBoundProjectedToken(t *testing.T) {
	fake := &fakeWorkloadIdentityReviewer{
		status: authenticationv1.TokenReviewStatus{
			Authenticated: true,
			Audiences:     []string{workloadIdentityAudience},
			User: authenticationv1.UserInfo{
				Username: "system:serviceaccount:runtime:actions",
				Extra: map[string]authenticationv1.ExtraValue{
					"authentication.kubernetes.io/pod-name": {"demo-dev-backend-abc"},
					"authentication.kubernetes.io/pod-uid":  {"pod-uid-1"},
				},
			},
		},
		pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo-dev-backend-abc",
				Namespace: "runtime",
				UID:       "pod-uid-1",
				Annotations: map[string]string{
					"faros.sh/actions-tenant":      "root:faros:tenants:org:ws",
					"faros.sh/actions-project":     "demo",
					"faros.sh/actions-project-uid": "project-uid-1",
					"faros.sh/actions-environment": "development",
					"faros.sh/actions-instance":    "demo-dev",
				},
			},
			Spec: corev1.PodSpec{ServiceAccountName: "actions"},
		},
	}
	handler := newWorkloadIdentityReviewHandler(fake)
	req := httptest.NewRequest(http.MethodPost, workloadIdentityReviewPath, stringsReader(`{"tenantPath":"root:faros:tenants:org:ws","project":"demo","projectUID":"project-uid-1","environment":"development","instance":"demo-dev"}`))
	req.Header.Set("Authorization", "Bearer projected-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if fake.token != "projected-token" || len(fake.audiences) != 1 || fake.audiences[0] != workloadIdentityAudience {
		t.Fatalf("review = token %q audiences %v", fake.token, fake.audiences)
	}
	var got workloadIdentityReviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Authenticated || got.Subject != "system:serviceaccount:runtime:actions" || got.Namespace != "runtime" || got.ServiceAccount != "actions" {
		t.Errorf("response = %+v", got)
	}
}

func TestWorkloadIdentityReviewFailsClosedOnInstanceMismatch(t *testing.T) {
	fake := &fakeWorkloadIdentityReviewer{
		status: authenticationv1.TokenReviewStatus{
			Authenticated: true,
			Audiences:     []string{workloadIdentityAudience},
			User: authenticationv1.UserInfo{
				Username: "system:serviceaccount:runtime:actions",
				Extra: map[string]authenticationv1.ExtraValue{
					"authentication.kubernetes.io/pod-name": {"demo-dev-backend-abc"},
					"authentication.kubernetes.io/pod-uid":  {"pod-uid-1"},
				},
			},
		},
		pod: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "demo-dev-backend-abc", UID: "pod-uid-1"},
			Spec:       corev1.PodSpec{ServiceAccountName: "actions"},
		},
	}
	handler := newWorkloadIdentityReviewHandler(fake)
	req := httptest.NewRequest(http.MethodPost, workloadIdentityReviewPath, stringsReader(`{"tenantPath":"tenant","project":"demo","projectUID":"project-uid-1","environment":"development","instance":"other-dev"}`))
	req.Header.Set("Authorization", "Bearer projected-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	var got workloadIdentityReviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Authenticated {
		t.Fatalf("mismatched instance was authenticated: %+v", got)
	}
}

func TestWorkloadIdentityReviewRequiresBearerAndExactBody(t *testing.T) {
	handler := newWorkloadIdentityReviewHandler(&fakeWorkloadIdentityReviewer{})
	for _, tc := range []struct {
		name string
		auth string
		body string
	}{
		{name: "missing auth", body: `{}`},
		{name: "unknown body field", auth: "Bearer token", body: `{"tenantPath":"t","project":"p","projectUID":"u","environment":"e","instance":"i","extra":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, workloadIdentityReviewPath, stringsReader(tc.body))
			req.Header.Set("Authorization", tc.auth)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code < http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func stringsReader(value string) *strings.Reader {
	return strings.NewReader(value)
}
