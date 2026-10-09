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
	endpoint, err := callers.ExportEndpointForCluster(context.Background(), "1v98kgkp03uox9qw")
	if err != nil {
		t.Fatalf("ExportEndpointForCluster: %v", err)
	}
	if want := "https://kcp.example:6443/services/apiexport/abc123/" + export; endpoint != want {
		t.Fatalf("endpoint = %q, want %q (trailing slash trimmed)", endpoint, want)
	}
	// Resolved once, then remembered.
	if _, err := callers.ExportEndpointForCluster(context.Background(), "1v98kgkp03uox9qw"); err != nil {
		t.Fatalf("second ExportEndpointForCluster: %v", err)
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
	endpoint, err := pinned.ExportEndpointForCluster(context.Background(), "1v98kgkp03uox9qw")
	if err != nil || endpoint != "https://kcp.example:6443/services/apiexport/abc/x" {
		t.Fatalf("pinned endpoint = %q err=%v", endpoint, err)
	}
}

// A sharded kcp publishes one export virtual-workspace URL per shard, and each
// one serves only the logical clusters its shard holds. Taking the first
// published URL worked for consumers on that shard and failed for every other
// with "cannot create resource subjectaccessreviews ... access denied", which
// is how every custom subresource of a cross-shard binding broke.
func TestAsProviderPicksTheEndpointThatServesTheCluster(t *testing.T) {
	const (
		export      = "edges.providers.railgrid.ai"
		onThisShard = "24192sxym7m5edtu"
	)

	// The shard that does NOT hold the consumer: kcp has no binding for it
	// there, so the claim does not apply and the review is refused.
	var wrongShardReviews atomic.Int32
	wrongShard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrongShardReviews.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","code":403,"reason":"Forbidden","message":"subjectaccessreviews.authorization.k8s.io is forbidden"}`))
	}))
	defer wrongShard.Close()

	var rightShardPaths []string
	rightShard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rightShardPaths = append(rightShardPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"authorization.k8s.io/v1","kind":"SubjectAccessReview","status":{"allowed":false}}`))
	}))
	defer rightShard.Close()

	var sliceReads atomic.Int32
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sliceReads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Wrong shard first, exactly as the live slice ordered them.
		_, _ = w.Write([]byte(`{"apiVersion":"apis.kcp.io/v1alpha1","kind":"APIExportEndpointSlice","metadata":{"name":"` + export + `"},"status":{"endpoints":[` +
			`{"url":"` + wrongShard.URL + `/services/apiexport/abc/` + export + `"},` +
			`{"url":"` + rightShard.URL + `/services/apiexport/abc/` + export + `"}]}}`))
	}))
	defer kcp.Close()

	base := &rest.Config{Host: kcp.URL + "/clusters/root:railgrid:providers:edges", BearerToken: "provider-token"}
	callers, err := NewCallerFactory(base, WithProviderConfig(base, export))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}

	client, err := callers.AsProvider(onThisShard)
	if err != nil {
		t.Fatalf("AsProvider: %v", err)
	}
	if client == nil {
		t.Fatal("AsProvider returned no client")
	}
	if wrongShardReviews.Load() != 1 {
		t.Fatalf("the refusing endpoint was tried %d times, want once", wrongShardReviews.Load())
	}
	if len(rightShardPaths) == 0 {
		t.Fatal("the endpoint that serves the cluster was never tried")
	}
	if want := "/services/apiexport/abc/" + export + "/clusters/" + onThisShard + "/apis/authorization.k8s.io/v1/subjectaccessreviews"; rightShardPaths[0] != want {
		t.Fatalf("probe path = %q, want %q", rightShardPaths[0], want)
	}

	// The choice is remembered: no second round of probing, and no second
	// slice read.
	before := wrongShardReviews.Load()
	if _, err := callers.AsProvider(onThisShard); err != nil {
		t.Fatalf("second AsProvider: %v", err)
	}
	if wrongShardReviews.Load() != before {
		t.Fatal("the refusing endpoint was probed again; the per-cluster choice is not cached")
	}
	if sliceReads.Load() != 1 {
		t.Fatalf("the slice was read %d times, want once", sliceReads.Load())
	}
}

// When no published endpoint serves the cluster, say so with both failures
// rather than returning a client that cannot work.
func TestAsProviderReportsWhenNoEndpointServesTheCluster(t *testing.T) {
	const export = "edges.providers.railgrid.ai"

	refuse := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","code":403,"reason":"Forbidden","message":"forbidden"}`))
		}))
	}
	a, b := refuse(), refuse()
	defer a.Close()
	defer b.Close()

	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"apis.kcp.io/v1alpha1","kind":"APIExportEndpointSlice","metadata":{"name":"` + export + `"},"status":{"endpoints":[` +
			`{"url":"` + a.URL + `/services/apiexport/abc/` + export + `"},` +
			`{"url":"` + b.URL + `/services/apiexport/abc/` + export + `"}]}}`))
	}))
	defer kcp.Close()

	base := &rest.Config{Host: kcp.URL + "/clusters/root:railgrid:providers:edges", BearerToken: "provider-token"}
	callers, err := NewCallerFactory(base, WithProviderConfig(base, export))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}
	_, err = callers.AsProvider("24192sxym7m5edtu")
	if err == nil {
		t.Fatal("AsProvider succeeded although no endpoint serves the cluster")
	}
	if !strings.Contains(err.Error(), "no export virtual workspace serves cluster") {
		t.Fatalf("err = %v, want it to name the cluster that is unserved", err)
	}
}
