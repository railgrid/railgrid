// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/faroshq/provider-sdk/tenantaccess"
)

// The org/workspace scope is resolved from kcp through the workspace lookup;
// without one, or without the credentials to run it, identity records why
// instead of guessing from a header.
func TestResolveWorkspace(t *testing.T) {
	lookupErr := errors.New("kcp said no")
	cases := []struct {
		name    string
		server  *Server
		id      identity
		wantOrg string
		wantWS  string
		wantErr error
	}{
		{
			name:    "no lookup wired",
			server:  &Server{},
			id:      identity{clusterID: "c1", token: "t"},
			wantErr: errNoWorkspaceLookup,
		},
		{
			name: "no token on the request",
			server: &Server{workspaces: staticWorkspaces{
				"c1": {ClusterID: "c1", Path: "root:faros:tenants:o:w", OrgUUID: "o", WorkspaceUUID: "w"},
			}.lookup},
			id:      identity{clusterID: "c1"},
			wantErr: errNoWorkspaceLookup,
		},
		{
			name: "resolved",
			server: &Server{workspaces: staticWorkspaces{
				"c1": {ClusterID: "c1", Path: "root:faros:tenants:o:w", OrgUUID: "o", WorkspaceUUID: "w"},
			}.lookup},
			id:      identity{clusterID: "c1", token: "t"},
			wantOrg: "o", wantWS: "w",
		},
		{
			name: "organization workspace",
			server: &Server{workspaces: staticWorkspaces{
				"org": {ClusterID: "org", Path: "root:faros:tenants:o", OrgUUID: "o"},
			}.lookup},
			id:      identity{clusterID: "org", token: "t"},
			wantOrg: "o",
		},
		{
			name: "lookup fails",
			server: &Server{workspaces: func(context.Context, string, string) (tenantaccess.Workspace, error) {
				return tenantaccess.Workspace{}, lookupErr
			}},
			id:      identity{clusterID: "c1", token: "t"},
			wantErr: lookupErr,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := c.id
			c.server.resolveWorkspace(context.Background(), &id)
			if id.orgUUID != c.wantOrg || id.workspaceUUID != c.wantWS {
				t.Errorf("scope = (%q, %q), want (%q, %q)", id.orgUUID, id.workspaceUUID, c.wantOrg, c.wantWS)
			}
			if !errors.Is(id.workspaceErr, c.wantErr) {
				t.Errorf("workspaceErr = %v, want %v", id.workspaceErr, c.wantErr)
			}
		})
	}
}

func TestBearerToken(t *testing.T) {
	for auth, want := range map[string]string{
		"Bearer abc":  "abc",
		"bearer abc ": "abc",
		"Basic abc":   "",
		"":            "",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if got := bearerToken(r); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", auth, got, want)
		}
	}
}
