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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"
)

type clientTestCall struct {
	gvr     schema.GroupVersionResource
	request dataplane.Request
}

type clientTestCaller struct {
	endpoint            string
	httpClient          *http.Client
	exportCalls         []clientTestCall
	providerClientCalls int
}

func (c *clientTestCaller) ExportVerbURL(_ context.Context, gvr schema.GroupVersionResource, request dataplane.Request) (string, error) {
	c.exportCalls = append(c.exportCalls, clientTestCall{gvr: gvr, request: request})
	return dataplane.SubresourceURL(c.endpoint, gvr.Group, gvr.Version, request)
}

func (c *clientTestCaller) ProviderHTTPClient() (*http.Client, error) {
	c.providerClientCalls++
	return c.httpClient, nil
}

type clientTestWireCall struct {
	method string
	path   string
	input  map[string]json.RawMessage
}

func serveClientTestAction(t *testing.T, result any) (*httptest.Server, *[]clientTestWireCall) {
	t.Helper()
	calls := new([]clientTestWireCall)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		var envelope struct {
			Input map[string]json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("decode action input: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		*calls = append(*calls, clientTestWireCall{method: r.Method, path: r.URL.Path, input: envelope.Input})
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"result": result}); err != nil {
			t.Errorf("encode action result: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func decodeClientTestInput[T any](t *testing.T, input map[string]json.RawMessage, key string) T {
	t.Helper()
	var value T
	raw, ok := input[key]
	if !ok {
		t.Fatalf("action input omitted %q: %#v", key, input)
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode action input %q: %v", key, err)
	}
	return value
}

func TestStageBundleValidationDoesNotCallCode(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{name: "missing repository UID", req: Request{Files: []File{{Path: "a.txt", Content: "a"}}}},
		{name: "missing files", req: Request{RepositoryUID: "repo-uid"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller := &clientTestCaller{endpoint: "http://127.0.0.1:1", httpClient: http.DefaultClient}
			_, err := (&Client{Callers: caller}).StageBundle(context.Background(), tt.req)
			if err == nil {
				t.Fatal("StageBundle succeeded with invalid input")
			}
			if len(caller.exportCalls) != 0 || caller.providerClientCalls != 0 {
				t.Fatalf("invalid StageBundle input reached Code: exports=%d clients=%d", len(caller.exportCalls), caller.providerClientCalls)
			}
		})
	}
}

func TestStageBundleSendsOnlyTheStagedBundleInput(t *testing.T) {
	server, calls := serveClientTestAction(t, StagedBundle{
		BundleRef: "bundle-a", BundleDigest: "sha256:bundle-a", FileCount: 1, Size: 7,
	})
	caller := &clientTestCaller{endpoint: server.URL, httpClient: server.Client()}
	client := &Client{Callers: caller}
	wantFiles := []File{{Path: "app.txt", Content: "content", Encoding: "utf-8"}}
	staged, err := client.StageBundle(context.Background(), Request{
		Cluster: "tenant-cluster", RepositoryRef: "repo-a", RepositoryUID: "repo-uid-a", Files: wantFiles,
	})
	if err != nil {
		t.Fatalf("StageBundle: %v", err)
	}
	if staged.BundleRef != "bundle-a" || staged.BundleDigest != "sha256:bundle-a" {
		t.Fatalf("staged bundle = %+v", staged)
	}
	if len(*calls) != 1 || len(caller.exportCalls) != 1 || caller.providerClientCalls != 1 {
		t.Fatalf("Code calls: wire=%d export=%d client=%d; want one each", len(*calls), len(caller.exportCalls), caller.providerClientCalls)
	}
	call := (*calls)[0]
	if call.method != http.MethodPost || !strings.HasSuffix(call.path, "/repositories/repo-a/"+StageBundleAction) {
		t.Fatalf("stage request = %s %s", call.method, call.path)
	}
	if got := caller.exportCalls[0]; got.gvr != RepositoriesGVR || got.request.ClusterID != "tenant-cluster" || got.request.Resource != "repositories" || got.request.Name != "repo-a" || got.request.Verb != StageBundleAction {
		t.Fatalf("stage export request = %+v", got)
	}
	if len(call.input) != 2 {
		t.Fatalf("stage input keys = %#v, want only repositoryUID and files", call.input)
	}
	if got := decodeClientTestInput[string](t, call.input, "repositoryUID"); got != "repo-uid-a" {
		t.Fatalf("repositoryUID = %q", got)
	}
	if got := decodeClientTestInput[[]File](t, call.input, "files"); !reflect.DeepEqual(got, wantFiles) {
		t.Fatalf("files = %#v, want %#v", got, wantFiles)
	}
}

func TestKeyedCommitSendsStagedHandleWithoutRestaging(t *testing.T) {
	server, calls := serveClientTestAction(t, map[string]any{"commit": Ref{Name: "commit-a", UID: "commit-uid-a"}})
	caller := &clientTestCaller{endpoint: server.URL, httpClient: server.Client()}
	client := &Client{Callers: caller}
	ref, err := client.Commit(context.Background(), Request{
		Cluster: "tenant-cluster", RepositoryRef: "repo-a", RepositoryUID: "repo-uid-a",
		RequestID: "request-a", BundleRef: "bundle-a", BundleDigest: "sha256:bundle-a",
		Message: "Update app", Branch: "main",
	})
	if err != nil {
		t.Fatalf("keyed Commit: %v", err)
	}
	if ref.Name != "commit-a" || ref.UID != "commit-uid-a" {
		t.Fatalf("commit ref = %+v", ref)
	}
	if len(*calls) != 1 || len(caller.exportCalls) != 1 || caller.providerClientCalls != 1 {
		t.Fatalf("keyed commit caused implicit restaging: wire=%d export=%d client=%d", len(*calls), len(caller.exportCalls), caller.providerClientCalls)
	}
	call := (*calls)[0]
	if call.method != http.MethodPost || !strings.HasSuffix(call.path, "/repositories/repo-a/"+Action) {
		t.Fatalf("commit request = %s %s", call.method, call.path)
	}
	if got := caller.exportCalls[0]; got.gvr != RepositoriesGVR || got.request.ClusterID != "tenant-cluster" || got.request.Resource != "repositories" || got.request.Name != "repo-a" || got.request.Verb != Action {
		t.Fatalf("commit export request = %+v", got)
	}
	if len(call.input) != 6 {
		t.Fatalf("keyed commit input keys = %#v, want UID, request, bundle handle, message and branch", call.input)
	}
	for key, want := range map[string]string{
		"repositoryUID": "repo-uid-a", "requestID": "request-a", "bundleRef": "bundle-a",
		"bundleDigest": "sha256:bundle-a", "message": "Update app", "branch": "main",
	} {
		if got := decodeClientTestInput[string](t, call.input, key); got != want {
			t.Errorf("input %s = %q, want %q", key, got, want)
		}
	}
	if _, ok := call.input["files"]; ok {
		t.Fatalf("keyed commit resent mutable files: %#v", call.input["files"])
	}
}

func TestCommitRejectsInvalidInputsBeforeCallingCode(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{
			name: "missing request ID for staged-only input",
			req:  Request{RepositoryUID: "repo-uid", BundleRef: "bundle-a", BundleDigest: "sha256:bundle-a"},
		},
		{
			name: "keyed commit includes mutable files",
			req:  Request{RepositoryUID: "repo-uid", RequestID: "request-a", BundleRef: "bundle-a", BundleDigest: "sha256:bundle-a", Files: []File{{Path: "a.txt", Content: "a"}}},
		},
		{
			name: "keyed commit missing bundle reference",
			req:  Request{RepositoryUID: "repo-uid", RequestID: "request-a", BundleDigest: "sha256:bundle-a"},
		},
		{
			name: "keyed commit missing bundle digest",
			req:  Request{RepositoryUID: "repo-uid", RequestID: "request-a", BundleRef: "bundle-a"},
		},
		{
			name: "blank request ID",
			req:  Request{RepositoryUID: "repo-uid", RequestID: "  ", BundleRef: "bundle-a", BundleDigest: "sha256:bundle-a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller := &clientTestCaller{endpoint: "http://127.0.0.1:1", httpClient: http.DefaultClient}
			_, err := (&Client{Callers: caller}).Commit(context.Background(), tt.req)
			if err == nil {
				t.Fatal("Commit succeeded with invalid input")
			}
			if len(caller.exportCalls) != 0 || caller.providerClientCalls != 0 {
				t.Fatalf("invalid Commit input reached Code: exports=%d clients=%d", len(caller.exportCalls), caller.providerClientCalls)
			}
		})
	}
}
