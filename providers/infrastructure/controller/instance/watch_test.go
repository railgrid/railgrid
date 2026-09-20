// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package instance

import (
	"context"
	"errors"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
	"github.com/railgrid/provider-infrastructure/instancespec"
)

// requestStrings renders requests as "cluster://<cluster>/[<namespace>/]<name>"
// (reconcile.Request.String() prints an empty namespace as "//name").
func requestStrings(reqs []mcreconcile.Request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		key := r.Name
		if r.Namespace != "" {
			key = r.Namespace + "/" + r.Name
		}
		out = append(out, "cluster://"+string(r.ClusterName)+"/"+key)
	}
	sort.Strings(out)
	return out
}

func assertRequests(t *testing.T, got []mcreconcile.Request, want ...string) {
	t.Helper()
	sort.Strings(want)
	g := requestStrings(got)
	if len(g) != len(want) {
		t.Fatalf("requests = %v, want %v", g, want)
	}
	for i := range g {
		if g[i] != want[i] {
			t.Fatalf("requests = %v, want %v", g, want)
		}
	}
}

func testController() *Controller {
	return &Controller{
		cfg:       Config{CredentialsNamespace: "default"},
		index:     newInstanceIndex(),
		contracts: map[string]*cachedContract{},
	}
}

func secretObj(namespace, name string) *unstructured.Unstructured {
	s := &unstructured.Unstructured{}
	s.SetGroupVersionKind(secretGVK)
	s.SetNamespace(namespace)
	s.SetName(name)
	return s
}

func createEvent(obj client.Object) event.CreateEvent { return event.CreateEvent{Object: obj} }

func TestMapTemplateFansOutToInstancesAcrossClustersAndDropsContract(t *testing.T) {
	c := testController()
	c.index.set("ws-a", types.NamespacedName{Name: "app"}, "simple-webapp")
	c.index.set("ws-a", types.NamespacedName{Name: "db"}, "database")
	c.index.set("ws-b", types.NamespacedName{Name: "app"}, "simple-webapp")
	c.contracts["simple-webapp"] = &cachedContract{resourceVersion: "1", contract: &instancespec.Contract{}}
	c.contracts["database"] = &cachedContract{resourceVersion: "1", contract: &instancespec.Contract{}}

	tmpl := &infrav1alpha1.Template{ObjectMeta: metav1.ObjectMeta{Name: "simple-webapp"}}
	got := c.mapTemplate(context.Background(), tmpl)
	assertRequests(t, got, "cluster://ws-a/app", "cluster://ws-b/app")

	if _, ok := c.contracts["simple-webapp"]; ok {
		t.Fatal("template event must invalidate the compiled contract of that template")
	}
	if _, ok := c.contracts["database"]; !ok {
		t.Fatal("template event must not touch other templates' contracts")
	}

	// A Template nobody references maps to nothing.
	if got := c.mapTemplate(context.Background(), &infrav1alpha1.Template{ObjectMeta: metav1.ObjectMeta{Name: "unused"}}); len(got) != 0 {
		t.Fatalf("unreferenced template mapped to %v", requestStrings(got))
	}
}

func TestInstanceIndexForgetsRemovedInstances(t *testing.T) {
	ix := newInstanceIndex()
	key := types.NamespacedName{Name: "app"}
	ix.set("ws-a", key, "simple-webapp")
	ix.set("ws-a", key, "database") // spec.template edited: last write wins
	assertRequests(t, ix.byTemplate("simple-webapp"))
	assertRequests(t, ix.byTemplate("database"), "cluster://ws-a/app")

	ix.remove("ws-a", key)
	assertRequests(t, ix.byTemplate("database"))
	ix.remove("ws-a", key) // idempotent on an unknown cluster/key
	assertRequests(t, ix.inCluster("ws-a", ""))
}

// A Secret event maps to the Instances that NAME it, not to a name derived
// from theirs. Two Instances may share one pull Secret, one Instance may
// reference two, and a Secret nobody references reconciles nothing.
func TestMapSecretTargetsReferencingInstances(t *testing.T) {
	c := testController()
	c.index.set("ws-a", types.NamespacedName{Name: "app"}, "simple-webapp", "team-registry", "team-oidc")
	c.index.set("ws-a", types.NamespacedName{Name: "other"}, "simple-webapp", "team-registry")
	c.index.set("ws-a", types.NamespacedName{Name: "public"}, "simple-webapp")
	c.index.set("ws-b", types.NamespacedName{Name: "app"}, "simple-webapp", "team-registry")

	// The shared pull Secret → both referencing Instances in that cluster,
	// and only in that cluster.
	assertRequests(t, c.mapSecret("ws-a", secretObj("default", "team-registry")), "cluster://ws-a/app", "cluster://ws-a/other")
	// The OIDC bridge Secret → only the Instance that names it.
	assertRequests(t, c.mapSecret("ws-a", secretObj("default", "team-oidc")), "cluster://ws-a/app")
	// Nothing references these: the old conventions are not honoured any more.
	assertRequests(t, c.mapSecret("ws-a", secretObj("default", "app-registry")))
	assertRequests(t, c.mapSecret("ws-a", secretObj("default", "cloud-credentials")))
	// Wrong namespace maps to nothing even for a referenced name.
	assertRequests(t, c.mapSecret("ws-a", secretObj("kube-system", "team-registry")))

	// Re-reconciling with a changed ref forgets the old one.
	c.index.set("ws-a", types.NamespacedName{Name: "app"}, "simple-webapp", "new-registry")
	assertRequests(t, c.mapSecret("ws-a", secretObj("default", "team-registry")), "cluster://ws-a/other")
	assertRequests(t, c.mapSecret("ws-a", secretObj("default", "new-registry")), "cluster://ws-a/app")

	pred := c.secretPredicate()
	if !pred.Create(createEvent(secretObj("default", "team-registry"))) {
		t.Fatal("predicate dropped a Secret in the credentials namespace")
	}
	if pred.Create(createEvent(secretObj("other", "team-registry"))) {
		t.Fatal("predicate passed a Secret outside the credentials namespace")
	}
}

func TestMapRuntimeObjectUsesInstanceAnnotations(t *testing.T) {
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(instanceGVK)
	inst.SetName("app")

	runtimeObj := &unstructured.Unstructured{}
	runtimeObj.SetName("app")
	runtimeObj.SetNamespace("root-orgs-x-default")
	runtimeObj.SetAnnotations(instanceAnnotations("root:orgs:x", inst))
	assertRequests(t, mapRuntimeObject(context.Background(), runtimeObj), "cluster://root:orgs:x/app")

	// The (cluster-scoped today) Instance namespace is carried when set.
	inst.SetNamespace("team")
	runtimeObj.SetAnnotations(instanceAnnotations("root:orgs:x", inst))
	assertRequests(t, mapRuntimeObject(context.Background(), runtimeObj), "cluster://root:orgs:x/team/app")

	// A runtime CR written before the annotations existed maps to nothing —
	// the next reconcile of the owning Instance stamps them, then it maps.
	bare := &unstructured.Unstructured{}
	bare.SetName("app")
	bare.SetLabels(map[string]string{"railgrid.ai/tenant": "abcdef012345"})
	assertRequests(t, mapRuntimeObject(context.Background(), bare))
	partial := bare.DeepCopy()
	partial.SetAnnotations(map[string]string{infrav1alpha1.RailgridInstanceNameAnnotation: "app"})
	assertRequests(t, mapRuntimeObject(context.Background(), partial))
}

// countingWatcher stands in for the started controller: it records the
// sources handed to Watch.
type countingWatcher struct {
	sources []source.TypedSource[mcreconcile.Request]
	err     error
}

func (w *countingWatcher) Watch(src source.TypedSource[mcreconcile.Request]) error {
	if w.err != nil {
		return w.err
	}
	w.sources = append(w.sources, src)
	return nil
}

func TestRuntimeWatchRegistrarRegistersEachGVROnce(t *testing.T) {
	w := &countingWatcher{}
	r := newRuntimeWatchRegistrar(w, nil)
	ctx := context.Background()

	apps := schema.GroupVersionResource{Group: "kro.run", Version: "v1alpha1", Resource: "simplewebapps"}
	dbs := schema.GroupVersionResource{Group: "kro.run", Version: "v1alpha1", Resource: "databases"}

	added, err := r.ensure(ctx, apps, "SimpleWebApp")
	if err != nil || !added {
		t.Fatalf("first ensure = (%v, %v), want (true, nil)", added, err)
	}
	added, err = r.ensure(ctx, apps, "SimpleWebApp")
	if err != nil || added {
		t.Fatalf("second ensure = (%v, %v), want (false, nil)", added, err)
	}
	added, err = r.ensure(ctx, dbs, "Database")
	if err != nil || !added {
		t.Fatalf("ensure of a second GVR = (%v, %v), want (true, nil)", added, err)
	}
	if len(w.sources) != 2 {
		t.Fatalf("controller.Watch called %d times, want 2 (one per GVR)", len(w.sources))
	}

	if _, err := r.ensure(ctx, schema.GroupVersionResource{}, "Nothing"); err == nil {
		t.Fatal("ensure accepted an empty GVR")
	}
	if _, err := r.ensure(ctx, dbs, ""); err == nil {
		t.Fatal("ensure accepted an empty kind")
	}
}

func TestRuntimeWatchRegistrarDoesNotRecordFailedRegistration(t *testing.T) {
	w := &countingWatcher{err: errors.New("controller stopped")}
	r := newRuntimeWatchRegistrar(w, nil)
	gvr := schema.GroupVersionResource{Group: "kro.run", Version: "v1alpha1", Resource: "simplewebapps"}
	if _, err := r.ensure(context.Background(), gvr, "SimpleWebApp"); err == nil {
		t.Fatal("ensure swallowed the Watch error")
	}
	w.err = nil
	added, err := r.ensure(context.Background(), gvr, "SimpleWebApp")
	if err != nil || !added {
		t.Fatalf("retry after a failed Watch = (%v, %v), want (true, nil)", added, err)
	}
}

func TestTemplateReady(t *testing.T) {
	tmpl := &infrav1alpha1.Template{}
	if templateReady(tmpl) {
		t.Fatal("template with no conditions reported Ready")
	}
	tmpl.Status.Conditions = []metav1.Condition{{Type: infrav1alpha1.ConditionReady, Status: metav1.ConditionFalse}}
	if templateReady(tmpl) {
		t.Fatal("Ready=False reported Ready")
	}
	tmpl.Status.Conditions = []metav1.Condition{
		{Type: infrav1alpha1.ConditionSchemaValid, Status: metav1.ConditionTrue},
		{Type: infrav1alpha1.ConditionReady, Status: metav1.ConditionTrue},
	}
	if !templateReady(tmpl) {
		t.Fatal("Ready=True not reported Ready")
	}
}
