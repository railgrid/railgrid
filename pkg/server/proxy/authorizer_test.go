/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package proxy

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// fakeAuthorizer builds a clusterAuthorizer over in-memory fixtures. entries is
// the caller's UserMembershipIndex; resolve maps "org/ws" → clusterID; children
// maps org → child workspace UUIDs.
func fakeAuthorizer(entries []tenancyv1alpha1.MembershipIndexEntry, resolve map[string]string, children map[string][]string) *clusterAuthorizer {
	members := func(_ context.Context, _ string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		return &tenancyv1alpha1.UserMembershipIndex{
			Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: entries},
		}, nil
	}
	res := func(_ context.Context, org, ws string) (string, error) {
		if cid, ok := resolve[org+"/"+ws]; ok {
			return cid, nil
		}
		return "", fmt.Errorf("no cluster for %s/%s", org, ws)
	}
	ch := func(_ context.Context, org string) ([]string, error) {
		return children[org], nil
	}
	return newClusterAuthorizer(members, res, ch)
}

func wsEntry(org, ws string) tenancyv1alpha1.MembershipIndexEntry {
	return tenancyv1alpha1.MembershipIndexEntry{OrgUUID: org, WorkspaceUUID: ws, Role: "admin"}
}

func orgEntry(org string) tenancyv1alpha1.MembershipIndexEntry {
	return tenancyv1alpha1.MembershipIndexEntry{OrgUUID: org, Role: "admin"}
}

func TestClusterAuthorizer(t *testing.T) {
	tests := []struct {
		name      string
		entries   []tenancyv1alpha1.MembershipIndexEntry
		resolve   map[string]string
		children  map[string][]string
		clusterID string
		want      bool
	}{
		{
			name:      "workspace-scope member is allowed",
			entries:   []tenancyv1alpha1.MembershipIndexEntry{wsEntry("o1", "w1")},
			resolve:   map[string]string{"o1/w1": "cidA"},
			clusterID: "cidA",
			want:      true,
		},
		{
			name:      "non-member is denied",
			entries:   []tenancyv1alpha1.MembershipIndexEntry{wsEntry("o1", "w1")},
			resolve:   map[string]string{"o1/w1": "cidA"},
			clusterID: "cidB",
			want:      false,
		},
		{
			name:      "org-scope member reaches every child workspace",
			entries:   []tenancyv1alpha1.MembershipIndexEntry{orgEntry("o1")},
			children:  map[string][]string{"o1": {"w1", "w2"}},
			resolve:   map[string]string{"o1/w1": "cidA", "o1/w2": "cidB"},
			clusterID: "cidB",
			want:      true,
		},
		{
			name:      "org-scope member denied a cluster outside the org",
			entries:   []tenancyv1alpha1.MembershipIndexEntry{orgEntry("o1")},
			children:  map[string][]string{"o1": {"w1"}},
			resolve:   map[string]string{"o1/w1": "cidA"},
			clusterID: "cidZ",
			want:      false,
		},
		{
			name:      "edge under a member workspace is allowed",
			entries:   []tenancyv1alpha1.MembershipIndexEntry{wsEntry("o1", "w1")},
			resolve:   map[string]string{"o1/w1": "cidA"},
			clusterID: "cidA:edge1",
			want:      true,
		},
		{
			name:      "edge under a non-member workspace is denied",
			entries:   []tenancyv1alpha1.MembershipIndexEntry{wsEntry("o1", "w1")},
			resolve:   map[string]string{"o1/w1": "cidA"},
			clusterID: "cidB:edge1",
			want:      false,
		},
		{
			name:      "cross-org isolation: member of o1 cannot reach o2's cluster",
			entries:   []tenancyv1alpha1.MembershipIndexEntry{wsEntry("o1", "w1")},
			resolve:   map[string]string{"o1/w1": "cidA", "o2/w9": "cidOther"},
			clusterID: "cidOther",
			want:      false,
		},
		{
			name:      "empty cluster id is denied",
			entries:   []tenancyv1alpha1.MembershipIndexEntry{wsEntry("o1", "w1")},
			resolve:   map[string]string{"o1/w1": "cidA"},
			clusterID: "",
			want:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := fakeAuthorizer(tc.entries, tc.resolve, tc.children)
			if got := a.authorize(context.Background(), "user", tc.clusterID); got != tc.want {
				t.Errorf("authorize(%q) = %v, want %v", tc.clusterID, got, tc.want)
			}
		})
	}
}

func TestAuthorizeKCPPath(t *testing.T) {
	p := &KCPProxy{
		logger: klog.Background(),
		authorizer: fakeAuthorizer(
			[]tenancyv1alpha1.MembershipIndexEntry{wsEntry("o1", "w1")},
			map[string]string{"o1/w1": "cidA"},
			nil,
		),
	}

	tests := []struct {
		name       string
		urlPath    string
		wantStatus int
		wantPath   string
	}{
		{
			name:       "bare /apis path is rejected (no default)",
			urlPath:    "/apis/v1/pods",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "bare /api path is rejected (no default)",
			urlPath:    "/api/v1/namespaces",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "org workspace path is refused (O-10)",
			urlPath:    "/clusters/root:kedge:tenants:org1/api/v1/pods",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "tenant path-form is refused (address by id)",
			urlPath:    "/clusters/root:kedge:tenants:org1:ws1/api/v1/pods",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "member cluster id passes through unchanged",
			urlPath:    "/clusters/cidA/apis/v1/pods",
			wantStatus: 0,
			wantPath:   "/clusters/cidA/apis/v1/pods",
		},
		{
			name:       "member edge passes through unchanged",
			urlPath:    "/clusters/cidA:edge1/apis/v1/pods",
			wantStatus: 0,
			wantPath:   "/clusters/cidA:edge1/apis/v1/pods",
		},
		{
			name:       "non-member cluster id is denied",
			urlPath:    "/clusters/cidB/apis/v1/pods",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, gotStatus, gotBody := p.authorizeKCPPath(context.Background(), "user", tc.urlPath)
			if gotStatus != tc.wantStatus {
				t.Fatalf("status = %d (body %q), want %d", gotStatus, gotBody, tc.wantStatus)
			}
			if tc.wantStatus == 0 && gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
			if tc.wantStatus != 0 && gotPath != "" {
				t.Errorf("expected empty path on denial, got %q", gotPath)
			}
		})
	}
}
