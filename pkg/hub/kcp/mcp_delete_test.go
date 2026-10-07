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

package kcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

func TestMCPInfoIncludesObjectIdentity(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "ops", "uid": "observed-uid"}}}
	if got := mcpInfoFrom(obj).UID; got != "observed-uid" {
		t.Fatalf("UID = %q", got)
	}
}

func TestDeleteMCPServerUIDPrecondition(t *testing.T) {
	for _, tc := range []struct {
		name, expectedUID, readUID, currentUID string
		wantGet, conflict                      bool
	}{
		{name: "observed identity", expectedUID: "current", currentUID: "current"},
		{name: "replacement survives", expectedUID: "old", currentUID: "replacement", conflict: true},
		{name: "legacy reads identity", readUID: "current", currentUID: "current", wantGet: true},
		{name: "legacy replacement survives", readUID: "old", currentUID: "replacement", wantGet: true, conflict: true},
		{name: "missing server identity refuses delete", wantGet: true, conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gets, deletes := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					gets++
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"apiVersion": "railgrid.ai/v1alpha1", "kind": "MCPServer", "metadata": map[string]interface{}{"name": "ops", "uid": tc.readUID}})
					return
				}
				if r.Method != http.MethodDelete {
					t.Errorf("unexpected method %s", r.Method)
					w.WriteHeader(405)
					return
				}
				deletes++
				var options metav1.DeleteOptions
				if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if options.Preconditions == nil || options.Preconditions.UID == nil {
					t.Error("DELETE has no UID precondition")
					w.WriteHeader(400)
					return
				}
				if string(*options.Preconditions.UID) != tc.currentUID {
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: metav1.StatusReasonConflict, Code: 409, Message: "UID changed"})
					return
				}
				_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Success", Code: 200})
			}))
			defer srv.Close()
			err := NewBootstrapper(&rest.Config{Host: srv.URL}).DeleteMCPServer(context.Background(), "test-cluster", "ops", tc.expectedUID)
			if apierrors.IsConflict(err) != tc.conflict || (err != nil && !tc.conflict) {
				t.Fatalf("delete error = %v, conflict = %v", err, tc.conflict)
			}
			if (gets == 1) != tc.wantGet {
				t.Fatalf("GET count = %d", gets)
			}
			wantDeletes := 1
			if tc.readUID == "" && tc.expectedUID == "" {
				wantDeletes = 0
			}
			if deletes != wantDeletes {
				t.Fatalf("DELETE count = %d, want %d", deletes, wantDeletes)
			}
		})
	}
}
