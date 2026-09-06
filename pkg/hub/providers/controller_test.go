/*
Copyright 2026 The Faros Authors.

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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
	"github.com/faroshq/faros/pkg/kcppaths"
	"github.com/faroshq/faros/utils/testfakes"
)

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (f httpDoerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

type edgeRouteResolverFunc func(context.Context, string, string, string) (*EdgeRoute, error)

func (f edgeRouteResolverFunc) ResolveProviderEdgeRoute(ctx context.Context, orgUUID, providerName, backendURL string) (*EdgeRoute, error) {
	return f(ctx, orgUUID, providerName, backendURL)
}

func healthyHTTPDoer() httpDoer {
	return httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})
}

func newProviderTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("adding core scheme: %v", err)
	}
	if err := providersv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding providers scheme: %v", err)
	}
	return s
}

func registerAppStudioBuiltin(t *testing.T) {
	t.Helper()
	if _, ok := BuiltinByName("app-studio"); ok {
		return
	}
	RegisterBuiltin(BuiltinSpec{
		Name:          "app-studio",
		DisplayName:   "App Studio",
		LocalUIAssets: fstest.MapFS{"main.js": &fstest.MapFile{Data: []byte("bundle")}},
	})
}

func TestCatalogReconciler_PreservesChartOwnedUIRoutingForBuiltinName(t *testing.T) {
	registerAppStudioBuiltin(t)
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "app-studio"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "App Studio from Chart",
			Description: "Persistent AI project workspace.",
			Dependencies: []providersv1alpha1.ProviderDependency{
				{Name: "code"},
			},
			UI: &providersv1alpha1.ProviderUI{
				URL: "http://app-studio.invalid",
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "app-studio")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got, ok := reg.Get("app-studio")
	if !ok {
		t.Fatal("expected app-studio in registry")
	}
	if got.UIURL == nil || got.UIURL.String() != "http://app-studio.invalid" {
		t.Fatalf("UIURL = %v, want http://app-studio.invalid", got.UIURL)
	}
	if got.LocalUIAssets != nil {
		t.Fatal("expected chart-owned provider to keep proxy routing, not embedded assets")
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0].Name != "code" {
		t.Fatalf("Dependencies = %#v, want [code]", got.Dependencies)
	}
	// The portal's catalog cards and first-run welcome flow render this; if the
	// reconciler drops it, both fall back to showing a bare provider name.
	if got.Description != "Persistent AI project workspace." {
		t.Fatalf("Description = %q, want the spec value", got.Description)
	}
	if !got.EndpointsValid {
		t.Fatal("expected endpoints to be valid when ui.url is present")
	}

	var updated providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app-studio"}, &updated); err != nil {
		t.Fatalf("get updated entry: %v", err)
	}
	if updated.Status.Endpoints == nil || updated.Status.Endpoints.UI != "http://app-studio.invalid" {
		t.Fatalf("status endpoints = %#v, want UI=http://app-studio.invalid", updated.Status.Endpoints)
	}
}

func TestCatalogReconcilerRejectsInvalidActionDeclarations(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-actions"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Invalid actions",
			UI:          &providersv1alpha1.ProviderUI{URL: "http://provider.invalid"},
			Actions: []providersv1alpha1.ProviderActionSpec{{
				ID:          "query_table/latest",
				DisplayName: "Invalid action",
			}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "invalid-actions")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := reg.Get("invalid-actions"); ok {
		t.Fatal("invalid action declaration must not enter the provider registry")
	}

	var updated providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "invalid-actions"}, &updated); err != nil {
		t.Fatalf("get updated entry: %v", err)
	}
	if len(updated.Status.Conditions) != 1 {
		t.Fatalf("conditions = %#v, want one Ready condition", updated.Status.Conditions)
	}
	condition := updated.Status.Conditions[0]
	if condition.Type != "Ready" || condition.Status != metav1.ConditionFalse || condition.Reason != "InvalidActions" {
		t.Fatalf("condition = %#v, want Ready=False/InvalidActions", condition)
	}
}

func TestCatalogReconcilerOmitsInvalidAssistantSkillAndKeepsValidSibling(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	valid := providersv1alpha1.ProviderAssistantSkillSpec{
		PackageName: "valid",
		Version:     "1.0.0",
		Skill:       "---\nname: valid\ndescription: valid guidance\n---\nbody\n",
	}
	digest, err := providersv1alpha1.ProviderAssistantSkillDigest(valid)
	if err != nil {
		t.Fatalf("skill digest: %v", err)
	}
	valid.Digest = digest
	invalid := valid
	invalid.PackageName = "invalid"
	invalid.Skill += "tampered"
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "skills"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Skills",
			UI:          &providersv1alpha1.ProviderUI{URL: "http://skills.invalid"},
			AssistantSkills: []providersv1alpha1.ProviderAssistantSkillSpec{
				invalid,
				valid,
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "skills")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got, ok := reg.Get("skills")
	if !ok {
		t.Fatal("expected provider in registry")
	}
	if len(got.AssistantSkills) != 1 || got.AssistantSkills[0].PackageName != "valid" || got.AssistantSkills[0].Digest != digest {
		t.Fatalf("assistant skills = %#v, want only valid sibling", got.AssistantSkills)
	}
}

// Every replica runs the catalog reconciler, so an unconditional status write
// is a cross-replica write storm: each Update bumps the resource version, every
// peer's watch fires, and they all write again. A steady-state reconcile must
// therefore leave the object untouched.
func TestCatalogReconcilerDoesNotRewriteUnchangedStatus(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "cost"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Cost",
			Backend:     &providersv1alpha1.ProviderBackend{URL: "http://cost.invalid"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true, healthClient: healthyHTTPDoer()}
	req := testfakes.NewRequest("cluster", "", "cost")
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	var afterFirst providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cost"}, &afterFirst); err != nil {
		t.Fatalf("get after first reconcile: %v", err)
	}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	var afterSecond providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cost"}, &afterSecond); err != nil {
		t.Fatalf("get after second reconcile: %v", err)
	}

	if afterFirst.ResourceVersion != afterSecond.ResourceVersion {
		t.Fatalf("status rewritten with no change: resourceVersion %s -> %s",
			afterFirst.ResourceVersion, afterSecond.ResourceVersion)
	}
}

// The status heartbeat is how a beat served by one replica reaches the others;
// the reconciler has to carry it into the registry.
func TestCatalogReconcilerAdoptsStatusHeartbeat(t *testing.T) {
	beat := metav1.NewTime(time.Now().Add(-time.Second).Truncate(time.Second))
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "cost"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			Backend: &providersv1alpha1.ProviderBackend{URL: "http://cost.invalid"},
		},
		Status: providersv1alpha1.CatalogEntryStatus{
			LastHeartbeat:   &beat,
			ReportedVersion: "v4.5.6",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true, healthClient: healthyHTTPDoer()}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "cost")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got, ok := reg.Get("cost")
	if !ok {
		t.Fatal("cost missing from registry")
	}
	if !got.LastHeartbeat.Equal(beat.Time) || got.ReportedVersion != "v4.5.6" || !got.HeartbeatRequired {
		t.Fatalf("registry did not adopt status heartbeat: %+v", got)
	}
	if !got.Ready() {
		t.Fatal("provider with a fresh heartbeat should be Ready on every replica")
	}
}

func TestCatalogReconcilerBackendHealthGatesReadyAndRecovers(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "code", Generation: 2},
		Spec: providersv1alpha1.CatalogEntrySpec{
			Backend: &providersv1alpha1.ProviderBackend{URL: "http://code.invalid", HealthPath: "/readyz"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()
	r := &CatalogReconciler{
		mgr: testfakes.NewManager(c), reg: reg, noKCP: true,
		healthClient: httpDoerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("sensitive upstream detail")),
				Header:     make(http.Header),
			}, nil
		}),
	}
	req := testfakes.NewRequest("cluster", "", "code")
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("reconcile unhealthy: %v", err)
	}
	if result.RequeueAfter != SweepInterval {
		t.Fatalf("requeueAfter = %v, want %v", result.RequeueAfter, SweepInterval)
	}
	got, ok := reg.Get("code")
	if !ok || got.Ready() || !got.BackendHealthRequired || got.BackendHealthy {
		t.Fatalf("unhealthy registry provider = %+v, found=%v", got, ok)
	}

	var updated providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "code"}, &updated); err != nil {
		t.Fatalf("get unhealthy entry: %v", err)
	}
	backendCondition := conditionByType(updated.Status.Conditions, "BackendHealthy")
	readyCondition := conditionByType(updated.Status.Conditions, "Ready")
	if backendCondition == nil || backendCondition.Status != metav1.ConditionFalse ||
		readyCondition == nil || readyCondition.Status != metav1.ConditionFalse || readyCondition.Reason != "BackendUnhealthy" {
		t.Fatalf("unhealthy conditions = %#v", updated.Status.Conditions)
	}
	for _, condition := range updated.Status.Conditions {
		if strings.Contains(condition.Message, "sensitive upstream detail") || strings.Contains(condition.Message, "code.invalid") {
			t.Fatalf("condition leaked probe detail: %#v", condition)
		}
	}

	r.healthClient = healthyHTTPDoer()
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile healthy: %v", err)
	}
	if got, _ := reg.Get("code"); !got.Ready() || !got.BackendHealthy {
		t.Fatalf("healthy registry provider = %+v", got)
	}
}

func TestCatalogReconcilerDoesNotDirectlyProbeOrgOwnedBackend(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "database"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			Backend: &providersv1alpha1.ProviderBackend{URL: "http://tenant-controlled.invalid", HealthPath: "/readyz"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()
	probes := 0
	r := &CatalogReconciler{
		mgr: testfakes.NewManager(c), reg: reg, prov: &Provisioner{},
		clusterPaths: map[string]string{"cluster": kcppaths.OrgProviderPath("org-1", "database")},
		edgeRoutes: edgeRouteResolverFunc(func(context.Context, string, string, string) (*EdgeRoute, error) {
			return &EdgeRoute{Cluster: "tenant-cluster", ServiceName: "provider-database"}, nil
		}),
		healthClient: httpDoerFunc(func(*http.Request) (*http.Response, error) {
			probes++
			return nil, fmt.Errorf("org-owned backend must not be dialled")
		}),
	}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "database")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if probes != 0 {
		t.Fatalf("org-owned backend received %d direct probes", probes)
	}
	got, ok := reg.GetForOrg("org-1", "database")
	if !ok || got.BackendHealthRequired || !got.Ready() {
		t.Fatalf("org-owned registry provider = %+v, found=%v", got, ok)
	}
}

func TestCatalogReconcilerRetriesOrgOwnedEdgeRouteAndRecovers(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "database"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			Backend: &providersv1alpha1.ProviderBackend{URL: "http://database.tenant.svc", HealthPath: "/readyz"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()
	routeReady := false
	r := &CatalogReconciler{
		mgr: testfakes.NewManager(c), reg: reg, prov: &Provisioner{},
		clusterPaths: map[string]string{"cluster": kcppaths.OrgProviderPath("org-1", "database")},
		edgeRoutes: edgeRouteResolverFunc(func(context.Context, string, string, string) (*EdgeRoute, error) {
			if !routeReady {
				return nil, fmt.Errorf("temporary route lookup failure")
			}
			return &EdgeRoute{Cluster: "tenant-cluster", ServiceName: "provider-database"}, nil
		}),
	}
	req := testfakes.NewRequest("cluster", "", "database")
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("reconcile unroutable: %v", err)
	}
	if result.RequeueAfter != SweepInterval {
		t.Fatalf("unroutable requeueAfter = %v, want %v", result.RequeueAfter, SweepInterval)
	}
	if got, ok := reg.GetForOrg("org-1", "database"); !ok || got.Ready() {
		t.Fatalf("unroutable registry provider = %+v, found=%v", got, ok)
	}
	var updated providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "database"}, &updated); err != nil {
		t.Fatalf("get unroutable entry: %v", err)
	}
	readyCondition := conditionByType(updated.Status.Conditions, "Ready")
	if readyCondition == nil || readyCondition.Status != metav1.ConditionFalse || readyCondition.Reason != "BackendUnroutable" {
		t.Fatalf("unroutable conditions = %#v", updated.Status.Conditions)
	}

	routeReady = true
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile recovered route: %v", err)
	}
	if got, ok := reg.GetForOrg("org-1", "database"); !ok || !got.Ready() || !got.EdgeRoute.Usable() {
		t.Fatalf("recovered registry provider = %+v, found=%v", got, ok)
	}
}

func TestProbeBackendHealthUsesSameAuthorityAndBoundedPath(t *testing.T) {
	var requested string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	backend, err := url.Parse(server.URL + "/services/provider")
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}
	if err := probeBackendHealth(context.Background(), server.Client(), backend, "/readyz?full=1"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if requested != "/services/provider/readyz?full=1" {
		t.Fatalf("request URI = %q", requested)
	}
	for _, healthPath := range []string{"https://attacker.invalid/healthz", "//attacker.invalid/healthz", "/../admin", "/%2e%2e/admin"} {
		if err := probeBackendHealth(context.Background(), server.Client(), backend, healthPath); err == nil {
			t.Fatalf("healthPath %q was accepted", healthPath)
		}
	}
}

func TestDefaultBackendHealthClientDoesNotFollowRedirects(t *testing.T) {
	redirectTargetHit := false
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTargetHit = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(redirectTarget.Close)
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", redirectTarget.URL)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(backendServer.Close)
	backend, err := url.Parse(backendServer.URL)
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}
	if err := probeBackendHealth(context.Background(), defaultBackendHealthClient(), backend, "/readyz"); err == nil {
		t.Fatal("redirecting backend was considered healthy")
	}
	if redirectTargetHit {
		t.Fatal("backend health probe followed a redirect to another authority")
	}
}

func conditionByType(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// Rotation only writes an expiry onto the retired credential; the catalog
// reconciler is what actually deletes it, in whichever workspace the provider's
// CatalogEntry lives — which is the same workspace holding its Secrets. A
// pending expiry has to bring the reconciler back, or the credential outlives
// its grace period until something unrelated happens to requeue the entry.
func TestCatalogReconcilerSweepsRotatedProviderCredentials(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "cost"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			APIExport: &providersv1alpha1.ProviderAPIExport{Name: "cost.providers.faros.sh"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	var swept []string
	next := time.Now().Add(time.Hour)
	r := &CatalogReconciler{
		mgr: testfakes.NewManager(c), reg: reg, noKCP: true,
		sweepCredentials: func(_ context.Context, cluster string) (int, time.Time, error) {
			swept = append(swept, cluster)
			return 1, next, nil
		},
	}
	res, err := r.Reconcile(context.Background(), testfakes.NewRequest("cost-cluster", "", "cost"))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(swept) != 1 || swept[0] != "cost-cluster" {
		t.Fatalf("swept %v, want the provider's own workspace cluster", swept)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("a pending credential expiry did not schedule a requeue; the retired token would outlive its grace period")
	}
}

// A sweep that cannot run must not take the provider out of the registry with
// it: routing is the reconciler's real job, and an un-deleted Secret is the
// lesser failure.
func TestCatalogReconcilerSurvivesASweepFailure(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "cost"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			APIExport: &providersv1alpha1.ProviderAPIExport{Name: "cost.providers.faros.sh"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{
		mgr: testfakes.NewManager(c), reg: reg, noKCP: true,
		sweepCredentials: func(context.Context, string) (int, time.Time, error) {
			return 0, time.Time{}, fmt.Errorf("kcp unavailable")
		},
	}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cost-cluster", "", "cost")); err != nil {
		t.Fatalf("reconcile failed on a sweep error: %v", err)
	}
	if _, ok := reg.Get("cost"); !ok {
		t.Fatal("provider dropped out of the registry because a credential sweep failed")
	}
}
