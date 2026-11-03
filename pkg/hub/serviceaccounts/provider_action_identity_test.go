// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package serviceaccounts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	nativeSA "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/client-go/rest"
)

func TestProviderActionServiceAccountRequiresOnlineTenantIdentity(t *testing.T) {
	for _, test := range []struct {
		name, subject, uid, audience          string
		foreignIssuer, missingIssuer          bool
		authenticated, managed, deleted, want bool
	}{
		{name: "tenant account", subject: "system:serviceaccount:default:factory", uid: "live-uid", audience: WorkloadIdentityTokenAudience, authenticated: true, want: true},
		{name: "foreign issuer same UID", subject: "system:serviceaccount:default:factory", uid: "live-uid", audience: WorkloadIdentityTokenAudience, authenticated: true, foreignIssuer: true},
		{name: "missing issuer", subject: "system:serviceaccount:default:factory", uid: "live-uid", audience: WorkloadIdentityTokenAudience, authenticated: true, missingIssuer: true},
		{name: "foreign UID", subject: "system:serviceaccount:default:factory", uid: "foreign-uid", audience: WorkloadIdentityTokenAudience, authenticated: true},
		{name: "missing UID", subject: "system:serviceaccount:default:factory", audience: WorkloadIdentityTokenAudience, authenticated: true},
		{name: "wrong audience", subject: "system:serviceaccount:default:factory", uid: "live-uid", audience: "other", authenticated: true},
		{name: "denied", subject: "system:serviceaccount:default:factory", uid: "live-uid", audience: WorkloadIdentityTokenAudience},
		{name: "wrong namespace", subject: "system:serviceaccount:other:factory", uid: "live-uid", audience: WorkloadIdentityTokenAudience, authenticated: true},
		{name: "managed workload", subject: "system:serviceaccount:default:factory", uid: "live-uid", audience: WorkloadIdentityTokenAudience, authenticated: true, managed: true},
		{name: "deleting", subject: "system:serviceaccount:default:factory", uid: "live-uid", audience: WorkloadIdentityTokenAudience, authenticated: true, deleted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reviews := 0
			issuer := "consumer-cluster"
			if test.foreignIssuer {
				issuer = "foreign-cluster"
			}
			if test.missingIssuer {
				issuer = ""
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/apis/authentication.k8s.io/v1/tokenreviews":
					reviews++
					var request authnv1.TokenReview
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					if request.Spec.Token != "tenant-token" || len(request.Spec.Audiences) != 1 || request.Spec.Audiences[0] != WorkloadIdentityTokenAudience {
						t.Error("incorrect online review")
					}
					_ = json.NewEncoder(w).Encode(authnv1.TokenReview{TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}, Status: authnv1.TokenReviewStatus{Authenticated: test.authenticated, Audiences: []string{test.audience}, User: authnv1.UserInfo{Username: test.subject, UID: test.uid, Extra: map[string]authnv1.ExtraValue{nativeSA.ClusterNameKey: {issuer}}}}})
				case "/api/v1/namespaces/default/serviceaccounts/factory":
					sa := corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: metav1.ObjectMeta{Name: "factory", Namespace: "default", UID: "live-uid", Annotations: map[string]string{logicalcluster.AnnotationKey: "consumer-cluster"}}}
					if test.managed {
						sa.Labels = map[string]string{LabelWorkloadIdentity: "true"}
					}
					if test.deleted {
						now := metav1.Now()
						sa.DeletionTimestamp = &now
					}
					_ = json.NewEncoder(w).Encode(sa)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			user, err := VerifyProviderActionServiceAccount(context.Background(), &rest.Config{Host: server.URL}, "tenant-token")
			if (err == nil) != test.want || (test.want && user != test.subject) || reviews != 1 {
				t.Fatalf("user=%q err=%v reviews=%d", user, err, reviews)
			}
		})
	}
}
