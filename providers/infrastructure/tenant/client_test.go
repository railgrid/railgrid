// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestConfigForRejectsUnsafeLogicalClusterIDs(t *testing.T) {
	factory := NewClientFactory(&rest.Config{Host: "https://hub.example"})
	for _, clusterID := range []string{".", "..", "ws/path", "ws?query", "ws#fragment", "ws%2Fpath", "ws\nother"} {
		t.Run(clusterID, func(t *testing.T) {
			if _, err := factory.configFor(clusterID, "caller-token"); err == nil {
				t.Fatalf("cluster ID %q must be rejected", clusterID)
			}
		})
	}
	cfg, err := factory.configFor("root:railgrid-org_1.2", "caller-token")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cfg.Host, "/clusters/root:railgrid-org_1.2") {
		t.Fatalf("cluster host = %q", cfg.Host)
	}
}

// DoVerb reaches a data-plane verb on the front door as the caller: the kube
// path is appended to the same host the tenant clients use, the credential is
// the caller's token and nothing else, and an Authorization header a caller
// tried to smuggle in through the extras never reaches the wire.
func TestDoVerbSendsTheKubePathAsTheCaller(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotExtra string
	var gotAuthCount int
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotAuthCount = len(r.Header.Values("Authorization"))
		gotExtra = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer front.Close()

	f := NewClientFactory(&rest.Config{Host: front.URL + "/clusters/root:providers:infra", BearerToken: "provider-token"})
	path := "/clusters/aaaaaaaaaaaaaaaa/apis/infrastructure.railgrid.ai/v1alpha1/instances/app/sync?component=backend"
	headers := http.Header{"Idempotency-Key": []string{"run-1"}, "Authorization": []string{"Bearer forged"}}
	resp, err := f.DoVerb(context.Background(), "caller-token", http.MethodPost, path, strings.NewReader(`{}`), headers)
	if err != nil {
		t.Fatalf("DoVerb: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if gotPath != "/clusters/aaaaaaaaaaaaaaaa/apis/infrastructure.railgrid.ai/v1alpha1/instances/app/sync" || gotQuery != "component=backend" {
		t.Errorf("front door saw %s?%s, want the kube path with its component query", gotPath, gotQuery)
	}
	if gotAuth != "Bearer caller-token" || gotAuthCount != 1 {
		t.Errorf("Authorization = %q (x%d), want only the caller's bearer", gotAuth, gotAuthCount)
	}
	if gotExtra != "run-1" {
		t.Errorf("Idempotency-Key = %q, want the verb's own header forwarded", gotExtra)
	}
}

func TestDoVerbRefusesWhatIsNotAVerb(t *testing.T) {
	f := NewClientFactory(&rest.Config{Host: "https://hub.example"})
	if _, err := f.DoVerb(context.Background(), "", http.MethodGet, "/clusters/a/apis/g/v/r/n/verb", nil, nil); err == nil {
		t.Error("an empty token must be refused")
	}
	if _, err := f.DoVerb(context.Background(), "tok", http.MethodGet, "/services/providers/infrastructure/dataplane/x", nil, nil); err == nil {
		t.Error("the retired hub-proxied grammar must be refused")
	}
	var nilFactory *ClientFactory
	if _, err := nilFactory.DoVerb(context.Background(), "tok", http.MethodGet, "/clusters/a/apis/g/v/r/n/verb", nil, nil); err == nil {
		t.Error("a nil factory must be refused")
	}
}
