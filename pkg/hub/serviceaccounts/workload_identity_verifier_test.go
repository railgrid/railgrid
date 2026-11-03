// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package serviceaccounts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	nativeSA "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/client-go/rest"
)

func TestWorkloadVerifierCannotBorrowAnotherClustersLocalProof(t *testing.T) {
	const tenant = "root:railgrid:tenants:org:workspace"
	const cluster = "consumer-cluster"
	const name = "railgrid-du-real-owner"
	key := bytes.Repeat([]byte{0x37}, 32)
	for _, tc := range []struct {
		name                                             string
		issuers                                          []string
		uid                                              string
		missingObjectCluster, missingObjectUID, deleting bool
		want                                             bool
	}{
		{name: "actual local identity", issuers: []string{cluster}, uid: "local-uid", want: true},
		{name: "foreign issuer with same username and UID", issuers: []string{"foreign-cluster"}, uid: "local-uid"},
		{name: "local issuer with foreign UID", issuers: []string{cluster}, uid: "foreign-uid"},
		{name: "missing verified issuer", uid: "local-uid"},
		{name: "ambiguous verified issuer", issuers: []string{cluster, "foreign-cluster"}, uid: "local-uid"},
		{name: "duplicate verified issuer", issuers: []string{cluster, cluster}, uid: "local-uid"},
		{name: "missing reviewed UID", issuers: []string{cluster}},
		{name: "missing local cluster", issuers: []string{cluster}, uid: "local-uid", missingObjectCluster: true},
		{name: "missing local UID", issuers: []string{cluster}, uid: "local-uid", missingObjectUID: true},
		{name: "deleting local identity", issuers: []string{cluster}, uid: "local-uid", deleting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The selected workspace really contains a hub-signed delegated account.
			// A foreign bearer must not borrow its valid proof by matching its name.
			sa := &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: Namespace, UID: "local-uid",
				Labels:      map[string]string{LabelWorkloadIdentity: "true", LabelDelegatedUser: "true"},
				Annotations: map[string]string{logicalcluster.AnnotationKey: cluster, AnnotationWorkloadIdentityTenantPath: tenant, AnnotationDelegatedUser: "alice", AnnotationDelegatedOrg: "org", AnnotationDelegatedWorkspace: "workspace", AnnotationDelegatedProvider: "edges"},
			}}
			if err := SignDelegatedUserServiceAccount(key, sa, tenant, Identity{User: "alice"}, "edges"); err != nil {
				t.Fatal(err)
			}
			if err := verifyWorkloadServiceAccountAnnotations(context.Background(), sa, tenant, StaticProofKeySource(key)); err != nil {
				t.Fatalf("local proof must actually be valid: %v", err)
			}
			if tc.missingObjectCluster {
				delete(sa.Annotations, logicalcluster.AnnotationKey)
			}
			if tc.missingObjectUID {
				sa.UID = ""
			}
			if tc.deleting {
				now := metav1.Now()
				sa.DeletionTimestamp = &now
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/apis/authentication.k8s.io/v1/tokenreviews"):
					var request authnv1.TokenReview
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					if request.Spec.Token != "opaque-authenticated-token" {
						t.Error("unexpected token")
					}
					_ = json.NewEncoder(w).Encode(authnv1.TokenReview{TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}, Status: authnv1.TokenReviewStatus{Authenticated: true, Audiences: []string{WorkloadIdentityTokenAudience}, User: authnv1.UserInfo{Username: "system:serviceaccount:default:" + name, UID: tc.uid, Extra: map[string]authnv1.ExtraValue{nativeSA.ClusterNameKey: tc.issuers}}}})
				case strings.HasSuffix(r.URL.Path, "/api/v1/namespaces/default/serviceaccounts/"+name):
					_ = json.NewEncoder(w).Encode(sa)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			// Deliberately use a workspace PATH in the endpoint, while the trusted
			// object and review carry the logical ID. Neither URL nor JWT may supply it.
			user, object, err := VerifyWorkloadServiceAccountDetails(context.Background(), &rest.Config{Host: server.URL + "/clusters/" + tenant}, "opaque-authenticated-token", tenant, StaticProofKeySource(key))
			if (err == nil) != tc.want {
				t.Fatalf("user=%q object=%v err=%v", user, object, err)
			}
			if tc.want && (user != "system:serviceaccount:default:"+name || object.UID != "local-uid") {
				t.Fatal("did not return actual local identity")
			}
		})
	}
}
