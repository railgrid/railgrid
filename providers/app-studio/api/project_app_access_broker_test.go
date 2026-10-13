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

package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAppAccessBrokerAcceptsMigrationHintAndKeepsStrictResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		wantError   bool
	}{
		{"pending migration", `,"migrationPending":true`, false},
		{"malformed migration hint", `,"migrationPending":"yes"`, true},
		{"unknown response field", `,"unexpected":true`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/orgs/org-a/workspaces/ws-a/app-auth/grants" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"items":[{"name":"grant-demo-prod-outside","user":"outside","publication":"demo-prod","phase":"Active","revoked":false`+tc.extra+`}]}`)
			}))
			defer hub.Close()
			server := &Server{hubBase: hub.URL, hubToken: "provider-test-token"}
			items, err := server.callAppAccessBroker(context.Background(), identity{orgUUID: "org-a", workspaceUUID: "ws-a", actionProof: "signed-test-proof"}, appAccessBrokerRequest{
				Operation: appAccessBrokerList, Group: "infrastructure.railgrid.ai", Resource: "instances", Name: "demo-prod", UID: "instance-uid", ProjectName: "demo", ProjectUID: "project-uid",
			})
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%v", err, tc.wantError)
			}
			if !tc.wantError && (len(items) != 1 || items[0].User != "outside" || !items[0].MigrationPending) {
				t.Fatalf("items=%#v", items)
			}
		})
	}
}
