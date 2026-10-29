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

package usage

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// The value was produced by contrib-metering's own v1alpha1.UsageRecordName
// for these inputs; the two must never diverge (window.go).
func TestRecordNameMatchesContract(t *testing.T) {
	start, end := AlignWindow(time.Date(2026, 10, 29, 19, 42, 7, 0, time.UTC), time.Hour)
	if start != time.Date(2026, 10, 29, 19, 0, 0, 0, time.UTC) || end != start.Add(time.Hour) {
		t.Fatalf("AlignWindow = [%s, %s)", start, end)
	}
	if got, want := RecordName("1pnfmd9guvzpucus", "agents", start, end), "u-5e7efd68e6f3727dbfd0c1d4e258add7"; got != want {
		t.Errorf("RecordName = %s, want %s", got, want)
	}
	// Local time in, UTC grid out.
	loc := time.FixedZone("plus2", 2*3600)
	s2, _ := AlignWindow(time.Date(2026, 10, 29, 21, 42, 7, 0, loc), 0)
	if !s2.Equal(start) {
		t.Errorf("AlignWindow ignores the zone: %s", s2)
	}
}

// fixture is an emitter with stubbed cluster lists and a recording writer;
// the gate runs against a fake dynamic client.
type fixture struct {
	e        *emitter
	written  []*unstructured.Unstructured
	writeErr error
	counts   map[multicluster.ClusterName][]int // per cluster, per kind
	gone     map[multicluster.ClusterName]bool
	lists    int
	now      time.Time
}

func newFixture(t *testing.T, served bool) *fixture {
	t.Helper()
	return newFixtureErr(t, served, nil)
}

// newFixtureErr is newFixture with the error the probe returns when the
// resource is not served.
func newFixtureErr(t *testing.T, served bool, probeErr error) *fixture {
	t.Helper()
	f := &fixture{
		counts: map[multicluster.ClusterName][]int{},
		gone:   map[multicluster.ClusterName]bool{},
		now:    time.Date(2026, 10, 29, 19, 42, 7, 0, time.UTC),
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{GVR: "UsageRecordList"})
	dyn.PrependReactor("list", "usagerecords", func(clienttesting.Action) (bool, runtime.Object, error) {
		f.lists++
		if served {
			return false, nil, nil
		}
		if probeErr != nil {
			return true, nil, probeErr
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: GVR.Group, Resource: GVR.Resource}, "")
	})
	g := &gate{res: dyn.Resource(GVR), logger: logr.Discard(), now: func() time.Time { return f.now }}
	kindIndex := 0
	f.e = &emitter{
		meter:       "things",
		granularity: time.Hour,
		logger:      logr.Discard(),
		gate:        g,
		now:         func() time.Time { return f.now },
		lists: []func() client.ObjectList{
			func() client.ObjectList { kindIndex = 0; return &corev1.ConfigMapList{} },
			func() client.ObjectList { kindIndex = 1; return &corev1.SecretList{} },
		},
		list: func(_ context.Context, cluster multicluster.ClusterName, l client.ObjectList) (bool, error) {
			if f.gone[cluster] {
				return true, nil
			}
			n := 0
			if c := f.counts[cluster]; kindIndex < len(c) {
				n = c[kindIndex]
			}
			switch l := l.(type) {
			case *corev1.ConfigMapList:
				l.Items = make([]corev1.ConfigMap, n)
			case *corev1.SecretList:
				l.Items = make([]corev1.Secret, n)
			}
			return false, nil
		},
		write: func(_ context.Context, r *unstructured.Unstructured) error {
			if f.writeErr != nil {
				return f.writeErr
			}
			f.written = append(f.written, r)
			return nil
		},
	}
	return f
}

func (f *fixture) reconcile(t *testing.T, cluster string) ctrl.Result {
	t.Helper()
	res, err := f.e.Reconcile(context.Background(), mcreconcile.Request{
		ClusterName: multicluster.ClusterName(cluster),
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: "things"}},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return res
}

func TestEmitterStatesTheCurrentWindow(t *testing.T) {
	f := newFixture(t, true)
	f.counts["ws-a"] = []int{2, 3}

	res := f.reconcile(t, "ws-a")
	if len(f.written) != 1 {
		t.Fatalf("written %d records, want 1", len(f.written))
	}
	r := f.written[0]
	start, end := AlignWindow(f.now, time.Hour)
	if r.GetName() != RecordName("ws-a", "things", start, end) {
		t.Errorf("name = %s", r.GetName())
	}
	spec, _, _ := unstructured.NestedMap(r.Object, "spec")
	if spec["quantity"] != "5" || spec["subjectCluster"] != "ws-a" || spec["meter"] != "things" {
		t.Errorf("spec = %v", spec)
	}
	window, _ := spec["window"].(map[string]any)
	if window["start"] != "2026-10-29T19:00:00Z" || window["end"] != "2026-10-29T20:00:00Z" {
		t.Errorf("window = %v", window)
	}
	if r.GetLabels()[LabelMeter] != "things" || r.GetLabels()[LabelSubject] != "ws-a" {
		t.Errorf("labels = %v", r.GetLabels())
	}
	// Re-stated at the next grid boundary, with bounded jitter.
	untilEnd := end.Sub(f.now)
	if res.RequeueAfter < untilEnd || res.RequeueAfter > untilEnd+maxJitter {
		t.Errorf("RequeueAfter = %s, want within [%s, %s]", res.RequeueAfter, untilEnd, untilEnd+maxJitter)
	}
	// A second pass in the same window upserts the same name.
	f.counts["ws-a"] = []int{1, 0}
	f.reconcile(t, "ws-a")
	if f.written[1].GetName() != r.GetName() {
		t.Errorf("same window produced a different name: %s vs %s", f.written[1].GetName(), r.GetName())
	}
	if q, _, _ := unstructured.NestedString(f.written[1].Object, "spec", "quantity"); q != "1" {
		t.Errorf("restated quantity = %s, want 1", q)
	}
}

func TestEmitterStatesZeroOnceAndStops(t *testing.T) {
	f := newFixture(t, true)
	f.counts["ws-a"] = []int{0, 0}
	res := f.reconcile(t, "ws-a")
	if len(f.written) != 1 {
		t.Fatalf("zero should still be stated, written %d", len(f.written))
	}
	if q, _, _ := unstructured.NestedString(f.written[0].Object, "spec", "quantity"); q != "0" {
		t.Errorf("quantity = %s, want 0", q)
	}
	if res != (ctrl.Result{}) {
		t.Errorf("zero should not be re-stated every window, got %+v", res)
	}
}

func TestEmitterEndsSeriesForGoneWorkspace(t *testing.T) {
	f := newFixture(t, true)
	f.gone["ws-a"] = true
	if res := f.reconcile(t, "ws-a"); res != (ctrl.Result{}) || len(f.written) != 0 {
		t.Errorf("gone workspace: result %+v, written %d", res, len(f.written))
	}
}

func TestEmitterPausesWhileMeteringIsNotBound(t *testing.T) {
	f := newFixture(t, false)
	f.counts["ws-a"] = []int{1, 0}
	f.counts["ws-b"] = []int{4, 0}

	if res := f.reconcile(t, "ws-a"); res.RequeueAfter != gateRetry || len(f.written) != 0 {
		t.Errorf("unbound: result %+v, written %d", res, len(f.written))
	}
	// The probe is shared and paced: another workspace within the minute
	// does not probe again.
	f.reconcile(t, "ws-b")
	if f.lists != 1 {
		t.Errorf("probed %d times within the retry interval, want 1", f.lists)
	}
	f.now = f.now.Add(gateRetry)
	f.reconcile(t, "ws-b")
	if f.lists != 2 {
		t.Errorf("probed %d times after the retry interval, want 2", f.lists)
	}
}

func TestEmitterReprobesWhenTheResourceVanishes(t *testing.T) {
	f := newFixture(t, true)
	f.counts["ws-a"] = []int{1, 0}
	f.reconcile(t, "ws-a")
	if !f.e.gate.available {
		t.Fatal("gate should be open after a successful probe")
	}
	f.writeErr = apierrors.NewNotFound(schema.GroupResource{Group: GVR.Group, Resource: GVR.Resource}, "")
	if res := f.reconcile(t, "ws-a"); res.RequeueAfter != gateRetry {
		t.Errorf("vanished resource: result %+v", res)
	}
	if f.e.gate.available {
		t.Error("gate should close when a write finds the resource gone")
	}
}

func TestListFactory(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	typed, err := listFactory(&corev1.ConfigMap{}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := typed().(*corev1.ConfigMapList); !ok {
		t.Errorf("typed factory returned %T", typed())
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "x.io", Version: "v1", Kind: "Widget"})
	un, err := listFactory(u, scheme)
	if err != nil {
		t.Fatal(err)
	}
	if got := un().(*unstructured.UnstructuredList).GroupVersionKind().Kind; got != "WidgetList" {
		t.Errorf("unstructured list kind = %s", got)
	}
	if _, err := listFactory(&unstructured.Unstructured{}, scheme); err == nil {
		t.Error("unstructured kind without a GVK should be refused")
	}
	if _, err := listFactory(&metav1.PartialObjectMetadata{}, scheme); err == nil {
		t.Error("a kind outside the scheme should be refused")
	}
}

func TestEmitterPausesWhenForbidden(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: GVR.Group, Resource: GVR.Resource}, "", nil)
	f := newFixtureErr(t, false, forbidden)
	f.counts["ws-a"] = []int{1, 0}
	if res := f.reconcile(t, "ws-a"); res.RequeueAfter != gateRetry || len(f.written) != 0 {
		t.Errorf("forbidden: result %+v, written %d", res, len(f.written))
	}
	if !f.e.gate.reported {
		t.Error("forbidden should be reported once, like not-bound")
	}
	// A write that turns forbidden closes the gate again.
	f2 := newFixture(t, true)
	f2.counts["ws-a"] = []int{1, 0}
	f2.reconcile(t, "ws-a")
	f2.writeErr = forbidden
	if res := f2.reconcile(t, "ws-a"); res.RequeueAfter != gateRetry || f2.e.gate.available {
		t.Errorf("forbidden write: result %+v, available %v", res, f2.e.gate.available)
	}
}
