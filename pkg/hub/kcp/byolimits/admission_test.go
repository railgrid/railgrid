// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package byolimits

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kubeCorelisters "github.com/kcp-dev/client-go/listers/core/v1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
)

func setConsumerQuota(g *Guard, cluster string, quota *corev1.ResourceQuota) {
	index := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{
		kcpcache.ClusterIndexName:             kcpcache.ClusterIndexFunc,
		kcpcache.ClusterAndNamespaceIndexName: kcpcache.ClusterAndNamespaceIndexFunc,
	})
	if quota != nil {
		quota = quota.DeepCopy()
		if quota.Annotations == nil {
			quota.Annotations = map[string]string{}
		}
		quota.Annotations[logicalcluster.AnnotationKey] = cluster
		_ = index.Add(quota)
	}
	g.quotaLister = kubeCorelisters.NewResourceQuotaClusterLister(index)
	g.quotaSynced = func() bool { return true }
}

func markConsumerQuotaRequired(t *testing.T, g *Guard, cluster string) {
	t.Helper()
	lc, err := g.lister.Cluster(logicalcluster.Name(cluster)).Get("cluster")
	if err != nil {
		t.Fatal(err)
	}
	if lc.Annotations == nil {
		lc.Annotations = map[string]string{}
	}
	lc.Annotations[ConsumerQuotaRequiredAnnotation] = ConsumerQuotaRequiredValue
}

func quotaFor(group, resourceName string, complete bool) *corev1.ResourceQuota {
	name := consumerQuotaCountKey(schema.GroupResource{Group: group, Resource: resourceName})
	quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: ConsumerQuotaName, Namespace: ConsumerQuotaNamespace, Annotations: map[string]string{ConsumerQuotaClusterScopedAnnotation: "true"}}}
	quantity := resource.MustParse("25")
	quota.Spec.Hard = corev1.ResourceList{corev1.ResourceName(name): quantity}
	if complete {
		quota.Status.Hard = corev1.ResourceList{corev1.ResourceName(name): quantity}
		quota.Status.Used = corev1.ResourceList{corev1.ResourceName(name): resource.MustParse("0")}
	}
	return quota
}

func quotaAdmission(g *Guard, cluster string, u *user.DefaultInfo, version, subresource string, operation admission.Operation) error {
	object := testObject("x")
	attrs := admission.NewAttributesRecord(object, nil, schema.GroupVersionKind{}, "", object.GetName(), schema.GroupVersionResource{Group: "demo.example", Version: version, Resource: "things"}, subresource, operation, nil, false, u)
	return (&sizeAdmission{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete), guard: g}).Validate(scopedContext(cluster, u), attrs, nil)
}

func TestConsumerQuotaCoverageIsRequiredForMarkedConsumerWrites(t *testing.T) {
	g := testGuard(t)
	markConsumerQuotaRequired(t, g, "a")
	setConsumerQuota(g, "a", nil)
	for _, identity := range []*user.DefaultInfo{{Name: "tenant"}, {Name: "admin", Groups: []string{user.SystemPrivilegedGroup}}} {
		if err := quotaAdmission(g, "a", identity, "v1", "", admission.Create); err == nil || !strings.Contains(err.Error(), "quota") {
			t.Fatalf("uncovered kind admitted for %q: %v", identity.Name, err)
		}
	}
}

func TestConsumerQuotaCoverageUsesGroupResourceAndInitializedStatus(t *testing.T) {
	g := testGuard(t)
	markConsumerQuotaRequired(t, g, "a")
	setConsumerQuota(g, "a", quotaFor("demo.example", "things", true))
	u := &user.DefaultInfo{Name: "tenant"}
	for _, tc := range []struct {
		version, subresource string
		operation            admission.Operation
	}{{"v1", "", admission.Create}, {"v2", "status", admission.Update}} {
		if err := quotaAdmission(g, "a", u, tc.version, tc.subresource, tc.operation); err != nil {
			t.Fatalf("covered %s/%s write denied: %v", tc.version, tc.subresource, err)
		}
	}
	setConsumerQuota(g, "a", quotaFor("demo.example", "things", false))
	if err := quotaAdmission(g, "a", u, "v2", "status", admission.Update); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("uninitialized quota status admitted: %v", err)
	}
}

func TestConsumerQuotaCoverageRejectsMissingKeyMismatchedStatusAndOveruse(t *testing.T) {
	gr := schema.GroupResource{Group: "demo.example", Resource: "things"}
	base := quotaFor(gr.Group, gr.Resource, true)
	cases := []struct {
		name   string
		mutate func(*corev1.ResourceQuota)
	}{
		{"missing hard key", func(q *corev1.ResourceQuota) { delete(q.Spec.Hard, corev1.ResourceName(consumerQuotaCountKey(gr))) }},
		{"status hard mismatch", func(q *corev1.ResourceQuota) {
			q.Status.Hard[corev1.ResourceName(consumerQuotaCountKey(gr))] = resource.MustParse("24")
		}},
		{"missing used key", func(q *corev1.ResourceQuota) { delete(q.Status.Used, corev1.ResourceName(consumerQuotaCountKey(gr))) }},
		{"observed overuse", func(q *corev1.ResourceQuota) {
			q.Status.Used[corev1.ResourceName(consumerQuotaCountKey(gr))] = resource.MustParse("26")
		}},
		{"not cluster scoped", func(q *corev1.ResourceQuota) { delete(q.Annotations, ConsumerQuotaClusterScopedAnnotation) }},
		{"scoped resource quota", func(q *corev1.ResourceQuota) {
			q.Spec.Scopes = []corev1.ResourceQuotaScope{corev1.ResourceQuotaScopeBestEffort}
		}},
		{"scope selector", func(q *corev1.ResourceQuota) { q.Spec.ScopeSelector = &corev1.ScopeSelector{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quota := base.DeepCopy()
			tc.mutate(quota)
			if err := safeStatusProof(quota, gr); err == nil {
				t.Fatal("incomplete or over-cap quota proof accepted")
			}
		})
	}
}

func TestConsumerQuotaCoverageRejectsMissingQuotaAndPreservesUnmarkedWorkspace(t *testing.T) {
	g := testGuard(t)
	setConsumerQuota(g, "a", nil)
	u := &user.DefaultInfo{Name: "tenant"}
	if err := quotaAdmission(g, "a", u, "v1", "", admission.Create); err != nil {
		t.Fatalf("unmarked workspace changed baseline: %v", err)
	}
	markConsumerQuotaRequired(t, g, "a")
	if err := quotaAdmission(g, "a", u, "v1", "", admission.Create); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("removed quota did not fail closed: %v", err)
	}
	if !(&sizeAdmission{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete), guard: g}).Handles(admission.Delete) {
		t.Fatal("quota guard must handle deletion policy")
	}
}

func TestConsumerQuotaCoverageAllowsNativeLogicalClusterBootstrap(t *testing.T) {
	g := testGuard(t)
	u := &user.DefaultInfo{Name: "workspace-bootstrapper", Groups: []string{bootstrap.SystemKcpWorkspaceBootstrapper}}
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster", "metadata": map[string]any{"name": corev1alpha1.LogicalClusterName, "annotations": map[string]any{pathAnnotation: "root:railgrid:tenants:org-new:workspace"}}}}
	attrs := admission.NewAttributesRecord(object, nil, schema.GroupVersionKind{Group: "core.kcp.io", Version: "v1alpha1", Kind: "LogicalCluster"}, "", corev1alpha1.LogicalClusterName, schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}, "", admission.Create, nil, false, u)
	ctx := request.WithRequestInfo(scopedContext("new", u), &request.RequestInfo{APIGroup: "core.kcp.io", Resource: "logicalclusters", Verb: "create"})
	if err := (&sizeAdmission{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete), guard: g}).Validate(ctx, attrs, nil); err != nil {
		t.Fatalf("native LogicalCluster bootstrap was denied: %v", err)
	}
}

func TestConsumerQuotaCoverageAllowsStructuralBootstrapBeforeInformerSync(t *testing.T) {
	g := testGuard(t)
	g.lister, g.synced = nil, nil
	u := &user.DefaultInfo{Name: "system:controller", Groups: []string{user.SystemPrivilegedGroup}}
	for _, cluster := range []string{"root", "system:kcp"} {
		if err := quotaAdmission(g, cluster, u, "v1", "", admission.Create); err != nil {
			t.Fatalf("structural cluster %q bootstrap write denied before informer sync: %v", cluster, err)
		}
	}
}

func TestConsumerQuotaCoverageFailsClosedForUnknownOrdinaryClusterBeforeInformerSync(t *testing.T) {
	g := testGuard(t)
	g.lister, g.synced = nil, nil
	u := &user.DefaultInfo{Name: "system:controller", Groups: []string{user.SystemPrivilegedGroup}}
	if err := quotaAdmission(g, "ordinary-not-ready", u, "v1", "", admission.Create); err == nil || !strings.Contains(err.Error(), "protection state") {
		t.Fatalf("unknown ordinary cluster did not fail closed: %v", err)
	}
}

func TestConsumerQuotaBootstrapAllowsOnlyAdminNamespaceCreate(t *testing.T) {
	g := testGuard(t)
	markConsumerQuotaRequired(t, g, "a")
	setConsumerQuota(g, "a", nil)
	a := &sizeAdmission{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete), guard: g}
	u := &user.DefaultInfo{Name: "admin", Groups: []string{user.SystemPrivilegedGroup}}
	for name, wantAllowed := range map[string]bool{ConsumerQuotaNamespace: true, "other": false} {
		object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": name}}}
		attrs := admission.NewAttributesRecord(object, nil, schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, "", name, schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, "", admission.Create, nil, false, u)
		err := a.Validate(scopedContext("a", u), attrs, nil)
		if (err == nil) != wantAllowed {
			t.Fatalf("namespace %q allowed=%t, want %t (err=%v)", name, err == nil, wantAllowed, err)
		}
	}
}

func TestMarkedConsumerControlDeletesAreProtected(t *testing.T) {
	g := testGuard(t)
	markConsumerQuotaRequired(t, g, "a")
	setConsumerQuota(g, "a", nil)
	a := &sizeAdmission{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete), guard: g}
	orgAdmin := &user.DefaultInfo{Name: "org-admin"}
	providerSA := &user.DefaultInfo{Name: "system:serviceaccount:admin:provider", Extra: map[string][]string{serviceaccount.ClusterNameKey: {"a"}}}
	host := &user.DefaultInfo{Name: "hub-controller", Groups: []string{user.SystemPrivilegedGroup}}
	namespace := func(name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": name}}}
	}
	resourceQuota := func(name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ResourceQuota", "metadata": map[string]any{"name": name, "namespace": ConsumerQuotaNamespace}}}
	}
	validate := func(cluster string, resource schema.GroupVersionResource, kind schema.GroupVersionKind, namespaceName, name string, object, oldObject *unstructured.Unstructured, identity user.Info, requestVerb string) error {
		attrs := admission.NewAttributesRecord(object, oldObject, kind, namespaceName, name, resource, "", admission.Delete, nil, false, identity)
		ctx := scopedContext(cluster, identity)
		if requestVerb != "" {
			ctx = request.WithRequestInfo(ctx, &request.RequestInfo{APIGroup: resource.Group, Resource: resource.Resource, Verb: requestVerb})
		}
		return a.Validate(ctx, attrs, nil)
	}
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	nsGVK := schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}
	rqGVR := schema.GroupVersionResource{Version: "v1", Resource: "resourcequotas"}
	rqGVK := schema.GroupVersionKind{Version: "v1", Kind: "ResourceQuota"}
	for _, identity := range []*user.DefaultInfo{orgAdmin, providerSA} {
		if err := validate("a", nsGVR, nsGVK, "", ConsumerQuotaNamespace, namespace(ConsumerQuotaNamespace), nil, identity, "delete"); err == nil {
			t.Fatalf("protected namespace deletion allowed for %s", identity.Name)
		}
		if err := validate("a", rqGVR, rqGVK, ConsumerQuotaNamespace, ConsumerQuotaName, resourceQuota(ConsumerQuotaName), nil, identity, "delete"); err == nil {
			t.Fatalf("protected quota deletion allowed for %s", identity.Name)
		}
	}
	if err := validate("a", nsGVR, nsGVK, "", "ordinary", namespace("ordinary"), nil, orgAdmin, "delete"); err != nil {
		t.Fatalf("ordinary namespace deletion denied: %v", err)
	}
	if err := validate("a", rqGVR, rqGVK, "other", "other-quota", resourceQuota("other-quota"), nil, orgAdmin, "delete"); err != nil {
		t.Fatalf("ordinary quota deletion denied: %v", err)
	}
	if err := validate("a", nsGVR, nsGVK, "", ConsumerQuotaNamespace, namespace(ConsumerQuotaNamespace), nil, host, "delete"); err != nil {
		t.Fatalf("native host namespace cleanup denied: %v", err)
	}
	if err := validate("a", rqGVR, rqGVK, ConsumerQuotaNamespace, ConsumerQuotaName, resourceQuota(ConsumerQuotaName), nil, host, "delete"); err != nil {
		t.Fatalf("native host quota cleanup denied: %v", err)
	}
}

func TestMarkedConsumerNamespaceDeleteCollectionFailsClosed(t *testing.T) {
	g := testGuard(t)
	markConsumerQuotaRequired(t, g, "a")
	setConsumerQuota(g, "a", nil)
	a := &sizeAdmission{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete), guard: g}
	orgAdmin := &user.DefaultInfo{Name: "org-admin"}
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	nsGVK := schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}
	for _, tc := range []struct {
		name, verb string
		old        *unstructured.Unstructured
	}{
		{"unknown collection", "deletecollection", nil},
		{"old object identifies admin namespace", "", &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ConsumerQuotaNamespace}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := admission.NewAttributesRecord(nil, tc.old, nsGVK, "", "", nsGVR, "", admission.Delete, nil, false, orgAdmin)
			ctx := scopedContext("a", orgAdmin)
			if tc.verb != "" {
				ctx = request.WithRequestInfo(ctx, &request.RequestInfo{Resource: "namespaces", Verb: tc.verb})
			}
			if err := a.Validate(ctx, attrs, nil); err == nil {
				t.Fatal("namespace collection deletion was not protected")
			}
		})
	}
	rqGVR := schema.GroupVersionResource{Version: "v1", Resource: "resourcequotas"}
	rqGVK := schema.GroupVersionKind{Version: "v1", Kind: "ResourceQuota"}
	for _, namespace := range []string{"", ConsumerQuotaNamespace} {
		attrs := admission.NewAttributesRecord(nil, nil, rqGVK, namespace, "", rqGVR, "", admission.Delete, nil, false, orgAdmin)
		ctx := request.WithRequestInfo(scopedContext("a", orgAdmin), &request.RequestInfo{Resource: "resourcequotas", Verb: "deletecollection", Namespace: namespace})
		if err := a.Validate(ctx, attrs, nil); err == nil {
			t.Fatalf("ResourceQuota collection deletion in namespace %q was not protected", namespace)
		}
	}
}

func TestMarkedConsumerEphemeralReviewCreatesDoNotRequireQuotaCoverage(t *testing.T) {
	g := testGuard(t)
	markConsumerQuotaRequired(t, g, "a")
	setConsumerQuota(g, "a", nil)
	a := &sizeAdmission{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete), guard: g}
	u := &user.DefaultInfo{Name: "org-admin"}
	cases := []struct {
		group, version, resource, kind string
	}{
		{"authentication.k8s.io", "v1", "selfsubjectreviews", "SelfSubjectReview"},
		{"authorization.k8s.io", "v1", "selfsubjectrulesreviews", "SelfSubjectRulesReview"},
	}
	for _, tc := range cases {
		t.Run(tc.resource, func(t *testing.T) {
			object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": tc.group + "/" + tc.version, "kind": tc.kind}}
			attrs := admission.NewAttributesRecord(object, nil, schema.GroupVersionKind{Group: tc.group, Version: tc.version, Kind: tc.kind}, "", "", schema.GroupVersionResource{Group: tc.group, Version: tc.version, Resource: tc.resource}, "", admission.Create, nil, false, u)
			if err := a.Validate(scopedContext("a", u), attrs, nil); err != nil {
				t.Fatalf("ephemeral review create incorrectly required quota coverage: %v", err)
			}
		})
	}
}

func TestUnmarkedConsumerControlDeletesKeepBaselineBehavior(t *testing.T) {
	g := testGuard(t)
	setConsumerQuota(g, "a", nil)
	a := &sizeAdmission{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete), guard: g}
	u := &user.DefaultInfo{Name: "org-admin"}
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ConsumerQuotaNamespace}}}
	attrs := admission.NewAttributesRecord(object, nil, schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, "", ConsumerQuotaNamespace, schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, "", admission.Delete, nil, false, u)
	if err := a.Validate(scopedContext("a", u), attrs, nil); err != nil {
		t.Fatalf("unmarked workspace admin namespace deletion changed baseline: %v", err)
	}
}

func TestConsumerQuotaCoverageAllowsStrictlyShrinkingCleanup(t *testing.T) {
	g := testGuard(t)
	markConsumerQuotaRequired(t, g, "a")
	setConsumerQuota(g, "a", nil)
	a := &sizeAdmission{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete), guard: g}
	u := &user.DefaultInfo{Name: "admin", Groups: []string{user.SystemPrivilegedGroup}}
	tests := []struct {
		label        string
		resource     schema.GroupVersionResource
		kind         schema.GroupVersionKind
		objectName   string
		subresource  string
		old, current *unstructured.Unstructured
		wantAllowed  bool
	}{
		{
			label: "metadata finalizer removal", resource: schema.GroupVersionResource{Group: "demo.example", Version: "v1", Resource: "things"}, kind: schema.GroupVersionKind{Group: "demo.example", Version: "v1", Kind: "Thing"}, objectName: "one",
			old: func() *unstructured.Unstructured {
				o := testObject("x")
				o.SetFinalizers([]string{"cleanup.example/finalizer"})
				return o
			}(),
			current: testObject("x"), wantAllowed: true,
		},
		{
			label: "namespace finalize removal", resource: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, kind: schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, objectName: "default", subresource: "finalize",
			old:     &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "default"}, "spec": map[string]any{"finalizers": []any{"kubernetes"}}}},
			current: &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "default"}, "spec": map[string]any{"finalizers": []any{}}}}, wantAllowed: true,
		},
		{
			label: "equal size update", resource: schema.GroupVersionResource{Group: "demo.example", Version: "v1", Resource: "things"}, kind: schema.GroupVersionKind{Group: "demo.example", Version: "v1", Kind: "Thing"}, objectName: "one",
			old: testObject("x"), current: testObject("y"), wantAllowed: false,
		},
		{
			label: "growing update", resource: schema.GroupVersionResource{Group: "demo.example", Version: "v1", Resource: "things"}, kind: schema.GroupVersionKind{Group: "demo.example", Version: "v1", Kind: "Thing"}, objectName: "one",
			old: testObject("x"), current: testObject("larger"), wantAllowed: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			attrs := admission.NewAttributesRecord(tc.current, tc.old, tc.kind, "", tc.objectName, tc.resource, tc.subresource, admission.Update, nil, false, u)
			err := a.Validate(scopedContext("a", u), attrs, nil)
			if (err == nil) != tc.wantAllowed {
				t.Fatalf("allowed=%t, want %t (err=%v)", err == nil, tc.wantAllowed, err)
			}
		})
	}
}

func testObject(data string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "demo.example/v1", "kind": "Thing", "metadata": map[string]any{"name": "one"}, "spec": map[string]any{"data": data}}}
}

func TestAdmissionMeasuresFullCreateAndPatchedStatusObject(t *testing.T) {
	g := testGuard(t)
	a := &sizeAdmission{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete), guard: g}
	u := &user.DefaultInfo{Name: "admin", Groups: []string{user.SystemPrivilegedGroup}}
	ctx := scopedContext("a", u)
	old := testObject(strings.Repeat("x", 800))
	encoded, _ := json.Marshal(old)
	patch := []byte(`{"status":{"data":"` + strings.Repeat("y", 200) + `"}}`)
	if int64(len(patch)) >= g.config.MaxObjectBytes {
		t.Fatal("fixture patch is not small")
	}
	merged, err := jsonpatch.MergePatch(encoded, patch)
	if err != nil {
		t.Fatal(err)
	}
	updated := &unstructured.Unstructured{}
	if err := json.Unmarshal(merged, updated); err != nil {
		t.Fatal(err)
	}
	validate := func(object, prior *unstructured.Unstructured, operation admission.Operation, subresource string) error {
		attrs := admission.NewAttributesRecord(object, prior, schema.GroupVersionKind{Group: "demo.example", Version: "v1", Kind: "Thing"}, "", "one", schema.GroupVersionResource{Group: "demo.example", Version: "v1", Resource: "things"}, subresource, operation, nil, false, u)
		return a.Validate(ctx, attrs, nil)
	}
	if err := validate(old, nil, admission.Create, ""); err != nil {
		t.Fatal(err)
	}
	if err := validate(updated, nil, admission.Create, ""); err == nil {
		t.Fatal("oversized CREATE admitted")
	}
	if err := validate(updated, old, admission.Update, "status"); err == nil {
		t.Fatal("small PATCH grew status beyond object cap")
	}
	if err := validate(old, updated, admission.Update, ""); err != nil {
		t.Fatalf("shrinking object denied: %v", err)
	}
	if !a.Handles(admission.Delete) {
		t.Fatal("admission plugin must apply its deletion policy")
	}
}

func TestAdmissionAllowsNonGrowingLegacyObjectCleanup(t *testing.T) {
	g := testGuard(t)
	a := &sizeAdmission{guard: g}
	u := &user.DefaultInfo{Name: "tenant"}
	prior := testObject(strings.Repeat("x", 2048))
	prior.SetFinalizers([]string{"cleanup.example/finalizer"})
	current := prior.DeepCopy()
	current.SetFinalizers(nil)
	attrs := admission.NewAttributesRecord(current, prior, schema.GroupVersionKind{}, "", "one", schema.GroupVersionResource{Group: "demo.example", Resource: "things"}, "", admission.Update, nil, false, u)
	if err := a.Validate(scopedContext("a", u), attrs, nil); err != nil {
		t.Fatalf("legacy cleanup denied: %v", err)
	}
	// This exception does not certify pre-existing storage usage or allow an
	// already oversized object to expand further.
	grown := prior.DeepCopy()
	if err := unstructured.SetNestedField(grown.Object, strings.Repeat("x", 2049), "spec", "data"); err != nil {
		t.Fatal(err)
	}
	attrs = admission.NewAttributesRecord(grown, prior, schema.GroupVersionKind{}, "", "one", schema.GroupVersionResource{Group: "demo.example", Resource: "things"}, "", admission.Update, nil, false, u)
	if err := a.Validate(scopedContext("a", u), attrs, nil); err == nil {
		t.Fatal("legacy oversized object grew beyond its existing size")
	}
}

func TestReadinessRequiresAdmissionBothHooksAndOwnershipSync(t *testing.T) {
	g := testGuard(t)
	setConsumerQuota(g, "a", nil)
	if g.Ready() == nil {
		t.Fatal("uninstalled guard ready")
	}
	g.admissionInstalled.Store(true)
	g.mainInstalled.Store(true)
	if g.Ready() == nil {
		t.Fatal("missing VW guard ready")
	}
	g.virtualInstalled.Store(true)
	if err := g.Ready(); err != nil {
		t.Fatal(err)
	}
	g.synced = func() bool { return false }
	if g.Ready() == nil {
		t.Fatal("unsynchronized guard ready")
	}
}

func TestWaitConsumerQuotaMarkerRequiresReadinessAndExactObservedMarker(t *testing.T) {
	g := testGuard(t)
	g.admissionInstalled.Store(true)
	g.mainInstalled.Store(true)
	g.virtualInstalled.Store(true)
	setConsumerQuota(g, "a", nil)
	markConsumerQuotaRequired(t, g, "a")
	if err := g.WaitConsumerQuotaMarker(context.Background(), "a"); err != nil {
		t.Fatalf("observed marker was not accepted: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := g.WaitConsumerQuotaMarker(ctx, "b"); err == nil || !strings.Contains(err.Error(), "marker") {
		t.Fatalf("missing marker was accepted: %v", err)
	}
}

func TestWaitConsumerQuotaMarkerHonorsContextCancellation(t *testing.T) {
	g := testGuard(t)
	g.admissionInstalled.Store(true)
	g.mainInstalled.Store(true)
	g.virtualInstalled.Store(true)
	setConsumerQuota(g, "a", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.WaitConsumerQuotaMarker(ctx, "a"); err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("canceled wait did not return context error: %v", err)
	}
}
