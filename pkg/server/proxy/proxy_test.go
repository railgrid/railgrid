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
	"net/http/httptest"
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

// TestIsOrgWorkspacePath covers the structural rule that decides whether
// a kcp logical-cluster path targets an Organization workspace (which the
// proxy rejects per O-10) or a child team Workspace under one (which
// remains tenant-accessible).
func TestIsOrgWorkspacePath(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		wantOK bool
	}{
		{"org workspace UUID", "root:kedge:tenants:7f3a91d2-aaaa-bbbb-cccc-1111", true},
		{"org workspace short", "root:kedge:tenants:acme", true},
		{"child team workspace", "root:kedge:tenants:7f3a:9c4b", false},
		{"child team workspace nested", "root:kedge:tenants:acme:platform", false},
		{"system tenants object store", "root:kedge:system:tenants", false},
		{"providers workspace", "root:kedge:providers", false},
		{"root", "root", false},
		{"empty", "", false},
		{"tenants parent (no org)", "root:kedge:tenants:", false},
		{"tenants parent (no trailing colon)", "root:kedge:tenants", false},
		{"random workspace under root", "root:other", false},
		{"path traversal attempt", "root:kedge:tenants:foo/etc/passwd", true /* path has no colon → structural match; caller's regex strips traversal earlier */},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOrgWorkspacePath(tc.path); got != tc.wantOK {
				t.Errorf("isOrgWorkspacePath(%q) = %v, want %v", tc.path, got, tc.wantOK)
			}
		})
	}
}

// TestExtractClusterPathFromKCPPath covers the helper that pulls the
// logical-cluster portion out of a kcp-syntax URL path.
func TestExtractClusterPathFromKCPPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clusters with subpath", "/clusters/root:kedge:tenants:7f3a/api/v1/pods", "root:kedge:tenants:7f3a"},
		{"clusters with mount suffix", "/clusters/root:tenant:abc:mount1/api/v1/pods", "root:tenant:abc:mount1"},
		{"clusters bare (no subpath)", "/clusters/root:kedge:tenants:7f3a", "root:kedge:tenants:7f3a"},
		{"non-clusters path", "/api/v1/pods", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractClusterPathFromKCPPath(tc.in); got != tc.want {
				t.Errorf("extractClusterPathFromKCPPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestWriteOrgWorkspaceForbidden verifies the 403 envelope is a valid
// Kubernetes Status object so kubectl renders it nicely, and carries the
// kedge-specific reason + a pointer at the hub REST surface for CLI tooling.
func TestWriteOrgWorkspaceForbidden(t *testing.T) {
	w := httptest.NewRecorder()
	writeOrgWorkspaceForbidden(w)

	if w.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusForbidden)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", got)
	}
	var status map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("body is not valid JSON: %v — %q", err, w.Body.String())
	}
	if status["kind"] != "Status" || status["apiVersion"] != "v1" {
		t.Errorf("body is not a v1.Status envelope: %v", status)
	}
	if status["code"] != float64(403) {
		t.Errorf("code: got %v, want 403", status["code"])
	}
	if status["reason"] != "OrgWorkspaceNotDirectlyAccessible" {
		t.Errorf("reason: got %v, want OrgWorkspaceNotDirectlyAccessible", status["reason"])
	}
	msg, _ := status["message"].(string)
	if msg == "" {
		t.Error("message should be a non-empty hint about the hub REST surface")
	}
}
