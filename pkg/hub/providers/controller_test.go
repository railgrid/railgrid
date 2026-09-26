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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/kcppaths"
	"github.com/railgrid/railgrid/utils/testfakes"
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
			Requires: []providersv1alpha1.ProviderRequirement{{
				Provider: "code",
				Group:    "code.railgrid.ai",
				Resources: []providersv1alpha1.ProviderRequiredResource{{
					Name:  "repositories",
					Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet},
				}},
			}},
			Export: &providersv1alpha1.ProviderExport{Name: "ai.providers.railgrid.ai"},
			Serving: &providersv1alpha1.ProviderServing{UI: &providersv1alpha1.ProviderUI{
				URL: "http://app-studio.invalid",
			}},
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
	if deps := providersv1alpha1.Dependencies(got.Requires); len(deps) != 1 || deps[0] != "code" {
		t.Fatalf("Dependencies = %#v, want [code]", deps)
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
			Serving:     &providersv1alpha1.ProviderServing{UI: &providersv1alpha1.ProviderUI{URL: "http://provider.invalid"}},

			Export: &providersv1alpha1.ProviderExport{
				Name: "invalid.providers.railgrid.ai",
				Resources: []providersv1alpha1.ProviderExportResource{{
					Name:       "tables",
					APIVersion: "invalid.railgrid.ai/v1alpha1",
					Kind:       "Table",
					Actions: []providersv1alpha1.ProviderAction{{
						Name:        "query_table",
						Version:     "latest",
						DisplayName: "Invalid action",
					}},
				}},
			},
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
	if condition.Type != "Ready" || condition.Status != metav1.ConditionFalse || condition.Reason != "InvalidExport" {
		t.Fatalf("condition = %#v, want Ready=False/InvalidExport", condition)
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
			Serving:     &providersv1alpha1.ProviderServing{UI: &providersv1alpha1.ProviderUI{URL: "http://skills.invalid"}},
			Hub: &providersv1alpha1.ProviderHub{AssistantSkills: []providersv1alpha1.ProviderAssistantSkillSpec{
				invalid,
				valid,
			}},
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
			Serving:     &providersv1alpha1.ProviderServing{Backend: &providersv1alpha1.ProviderBackend{URL: "http://cost.invalid"}},
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
			Serving: &providersv1alpha1.ProviderServing{Backend: &providersv1alpha1.ProviderBackend{URL: "http://cost.invalid"}},
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
			Serving: &providersv1alpha1.ProviderServing{Backend: &providersv1alpha1.ProviderBackend{URL: "http://code.invalid", HealthPath: "/readyz"}},
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
			Serving: &providersv1alpha1.ProviderServing{Backend: &providersv1alpha1.ProviderBackend{URL: "http://tenant-controlled.invalid", HealthPath: "/readyz"}},
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
			Serving: &providersv1alpha1.ProviderServing{Backend: &providersv1alpha1.ProviderBackend{URL: "http://database.tenant.svc", HealthPath: "/readyz"}},
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
			Export: &providersv1alpha1.ProviderExport{Name: "cost.providers.railgrid.ai"},
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
			Export: &providersv1alpha1.ProviderExport{Name: "cost.providers.railgrid.ai"},
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

// TestCatalogReconcilerRejectsRequirementOnAForeignGroup: a provider may only
// attribute a group to the provider that actually serves it. Pointing a
// requirement at somebody else's group would make the Enable dialog describe one
// provider's API while the grant reached another's.
//
// The named provider here has the shape every real provider has — an export
// named after the provider, kinds in a different group — so the check cannot
// pass by comparing the two names.
func TestCatalogReconcilerRejectsRequirementOnAForeignGroup(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{
		Name:          "code",
		APIExportName: "code.providers.railgrid.ai",
		APIGroups:     []string{"code.railgrid.ai"},
	})
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "app-studio-composer"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "App Studio",
			Serving:     &providersv1alpha1.ProviderServing{UI: &providersv1alpha1.ProviderUI{URL: "http://provider.invalid"}},

			Export: &providersv1alpha1.ProviderExport{Name: "app-studio.railgrid.ai"},
			Requires: []providersv1alpha1.ProviderRequirement{{
				Provider: "code",
				Group:    "infrastructure.railgrid.ai",
				Resources: []providersv1alpha1.ProviderRequiredResource{{
					Name:  "instances",
					Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbCreate},
				}},
			}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "app-studio-composer")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := reg.Get("app-studio-composer"); ok {
		t.Fatal("a requirement on a group the named provider does not serve must not enter the registry")
	}
	var updated providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app-studio-composer"}, &updated); err != nil {
		t.Fatalf("get updated entry: %v", err)
	}
	if len(updated.Status.Conditions) != 1 || updated.Status.Conditions[0].Reason != "InvalidRequirements" {
		t.Fatalf("conditions = %#v, want Ready=False/InvalidRequirements", updated.Status.Conditions)
	}
}

// A wildcard is refused on shape alone, with no registry lookup involved.
func TestCatalogReconcilerRejectsWildcardRequirement(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "wildcard-composer"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Wildcard",
			Serving:     &providersv1alpha1.ProviderServing{UI: &providersv1alpha1.ProviderUI{URL: "http://provider.invalid"}},

			Export: &providersv1alpha1.ProviderExport{Name: "wildcard.railgrid.ai"},
			Requires: []providersv1alpha1.ProviderRequirement{{
				Provider: "infrastructure",
				Group:    "infrastructure.railgrid.ai",
				Resources: []providersv1alpha1.ProviderRequiredResource{{
					Name:  "*",
					Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbCreate},
				}},
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).WithObjects(entry).Build()
	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "wildcard-composer")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := reg.Get("wildcard-composer"); ok {
		t.Fatal("a wildcard requirement must not enter the registry")
	}
}

// A requirement on another provider, declared without an export of one's own,
// is refused: only a provider with its own API surface has reconcilers that
// would write another provider's objects into a tenant workspace.
func TestCatalogReconcilerRejectsRequirementWithoutOwnExport(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "exportless-composer"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Exportless",
			Serving:     &providersv1alpha1.ProviderServing{UI: &providersv1alpha1.ProviderUI{URL: "http://provider.invalid"}},

			Requires: []providersv1alpha1.ProviderRequirement{{
				Provider: "infrastructure",
				Group:    "infrastructure.railgrid.ai",
				Resources: []providersv1alpha1.ProviderRequiredResource{{
					Name:  "instances",
					Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbCreate},
				}},
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).WithObjects(entry).Build()
	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "exportless-composer")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := reg.Get("exportless-composer"); ok {
		t.Fatal("a requirement on another provider without spec.export must not enter the registry")
	}
}

// The happy path: a valid declaration reaches the registry and the API, verbs
// and all, so a consumer reads what to ask for instead of guessing.
func TestCatalogReconcilerProjectsRequirements(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{
		Name:          "infrastructure",
		APIExportName: "infrastructure.providers.railgrid.ai",
		APIGroups:     []string{"infrastructure.railgrid.ai"},
	})
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "valid-composer"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Valid",
			Serving:     &providersv1alpha1.ProviderServing{UI: &providersv1alpha1.ProviderUI{URL: "http://provider.invalid"}},

			Export: &providersv1alpha1.ProviderExport{Name: "valid.railgrid.ai"},
			Requires: []providersv1alpha1.ProviderRequirement{{
				Provider: "infrastructure",
				Group:    "infrastructure.railgrid.ai",
				Resources: []providersv1alpha1.ProviderRequiredResource{
					{Name: "instances", Verbs: []providersv1alpha1.ProviderRequiredVerb{
						providersv1alpha1.RequiredVerbGet, providersv1alpha1.RequiredVerbList,
						providersv1alpha1.RequiredVerbWatch, providersv1alpha1.RequiredVerbCreate,
						providersv1alpha1.RequiredVerbUpdate, providersv1alpha1.RequiredVerbDelete,
					}},
					{Name: "instances/exec"},
				},
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).WithObjects(entry).Build()
	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "valid-composer")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	prov, ok := reg.Get("valid-composer")
	if !ok || len(prov.Requires) != 1 || len(prov.Requires[0].Resources) != 2 {
		t.Fatalf("registry entry = %#v, want one requirement with two coordinates", prov.Requires)
	}
	coordinates := providersv1alpha1.RequiredCoordinates(prov.Requires)
	if len(coordinates) != 2 {
		t.Fatalf("coordinates = %#v, want two claims", coordinates)
	}
	if coordinates[0].Group != "infrastructure.railgrid.ai" || coordinates[0].Resource != "instances" || len(coordinates[0].Verbs) != 6 {
		t.Fatalf("kind claim = %#v", coordinates[0])
	}
	// A verb coordinate carries no verbs of its own: the verb IS the capability.
	if !coordinates[1].Coordinate || coordinates[1].Resource != "instances/exec" || len(coordinates[1].Verbs) != 0 {
		t.Fatalf("verb coordinate = %#v", coordinates[1])
	}
	// The same declaration is also the dependency edge the Enable flow checks.
	if deps := providersv1alpha1.Dependencies(prov.Requires); len(deps) != 1 || deps[0] != "infrastructure" {
		t.Fatalf("dependencies = %#v, want [infrastructure]", deps)
	}
}

// fakeAPIExport builds the object the hub reads a provider's served groups
// out of: an apis.kcp.io/v1alpha2 APIExport whose spec.resources name the
// schemas it serves, each tagged with the group the kinds live in.
func fakeAPIExport(name string, resources ...[2]string) *unstructured.Unstructured {
	entries := make([]any, 0, len(resources))
	for _, resource := range resources {
		entries = append(entries, map[string]any{
			"group":   resource[0],
			"name":    resource[1],
			"schema":  "v260919-abcdef12." + resource[1] + "." + resource[0],
			"storage": map[string]any{"crd": map[string]any{}},
		})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIExport",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"resources": entries},
	}}
}

// TestAPIExportGroupsProjectsServedGroups: the projection is the whole source
// of truth for "who owns this API group", so it dedupes, sorts, and refuses to
// let a core-group entry make the empty group look owned.
func TestAPIExportGroupsProjectsServedGroups(t *testing.T) {
	export := fakeAPIExport("edges.providers.railgrid.ai",
		[2]string{"edges.railgrid.ai", "kubernetesclusters"},
		[2]string{"edges.railgrid.ai", "linuxservers"},
		[2]string{"addons.edges.railgrid.ai", "addons"},
		[2]string{"", "configmaps"},
	)
	got := APIExportGroups(export)
	want := []string{"addons.edges.railgrid.ai", "edges.railgrid.ai"}
	if len(got) != len(want) {
		t.Fatalf("APIExportGroups = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("APIExportGroups = %v, want %v", got, want)
		}
	}
	if len(APIExportGroups(nil)) != 0 {
		t.Fatal("a nil export must project to no groups, not to an owned one")
	}
}

// TestCatalogReconcilerReadsAPIGroupsFromTheAPIExport is the bug this field
// exists for: the edges provider's APIExport is named
// edges.providers.railgrid.ai and its kinds live in edges.railgrid.ai. Nothing
// may infer one from the other, so the reconciler reads the export and records
// what it actually serves.
func TestCatalogReconcilerReadsAPIGroupsFromTheAPIExport(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "edges"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Edges",
			Export:      &providersv1alpha1.ProviderExport{Name: "edges.providers.railgrid.ai"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	var askedPath, askedExport string
	r := &CatalogReconciler{
		mgr: testfakes.NewManager(c), reg: reg, noKCP: true,
		resolveAPIGroups: func(_ context.Context, workspacePath, exportName string) ([]string, error) {
			askedPath, askedExport = workspacePath, exportName
			return APIExportGroups(fakeAPIExport(exportName,
				[2]string{"edges.railgrid.ai", "kubernetesclusters"},
				[2]string{"edges.railgrid.ai", "linuxservers"},
			)), nil
		},
	}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "edges")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if askedExport != "edges.providers.railgrid.ai" || askedPath != kcppaths.ProvidersParent+":edges" {
		t.Fatalf("read APIExport %q in %q, want the declared export in the provider workspace", askedExport, askedPath)
	}
	prov, ok := reg.Get("edges")
	if !ok {
		t.Fatal("expected edges in the registry")
	}
	if len(prov.APIGroups) != 1 || prov.APIGroups[0] != "edges.railgrid.ai" {
		t.Fatalf("APIGroups = %v, want [edges.railgrid.ai] — the group the export SERVES, not its name", prov.APIGroups)
	}
	if prov.APIExportName != "edges.providers.railgrid.ai" {
		t.Fatalf("APIExportName = %q, want the export's own name kept alongside the groups", prov.APIExportName)
	}

	var updated providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "edges"}, &updated); err != nil {
		t.Fatalf("get updated entry: %v", err)
	}
	if len(updated.Status.APIGroups) != 1 || updated.Status.APIGroups[0] != "edges.railgrid.ai" {
		t.Fatalf("status.apiGroups = %v, want the resolved projection mirrored for the portal and /api/providers", updated.Status.APIGroups)
	}
	if condition := findCondition(updated.Status.Conditions, ConditionAPIGroupsUnknown); condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("APIGroupsUnknown = %#v, want False once the export was read", condition)
	}
}

// An unreadable APIExport leaves the provider owning NO group. Nothing is
// guessed from the export name, the gap is visible as a condition, and the
// entry comes back to try again.
func TestCatalogReconcilerReportsUnknownAPIGroups(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "edges"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Edges",
			Export:      &providersv1alpha1.ProviderExport{Name: "edges.providers.railgrid.ai"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{
		mgr: testfakes.NewManager(c), reg: reg, noKCP: true,
		resolveAPIGroups: func(context.Context, string, string) ([]string, error) {
			return nil, fmt.Errorf("APIExport not found")
		},
	}
	res, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "edges"))
	if err != nil {
		t.Fatalf("an unreadable APIExport must not fail the reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("unknown API groups did not schedule a retry; nothing else would bring this entry back")
	}
	prov, ok := reg.Get("edges")
	if !ok {
		t.Fatal("an unreadable export must not take the provider out of the registry; only its group ownership is withheld")
	}
	if len(prov.APIGroups) != 0 {
		t.Fatalf("APIGroups = %v, want none: an unreadable export owns nothing", prov.APIGroups)
	}
	var updated providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "edges"}, &updated); err != nil {
		t.Fatalf("get updated entry: %v", err)
	}
	condition := findCondition(updated.Status.Conditions, ConditionAPIGroupsUnknown)
	if condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("APIGroupsUnknown = %#v, want True so the gap is visible", condition)
	}
	if !strings.Contains(condition.Message, "APIExport not found") {
		t.Fatalf("condition message %q does not say why the export could not be read", condition.Message)
	}
}

// A read that fails after one succeeded must not RETRACT the groups: an
// export's group set does not change because kcp blinked, and dropping it
// would refuse every consumer's cross-provider rule on the next refresh.
// status carries the last successful read across replicas and restarts.
func TestCatalogReconcilerKeepsLastKnownAPIGroupsOnAReadFailure(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "edges"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Edges",
			Export:      &providersv1alpha1.ProviderExport{Name: "edges.providers.railgrid.ai"},
		},
		Status: providersv1alpha1.CatalogEntryStatus{APIGroups: []string{"edges.railgrid.ai"}},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{
		mgr: testfakes.NewManager(c), reg: reg, noKCP: true,
		resolveAPIGroups: func(context.Context, string, string) ([]string, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "edges")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	prov, ok := reg.Get("edges")
	if !ok {
		t.Fatal("expected edges in the registry")
	}
	if len(prov.APIGroups) != 1 || prov.APIGroups[0] != "edges.railgrid.ai" {
		t.Fatalf("APIGroups = %v, want the last successful read kept across a transient failure", prov.APIGroups)
	}
}

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
