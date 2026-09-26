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

package dataplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/rest"
)

// AsProvider must act through the export's virtual workspace, which the
// provider learns from its APIExportEndpointSlice: seen against
// kcp-dev/kcp#4388, a SubjectAccessReview sent to the shard's own
// /clusters/<tenant> as the provider ServiceAccount is refused (the provider
// has no RBAC inside a tenant workspace), while the virtual workspace serves
// SubjectAccessReview as a builtin and authorizes the provider as the export
// owner.
func TestAsProviderActsThroughTheExportVirtualWorkspace(t *testing.T) {
	const export = "quickstart.providers.railgrid.ai"
	var reads atomic.Int32
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clusters/root:railgrid:providers:quickstart/apis/apis.kcp.io/v1alpha1/apiexportendpointslices/"+export {
			t.Errorf("unexpected read %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer provider-token" {
			t.Errorf("slice read authenticated as %q, want the provider's own credential", got)
		}
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"apis.kcp.io/v1alpha1","kind":"APIExportEndpointSlice","metadata":{"name":"` + export + `"},"status":{"endpoints":[{"url":"https://kcp.example:6443/services/apiexport/abc123/` + export + `/"}]}}`))
	}))
	defer kcp.Close()

	base := &rest.Config{Host: kcp.URL + "/clusters/root:railgrid:providers:quickstart", BearerToken: "provider-token"}
	callers, err := NewCallerFactory(base, WithProviderConfig(base, export))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}
	endpoint, err := callers.exportEndpoint(context.Background())
	if err != nil {
		t.Fatalf("exportEndpoint: %v", err)
	}
	if want := "https://kcp.example:6443/services/apiexport/abc123/" + export; endpoint != want {
		t.Fatalf("endpoint = %q, want %q (trailing slash trimmed)", endpoint, want)
	}
	// Resolved once, then remembered.
	if _, err := callers.exportEndpoint(context.Background()); err != nil {
		t.Fatalf("second exportEndpoint: %v", err)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("the slice was read %d times, want once", got)
	}
	if _, err := callers.AsProvider("1v98kgkp03uox9qw"); err != nil {
		t.Fatalf("AsProvider: %v", err)
	}
}

func TestAsProviderNeedsAnExportOrAnEndpoint(t *testing.T) {
	base := &rest.Config{Host: "https://kcp.example:6443/clusters/root:x", BearerToken: "t"}
	callers, err := NewCallerFactory(base, WithProviderConfig(base, ""))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}
	if _, err := callers.AsProvider("1v98kgkp03uox9qw"); err == nil || !strings.Contains(err.Error(), "WithProviderEndpoint") {
		t.Fatalf("AsProvider with no export name: err = %v, want a refusal naming the missing option", err)
	}

	pinned, err := NewCallerFactory(base, WithProviderConfig(base, ""), WithProviderEndpoint("https://kcp.example:6443/services/apiexport/abc/x/"))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}
	endpoint, err := pinned.exportEndpoint(context.Background())
	if err != nil || endpoint != "https://kcp.example:6443/services/apiexport/abc/x" {
		t.Fatalf("pinned endpoint = %q err=%v", endpoint, err)
	}
}
