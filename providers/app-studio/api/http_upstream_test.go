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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-app-studio/internal/codecommit"
)

// A non-2xx answer from the code provider on a call App Studio makes as
// itself keeps its status: a refused checkout is a 403 with the claim hint,
// not a 500, so the caller can tell a binding gap from a crash.
func TestWriteUpstreamErrorMapsCodeProviderStatus(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		status   int
		contains string
	}{
		{"checkout refused", &codecommit.StatusError{Action: "checkout", Status: http.StatusForbidden}, http.StatusForbidden, "App Studio binding"},
		{"missing repository", &codecommit.StatusError{Action: "checkout", Status: http.StatusNotFound}, http.StatusNotFound, "checkout: HTTP 404"},
		{"other status", &codecommit.StatusError{Action: "commit", Status: http.StatusBadGateway}, http.StatusInternalServerError, "commit: HTTP 502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeUpstreamError(rec, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			var body struct{ Message string }
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
			}
			if !strings.Contains(body.Message, tc.contains) {
				t.Fatalf("message %q does not contain %q", body.Message, tc.contains)
			}
		})
	}
}
