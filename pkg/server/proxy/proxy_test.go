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
