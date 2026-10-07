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

package restapi

import (
	"fmt"
	"net/http"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestDeleteMCPServerObservedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, query, uid string
		status, calls    int
		conflict         bool
	}{
		{name: "observed object", query: "?uid=observed-uid", uid: "observed-uid", status: 204, calls: 1},
		{name: "legacy caller", status: 204, calls: 1},
		{name: "empty identity", query: "?uid=", status: 400},
		{name: "duplicate identity", query: "?uid=one&uid=two", status: 400},
		{name: "replacement conflict", query: "?uid=old-uid", uid: "old-uid", status: 409, calls: 1, conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, ops, _ := newTestManager(t)
			ops.childWorkspaces["org-a"] = map[string]bool{"ws-1": true}
			if tc.conflict {
				ops.mcpDeleteErr = apierrors.NewConflict(schema.GroupResource{Group: "railgrid.ai", Resource: "mcpservers"}, "ops", fmt.Errorf("UID changed"))
			}
			srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
			defer srv.Close()
			req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/workspaces/ws-1/mcpservers/ops"+tc.query, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, tc.status)
			}
			ops.mu.Lock()
			defer ops.mu.Unlock()
			if ops.mcpDeleteCalls != tc.calls || ops.mcpDeleteUID != tc.uid {
				t.Fatalf("deletion calls = %d, UID = %q; want %d, %q", ops.mcpDeleteCalls, ops.mcpDeleteUID, tc.calls, tc.uid)
			}
		})
	}
}
