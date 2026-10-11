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

package codecommit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"
)

func TestClassifyPermanentCommitReplayFailure(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		reason  string
		blocked bool
	}{
		{
			name:    "request ID conflict",
			err:     &ActionError{Action: Action, Code: "commit_request_conflict", Status: http.StatusConflict},
			reason:  "CommitRequestConflict",
			blocked: true,
		},
		{
			name:    "missing immutable bundle from commit action",
			err:     &ActionError{Action: Action, Code: "bundle_unavailable", Status: http.StatusUnprocessableEntity},
			reason:  "StagedBundleMissing",
			blocked: true,
		},
		{
			name:    "forbidden action",
			err:     &ActionError{Action: Action, Code: "action_forbidden", Status: http.StatusForbidden},
			reason:  "ActionForbidden",
			blocked: true,
		},
		{
			name:    "bare forbidden response",
			err:     &StatusError{Action: Action, Status: http.StatusForbidden},
			reason:  "ActionForbidden",
			blocked: true,
		},
		{
			name:    "retryable capacity refusal",
			err:     &ActionError{Action: Action, Code: "bundle_quota_exceeded", Retryable: true, Status: http.StatusTooManyRequests},
			blocked: false,
		},
		{
			name:    "generic commit backend failure",
			err:     &ActionError{Action: Action, Code: "commit_not_created", Status: http.StatusBadGateway},
			blocked: false,
		},
		{
			name:    "bundle unavailable during staging",
			err:     &ActionError{Action: StageBundleAction, Code: "bundle_unavailable", Status: http.StatusUnprocessableEntity},
			blocked: false,
		},
		{
			name:    "temporary transport status",
			err:     &StatusError{Action: Action, Status: http.StatusBadGateway},
			blocked: false,
		},
		{
			name:    "wrapped generic backend failure",
			err:     errors.New("commit: commit_not_created"),
			blocked: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, message, blocked := ClassifyPermanentCommitReplayFailure(tt.err)
			if blocked != tt.blocked {
				t.Fatalf("blocked = %t, want %t (reason=%q message=%q)", blocked, tt.blocked, reason, message)
			}
			if blocked {
				if reason != tt.reason || message == "" || len(message) > 512 {
					t.Fatalf("classification = %q, %q; want reason %q and bounded user message", reason, message, tt.reason)
				}
			}
		})
	}
}

type codecommitTestCaller struct {
	endpoint string
	client   *http.Client
}

func (c codecommitTestCaller) ExportVerbURL(context.Context, schema.GroupVersionResource, dataplane.Request) (string, error) {
	return c.endpoint, nil
}

func (c codecommitTestCaller) ProviderHTTPClient() (*http.Client, error) {
	return c.client, nil
}

func TestCommitPreservesRetryableActionwireFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"bundle_quota_exceeded","message":"temporary capacity","retryable":true}}`))
	}))
	defer server.Close()

	client := &Client{Callers: codecommitTestCaller{endpoint: server.URL, client: server.Client()}}
	_, err := client.Commit(context.Background(), Request{
		Cluster: "cluster", RepositoryRef: "repo", RepositoryUID: "repo-uid",
		RequestID: "request-a", BundleRef: "bundle-a", BundleDigest: "sha256:bundle-a",
	})
	var actionErr *ActionError
	if !errors.As(err, &actionErr) {
		t.Fatalf("Commit error = %T %v, want ActionError", err, err)
	}
	if actionErr.Code != "bundle_quota_exceeded" || !actionErr.Retryable || actionErr.Status != http.StatusTooManyRequests {
		t.Fatalf("decoded action error = %+v", actionErr)
	}
	if _, _, blocked := ClassifyPermanentCommitReplayFailure(err); blocked {
		t.Fatal("retryable capacity error was classified as a permanent commit block")
	}
}
