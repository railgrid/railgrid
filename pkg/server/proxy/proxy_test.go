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

package proxy

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestResolveKCPPath tests the path-routing logic used by serveOIDC and
// serveStaticToken to scope incoming requests to the correct tenant workspace.
//
// This covers the fix for issue #65: bare paths (/api/..., /apis/...) must be
// routed to the user's default cluster, not forwarded to kcp root.
func TestResolveKCPPath(t *testing.T) {
	const defaultCluster = "root:tenant-abc"

	tests := []struct {
		name           string
		urlPath        string
		defaultCluster string
		wantPath       string
		wantStatus     int // 0 means success
		wantMsgSubstr  string
	}{
		// ── issue #65 regression: bare paths must route to the user workspace ───
		{
			name:           "bare /api path routes to user workspace",
			urlPath:        "/apis/v1/pods",
			defaultCluster: defaultCluster,
			wantPath:       "/clusters/" + defaultCluster + "/apis/v1/pods",
			wantStatus:     0,
		},
		{
			name:           "bare /apis path routes to user workspace",
			urlPath:        "/apis/apps/v1/deployments",
			defaultCluster: defaultCluster,
			wantPath:       "/clusters/" + defaultCluster + "/apis/apps/v1/deployments",
			wantStatus:     0,
		},
		{
			name:           "bare path with empty defaultCluster returns 403",
			urlPath:        "/apis/v1/pods",
			defaultCluster: "",
			wantStatus:     http.StatusForbidden,
			wantMsgSubstr:  "user has no default cluster",
		},
		// ── /clusters/{id}/... paths ─────────────────────────────────────────────
		{
			name:           "cluster-syntax path with matching cluster passes through",
			urlPath:        "/clusters/" + defaultCluster + "/apis/v1/pods",
			defaultCluster: defaultCluster,
			wantPath:       "/clusters/" + defaultCluster + "/apis/v1/pods",
			wantStatus:     0,
		},
		{
			name:           "cluster-syntax path with mount suffix passes through",
			urlPath:        "/clusters/" + defaultCluster + ":mount1/api/v1/pods",
			defaultCluster: defaultCluster,
			wantPath:       "/clusters/" + defaultCluster + ":mount1/api/v1/pods",
			wantStatus:     0,
		},
		{
			name:           "cluster-syntax path with wrong cluster returns 403",
			urlPath:        "/clusters/root:other-tenant/api/v1/pods",
			defaultCluster: defaultCluster,
			wantStatus:     http.StatusForbidden,
			wantMsgSubstr:  "cluster access denied",
		},
		{
			name:           "cluster-syntax path with no trailing slash (bare cluster) passes",
			urlPath:        "/clusters/" + defaultCluster,
			defaultCluster: defaultCluster,
			wantPath:       "/clusters/" + defaultCluster,
			wantStatus:     0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, gotStatus, gotBody := resolveKCPPath(tc.urlPath, tc.defaultCluster)

			if tc.wantStatus == 0 {
				// Expect success.
				if gotStatus != 0 {
					t.Fatalf("expected success (status 0), got status %d body %q", gotStatus, gotBody)
				}
				if gotPath != tc.wantPath {
					t.Errorf("kcpPath: got %q, want %q", gotPath, tc.wantPath)
				}
			} else {
				// Expect error.
				if gotStatus != tc.wantStatus {
					t.Errorf("status: got %d, want %d", gotStatus, tc.wantStatus)
				}
				if gotPath != "" {
					t.Errorf("expected empty kcpPath on error, got %q", gotPath)
				}
				// Verify the response body is valid JSON and contains the expected message.
				var status map[string]interface{}
				if err := json.Unmarshal([]byte(gotBody), &status); err != nil {
					t.Fatalf("response body is not valid JSON: %v — body: %q", err, gotBody)
				}
				msg, _ := status["message"].(string)
				if tc.wantMsgSubstr != "" && msg != tc.wantMsgSubstr {
					t.Errorf("message: got %q, want %q", msg, tc.wantMsgSubstr)
				}
			}
		})
	}
}
