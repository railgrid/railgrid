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
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// The published endpoint set is not fixed: kcp publishes a shard's URL only
// once the export has a consumer there, so it grows as workspaces on new
// shards enable the provider. Caching it for the life of the process meant a
// factory that first saw one shard never saw the second, and sent that
// shard's consumers to the one endpoint it knew -- the refusal this change
// exists to prevent.
func TestAsProviderPicksUpAShardPublishedLater(t *testing.T) {
	const (
		export     = "edges.providers.railgrid.ai"
		onNewShard = "24192sxym7m5edtu"
	)

	// Shard A is published from the start and does not serve the consumer.
	shardA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","code":403,"reason":"Forbidden","message":"forbidden"}`))
	}))
	defer shardA.Close()

	// Shard B serves it, and is published only later.
	var shardBCalls atomic.Int32
	shardB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		shardBCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"authorization.k8s.io/v1","kind":"SubjectAccessReview","status":{"allowed":false}}`))
	}))
	defer shardB.Close()

	var bPublished atomic.Bool
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eps := `{"url":"` + shardA.URL + `/services/apiexport/abc/` + export + `"}`
		if bPublished.Load() {
			eps += `,{"url":"` + shardB.URL + `/services/apiexport/abc/` + export + `"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"apis.kcp.io/v1alpha1","kind":"APIExportEndpointSlice","metadata":{"name":"` + export + `"},"status":{"endpoints":[` + eps + `]}}`))
	}))
	defer kcp.Close()

	base := &rest.Config{Host: kcp.URL + "/clusters/root:railgrid:providers:edges", BearerToken: "provider-token"}
	callers, err := NewCallerFactory(base, WithProviderConfig(base, export))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}

	// First lookup, while only shard A exists. One endpoint means nothing to
	// choose between, so A comes back and nothing is remembered for the
	// cluster.
	first, err := callers.endpointForCluster(t.Context(), onNewShard)
	if err != nil {
		t.Fatalf("first endpointForCluster: %v", err)
	}
	if !strings.HasPrefix(first, shardA.URL) {
		t.Fatalf("first endpoint = %q, want shard A while it is the only one published", first)
	}

	// The consumer's shard now has the export, so kcp publishes its URL.
	bPublished.Store(true)

	// Without a refresh this keeps returning shard A forever. The refresh
	// interval is squeezed rather than waited out.
	callers.providerMu.Lock()
	callers.endpointRefresh = 0
	callers.providerMu.Unlock()

	got, err := callers.endpointForCluster(t.Context(), onNewShard)
	if err != nil {
		t.Fatalf("endpointForCluster after the slice grew: %v", err)
	}
	if !strings.HasPrefix(got, shardB.URL) {
		t.Fatalf("endpoint = %q, want shard B once it is published", got)
	}
	if shardBCalls.Load() == 0 {
		t.Fatal("shard B was never probed; the endpoint set was not re-read")
	}
}

func TestAsProviderWithRateLimiterContextCancelsFirstEndpointLookup(t *testing.T) {
	const export = "edges.providers.railgrid.ai"
	var sliceReads atomic.Int32
	lookupStarted := make(chan struct{})
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/apiexportendpointslices/"+export) {
			t.Errorf("unexpected follow-on request during canceled lookup: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if sliceReads.Add(1) == 1 {
			close(lookupStarted)
		}
		<-r.Context().Done()
	}))
	defer kcp.Close()

	base := &rest.Config{Host: kcp.URL + "/clusters/root:railgrid:providers:edges", BearerToken: "provider-token"}
	callers, err := NewCallerFactory(base, WithProviderConfig(base, export))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, callErr := callers.AsProviderWithRateLimiterContext(ctx, "24192sxym7m5edtu", nil)
		done <- callErr
	}()
	select {
	case <-lookupStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("endpoint-slice lookup did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled endpoint lookup = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AsProviderWithRateLimiterContext did not return after cancellation")
	}
	if got := sliceReads.Load(); got != 1 {
		t.Fatalf("endpoint-slice reads = %d, want one canceled lookup and no follow-on request", got)
	}
}

func TestAsProviderWithRateLimiterContextCancelsExpiredEndpointRefreshWithoutProbes(t *testing.T) {
	const (
		export    = "edges.providers.railgrid.ai"
		clusterID = "24192sxym7m5edtu"
	)
	var sliceReads atomic.Int32
	refreshStarted := make(chan struct{})
	endpoint := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("canceled endpoint refresh must not start a shard probe")
	}))
	defer endpoint.Close()
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sliceReads.Add(1) == 2 {
			close(refreshStarted)
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"apis.kcp.io/v1alpha1","kind":"APIExportEndpointSlice","metadata":{"name":"` + export + `"},"status":{"endpoints":[{"url":"` + endpoint.URL + `/services/apiexport/abc/` + export + `"}]}}`))
	}))
	defer kcp.Close()

	base := &rest.Config{Host: kcp.URL + "/clusters/root:railgrid:providers:edges", BearerToken: "provider-token"}
	callers, err := NewCallerFactory(base, WithProviderConfig(base, export))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}
	if _, err := callers.AsProviderWithRateLimiterContext(context.Background(), clusterID, nil); err != nil {
		t.Fatalf("initial endpoint lookup: %v", err)
	}
	callers.providerMu.Lock()
	callers.endpointRefresh = 0
	callers.providerMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, callErr := callers.AsProviderWithRateLimiterContext(ctx, clusterID, nil)
		done <- callErr
	}()
	select {
	case <-refreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("expired endpoint refresh did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled endpoint refresh = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AsProviderWithRateLimiterContext did not return after refresh cancellation")
	}
	if got := sliceReads.Load(); got != 2 {
		t.Fatalf("endpoint-slice reads = %d, want initial read and one canceled refresh", got)
	}
}

func TestCanceledEndpointWaiterReturnsBeforeRefreshCompletes(t *testing.T) {
	const (
		export    = "edges.providers.railgrid.ai"
		clusterID = "24192sxym7m5edtu"
	)
	firstLookupStarted := make(chan struct{})
	releaseFirstLookup := make(chan struct{})
	var sliceReads atomic.Int32
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sliceReads.Add(1) != 1 {
			t.Errorf("canceled waiter made an unexpected endpoint-slice request: %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		close(firstLookupStarted)
		<-releaseFirstLookup
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"apis.kcp.io/v1alpha1","kind":"APIExportEndpointSlice","metadata":{"name":"` + export + `"},"status":{"endpoints":[{"url":"https://shard.example/services/apiexport/abc/` + export + `"}]}}`))
	}))
	defer kcp.Close()

	base := &rest.Config{Host: kcp.URL + "/clusters/root:railgrid:providers:edges", BearerToken: "provider-token"}
	callers, err := NewCallerFactory(base, WithProviderConfig(base, export))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, callErr := callers.AsProviderWithRateLimiterContext(context.Background(), clusterID, nil)
		firstDone <- callErr
	}()
	select {
	case <-firstLookupStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first endpoint-slice lookup did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		_, callErr := callers.AsProviderWithRateLimiterContext(ctx, clusterID, nil)
		secondDone <- callErr
	}()
	<-secondStarted
	if !waitForEndpointRefreshWait(2 * time.Second) {
		cancel()
		close(releaseFirstLookup)
		t.Fatal("second lookup did not join the in-flight endpoint refresh")
	}

	cancel()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled endpoint waiter = %v, want context.Canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("canceled endpoint waiter remained blocked behind the in-flight GET")
	}
	if got := sliceReads.Load(); got != 1 {
		t.Fatalf("canceled waiter triggered another endpoint-slice request: reads=%d", got)
	}
	close(releaseFirstLookup)

	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first endpoint lookup: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first endpoint lookup did not finish after release")
	}
	if got := sliceReads.Load(); got != 1 {
		t.Fatalf("endpoint-slice reads = %d, want only the first coalesced refresh", got)
	}
}

func TestConcurrentEndpointRefreshIsCoalesced(t *testing.T) {
	const (
		export    = "edges.providers.railgrid.ai"
		clusterID = "24192sxym7m5edtu"
	)
	firstLookupStarted := make(chan struct{})
	releaseFirstLookup := make(chan struct{})
	var sliceReads atomic.Int32
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if sliceReads.Add(1) == 1 {
			close(firstLookupStarted)
			<-releaseFirstLookup
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testEndpointSliceJSON(export, "https://shard.example/services/apiexport/abc/"+export)))
	}))
	defer kcp.Close()
	callers := newTestProviderCallers(t, kcp, export)

	firstDone := make(chan error, 1)
	go func() {
		_, err := callers.AsProviderWithRateLimiterContext(context.Background(), clusterID, nil)
		firstDone <- err
	}()
	select {
	case <-firstLookupStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first endpoint-slice lookup did not start")
	}
	secondDone := make(chan error, 1)
	go func() {
		_, err := callers.AsProviderWithRateLimiterContext(context.Background(), clusterID, nil)
		secondDone <- err
	}()
	if !waitForEndpointRefreshWait(2 * time.Second) {
		close(releaseFirstLookup)
		t.Fatal("second lookup did not join the in-flight endpoint refresh")
	}
	close(releaseFirstLookup)
	for name, done := range map[string]<-chan error{"first": firstDone, "second": secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s endpoint lookup: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s endpoint lookup did not finish", name)
		}
	}
	if got := sliceReads.Load(); got != 1 {
		t.Fatalf("coalesced endpoint-slice reads = %d, want one", got)
	}
}

func TestCanceledEndpointRefreshLeaderDoesNotPoisonWaiter(t *testing.T) {
	const (
		export    = "edges.providers.railgrid.ai"
		clusterID = "24192sxym7m5edtu"
	)
	leaderLookupStarted := make(chan struct{})
	var sliceReads atomic.Int32
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sliceReads.Add(1) == 1 {
			close(leaderLookupStarted)
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testEndpointSliceJSON(export, "https://shard.example/services/apiexport/abc/"+export)))
	}))
	defer kcp.Close()
	callers := newTestProviderCallers(t, kcp, export)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := callers.AsProviderWithRateLimiterContext(leaderCtx, clusterID, nil)
		leaderDone <- err
	}()
	select {
	case <-leaderLookupStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("leader endpoint lookup did not start")
	}
	waiterDone := make(chan error, 1)
	go func() {
		_, err := callers.AsProviderWithRateLimiterContext(context.Background(), clusterID, nil)
		waiterDone <- err
	}()
	if !waitForEndpointRefreshWait(2 * time.Second) {
		cancelLeader()
		t.Fatal("active waiter did not join the leader's endpoint refresh")
	}
	cancelLeader()
	select {
	case err := <-leaderDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled refresh leader = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled refresh leader did not return")
	}
	select {
	case err := <-waiterDone:
		if err != nil {
			t.Fatalf("active waiter inherited the leader's cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active waiter did not retry the endpoint lookup")
	}
	if got := sliceReads.Load(); got != 2 {
		t.Fatalf("endpoint-slice reads = %d, want canceled leader plus one waiter retry", got)
	}
}

func TestEndpointRefreshDependencyErrorUsesStaleEndpointsWithoutRefreshingTimestamp(t *testing.T) {
	const (
		export    = "edges.providers.railgrid.ai"
		clusterID = "24192sxym7m5edtu"
	)
	var sliceReads atomic.Int32
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if sliceReads.Add(1) == 2 {
			http.Error(w, "temporary dependency error", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testEndpointSliceJSON(export, "https://stale.example/services/apiexport/abc/"+export)))
	}))
	defer kcp.Close()
	callers := newTestProviderCallers(t, kcp, export)
	if _, err := callers.AsProviderWithRateLimiterContext(context.Background(), clusterID, nil); err != nil {
		t.Fatalf("initial endpoint lookup: %v", err)
	}
	callers.providerMu.Lock()
	callers.endpointRefresh = 0
	previousRead := callers.providerEndpointsRead
	callers.providerMu.Unlock()
	client, err := callers.AsProviderWithRateLimiterContext(context.Background(), clusterID, nil)
	if err != nil || client == nil {
		t.Fatalf("refresh with a dependency error should use stale endpoint: client=%v err=%v", client != nil, err)
	}
	callers.providerMu.Lock()
	gotRead := callers.providerEndpointsRead
	callers.providerMu.Unlock()
	if !gotRead.Equal(previousRead) {
		t.Fatalf("failed refresh advanced endpoint timestamp from %v to %v", previousRead, gotRead)
	}
	if got := sliceReads.Load(); got != 2 {
		t.Fatalf("endpoint-slice reads = %d, want initial read and one failed refresh", got)
	}
}

func TestForcedEndpointRefreshJoinsOrdinaryStaleFallbackAndNextCallerRetries(t *testing.T) {
	const export = "edges.providers.railgrid.ai"
	firstLookupStarted := make(chan struct{})
	releaseFirstLookup := make(chan struct{})
	var sliceReads atomic.Int32
	newEndpoint := "https://new-shard.example/services/apiexport/abc/" + export
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch sliceReads.Add(1) {
		case 1:
			close(firstLookupStarted)
			<-releaseFirstLookup
			http.Error(w, "temporary dependency error", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(testEndpointSliceJSON(export, newEndpoint)))
		}
	}))
	defer kcp.Close()
	callers := newTestProviderCallers(t, kcp, export)
	callers.providerMu.Lock()
	callers.providerEndpoints = []string{"https://stale.example/services/apiexport/abc/" + export}
	previousRead := time.Now().Add(-time.Hour)
	callers.providerEndpointsRead = previousRead
	callers.endpointRefresh = time.Minute
	callers.providerMu.Unlock()

	type result struct {
		snapshot providerEndpointSnapshot
		err      error
	}
	normalDone := make(chan result, 1)
	go func() {
		snapshot, err := callers.providerEndpointSnapshotForContext(context.Background(), false)
		normalDone <- result{snapshot: snapshot, err: err}
	}()
	select {
	case <-firstLookupStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("ordinary endpoint refresh did not start")
	}

	forcedDone := make(chan result, 1)
	go func() {
		snapshot, err := callers.providerEndpointSnapshotForContext(context.Background(), true)
		forcedDone <- result{snapshot: snapshot, err: err}
	}()
	if !waitForEndpointRefreshWait(2 * time.Second) {
		close(releaseFirstLookup)
		t.Fatal("forced lookup did not join the ordinary refresh")
	}
	close(releaseFirstLookup)

	select {
	case got := <-normalDone:
		if got.err != nil || len(got.snapshot.endpoints) != 1 || !strings.Contains(got.snapshot.endpoints[0], "stale.example") {
			t.Fatalf("ordinary refresh result = %+v, want stale fallback: %v", got.snapshot, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ordinary stale fallback did not finish")
	}
	select {
	case got := <-forcedDone:
		if got.err != nil || len(got.snapshot.endpoints) != 1 || !strings.Contains(got.snapshot.endpoints[0], "stale.example") {
			t.Fatalf("forced refresh follower result = %+v, want shared stale fallback: %v", got.snapshot, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forced refresh follower did not finish with the shared stale fallback")
	}
	callers.providerMu.Lock()
	gotRead := callers.providerEndpointsRead
	callers.providerMu.Unlock()
	if !gotRead.Equal(previousRead) {
		t.Fatalf("shared failed refresh advanced timestamp from %v to %v", previousRead, gotRead)
	}
	if got := sliceReads.Load(); got != 1 {
		t.Fatalf("overlapping ordinary and forced lookups made %d endpoint-slice reads, want one shared GET", got)
	}

	// The stale timestamp remains expired, so a later non-forced caller starts
	// a fresh GET and observes the newly published endpoint.
	next, err := callers.providerEndpointSnapshotForContext(context.Background(), false)
	if err != nil || len(next.endpoints) != 1 || next.endpoints[0] != newEndpoint {
		t.Fatalf("subsequent endpoint lookup = %+v err=%v, want refreshed endpoint %q", next, err, newEndpoint)
	}
	if got := sliceReads.Load(); got != 2 {
		t.Fatalf("endpoint-slice reads after subsequent caller = %d, want a new GET", got)
	}
}

func TestForcedEndpointRefreshFollowerAcceptsForcedStaleFallback(t *testing.T) {
	const export = "edges.providers.railgrid.ai"
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	var sliceReads atomic.Int32
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if sliceReads.Add(1) != 1 {
			http.Error(w, "unexpected duplicate refresh", http.StatusInternalServerError)
			return
		}
		close(lookupStarted)
		<-releaseLookup
		http.Error(w, "temporary dependency error", http.StatusServiceUnavailable)
	}))
	defer kcp.Close()
	callers := newTestProviderCallers(t, kcp, export)
	staleEndpoint := "https://stale.example/services/apiexport/abc/" + export
	callers.providerMu.Lock()
	callers.providerEndpoints = []string{staleEndpoint}
	callers.providerEndpointsRead = time.Now().Add(-time.Hour)
	callers.providerMu.Unlock()

	type result struct {
		snapshot providerEndpointSnapshot
		err      error
	}
	leaderDone := make(chan result, 1)
	go func() {
		snapshot, err := callers.providerEndpointSnapshotForContext(context.Background(), true)
		leaderDone <- result{snapshot: snapshot, err: err}
	}()
	select {
	case <-lookupStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("forced endpoint refresh did not start")
	}
	followerDone := make(chan result, 1)
	go func() {
		snapshot, err := callers.providerEndpointSnapshotForContext(context.Background(), true)
		followerDone <- result{snapshot: snapshot, err: err}
	}()
	if !waitForEndpointRefreshWait(2 * time.Second) {
		close(releaseLookup)
		t.Fatal("forced follower did not join the forced refresh")
	}
	close(releaseLookup)
	for name, done := range map[string]<-chan result{"leader": leaderDone, "follower": followerDone} {
		select {
		case got := <-done:
			if got.err != nil || len(got.snapshot.endpoints) != 1 || got.snapshot.endpoints[0] != staleEndpoint {
				t.Fatalf("%s forced refresh result = %+v err=%v, want cohort stale fallback %q", name, got.snapshot, got.err, staleEndpoint)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s forced refresh did not finish", name)
		}
	}
	if got := sliceReads.Load(); got != 1 {
		t.Fatalf("endpoint-slice reads = %d, want one forced cohort GET", got)
	}
}

func TestEndpointGenerationChangeDuringShardProbeRetriesNewSet(t *testing.T) {
	const (
		export    = "edges.providers.railgrid.ai"
		clusterID = "24192sxym7m5edtu"
	)
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	var releaseProbeOnce sync.Once
	releaseProbeNow := func() { releaseProbeOnce.Do(func() { close(releaseProbe) }) }
	defer releaseProbeNow()
	oldShard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(probeStarted)
		<-releaseProbe
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"authorization.k8s.io/v1","kind":"SubjectAccessReview","status":{"allowed":false}}`))
	}))
	defer oldShard.Close()
	otherOldShard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "wrong shard", http.StatusForbidden)
	}))
	defer otherOldShard.Close()
	newShard := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("single newly published endpoint should not need a probe")
	}))
	defer newShard.Close()
	var updated atomic.Bool
	var sliceReads atomic.Int32
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sliceReads.Add(1)
		urls := []string{oldShard.URL + "/services/apiexport/abc/" + export, otherOldShard.URL + "/services/apiexport/abc/" + export}
		if updated.Load() {
			urls = []string{newShard.URL + "/services/apiexport/abc/" + export}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testEndpointSliceJSON(export, urls...)))
	}))
	defer kcp.Close()
	callers := newTestProviderCallers(t, kcp, export)
	if _, err := callers.providerEndpointSnapshotForContext(context.Background(), false); err != nil {
		t.Fatalf("seed endpoint snapshot: %v", err)
	}
	callers.providerMu.Lock()
	callers.endpointRefresh = 0
	callers.providerMu.Unlock()

	firstDone := make(chan struct {
		endpoint string
		err      error
	}, 1)
	go func() {
		endpoint, err := callers.endpointForCluster(context.Background(), clusterID)
		firstDone <- struct {
			endpoint string
			err      error
		}{endpoint: endpoint, err: err}
	}()
	select {
	case <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old shard probe did not start")
	}
	updated.Store(true)
	secondEndpoint, err := callers.endpointForCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("lookup after endpoint set changed: %v", err)
	}
	releaseProbeNow()
	first := <-firstDone
	if first.err != nil {
		t.Fatalf("lookup racing endpoint set change: %v", first.err)
	}
	want := newShard.URL + "/services/apiexport/abc/" + export
	if secondEndpoint != want || first.endpoint != want {
		t.Fatalf("endpoint choices after generation change = %q and %q, want %q", secondEndpoint, first.endpoint, want)
	}
}

func newTestProviderCallers(t *testing.T, kcp *httptest.Server, export string) *Callers {
	t.Helper()
	base := &rest.Config{Host: kcp.URL + "/clusters/root:railgrid:providers:edges", BearerToken: "provider-token"}
	callers, err := NewCallerFactory(base, WithProviderConfig(base, export))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}
	return callers
}

func testEndpointSliceJSON(export string, endpoints ...string) string {
	entries := make([]string, len(endpoints))
	for i, endpoint := range endpoints {
		entries[i] = `{"url":"` + endpoint + `"}`
	}
	return `{"apiVersion":"apis.kcp.io/v1alpha1","kind":"APIExportEndpointSlice","metadata":{"name":"` + export + `"},"status":{"endpoints":[` + strings.Join(entries, ",") + `]}}`
}

func waitForEndpointRefreshWait(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	stack := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		n := runtime.Stack(stack, true)
		for _, goroutine := range strings.Split(string(stack[:n]), "\n\n") {
			if strings.Contains(goroutine, "providerEndpointSnapshotForContext") && strings.Contains(goroutine, "[select]") {
				return true
			}
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// A choice already made is dropped when the published set changes, so a
// consumer is not pinned to an endpoint that has gone away.
func TestEndpointSetChangeInvalidatesTheClusterChoice(t *testing.T) {
	c := &Callers{
		clusterEndpoint:   map[string]string{"24192sxym7m5edtu": "https://old.example/services/apiexport/abc/x"},
		providerEndpoints: []string{"https://old.example/services/apiexport/abc/x"},
	}
	if !sameEndpoints([]string{"https://old.example/services/apiexport/abc/x"}, c.providerEndpoints) {
		t.Fatal("sameEndpoints should ignore nothing when the sets match")
	}
	if sameEndpoints([]string{"https://new.example/services/apiexport/abc/x"}, c.providerEndpoints) {
		t.Fatal("sameEndpoints must notice a different set")
	}
	// Order is kcp's choice and must not count as a change.
	a := []string{"https://one.example/x", "https://two.example/x"}
	if !sameEndpoints(a, []string{a[1], a[0]}) {
		t.Fatal("sameEndpoints must compare as a set, not a list")
	}
}
