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

package providers

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/utils/testfakes"
)

func wantSRI(t *testing.T, body string) string {
	t.Helper()
	sum := sha512.Sum384([]byte(body))
	return "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
}

// The portal pins each provider bundle with the hash the hub computed from the
// same source the UI proxy serves. A reconcile at registration must record it
// in both the registry (what /api/providers returns) and the CatalogEntry
// status, and a heartbeat that reports a new version must re-hash.
func TestCatalogReconcilerPinsProviderUIBundle(t *testing.T) {
	var body atomic.Value
	body.Store("customElements.define('railgrid-provider-cost', class extends HTMLElement {})")
	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ui/main.js" {
			http.NotFound(w, r)
			return
		}
		fetches.Add(1)
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer server.Close()

	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "cost"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Cost",
			Version:     "1.0.0",
			UI:          &providersv1alpha1.ProviderUI{URL: server.URL + "/ui"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{
		mgr:          testfakes.NewManager(c),
		reg:          reg,
		noKCP:        true,
		healthClient: healthyHTTPDoer(),
		uiClient:     server.Client(),
	}
	req := testfakes.NewRequest("cluster", "", "cost")
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	first := wantSRI(t, body.Load().(string))
	got, ok := reg.Get("cost")
	if !ok {
		t.Fatal("cost missing from registry")
	}
	if got.MainJSIntegrity != first {
		t.Fatalf("registry MainJSIntegrity = %q, want %q", got.MainJSIntegrity, first)
	}
	var status providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cost"}, &status); err != nil {
		t.Fatalf("get: %v", err)
	}
	if status.Status.UI == nil || status.Status.UI.MainJSIntegrity != first || status.Status.UI.MainJSIntegrityVersion != "1.0.0" {
		t.Fatalf("status.ui = %#v, want pin %q at version 1.0.0", status.Status.UI, first)
	}

	// Same version, bundle swapped behind the URL: the cached pin stands and no
	// fetch happens, so the browser refuses the swapped bundle.
	body.Store("alert('swapped')")
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if got, _ := reg.Get("cost"); got.MainJSIntegrity != first {
		t.Fatalf("pin changed without a version change: %q", got.MainJSIntegrity)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("bundle fetched %d times, want 1 (cached pin)", n)
	}

	// A heartbeat reporting a new version re-admits the bundle by re-hashing.
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cost"}, &status); err != nil {
		t.Fatalf("get: %v", err)
	}
	status.Status.ReportedVersion = "1.1.0"
	if err := c.Status().Update(context.Background(), &status); err != nil {
		t.Fatalf("update status: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("third reconcile: %v", err)
	}
	second := wantSRI(t, "alert('swapped')")
	if got, _ := reg.Get("cost"); got.MainJSIntegrity != second {
		t.Fatalf("registry MainJSIntegrity = %q, want re-hashed %q", got.MainJSIntegrity, second)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cost"}, &status); err != nil {
		t.Fatalf("get: %v", err)
	}
	if status.Status.UI == nil || status.Status.UI.MainJSIntegrity != second || status.Status.UI.MainJSIntegrityVersion != "1.1.0" {
		t.Fatalf("status.ui = %#v, want pin %q at version 1.1.0", status.Status.UI, second)
	}
}

// A transient fetch failure at an unchanged version must not unpin the bundle,
// and a stale cached pin must be re-read after the resync interval.
func TestPinUIIntegrityKeepsPinAcrossTransientFailureAndResyncs(t *testing.T) {
	var fail atomic.Bool
	const bundle = "export {}"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(bundle))
	}))
	defer server.Close()

	ui, err := ParseURL(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := &CatalogReconciler{uiClient: server.Client(), uiIntegrity: map[providerKey]uiIntegrityRecord{}}
	prov := Provider{Name: "cost", UIURL: ui}
	logger := logr.Discard()

	pin, ok := r.pinUIIntegrity(context.Background(), logger, prov, "1")
	if !ok || pin != wantSRI(t, bundle) {
		t.Fatalf("pinUIIntegrity = %q, %v; want %q, true", pin, ok, wantSRI(t, bundle))
	}

	// Expire the cache, then fail the fetch: the old pin survives.
	key := providerKey{Name: "cost"}
	r.uiIntegrity[key] = uiIntegrityRecord{version: "1", integrity: pin, hashedAt: time.Now().Add(-2 * UIIntegrityResync)}
	fail.Store(true)
	if got, ok := r.pinUIIntegrity(context.Background(), logger, prov, "1"); !ok || got != pin {
		t.Fatalf("pin after transient failure = %q, %v; want kept %q", got, ok, pin)
	}

	// A failure after a version change drops the pin rather than pinning the
	// new bundle to the old hash, which would refuse it in every browser.
	if got, ok := r.pinUIIntegrity(context.Background(), logger, prov, "2"); ok || got != "" {
		t.Fatalf("pin after failed re-hash = %q, %v; want unpinned", got, ok)
	}
	if _, cached := r.uiIntegrity[key]; cached {
		t.Fatal("failed re-hash left a cache record; the next reconcile would not retry")
	}

	// Org-owned providers travel the edge tunnel and are never dialled.
	fail.Store(false)
	if got, ok := r.pinUIIntegrity(context.Background(), logger, Provider{Name: "cost", OrgUUID: "org", UIURL: ui}, "2"); ok || got != "" {
		t.Fatalf("org-owned provider pinned: %q, %v", got, ok)
	}
}

// First-party providers ship their bundle inside the hub binary; the pin comes
// straight from the embedded FS, matching what serveLocalAsset writes.
func TestHashProviderMainJSReadsEmbeddedAssets(t *testing.T) {
	assets := fstest.MapFS{"main.js": {Data: []byte("customElements.define('x-y', class extends HTMLElement {})")}}
	got, err := hashProviderMainJS(context.Background(), nil, Provider{Name: "builtin", LocalUIAssets: assets})
	if err != nil {
		t.Fatal(err)
	}
	if want := wantSRI(t, string(assets["main.js"].Data)); got != want {
		t.Fatalf("hashProviderMainJS = %q, want %q", got, want)
	}
	if _, err := hashProviderMainJS(context.Background(), nil, Provider{Name: "none"}); err == nil {
		t.Fatal("expected an error for a provider without a hub-served UI")
	}
}

// pinUIIntegrity drops the mutex across the network fetch, so its error path
// must decide on the entry that is in the map when it re-locks, not on the
// snapshot it took before fetching. A reconcile whose fetch fails must not
// delete a pin another reconcile wrote while that fetch was in flight.
//
// The client blocks until the test has performed the concurrent write, so the
// interleaving is forced rather than raced.
func TestPinUIIntegrityErrorPathKeepsConcurrentlyWrittenPin(t *testing.T) {
	ui, err := url.Parse("http://provider.invalid/ui")
	if err != nil {
		t.Fatal(err)
	}
	prov := Provider{Name: "cost", UIURL: ui}
	key := providerKey{Name: "cost"}
	// The pin this reconcile set out to replace: an older version, so the
	// version-changed branch of the error path is the one under test.
	stale := uiIntegrityRecord{version: "1", integrity: "sha384-stale", hashedAt: time.Now()}
	// What a concurrent reconcile pins while the fetch below is blocked.
	concurrent := uiIntegrityRecord{version: "3", integrity: "sha384-concurrent", hashedAt: time.Now()}

	fetchStarted := make(chan struct{})
	releaseFetch := make(chan struct{})
	r := &CatalogReconciler{
		uiIntegrity: map[providerKey]uiIntegrityRecord{key: stale},
		uiClient: httpDoerFunc(func(*http.Request) (*http.Response, error) {
			close(fetchStarted)
			<-releaseFetch
			return nil, errors.New("upstream unreachable")
		}),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Version 2: neither the stale snapshot's version nor the concurrent
		// writer's, so the error path cannot keep this call's own pin.
		if got, ok := r.pinUIIntegrity(context.Background(), logr.Discard(), prov, "2"); ok || got != "" {
			t.Errorf("pinUIIntegrity after a failed fetch = %q, %v; want \"\", false", got, ok)
		}
	}()

	<-fetchStarted
	r.uiIntegrityMu.Lock()
	r.uiIntegrity[key] = concurrent
	r.uiIntegrityMu.Unlock()
	close(releaseFetch)
	<-done

	r.uiIntegrityMu.Lock()
	got, ok := r.uiIntegrity[key]
	r.uiIntegrityMu.Unlock()
	if !ok {
		t.Fatal("the concurrently written pin was deleted by the failing reconcile's error path")
	}
	if got != concurrent {
		t.Fatalf("cache entry = %+v, want the concurrently written %+v", got, concurrent)
	}
}

// The concurrency fix must not weaken the single-reconcile behaviour the error
// path documents: an untouched pin for a superseded version is still dropped so
// the portal loads the new bundle unpinned rather than with a stale hash, and a
// failure at an unchanged version still keeps its pin.
func TestPinUIIntegrityErrorPathUncontendedBehaviour(t *testing.T) {
	ui, err := url.Parse("http://provider.invalid/ui")
	if err != nil {
		t.Fatal(err)
	}
	prov := Provider{Name: "cost", UIURL: ui}
	key := providerKey{Name: "cost"}
	failing := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("upstream unreachable")
	})

	t.Run("drops an untouched pin for a superseded version", func(t *testing.T) {
		r := &CatalogReconciler{
			uiIntegrity: map[providerKey]uiIntegrityRecord{
				key: {version: "1", integrity: "sha384-stale", hashedAt: time.Now()},
			},
			uiClient: failing,
		}
		if got, ok := r.pinUIIntegrity(context.Background(), logr.Discard(), prov, "2"); ok || got != "" {
			t.Fatalf("pinUIIntegrity = %q, %v; want \"\", false", got, ok)
		}
		if _, ok := r.uiIntegrity[key]; ok {
			t.Fatal("a stale pin for a superseded version was kept")
		}
	})

	t.Run("keeps the pin when the version has not changed", func(t *testing.T) {
		// hashedAt is old enough that the resync window has expired, so the
		// call re-fetches rather than returning from cache.
		kept := uiIntegrityRecord{version: "2", integrity: "sha384-kept", hashedAt: time.Now().Add(-2 * UIIntegrityResync)}
		r := &CatalogReconciler{
			uiIntegrity: map[providerKey]uiIntegrityRecord{key: kept},
			uiClient:    failing,
		}
		got, ok := r.pinUIIntegrity(context.Background(), logr.Discard(), prov, "2")
		if !ok || got != kept.integrity {
			t.Fatalf("pinUIIntegrity = %q, %v; want %q, true", got, ok, kept.integrity)
		}
		if r.uiIntegrity[key] != kept {
			t.Fatalf("cache entry = %+v, want it kept as %+v", r.uiIntegrity[key], kept)
		}
	})
}

// etagBundleServer serves a mutable /main.js with a strong ETag derived from
// the body, exactly as provider-sdk/serve does for a provider portal, and
// answers If-None-Match with 304.
type etagBundleServer struct {
	body       atomic.Value // string
	fullGets   atomic.Int32
	revalidate atomic.Int32
}

func newETagBundleServer(body string) (*etagBundleServer, *httptest.Server) {
	s := &etagBundleServer{}
	s.body.Store(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/main.js" {
			http.NotFound(w, r)
			return
		}
		current := s.body.Load().(string)
		sum := sha256.Sum256([]byte(current))
		etag := `"` + hex.EncodeToString(sum[:16]) + `"`
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("ETag", etag)
		if match := r.Header.Get("If-None-Match"); match != "" {
			s.revalidate.Add(1)
			if match == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		s.fullGets.Add(1)
		_, _ = w.Write([]byte(current))
	}))
	return s, srv
}

// With an upstream ETag the hub revalidates on every reconcile rather than
// trusting a pin for UIIntegrityResync. A 304 keeps the pin and costs no body;
// a 200 means the bundle behind the URL was rebuilt at the same version — every
// Tilt rebuild — and is re-pinned at once, which is what stops a browser being
// handed a pin no bundle can match.
func TestPinUIIntegrityRevalidatesWithETag(t *testing.T) {
	const first = "customElements.define('x-a', class extends HTMLElement {})"
	const second = "customElements.define('x-b', class extends HTMLElement {})"
	upstream, server := newETagBundleServer(first)
	defer server.Close()

	ui, err := ParseURL(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := &CatalogReconciler{uiClient: server.Client(), uiIntegrity: map[providerKey]uiIntegrityRecord{}}
	prov := Provider{Name: "cost", UIURL: ui}
	key := providerKey{Name: "cost"}
	ctx := context.Background()

	pin, ok := r.pinUIIntegrity(ctx, logr.Discard(), prov, "1")
	if !ok || pin != wantSRI(t, first) {
		t.Fatalf("first pin = %q, %v; want %q, true", pin, ok, wantSRI(t, first))
	}
	if r.uiIntegrity[key].etag == "" {
		t.Fatal("upstream ETag was not stored with the pin; the next reconcile cannot revalidate")
	}

	// Unchanged bundle, unchanged version, well inside the resync window: the
	// reconcile still asks, and the 304 answer keeps the pin. hashedAt is
	// refreshed because the pin was confirmed, not merely assumed.
	before := r.uiIntegrity[key]
	if got, ok := r.pinUIIntegrity(ctx, logr.Discard(), prov, "1"); !ok || got != pin {
		t.Fatalf("pin after revalidation = %q, %v; want the same %q", got, ok, pin)
	}
	if n := upstream.revalidate.Load(); n != 1 {
		t.Fatalf("conditional GETs = %d, want 1", n)
	}
	if n := upstream.fullGets.Load(); n != 1 {
		t.Fatalf("full GETs = %d, want 1 (the 304 must not transfer a body)", n)
	}
	if after := r.uiIntegrity[key]; !after.hashedAt.After(before.hashedAt) || after.integrity != pin || after.etag != before.etag {
		t.Fatalf("record after 304 = %+v, want %+v with a newer hashedAt", after, before)
	}

	// The bundle is rebuilt at the SAME version. The conditional GET now gets
	// 200 and the pin follows the bytes within this one reconcile.
	upstream.body.Store(second)
	got, ok := r.pinUIIntegrity(ctx, logr.Discard(), prov, "1")
	if !ok || got != wantSRI(t, second) {
		t.Fatalf("pin after a same-version rebuild = %q, %v; want %q", got, ok, wantSRI(t, second))
	}
	if n := upstream.fullGets.Load(); n != 2 {
		t.Fatalf("full GETs = %d, want 2 (the changed bundle must be read)", n)
	}
	if rec := r.uiIntegrity[key]; rec.etag == "" || rec.etag == before.etag {
		t.Fatalf("record after a rebuild kept the old validator: %+v", rec)
	}

	// And the new pin is itself revalidated, not re-downloaded.
	if got, ok := r.pinUIIntegrity(ctx, logr.Discard(), prov, "1"); !ok || got != wantSRI(t, second) {
		t.Fatalf("pin after re-revalidation = %q, %v; want %q", got, ok, wantSRI(t, second))
	}
	if n := upstream.fullGets.Load(); n != 2 {
		t.Fatalf("full GETs = %d, want 2 (the new pin must revalidate with a 304)", n)
	}
}

// An upstream without an ETag has nothing to revalidate against, so the resync
// interval stays its only re-read trigger: the hub must not turn every
// reconcile into a full bundle download for such a provider.
func TestPinUIIntegrityWithoutETagKeepsTheResyncWindow(t *testing.T) {
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/main.js" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") != "" {
			t.Errorf("hub sent If-None-Match to an upstream that never offered an ETag")
		}
		gets.Add(1)
		_, _ = w.Write([]byte("export {}"))
	}))
	defer server.Close()

	ui, err := ParseURL(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := &CatalogReconciler{uiClient: server.Client(), uiIntegrity: map[providerKey]uiIntegrityRecord{}}
	prov := Provider{Name: "cost", UIURL: ui}
	key := providerKey{Name: "cost"}
	ctx := context.Background()

	pin, ok := r.pinUIIntegrity(ctx, logr.Discard(), prov, "1")
	if !ok || pin != wantSRI(t, "export {}") {
		t.Fatalf("pin = %q, %v; want %q, true", pin, ok, wantSRI(t, "export {}"))
	}
	for range 3 {
		if got, _ := r.pinUIIntegrity(ctx, logr.Discard(), prov, "1"); got != pin {
			t.Fatalf("pin changed inside the resync window: %q", got)
		}
	}
	if n := gets.Load(); n != 1 {
		t.Fatalf("bundle fetched %d times, want 1 (the resync window still caches)", n)
	}

	// Past the window it is re-read, as before.
	rec := r.uiIntegrity[key]
	rec.hashedAt = time.Now().Add(-2 * UIIntegrityResync)
	r.uiIntegrity[key] = rec
	if got, _ := r.pinUIIntegrity(ctx, logr.Discard(), prov, "1"); got != pin {
		t.Fatalf("pin after the resync window = %q, want the unchanged %q", got, pin)
	}
	if n := gets.Load(); n != 2 {
		t.Fatalf("bundle fetched %d times, want 2 (the window expired)", n)
	}
}

// ObserveMainJSIntegrity is what the UI proxy calls with the bundle it actually
// served. It must correct the registry pin AND the cache record for the
// provider's current version — and drop the stale validator, or the next
// conditional GET would confirm the superseded pin with a 304.
func TestObserveMainJSIntegrityCorrectsThePin(t *testing.T) {
	ui, err := url.Parse("http://provider.invalid/ui")
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "cost", UIURL: ui, Version: "0.1.0", EndpointsValid: true, MainJSIntegrity: "sha384-stale"})
	r := &CatalogReconciler{
		reg: reg,
		uiIntegrity: map[providerKey]uiIntegrityRecord{
			{Name: "cost"}: {version: "0.1.0", integrity: "sha384-stale", hashedAt: time.Now(), etag: `"old"`},
		},
	}

	r.ObserveMainJSIntegrity("cost", "sha384-served")
	got, ok := reg.Get("cost")
	if !ok || got.MainJSIntegrity != "sha384-served" {
		t.Fatalf("registry pin = %q, want the served %q", got.MainJSIntegrity, "sha384-served")
	}
	rec := r.uiIntegrity[providerKey{Name: "cost"}]
	if rec.version != "0.1.0" || rec.integrity != "sha384-served" || rec.etag != "" {
		t.Fatalf("cache record = %+v, want the served pin at 0.1.0 with no validator", rec)
	}

	// Unknown providers, and providers the hub does not reverse-proxy a bundle
	// for, are left alone.
	r.ObserveMainJSIntegrity("nope", "sha384-x")
	reg.Upsert(Provider{Name: "builtin", LocalUIAssets: fstest.MapFS{}, MainJSIntegrity: "sha384-embedded"})
	r.ObserveMainJSIntegrity("builtin", "sha384-x")
	if got, _ := reg.Get("builtin"); got.MainJSIntegrity != "sha384-embedded" {
		t.Fatalf("embedded provider pin = %q, want it untouched", got.MainJSIntegrity)
	}
}
