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

package serviceaccounts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestDelegatedTokenRejectsCoMemberTokenRequest(t *testing.T) {
	ctx := context.Background()
	tenantPath := tenantPathFor(delegatedTestOrg, delegatedTestWS)
	sa := hubMintedDelegatedServiceAccount(t, tenantPath, delegatedTestUser, "victim-uid")
	audience, err := DelegatedTokenAudience(ctx, testProofKeySource, sa)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, token, uid string
		want             bool
	}{
		{"hub issued dual audience", "hub", string(sa.UID), true},
		{"co-member native only", "native", string(sa.UID), false},
		{"co-member guessed audience", "guessed", string(sa.UID), false},
		{"different token subject UID", "hub", "other-uid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					_ = json.NewEncoder(w).Encode(sa)
					return
				}
				var request authnv1.TokenReview
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				// A native-only or guessed-audience token genuinely authenticates as the
				// victim SA at KCP, but cannot pass the hub's separate audience review.
				var accepted []string
				if len(request.Spec.Audiences) == 1 && request.Spec.Audiences[0] == WorkloadIdentityTokenAudience {
					accepted = []string{WorkloadIdentityTokenAudience}
				} else if tc.token == "hub" && containsString(request.Spec.Audiences, audience) {
					accepted = []string{audience}
				}
				_ = json.NewEncoder(w).Encode(&authnv1.TokenReview{TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}, Status: authnv1.TokenReviewStatus{Authenticated: len(accepted) > 0, Audiences: accepted, User: authnv1.UserInfo{Username: "system:serviceaccount:default:" + sa.Name, UID: tc.uid}}})
			}))
			defer server.Close()
			_, _, err := VerifyWorkloadServiceAccountDetails(ctx, &rest.Config{Host: server.URL}, tc.token, tenantPath, testProofKeySource)
			if (err == nil) != tc.want {
				t.Fatalf("verification error = %v, want success %v", err, tc.want)
			}
		})
	}
	// A public metadata proof cannot be reused as the audience MAC. The
	// independently computed audience also changes across account lifetimes.
	if strings.Contains(audience, sa.Annotations[AnnotationDelegatedProof]) {
		t.Fatal("audience derived without a secret domain-separated MAC")
	}
	replacement := hubMintedDelegatedServiceAccount(t, tenantPath, delegatedTestUser, "replacement-uid")
	other, err := DelegatedTokenAudience(ctx, testProofKeySource, replacement)
	if err != nil || other == audience {
		t.Fatalf("replacement UID did not change token audience: %v", err)
	}
}

func TestDelegatedAudienceWindowBoundsReissuedOldAudienceLifetime(t *testing.T) {
	ctx := context.Background()
	sa := hubMintedDelegatedServiceAccount(t, tenantPathFor(delegatedTestOrg, delegatedTestWS), delegatedTestUser, "victim-uid")
	issuedAt := time.Unix(1_800_000_000, 0).UTC()
	oldAudience, err := delegatedTokenAudienceAt(ctx, testProofKeySource, sa, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var request authnv1.TokenReview
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Model a co-member using the copied audience to mint a fresh token
		// long after the original hub-issued token expired.
		accepted := containsString(request.Spec.Audiences, oldAudience)
		var audiences []string
		if accepted {
			audiences = []string{oldAudience}
		}
		_ = json.NewEncoder(w).Encode(&authnv1.TokenReview{TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}, Status: authnv1.TokenReviewStatus{Authenticated: accepted, Audiences: audiences, User: authnv1.UserInfo{Username: "system:serviceaccount:default:" + sa.Name, UID: string(sa.UID)}}})
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "reissued token within ten minute lifetime", now: issuedAt.Add(10 * time.Minute), want: true},
		{name: "reissued token after audience window", now: issuedAt.Add(11 * time.Minute), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyDelegatedTokenAudience(ctx, client, "reissued-token", sa, testProofKeySource, "system:serviceaccount:default:"+sa.Name, tc.now)
			if (err == nil) != tc.want {
				t.Fatalf("verification error = %v, want success %v", err, tc.want)
			}
		})
	}
}
