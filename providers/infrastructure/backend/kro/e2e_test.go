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

// E2E coverage for the seeded infrastructure Templates against a real, CLEAN kro
// cluster (one fresh cluster per run — see `make e2e-infrastructure`). Build-
// tagged `e2e` so it never runs in the normal `go test ./...` unit pass.
//
// For every Template the provider ships it proves two things unit tests can't
// (they only check buildRGD's output):
//
//  1. kro ACCEPTS the authored ResourceGraphDefinition (status GraphAccepted=
//     True) and establishes the per-template instance CRD. Catches malformed
//     graphs — an integer field fed a string, an includeWhen that isn't a
//     standalone CEL expression, etc.
//  2. kro can CREATE an instance's objects: a sample instance reconciles WITHOUT
//     an apply error. This is the exact failure that motivated the schema-default
//     image convention ("apply results contain errors: ... image: Required
//     value"). It does NOT require the images to pull — apply validates the spec.
//  3. The child objects the RGD declares actually EXIST in the runtime cluster
//     (the Deployment, Service, StatefulSet, HTTPRoute, …), matched by kro's
//     instance-id/node-id labels. Resources gated by includeWhen (e.g. the
//     preview HTTPRoute) are verified only when present, never required.
package kro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// kro stamps every child object it applies for an instance with these labels
// (see the kro fork's pkg/metadata). instance-id is the instance UID; node-id is
// the RGD resource's id. We use them to prove the actual Deployment/Service/
// HTTPRoute/etc. landed in the runtime cluster, not just that the instance
// reported no apply error.
const (
	kroInstanceIDLabel = "kro.run/instance-id"
	kroNodeIDLabel     = "kro.run/node-id"
)

const (
	e2eGraphWait    = 90 * time.Second
	e2eCRDWait      = 60 * time.Second
	e2eInstanceWait = 120 * time.Second
	e2ePollEvery    = 2 * time.Second
)

// e2eMinimalSpecs supplies a valid instance spec for templates that ship no
// sampleValues (those that do — application, database — use them directly).
var e2eMinimalSpecs = map[string]map[string]any{}

// e2ePlatformStamped are spec fields a platform component normally writes onto an
// instance before kro reconciles it — values the user does NOT supply (the
// sampleValues correctly omit them, marked "do NOT set"). With no controller
// running in the standalone-kro e2e, the test stands in for the platform and
// supplies them, so platform-stamped resources can render (e.g. application's
// HTTPRoute needs spec.expose.fqdn — without it kro can't evaluate the route's
// hostname and never creates it). Deep-merged onto the spec.
var e2ePlatformStamped = map[string]map[string]any{
	// The infra controller computes expose.fqdn and stamps credentialsSecretName
	// (see the application template's "platform stamps …" note).
	"application": {
		"expose":                map[string]any{"fqdn": "demo.e2e.test"},
		"credentialsSecretName": "demo-oidc",
	},
	// simple-webapp is exposure-only: the controller stamps just expose.fqdn.
	"simple-webapp": {
		"expose": map[string]any{"fqdn": "web.e2e.test"},
	},
}

// e2eApplyErrorMarkers are substrings kro puts in an instance condition when it
// FAILS to apply a child resource (the bug class we guard against). Readiness
// waits (pods not up because an image can't pull in CI) do not contain these.
var e2eApplyErrorMarkers = []string{
	"apply results contain errors",
	"is invalid",
	"Required value",
	"failed to apply",
	"admission webhook",
}

func TestE2ESeedTemplates(t *testing.T) {
	dyn, mapper := e2eClients(t)
	// A per-run nonce makes every instance (and therefore every child object)
	// uniquely named, so re-running against a reused cluster can't collide with a
	// previous run's not-yet-garbage-collected objects. 16 hex digits because
	// sandbox-runner's name must match ^kedge-sandbox-[a-f0-9]{16}$.
	runID := fmt.Sprintf("%016x", time.Now().UnixNano())
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
		seen++
		t.Run(entry.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", entry.Name(), err)
			}
			tmpl := decodeTemplate(t, raw)

			rgd, err := buildRGD(tmpl, testTokens())
			if err != nil {
				t.Fatalf("buildRGD(%s): %v", tmpl.Name, err)
			}
			applyRGD(t, dyn, rgd)
			t.Cleanup(func() { _ = dyn.Resource(rgdGVR).Delete(context.Background(), rgd.GetName(), metav1.DeleteOptions{}) })

			// 1. kro accepts the graph.
			if status, msg := waitGraphAccepted(t, dyn, rgd.GetName()); status != "True" {
				t.Fatalf("kro rejected RGD for template %q: GraphAccepted=%s: %s", tmpl.Name, status, msg)
			}
			t.Logf("template %q: RGD accepted", tmpl.Name)

			// 2. kro creates an instance's objects without an apply error.
			instGVR := schema.GroupVersionResource{
				Group:    tmpl.Spec.InstanceCRD.Group,
				Version:  tmpl.Spec.InstanceCRD.Version,
				Resource: tmpl.Spec.InstanceCRD.Resource,
			}
			inst := e2eInstance(t, tmpl, runID)
			createInstance(t, dyn, instGVR, inst)
			t.Cleanup(func() {
				_ = dyn.Resource(instGVR).Delete(context.Background(), inst.GetName(), metav1.DeleteOptions{})
			})

			waitInstanceApplied(t, dyn, instGVR, inst.GetName(), tmpl.Name)
			t.Logf("template %q: instance reconciled (no apply error)", tmpl.Name)

			// 3. The child objects the RGD declares actually exist in the
			// runtime cluster (the Deployment/Service/StatefulSet/HTTPRoute/…),
			// not just a clean instance status.
			created, err := dyn.Resource(instGVR).Get(context.Background(), inst.GetName(), metav1.GetOptions{})
			if err != nil {
				t.Fatalf("template %q: re-get instance for UID: %v", tmpl.Name, err)
			}
			verifyChildrenCreated(t, dyn, mapper, rgd, string(created.GetUID()), tmpl.Name)
		})
	}
	if seen == 0 {
		t.Fatal("no seed templates found")
	}
}

// e2eClients builds a dynamic client and a discovery-backed REST mapper against
// the cluster in KUBECONFIG. The mapper turns each child kind the RGD declares
// (Deployment, Service, HTTPRoute, …) into the resource we List to verify it was
// created.
func e2eClients(t *testing.T) (dynamic.Interface, meta.RESTMapper) {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("KUBECONFIG not set; this e2e needs a clean kro cluster (see make e2e-infrastructure)")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("build rest config from %s: %v", kubeconfig, err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatalf("discovery client: %v", err)
	}
	groups, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		t.Fatalf("discover API groups: %v", err)
	}
	return dyn, restmapper.NewDiscoveryRESTMapper(groups)
}

// applyRGD creates or updates the RGD, tolerating the lifecycle races between
// tests that share a template name: a prior test's cleanup delete may still be
// finalizing (Get succeeds, then Update/Create 404s or conflicts), so retry
// until the apply lands or the deadline passes.
func applyRGD(t *testing.T, dyn dynamic.Interface, rgd *unstructured.Unstructured) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(e2eGraphWait)
	var lastErr error
	for time.Now().Before(deadline) {
		existing, err := dyn.Resource(rgdGVR).Get(ctx, rgd.GetName(), metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			if _, err := dyn.Resource(rgdGVR).Create(ctx, rgd.DeepCopy(), metav1.CreateOptions{}); err == nil {
				return
			} else if !apierrors.IsAlreadyExists(err) {
				lastErr = fmt.Errorf("create RGD %q: %w", rgd.GetName(), err)
			}
		case err != nil:
			lastErr = fmt.Errorf("get RGD %q: %w", rgd.GetName(), err)
		case existing.GetDeletionTimestamp() != nil:
			lastErr = fmt.Errorf("RGD %q is still terminating", rgd.GetName())
		default:
			next := rgd.DeepCopy()
			next.SetResourceVersion(existing.GetResourceVersion())
			if _, err := dyn.Resource(rgdGVR).Update(ctx, next, metav1.UpdateOptions{}); err == nil {
				return
			} else if !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
				t.Fatalf("update RGD %q: %v", rgd.GetName(), err)
			} else {
				lastErr = fmt.Errorf("update RGD %q: %w", rgd.GetName(), err)
			}
		}
		time.Sleep(e2ePollEvery)
	}
	t.Fatalf("apply RGD %q never settled: %v", rgd.GetName(), lastErr)
}

// e2eInstance builds a sample instance: the template's sampleValues when present
// (the curated working example), otherwise a minimal valid spec.
func e2eInstance(t *testing.T, tmpl *infrav1alpha1.Template, runID string) *unstructured.Unstructured {
	t.Helper()
	spec := map[string]any{}
	if sv := tmpl.Spec.SampleValues; sv != nil && len(sv.Raw) > 0 {
		if err := json.Unmarshal(sv.Raw, &spec); err != nil {
			t.Fatalf("template %q: decode sampleValues: %v", tmpl.Name, err)
		}
	} else if min, ok := e2eMinimalSpecs[tmpl.Name]; ok {
		// Copy so the per-run rename below never mutates the shared map.
		spec = map[string]any{}
		mergeSpec(spec, min)
	} else {
		t.Fatalf("template %q has no sampleValues and no e2eMinimalSpecs entry — add one", tmpl.Name)
	}
	if overlay, ok := e2ePlatformStamped[tmpl.Name]; ok {
		mergeSpec(spec, overlay)
	}
	name := e2eInstanceName(tmpl.Name, spec, runID)
	spec["name"] = name
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": tmpl.Spec.InstanceCRD.Group + "/" + tmpl.Spec.InstanceCRD.Version,
		"kind":       tmpl.Spec.InstanceCRD.Kind,
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
}

// e2eInstanceName returns a per-run-unique instance name so child objects from
// one run never collide with a prior run's on a reused cluster. sandbox-runner's
// name is constrained to ^kedge-sandbox-[a-f0-9]{16}$, so it takes the 16-hex
// runID verbatim; every other template suffixes its base name with the LOW
// half of it. The low half, not the high: the runID is a hex UnixNano, whose
// top 32 bits change every ~4s — two instances created seconds apart (the
// dev-mode e2e's production/development pair) would collide on runID[:8], and
// the second create would silently adopt the first's terminating instance.
func e2eInstanceName(tmplName string, spec map[string]any, runID string) string {
	base, _ := spec["name"].(string)
	if base == "" {
		base = "e2e-" + tmplName
	}
	return base + "-" + runID[8:]
}

// mergeSpec deep-merges src into dst (nested maps recurse; other values
// overwrite). Used to overlay platform-stamped fields onto a sample spec.
func mergeSpec(dst, src map[string]any) {
	for k, v := range src {
		if vm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				mergeSpec(dm, vm)
				continue
			}
		}
		dst[k] = v
	}
}

// createInstance retries Create until kro has established + served the
// per-template CRD (a fresh RGD takes a few seconds to register the kind).
func createInstance(t *testing.T, dyn dynamic.Interface, gvr schema.GroupVersionResource, inst *unstructured.Unstructured) {
	t.Helper()
	deadline := time.Now().Add(e2eCRDWait)
	for {
		_, err := dyn.Resource(gvr).Create(context.Background(), inst, metav1.CreateOptions{})
		if err == nil || apierrors.IsAlreadyExists(err) {
			return
		}
		// "no matches for kind" / NotFound while the CRD is still registering.
		if time.Now().After(deadline) {
			t.Fatalf("create %s instance %q: CRD never became servable: %v", gvr.Resource, inst.GetName(), err)
		}
		time.Sleep(e2ePollEvery)
	}
}

// waitInstanceApplied waits until kro has reconciled the instance and asserts it
// applied its objects without an apply error. A readiness wait (images not
// pulled in CI) is success — apply succeeded. An apply-error marker is failure.
func waitInstanceApplied(t *testing.T, dyn dynamic.Interface, gvr schema.GroupVersionResource, name, tmplName string) {
	t.Helper()
	deadline := time.Now().Add(e2eInstanceWait)
	var sawConditions bool
	for time.Now().Before(deadline) {
		obj, err := dyn.Resource(gvr).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			time.Sleep(e2ePollEvery)
			continue
		}
		conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
		for _, c := range conds {
			cond, ok := c.(map[string]any)
			if !ok {
				continue
			}
			sawConditions = true
			msg, _, _ := unstructured.NestedString(cond, "message")
			for _, marker := range e2eApplyErrorMarkers {
				if strings.Contains(msg, marker) {
					ctype, _, _ := unstructured.NestedString(cond, "type")
					t.Fatalf("template %q: kro failed to apply instance objects (%s): %s", tmplName, ctype, msg)
				}
			}
		}
		// kro reconciled it and recorded conditions, none of which are apply
		// errors → the objects were applied. (Readiness is out of scope: the
		// images may be unpullable in CI.)
		if sawConditions {
			return
		}
		time.Sleep(e2ePollEvery)
	}
	if !sawConditions {
		t.Fatalf("template %q: kro never reconciled instance %q within %s", tmplName, name, e2eInstanceWait)
	}
}

// verifyChildrenCreated asserts kro actually created, in the runtime cluster,
// the child objects the RGD declares for this instance — not merely that the
// instance reported no apply error. The expected set comes straight from the
// RGD: every resource with NO includeWhen is created unconditionally and must
// be found; a resource gated by includeWhen (e.g. the preview HTTPRoute +
// ReferenceGrant, off in the minimal specs) is verified only if it shows up,
// never required. Children are matched by kro's own labels
// (kro.run/instance-id = the instance UID, kro.run/node-id = the RGD resource
// id), so this is fully template-driven: add a resource to a template and it is
// automatically required here.
func verifyChildrenCreated(t *testing.T, dyn dynamic.Interface, mapper meta.RESTMapper, rgd *unstructured.Unstructured, instanceUID, tmplName string) {
	t.Helper()
	type childNode struct {
		id       string
		gvk      schema.GroupVersionKind
		required bool
	}
	resources, _, _ := unstructured.NestedSlice(rgd.Object, "spec", "resources")
	var nodes []childNode
	gvks := map[schema.GroupVersionKind]bool{}
	for _, r := range resources {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		id, _, _ := unstructured.NestedString(rm, "id")
		apiVersion, _, _ := unstructured.NestedString(rm, "template", "apiVersion")
		kind, _, _ := unstructured.NestedString(rm, "template", "kind")
		if apiVersion == "" || kind == "" {
			continue
		}
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			t.Fatalf("template %q: resource %q has invalid apiVersion %q: %v", tmplName, id, apiVersion, err)
		}
		gvk := gv.WithKind(kind)
		gvks[gvk] = true
		includeWhen, hasInclude, _ := unstructured.NestedSlice(rm, "includeWhen")
		nodes = append(nodes, childNode{id: id, gvk: gvk, required: !hasInclude || len(includeWhen) == 0})
	}
	if len(nodes) == 0 {
		t.Fatalf("template %q: RGD declares no child resources to verify", tmplName)
	}

	// Resolve each declared kind to a listable resource once.
	gvrByGVK := map[schema.GroupVersionKind]schema.GroupVersionResource{}
	for gvk := range gvks {
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			t.Fatalf("template %q: cannot map child kind %s to a resource (is its CRD installed?): %v", tmplName, gvk, err)
		}
		gvrByGVK[gvk] = mapping.Resource
	}

	selector := kroInstanceIDLabel + "=" + instanceUID
	deadline := time.Now().Add(e2eInstanceWait)
	var missing []string
	for {
		present := map[string]bool{}    // node-id -> created
		byKind := map[string][]string{} // kind -> object names (for the log)
		for gvk, gvr := range gvrByGVK {
			list, err := dyn.Resource(gvr).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
			if err != nil {
				continue // transient; retried until the deadline
			}
			for i := range list.Items {
				item := &list.Items[i]
				present[item.GetLabels()[kroNodeIDLabel]] = true
				byKind[gvk.Kind] = append(byKind[gvk.Kind], item.GetName())
			}
		}
		missing = missing[:0]
		for _, n := range nodes {
			if n.required && !present[n.id] {
				missing = append(missing, n.id+" ("+n.gvk.Kind+")")
			}
		}
		if len(missing) == 0 {
			summary := make([]string, 0, len(byKind))
			for kind, names := range byKind {
				summary = append(summary, kind+"×"+strconv.Itoa(len(names)))
			}
			sort.Strings(summary)
			t.Logf("template %q: kro created child objects in the runtime cluster: %s", tmplName, strings.Join(summary, ", "))
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(e2ePollEvery)
	}
	sort.Strings(missing)
	t.Fatalf("template %q: kro did not create these required child objects within %s: %s",
		tmplName, e2eInstanceWait, strings.Join(missing, ", "))
}

func waitGraphAccepted(t *testing.T, dyn dynamic.Interface, name string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(e2eGraphWait)
	var lastStatus, lastMsg string
	for time.Now().Before(deadline) {
		obj, err := dyn.Resource(rgdGVR).Get(context.Background(), name, metav1.GetOptions{})
		if err == nil {
			lastStatus, lastMsg = conditionByType(obj, "GraphAccepted")
			if lastStatus == "True" || lastStatus == "False" {
				return lastStatus, lastMsg
			}
		}
		time.Sleep(e2ePollEvery)
	}
	t.Fatalf("timed out after %s waiting for RGD %q GraphAccepted (last=%q: %s)", e2eGraphWait, name, lastStatus, lastMsg)
	return lastStatus, lastMsg
}

func conditionByType(obj *unstructured.Unstructured, condType string) (string, string) {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if tp, _, _ := unstructured.NestedString(cond, "type"); tp != condType {
			continue
		}
		status, _, _ := unstructured.NestedString(cond, "status")
		msg, _, _ := unstructured.NestedString(cond, "message")
		return status, msg
	}
	return "", ""
}
