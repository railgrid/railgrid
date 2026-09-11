/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tenant_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/tenant/tenanttest"
)

var (
	widgetsGVR = schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	widgets    = tenant.Resource{GVR: widgetsGVR, Kind: "Widget", Plural: "Widgets"}
	secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	secrets    = tenant.Resource{GVR: secretsGVR, Kind: "Secret", Plural: "Secrets", Namespaced: true}
)

func widget(name, colour string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.test/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": name, "labels": map[string]any{"colour": colour}},
		"spec":       map[string]any{"colour": colour},
	}}
}

func scopeFor(t *testing.T, proxy *tenanttest.Server) *tenant.Scope {
	t.Helper()
	scope, err := proxy.Client().For("cluster-a", "caller-token")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	return scope
}

func TestForRequiresClusterAndToken(t *testing.T) {
	c := tenant.NewClient("https://hub.example/", false)
	if _, err := c.For("", "token"); err == nil || !strings.Contains(err.Error(), "X-Faros-Cluster") {
		t.Fatalf("For with empty cluster = %v, want missing-cluster error", err)
	}
	if _, err := c.For("cluster-a", ""); err == nil || !strings.Contains(err.Error(), "bearer token") {
		t.Fatalf("For with empty token = %v, want missing-token error", err)
	}
	if _, err := c.For("cluster-a", "token"); err != nil {
		t.Fatalf("For: %v", err)
	}
}

func TestScopeTargetsClusterProxyAsCaller(t *testing.T) {
	proxy := tenanttest.NewServer(t)
	proxy.Add(widgetsGVR, widget("w1", "red"))
	scope := scopeFor(t, proxy)
	got, err := scope.Get(context.Background(), widgets, "", "w1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.GetName() != "w1" || got.GetResourceVersion() == "" {
		t.Fatalf("Get = %#v, want stored w1 with a resourceVersion", got.Object)
	}
	reqs := proxy.Requests()
	if len(reqs) != 1 || reqs[0].Path != "/clusters/cluster-a/apis/example.test/v1/widgets/w1" || reqs[0].Bearer != "caller-token" {
		t.Fatalf("requests = %#v, want one GET under /clusters/cluster-a as the caller", reqs)
	}
}

func TestScopeMapsServerFailuresToAPIErrors(t *testing.T) {
	proxy := tenanttest.NewServer(t)
	proxy.Register(widgetsGVR)
	scope := scopeFor(t, proxy)
	ctx := context.Background()

	if _, err := scope.Get(ctx, widgets, "", "missing"); !apierrors.IsNotFound(err) {
		t.Fatalf("Get missing = %v, want NotFound", err)
	}
	unserved := tenant.Resource{GVR: schema.GroupVersionResource{Group: "nope.test", Version: "v1", Resource: "things"}, Kind: "Thing"}
	_, err := scope.List(ctx, unserved, "")
	if !apierrors.IsNotFound(err) || !strings.Contains(err.Error(), "the server could not find the requested resource") {
		t.Fatalf("List unserved API = %v, want kcp-style NotFound", err)
	}
	if err := scope.Delete(ctx, widgets, "", "missing"); !apierrors.IsNotFound(err) {
		t.Fatalf("Delete missing = %v, want NotFound", err)
	}
}

func TestApplyIsCreateOrUpdateWithoutResourceVersion(t *testing.T) {
	proxy := tenanttest.NewServer(t)
	proxy.Register(widgetsGVR)
	scope := scopeFor(t, proxy)
	ctx := context.Background()

	created, err := scope.Apply(ctx, widgets, widget("w1", "red"))
	if err != nil {
		t.Fatalf("Apply create: %v", err)
	}
	if created.GetResourceVersion() == "" {
		t.Fatalf("created = %#v, want resourceVersion", created.Object)
	}
	updated, err := scope.Apply(ctx, widgets, widget("w1", "blue"))
	if err != nil {
		t.Fatalf("Apply update: %v", err)
	}
	if colour, _, _ := unstructured.NestedString(updated.Object, "spec", "colour"); colour != "blue" {
		t.Fatalf("updated colour = %q, want blue", colour)
	}
	if updated.GetResourceVersion() == created.GetResourceVersion() || updated.GetUID() != created.GetUID() {
		t.Fatalf("update = rv %q uid %q, want new rv and same uid as create (rv %q uid %q)", updated.GetResourceVersion(), updated.GetUID(), created.GetResourceVersion(), created.GetUID())
	}
	var methods []string
	for _, r := range proxy.Requests() {
		methods = append(methods, r.Method)
	}
	if got := strings.Join(methods, " "); got != "POST POST GET PUT" {
		t.Fatalf("request sequence = %q, want create, then conflict-create/get/update", got)
	}
}

func TestApplyWithResourceVersionIsCompareAndSwap(t *testing.T) {
	proxy := tenanttest.NewServer(t)
	seed := widget("w1", "red")
	seed.SetResourceVersion("10")
	proxy.Add(widgetsGVR, seed)
	scope := scopeFor(t, proxy)
	ctx := context.Background()

	stale := widget("w1", "green")
	stale.SetResourceVersion("9")
	if _, err := scope.Apply(ctx, widgets, stale); !apierrors.IsConflict(err) {
		t.Fatalf("Apply stale rv = %v, want Conflict", err)
	}
	fresh := widget("w1", "green")
	fresh.SetResourceVersion("10")
	if _, err := scope.Apply(ctx, widgets, fresh); err != nil {
		t.Fatalf("Apply matching rv: %v", err)
	}
	for _, r := range proxy.Requests() {
		if r.Method == http.MethodPost {
			t.Fatalf("Apply with a resourceVersion issued a create: %#v", r)
		}
	}
}

func TestApplyStatusMergePatchesStatusSubresource(t *testing.T) {
	proxy := tenanttest.NewServer(t)
	seed := widget("w1", "red")
	seed.Object["status"] = map[string]any{"phase": "Pending", "observed": int64(1)}
	proxy.Add(widgetsGVR, seed)
	scope := scopeFor(t, proxy)

	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.test/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "w1"},
		"spec":     map[string]any{"colour": "ignored-by-status-write"},
		"status":   map[string]any{"phase": "Ready"},
	}}
	if err := scope.ApplyStatus(context.Background(), widgets, desired); err != nil {
		t.Fatalf("ApplyStatus: %v", err)
	}
	reqs := proxy.RequestsFor(http.MethodPatch, widgetsGVR)
	if len(reqs) != 1 || reqs[0].Subresource != "status" || reqs[0].Headers.Get("Content-Type") != string(types.MergePatchType) {
		t.Fatalf("patch requests = %#v, want one merge patch on /status", reqs)
	}
	if string(reqs[0].Body) != `{"status":{"phase":"Ready"}}` {
		t.Fatalf("patch body = %s, want only the status", reqs[0].Body)
	}
	stored := proxy.Get(widgetsGVR, "", "w1")
	if phase, _, _ := unstructured.NestedString(stored.Object, "status", "phase"); phase != "Ready" {
		t.Fatalf("stored phase = %q, want Ready", phase)
	}
	if observed, _, _ := unstructured.NestedInt64(stored.Object, "status", "observed"); observed != 1 {
		t.Fatalf("stored status.observed = %d, want merge to keep untouched fields", observed)
	}
	if colour, _, _ := unstructured.NestedString(stored.Object, "spec", "colour"); colour != "red" {
		t.Fatalf("stored spec.colour = %q, want status write to leave spec alone", colour)
	}
}

func TestListWithOptionsAppliesSelectorServerSide(t *testing.T) {
	proxy := tenanttest.NewServer(t)
	proxy.Add(widgetsGVR, widget("w1", "red"), widget("w2", "blue"))
	scope := scopeFor(t, proxy)

	got, err := scope.ListWithOptions(context.Background(), widgets, "", metav1.ListOptions{LabelSelector: "colour=blue"})
	if err != nil {
		t.Fatalf("ListWithOptions: %v", err)
	}
	if len(got) != 1 || got[0].GetName() != "w2" {
		t.Fatalf("list = %#v, want only w2", got)
	}
	if reqs := proxy.RequestsFor(http.MethodGet, widgetsGVR); len(reqs) != 1 || reqs[0].Query.Get("labelSelector") != "colour=blue" {
		t.Fatalf("list requests = %#v, want labelSelector forwarded", reqs)
	}
}

func TestListInfrastructureInstancesForwardsLabelSelectorAndPreservesMetadata(t *testing.T) {
	const selector = "faros.sh/app-studio-run-sandbox=true"
	proxy := tenanttest.NewServer(t)
	proxy.Add(tenant.InfrastructureInstancesResource.GVR,
		tenanttest.ObjectFromYAML(t, `{"apiVersion":"infrastructure.faros.sh/v1alpha1","kind":"Instance","metadata":{"name":"sandbox-a","labels":{"faros.sh/app-studio-run-sandbox":"true"},"annotations":{"faros.sh/app-studio-run-sandbox-hard-expires-at":"2099-01-01T00:00:00Z"}},"status":{"phase":"Ready"}}`),
		tenanttest.ObjectFromYAML(t, `{"apiVersion":"infrastructure.faros.sh/v1alpha1","kind":"Instance","metadata":{"name":"other"},"status":{"phase":"Ready"}}`),
	)
	scope := scopeFor(t, proxy)
	got, err := scope.ListInfrastructureInstances(context.Background(), metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		t.Fatalf("list infrastructure instances: %v", err)
	}
	if len(got) != 1 || got[0].GetName() != "sandbox-a" {
		t.Fatalf("instances = %#v, want sandbox-a", got)
	}
	if got[0].GetAnnotations()["faros.sh/app-studio-run-sandbox-hard-expires-at"] == "" {
		t.Fatalf("instance annotations = %#v, want expiry annotation", got[0].GetAnnotations())
	}
	if phase, _, _ := unstructured.NestedString(got[0].Object, "status", "phase"); phase != "Ready" {
		t.Fatalf("instance status.phase = %q, want Ready", phase)
	}
	reqs := proxy.RequestsFor(http.MethodGet, tenant.InfrastructureInstancesResource.GVR)
	if len(reqs) != 1 || reqs[0].Query.Get("labelSelector") != selector {
		t.Fatalf("requests = %#v, want one list with labelSelector %q", reqs, selector)
	}
}

func TestNamespacedResourcesUseNamespacePaths(t *testing.T) {
	proxy := tenanttest.NewServer(t)
	proxy.Register(secretsGVR)
	scope := scopeFor(t, proxy)
	ctx := context.Background()

	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "llm", "namespace": "default"},
		"data":     map[string]any{"k": "dg=="},
	}}
	if _, err := scope.Apply(ctx, secrets, secret); err != nil {
		t.Fatalf("Apply secret: %v", err)
	}
	if _, err := scope.Get(ctx, secrets, "default", "llm"); err != nil {
		t.Fatalf("Get secret: %v", err)
	}
	if err := scope.DeleteWithOptions(ctx, secrets, "", "llm", metav1.DeleteOptions{}); err == nil || !strings.Contains(err.Error(), "namespace is required") {
		t.Fatalf("Delete without namespace = %v, want namespace-required error", err)
	}
	uid := proxy.Get(secretsGVR, "default", "llm").GetUID()
	if err := scope.DeleteWithOptions(ctx, secrets, "default", "llm", metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		t.Fatalf("Delete secret: %v", err)
	}
	for _, r := range proxy.Requests() {
		if !strings.HasPrefix(r.Path, "/clusters/cluster-a/api/v1/namespaces/default/secrets") {
			t.Fatalf("request path = %q, want core-group namespaced path", r.Path)
		}
	}
	if proxy.Get(secretsGVR, "default", "llm") != nil {
		t.Fatal("secret still stored after delete")
	}
}

func TestNewScopeFromDynamicWrapsExistingClient(t *testing.T) {
	proxy := tenanttest.NewServer(t)
	proxy.Add(widgetsGVR, widget("w1", "red"))
	scope := scopeFor(t, proxy)
	wrapped := tenant.NewScopeFromDynamic(scope.Dynamic())
	if _, err := wrapped.Get(context.Background(), widgets, "", "w1"); err != nil {
		t.Fatalf("Get through wrapped scope: %v", err)
	}
}
