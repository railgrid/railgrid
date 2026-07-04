//go:build e2e

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

// E2E coverage for the dev overlay (docs/app-studio-template-sandboxes.md §7
// Phase 1) against a real kro. The unit tests prove buildRGD synthesizes the
// overlay; this proves the two kro behaviors the design flagged as
// live-validation risks, plus the mode split end to end:
//
//  1. kro ACCEPTS a graph carrying two same-named Deployment variants
//     (prod + dev) gated by complementary includeWhen expressions, and status
//     expressions referencing mode-excluded resources (GraphAccepted=True —
//     already implied by TestE2ESeedTemplates now that seed templates carry
//     the overlay, asserted here explicitly per mode).
//  2. A PRODUCTION-mode instance (kedgeMode defaulted) materializes the
//     production workloads and NONE of the synthesized dev resources, and
//     reconciles cleanly even though the status mapping references dev-only
//     resources (they resolve to unset, not an error).
//  3. A DEVELOPMENT-mode instance materializes the dev variant, per-component
//     workspace PVC + control Service, and the instance-wide control-token
//     Secret/Job; the production workloads stay out; and the overlay's status
//     additions (runtimeNamespace, controlSecretRef, components.<c>.
//     controlServiceRef) actually resolve. For the application template the
//     dev instance deliberately omits frontendImage/backendImage — dev mode
//     must not require built images.
package kro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// e2eDevImageStripped are sample-spec fields a development-mode instance must
// NOT need — dev mode swaps the tier images for platform dev images, so the
// instance is created without them to prove the graph renders regardless.
var e2eDevImageStripped = map[string][]string{
	"application": {"frontendImage", "backendImage"},
}

func TestE2EDevelopmentMode(t *testing.T) {
	dyn, mapper := e2eClients(t)

	dir := filepath.Join("..", "..", "install", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read seed templates dir: %v", err)
	}

	var seen int
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		tmpl := decodeTemplate(t, raw)
		if tmpl.Spec.Development == nil {
			continue
		}
		seen++

		t.Run(tmpl.Name, func(t *testing.T) {
			rgd, err := buildRGD(tmpl, testTokens())
			if err != nil {
				t.Fatalf("buildRGD(%s): %v", tmpl.Name, err)
			}
			applyRGD(t, dyn, rgd)
			t.Cleanup(func() { _ = dyn.Resource(rgdGVR).Delete(context.Background(), rgd.GetName(), metav1.DeleteOptions{}) })

			// Risk 1: kro accepts the overlay-extended graph.
			if status, msg := waitGraphAccepted(t, dyn, rgd.GetName()); status != "True" {
				t.Fatalf("kro rejected the dev-overlay RGD for %q: GraphAccepted=%s: %s", tmpl.Name, status, msg)
			}
			t.Logf("template %q: dev-overlay RGD accepted", tmpl.Name)

			prodIDs, devIDs := overlayNodeIDs(t, rgd)
			instGVR := schema.GroupVersionResource{
				Group:    tmpl.Spec.InstanceCRD.Group,
				Version:  tmpl.Spec.InstanceCRD.Version,
				Resource: tmpl.Spec.InstanceCRD.Resource,
			}

			t.Run("production", func(t *testing.T) {
				inst := e2eInstance(t, tmpl, fmt.Sprintf("%016x", time.Now().UnixNano()))
				createInstance(t, dyn, instGVR, inst)
				t.Cleanup(func() {
					_ = dyn.Resource(instGVR).Delete(context.Background(), inst.GetName(), metav1.DeleteOptions{})
				})
				waitInstanceApplied(t, dyn, instGVR, inst.GetName(), tmpl.Name)

				created, err := dyn.Resource(instGVR).Get(context.Background(), inst.GetName(), metav1.GetOptions{})
				if err != nil {
					t.Fatalf("re-get instance: %v", err)
				}
				uid := string(created.GetUID())

				// Risk 2: production materializes the (now includeWhen-gated)
				// workloads and none of the dev resources.
				lister := newNodeLister(t, mapper, rgd)
				waitNodeIDs(t, dyn, lister, uid, prodIDs, tmpl.Name+" production workloads")
				assertNodeIDsAbsent(t, dyn, lister, uid, devIDs, tmpl.Name+" dev resources in production mode")
			})

			t.Run("development", func(t *testing.T) {
				inst := e2eDevInstance(t, tmpl, fmt.Sprintf("%016x", time.Now().UnixNano()))
				createInstance(t, dyn, instGVR, inst)
				t.Cleanup(func() {
					_ = dyn.Resource(instGVR).Delete(context.Background(), inst.GetName(), metav1.DeleteOptions{})
				})
				// On failure, surface the instance conditions before the delete
				// cleanup runs (cleanups are LIFO) — the only place kro explains
				// WHY a child object never materialized.
				t.Cleanup(func() { dumpInstanceConditions(t, dyn, instGVR, inst.GetName()) })
				waitInstanceApplied(t, dyn, instGVR, inst.GetName(), tmpl.Name)

				created, err := dyn.Resource(instGVR).Get(context.Background(), inst.GetName(), metav1.GetOptions{})
				if err != nil {
					t.Fatalf("re-get instance: %v", err)
				}
				uid := string(created.GetUID())

				// Risk 3: the dev variant + per-component control plane exist,
				// the production workloads stay out, and the overlay's status
				// additions resolve.
				lister := newNodeLister(t, mapper, rgd)
				waitNodeIDs(t, dyn, lister, uid, devIDs, tmpl.Name+" dev resources")
				assertNodeIDsAbsent(t, dyn, lister, uid, prodIDs, tmpl.Name+" production workloads in development mode")
				waitDevStatus(t, dyn, instGVR, inst.GetName(), tmpl)
			})
		})
	}
	if seen == 0 {
		t.Fatal("no seed template declares spec.development — the dev-overlay e2e covered nothing")
	}
}

// e2eDevInstance builds a development-mode instance: the standard sample spec
// with kedgeMode=development and — for templates listed in
// e2eDevImageStripped — the production image fields removed.
func e2eDevInstance(t *testing.T, tmpl *infrav1alpha1.Template, runID string) *unstructured.Unstructured {
	t.Helper()
	inst := e2eInstance(t, tmpl, runID)
	spec, _, _ := unstructured.NestedMap(inst.Object, "spec")
	for _, field := range e2eDevImageStripped[tmpl.Name] {
		delete(spec, field)
	}
	spec[infrav1alpha1.KedgeModeField] = infrav1alpha1.KedgeModeDevelopment
	_ = unstructured.SetNestedMap(inst.Object, spec, "spec")
	return inst
}

// overlayNodeIDs derives, from the BUILT RGD, the graph node ids the two
// modes split on: resources gated to production (the component workloads the
// overlay fenced with the prod condition) and resources gated to development
// (everything the overlay synthesized). Reading the RGD rather than
// re-deriving the overlay's rules keeps the test honest against what actually
// ships — including conditional pieces like the per-component workspace PVC,
// which is skipped when the workload already mounts workingDir.
func overlayNodeIDs(t *testing.T, rgd *unstructured.Unstructured) (prodIDs, devIDs []string) {
	t.Helper()
	resources, _, _ := unstructured.NestedSlice(rgd.Object, "spec", "resources")
	for _, r := range resources {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		id, _, _ := unstructured.NestedString(rm, "id")
		conds, _, _ := unstructured.NestedStringSlice(rm, "includeWhen")
		for _, c := range conds {
			switch c {
			case prodModeCondition:
				prodIDs = append(prodIDs, id)
			case devModeCondition:
				devIDs = append(devIDs, id)
			}
		}
	}
	sort.Strings(prodIDs)
	sort.Strings(devIDs)
	if len(prodIDs) == 0 || len(devIDs) == 0 {
		t.Fatalf("RGD %q carries no mode-gated resources (prod=%v dev=%v)", rgd.GetName(), prodIDs, devIDs)
	}
	return prodIDs, devIDs
}

// nodeGVKs maps every RGD resource id to its declared child kind.
func nodeGVKs(t *testing.T, rgd *unstructured.Unstructured) map[string]schema.GroupVersionKind {
	t.Helper()
	out := map[string]schema.GroupVersionKind{}
	resources, _, _ := unstructured.NestedSlice(rgd.Object, "spec", "resources")
	for _, r := range resources {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		id, _, _ := unstructured.NestedString(rm, "id")
		apiVersion, _, _ := unstructured.NestedString(rm, "template", "apiVersion")
		kind, _, _ := unstructured.NestedString(rm, "template", "kind")
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			t.Fatalf("resource %q: invalid apiVersion %q: %v", id, apiVersion, err)
		}
		out[id] = gv.WithKind(kind)
	}
	return out
}

// nodeLister resolves the RGD's child kinds to listable resources using the
// test's discovery mapper.
type nodeLister struct {
	gvrByID map[string]schema.GroupVersionResource
}

func newNodeLister(t *testing.T, mapper meta.RESTMapper, rgd *unstructured.Unstructured) *nodeLister {
	t.Helper()
	byID := nodeGVKs(t, rgd)
	out := &nodeLister{gvrByID: map[string]schema.GroupVersionResource{}}
	for id, gvk := range byID {
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			t.Fatalf("cannot map child kind %s (resource %q): %v", gvk, id, err)
		}
		out.gvrByID[id] = mapping.Resource
	}
	return out
}

// presentNodeIDs lists the instance's children (by kro's instance-id label)
// for the given node ids and reports which exist.
func presentNodeIDs(dyn dynamic.Interface, lister *nodeLister, uid string, ids []string) map[string]bool {
	selector := kroInstanceIDLabel + "=" + uid
	present := map[string]bool{}
	listed := map[schema.GroupVersionResource]bool{}
	found := map[string]bool{}
	for _, id := range ids {
		gvr, ok := lister.gvrByID[id]
		if !ok || listed[gvr] {
			continue
		}
		listed[gvr] = true
		list, err := dyn.Resource(gvr).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			continue
		}
		for i := range list.Items {
			found[list.Items[i].GetLabels()[kroNodeIDLabel]] = true
		}
	}
	for _, id := range ids {
		present[id] = found[id]
	}
	return present
}

// waitNodeIDs waits until every listed node id has a child object.
func waitNodeIDs(t *testing.T, dyn dynamic.Interface, lister *nodeLister, uid string, ids []string, what string) {
	t.Helper()
	deadline := time.Now().Add(e2eInstanceWait)
	var missing []string
	for {
		present := presentNodeIDs(dyn, lister, uid, ids)
		missing = missing[:0]
		for _, id := range ids {
			if !present[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) == 0 {
			t.Logf("%s: all child objects created (%s)", what, strings.Join(ids, ", "))
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(e2ePollEvery)
	}
	sort.Strings(missing)
	t.Fatalf("%s: missing child objects after %s: %s", what, e2eInstanceWait, strings.Join(missing, ", "))
}

// assertNodeIDsAbsent asserts none of the node ids has a child object. Called
// after the wanted set fully materialized, so kro has completed an apply pass
// — an excluded resource showing up would be a mode-gating bug.
func assertNodeIDsAbsent(t *testing.T, dyn dynamic.Interface, lister *nodeLister, uid string, ids []string, what string) {
	t.Helper()
	present := presentNodeIDs(dyn, lister, uid, ids)
	var leaked []string
	for _, id := range ids {
		if present[id] {
			leaked = append(leaked, id)
		}
	}
	if len(leaked) > 0 {
		sort.Strings(leaked)
		t.Fatalf("%s: mode-excluded resources were created: %s", what, strings.Join(leaked, ", "))
	}
}

// dumpInstanceConditions logs the instance's status conditions when the test
// has failed — kro's explanation for a child object that never applied.
func dumpInstanceConditions(t *testing.T, dyn dynamic.Interface, gvr schema.GroupVersionResource, name string) {
	t.Helper()
	if !t.Failed() {
		return
	}
	obj, err := dyn.Resource(gvr).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Logf("dump %s/%s: %v", gvr.Resource, name, err)
		return
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	raw, _ := json.MarshalIndent(conds, "", "  ")
	t.Logf("instance %s/%s conditions:\n%s", gvr.Resource, name, raw)
}

// waitDevStatus waits for the overlay's status additions to resolve on a
// development-mode instance: runtimeNamespace, controlSecretRef.name, and
// components.<name>.controlServiceRef.name for every declared component.
func waitDevStatus(t *testing.T, dyn dynamic.Interface, gvr schema.GroupVersionResource, name string, tmpl *infrav1alpha1.Template) {
	t.Helper()
	deadline := time.Now().Add(e2eInstanceWait)
	var lastMissing []string
	for time.Now().Before(deadline) {
		obj, err := dyn.Resource(gvr).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			time.Sleep(e2ePollEvery)
			continue
		}
		lastMissing = lastMissing[:0]
		if v, _, _ := unstructured.NestedString(obj.Object, "status", "runtimeNamespace"); v == "" {
			lastMissing = append(lastMissing, "status.runtimeNamespace")
		}
		if v, _, _ := unstructured.NestedString(obj.Object, "status", "controlSecretRef", "name"); v == "" {
			lastMissing = append(lastMissing, "status.controlSecretRef.name")
		}
		for comp := range tmpl.Spec.Development.Components {
			if v, _, _ := unstructured.NestedString(obj.Object, "status", "components", comp, "controlServiceRef", "name"); v == "" {
				lastMissing = append(lastMissing, "status.components."+comp+".controlServiceRef.name")
			}
		}
		if len(lastMissing) == 0 {
			t.Logf("template %q: dev-mode status additions resolved", tmpl.Name)
			return
		}
		time.Sleep(e2ePollEvery)
	}
	sort.Strings(lastMissing)
	t.Fatalf("template %q: dev-mode status additions never resolved within %s: %s",
		tmpl.Name, e2eInstanceWait, strings.Join(lastMissing, ", "))
}
