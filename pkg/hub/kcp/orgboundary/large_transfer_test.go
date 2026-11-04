// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package orgboundary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
)

// Existing provider transfer contracts must survive native admission.
func TestDefaultGuardAllowsExistingLargeTransfer(t *testing.T) {
	guard := testGuard(t, DefaultConfig())
	files := make([]map[string]any, 9)
	for i := range files {
		files[i] = map[string]any{"path": fmt.Sprintf("file-%d.txt", i), "content": strings.Repeat("a", 1<<20)}
	}
	body, err := json.Marshal(map[string]any{"input": map[string]any{"repositoryUID": "fixture-repo-uid", "files": files}})
	if err != nil {
		t.Fatal(err)
	}
	const codeInputLimit = 68 << 20 // providers/code/actions.MaxCommitBundleInputBytes
	if len(body) <= 8<<20 || len(body) >= codeInputLimit {
		t.Fatal("probe body outside intended bounds")
	}
	path := "/clusters/" + sameTargetID + "/apis/code.railgrid.ai/v1alpha1/repositories/repo/stage-commit-bundle"
	newRequest := func(chunked bool) *http.Request {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		if chunked {
			r.ContentLength = -1
			r.TransferEncoding = []string{"chunked"}
		}
		r = withScopeContext(r, &user.DefaultInfo{Name: "railgrid:member@example.test"}, request.Cluster{Name: logicalcluster.Name(sameTargetID)})
		info := &request.RequestInfo{IsResourceRequest: true, Verb: "create", APIGroup: "code.railgrid.ai", APIVersion: "v1alpha1", Resource: "repositories", Name: "repo", Subresource: "stage-commit-bundle"}
		return r.WithContext(request.WithRequestInfo(r.Context(), info))
	}
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked=%t", chunked), func(t *testing.T) {
			executions := 0
			provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				env := actionwire.New(r, "code", "stage-commit-bundle", actionwire.ResourceRef{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Repository", Resource: "repositories", Name: "repo"})
				dataplane.Serve(w, r, env, dataplane.Limits{MaxInputBytes: codeInputLimit}, func(context.Context, json.RawMessage) (any, *actionwire.Error) {
					executions++
					return map[string]any{"accepted": true}, nil
				})
			})
			control := httptest.NewRecorder()
			provider.ServeHTTP(control, newRequest(chunked))
			if control.Code != http.StatusOK || executions != 1 {
				t.Fatalf("positive control failed: HTTP%d exec%d: %s", control.Code, executions, control.Body.String())
			}
			bounded := httptest.NewRecorder()
			guard.Wrap(provider).ServeHTTP(bounded, newRequest(chunked))
			if bounded.Code != http.StatusOK || executions != 2 {
				t.Fatalf("expected supported transfer to execute: HTTP%d exec%d", bounded.Code, executions)
			}
			t.Logf("SDK action accepts %d bytes with 68MiB bound; default guard returns HTTP%d; chunked=%t", len(body), bounded.Code, chunked)
		})
	}
}
