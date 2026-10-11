/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
)

const browserTemplateTestVersion = "0.2.0"

// managedBrowserIsolationTestObjects models the public contract App Studio
// consumes: an Infrastructure-issued Template capability plus current
// Template and Instance readiness. Backend configuration stays owned by the
// Infrastructure provider and is tested there.
func managedBrowserIsolationTestObjects(t *testing.T) []runtime.Object {
	t.Helper()
	return []runtime.Object{
		readyBrowserIsolationStudio(),
		readyBrowserIsolationInstance(),
		readyManagedBrowserTemplate(t),
	}
}

func readyManagedBrowserTemplate(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	template := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": templatesGVR.GroupVersion().String(),
		"kind":       templateResource.Kind,
		"metadata":   map[string]any{"name": managedBrowserTemplateName, "uid": "browser-template-uid"},
		"spec":       map[string]any{"version": browserTemplateTestVersion},
		"status": map[string]any{
			"capabilities": []any{map[string]any{
				"name": managedBrowserIsolationCapabilityName, "version": managedBrowserIsolationCapabilityV1,
			}},
		},
	}}
	template.SetGeneration(6)
	_ = unstructured.SetNestedField(template.Object, int64(6), "status", "observedGeneration")
	_ = unstructured.SetNestedSlice(template.Object, []any{map[string]any{
		"type": "Ready", "status": "True", "observedGeneration": int64(6),
	}}, "status", "conditions")
	return template
}

func readyBrowserIsolationStudio() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Studio",
		"metadata":   map[string]any{"name": aiv1alpha1.StudioName},
		"spec": map[string]any{"browser": map[string]any{
			"resourceRef": map[string]any{
				"apiVersion": infrastructureGroupVersion.String(),
				"kind":       "Instance",
				"resource":   "instances",
				"name":       studioBrowserInstanceName,
			},
		}},
		"status": map[string]any{"browser": map[string]any{
			"phase": aiv1alpha1.StudioServiceReady, "resource": "instances", "instance": studioBrowserInstanceName,
		}},
	}}
}

func readyBrowserIsolationInstance() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": infrastructureGroupVersion.String(),
		"kind":       "Instance",
		"metadata": map[string]any{
			"name": studioBrowserInstanceName, "uid": "browser-instance-uid", "generation": int64(4),
		},
		"spec": map[string]any{"template": managedBrowserTemplateName},
		"status": map[string]any{
			"template":           managedBrowserTemplateName,
			"templateVersion":    browserTemplateTestVersion,
			"phase":              "Ready",
			"observedGeneration": int64(4),
			"mcpIsolationReady":  true,
			"conditions": []any{map[string]any{
				"type": "Ready", "status": "True", "observedGeneration": int64(4),
			}},
		},
	}}
}

func setBrowserReadyConditionGeneration(instance *unstructured.Unstructured, generation int64) {
	conditions, _, _ := unstructured.NestedSlice(instance.Object, "status", "conditions")
	condition := conditions[0].(map[string]any)
	condition["observedGeneration"] = generation
	_ = unstructured.SetNestedSlice(instance.Object, conditions, "status", "conditions")
}

func setBrowserTemplateReadyConditionGeneration(template *unstructured.Unstructured, generation int64) {
	conditions, _, _ := unstructured.NestedSlice(template.Object, "status", "conditions")
	condition := conditions[0].(map[string]any)
	condition["observedGeneration"] = generation
	_ = unstructured.SetNestedSlice(template.Object, conditions, "status", "conditions")
}

func TestBrowserBackendRefRequiresCurrentInfrastructureCapabilityAndRuntimeProof(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(studio, instance, template *unstructured.Unstructured)
		want   bool
	}{
		{name: "current Infrastructure capability", want: true},
		{name: "stale Instance observed generation", mutate: func(_ *unstructured.Unstructured, instance, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(instance.Object, int64(3), "status", "observedGeneration")
		}},
		{name: "stale Instance Ready condition", mutate: func(_ *unstructured.Unstructured, instance, _ *unstructured.Unstructured) {
			setBrowserReadyConditionGeneration(instance, 3)
		}},
		{name: "Instance has not observed compatible Template patch", mutate: func(_ *unstructured.Unstructured, instance, template *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(template.Object, "0.2.1", "spec", "version")
		}},
		{name: "runtime Deployment proof absent", mutate: func(_ *unstructured.Unstructured, instance, _ *unstructured.Unstructured) {
			unstructured.RemoveNestedField(instance.Object, "status", "mcpIsolationReady")
		}},
		{name: "runtime Deployment proof false", mutate: func(_ *unstructured.Unstructured, instance, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(instance.Object, false, "status", "mcpIsolationReady")
		}},
		{name: "stale Template generation", mutate: func(_ *unstructured.Unstructured, _ *unstructured.Unstructured, template *unstructured.Unstructured) {
			template.SetGeneration(7)
		}},
		{name: "stale Template Ready condition", mutate: func(_ *unstructured.Unstructured, _ *unstructured.Unstructured, template *unstructured.Unstructured) {
			setBrowserTemplateReadyConditionGeneration(template, 5)
		}},
		{name: "missing capability", mutate: func(_ *unstructured.Unstructured, _ *unstructured.Unstructured, template *unstructured.Unstructured) {
			unstructured.RemoveNestedField(template.Object, "status", "capabilities")
		}},
		{name: "unsupported capability version", mutate: func(_ *unstructured.Unstructured, _ *unstructured.Unstructured, template *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(template.Object, []any{map[string]any{
				"name": managedBrowserIsolationCapabilityName, "version": "v2",
			}}, "status", "capabilities")
		}},
		{name: "Template Ready condition false", mutate: func(_ *unstructured.Unstructured, _ *unstructured.Unstructured, template *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(template.Object, []any{map[string]any{
				"type": "Ready", "status": "False", "observedGeneration": int64(6),
			}}, "status", "conditions")
		}},
		{name: "missing Template UID", mutate: func(_ *unstructured.Unstructured, _ *unstructured.Unstructured, template *unstructured.Unstructured) {
			template.SetUID("")
		}},
		{name: "deleting Template", mutate: func(_ *unstructured.Unstructured, _ *unstructured.Unstructured, template *unstructured.Unstructured) {
			markBrowserObjectDeleting(template)
		}},
		{name: "deleting Instance", mutate: func(_ *unstructured.Unstructured, instance, _ *unstructured.Unstructured) {
			markBrowserObjectDeleting(instance)
		}},
		{name: "deleting Studio", mutate: func(studio, _ *unstructured.Unstructured, _ *unstructured.Unstructured) {
			markBrowserObjectDeleting(studio)
		}},
		{name: "Studio points at a different Instance", mutate: func(studio, _ *unstructured.Unstructured, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(studio.Object, "other-browser", "spec", "browser", "resourceRef", "name")
		}},
		{name: "custom Instance template", mutate: func(_ *unstructured.Unstructured, instance, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(instance.Object, "custom-browser", "spec", "template")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := managedBrowserIsolationTestObjects(t)
			studio := objects[0].(*unstructured.Unstructured)
			instance := objects[1].(*unstructured.Unstructured)
			template := objects[2].(*unstructured.Unstructured)
			if test.mutate != nil {
				test.mutate(studio, instance, template)
			}
			dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(), studio, instance, template)
			server := &Server{}
			ref, isolated := server.browserBackendRef(context.Background(), asclient.NewFromDynamic(dynamicClient))
			if isolated != test.want {
				t.Fatalf("isolated MCP contexts = %v, want %v (ref=%+v)", isolated, test.want, ref)
			}
			if test.want {
				if ref.Resource != "instances" || ref.Name != studioBrowserInstanceName || !ref.IsolatedMCPContexts {
					t.Fatalf("managed browser ref = %+v, want isolated %s/%s", ref, "instances", studioBrowserInstanceName)
				}
				if ref.InstanceUID != "browser-instance-uid" || ref.TemplateUID != "browser-template-uid" || ref.TemplateVersion != browserTemplateTestVersion || ref.TemplateGeneration != 6 {
					t.Fatalf("managed browser identity = instance uid %q, template uid %q version %q generation %d", ref.InstanceUID, ref.TemplateUID, ref.TemplateVersion, ref.TemplateGeneration)
				}
			}
		})
	}
}

func markBrowserObjectDeleting(object *unstructured.Unstructured) {
	_ = unstructured.SetNestedField(object.Object, "2026-10-11T00:00:00Z", "metadata", "deletionTimestamp")
}

func TestBrowserBackendRefAcceptsCurrentCompatibleTemplatePatch(t *testing.T) {
	objects := managedBrowserIsolationTestObjects(t)
	instance := objects[1].(*unstructured.Unstructured)
	template := objects[2].(*unstructured.Unstructured)
	template.SetGeneration(7)
	_ = unstructured.SetNestedField(template.Object, int64(7), "status", "observedGeneration")
	setBrowserTemplateReadyConditionGeneration(template, 7)
	_ = unstructured.SetNestedField(template.Object, "0.2.1", "spec", "version")
	_ = unstructured.SetNestedField(instance.Object, "0.2.1", "status", "templateVersion")

	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	ref, isolated := (&Server{}).browserBackendRef(context.Background(), asclient.NewFromDynamic(dynamicClient))
	if !isolated || ref.TemplateVersion != "0.2.1" {
		t.Fatalf("compatible current Template patch = isolated %v, ref %+v", isolated, ref)
	}
}

func studioSharedServiceTemplate(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": templatesGVR.GroupVersion().String(),
		"kind":       templateResource.Kind,
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"backend": "kro"},
	}}
}

// studioMissingSearchRef is the shape the bug left behind: a Studio written
// while the searxng Template was not yet visible in the workspace (its
// infrastructure APIBinding had not landed), so spec.search carries a size but
// no resourceRef, while browser was retrofitted later and is complete.
func studioMissingSearchRef() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Studio",
		"metadata":   map[string]any{"name": aiv1alpha1.StudioName},
		"spec": map[string]any{
			"search": map[string]any{"size": "small"},
			"browser": map[string]any{
				"size": "small",
				"resourceRef": map[string]any{
					"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
					"kind":       "Instance",
					"name":       studioBrowserInstanceName,
					"resource":   "instances",
				},
			},
		},
	}}
}

func studioServiceRef(t *testing.T, c *asclient.Client, service string) map[string]any {
	t.Helper()
	st, err := c.Resource(studioResource, "").Get(context.Background(), aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ref, _, _ := unstructured.NestedMap(st.Object, "spec", service, "resourceRef")
	return ref
}

func TestRetrofitStudioServicesBackfillsSearch(t *testing.T) {
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(),
		studioMissingSearchRef(),
		studioSharedServiceTemplate("searxng"),
		studioSharedServiceTemplate("browser"),
	)
	c := asclient.NewFromDynamic(dynamicClient)
	server := &Server{}

	st, err := c.Resource(studioResource, "").Get(context.Background(), aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !server.retrofitStudioServices(context.Background(), c, st) {
		t.Fatal("retrofit reported incomplete although both templates resolve")
	}

	search := studioServiceRef(t, c, "search")
	if search["resource"] != "instances" || search["name"] != studioSearchInstanceName || search["kind"] != "Instance" {
		t.Fatalf("search resourceRef = %#v, want instances/%s", search, studioSearchInstanceName)
	}
	browser := studioServiceRef(t, c, "browser")
	if browser["name"] != studioBrowserInstanceName {
		t.Fatalf("browser resourceRef = %#v, want it left as it was", browser)
	}
}

func TestRetrofitStudioServicesSkipsDisabledService(t *testing.T) {
	st := studioMissingSearchRef()
	_ = unstructured.SetNestedField(st.Object, true, "spec", "search", "disabled")
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(), st, studioSharedServiceTemplate("searxng"), studioSharedServiceTemplate("browser"))
	c := asclient.NewFromDynamic(dynamicClient)
	server := &Server{}

	if !server.retrofitStudioServices(context.Background(), c, st.DeepCopy()) {
		t.Fatal("a disabled service must not count as unresolved")
	}
	if ref := studioServiceRef(t, c, "search"); len(ref) != 0 {
		t.Fatalf("disabled search gained a resourceRef: %#v", ref)
	}
	if len(dynamicClient.Actions()) != 1 { // the Get in studioServiceRef only; no Update
		t.Fatalf("expected no write for a disabled service, got actions %v", dynamicClient.Actions())
	}
}

func TestEnsureStudioRetriesUntilSearchTemplateResolves(t *testing.T) {
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(),
		studioMissingSearchRef(),
		studioSharedServiceTemplate("browser"),
	)
	c := asclient.NewFromDynamic(dynamicClient)
	server := &Server{}
	id := identity{clusterID: "ensure-studio-retries-" + t.Name()}
	t.Cleanup(func() { studioEnsured.Delete(id.clusterID) })

	// The searxng Template is not visible yet: the Studio stays as it is and
	// the workspace is NOT marked ensured, so the next call tries again.
	server.ensureStudio(context.Background(), c, id)
	if ref := studioServiceRef(t, c, "search"); len(ref) != 0 {
		t.Fatalf("search gained a resourceRef without a template: %#v", ref)
	}
	if _, done := studioEnsured.Load(id.clusterID); done {
		t.Fatal("workspace marked ensured while search is still unresolved")
	}

	// The APIBinding lands and the Template appears: the next project
	// creation retrofits search and the workspace is finally ensured.
	if err := dynamicClient.Tracker().Add(studioSharedServiceTemplate("searxng")); err != nil {
		t.Fatal(err)
	}
	server.ensureStudio(context.Background(), c, id)
	if ref := studioServiceRef(t, c, "search"); ref["name"] != studioSearchInstanceName {
		t.Fatalf("search resourceRef = %#v after the template appeared, want %s", ref, studioSearchInstanceName)
	}
	if _, done := studioEnsured.Load(id.clusterID); !done {
		t.Fatal("workspace not marked ensured once every service resolved")
	}

	// Ensured: no further reads or writes for this workspace.
	before := len(dynamicClient.Actions())
	server.ensureStudio(context.Background(), c, id)
	if got := len(dynamicClient.Actions()); got != before {
		t.Fatalf("ensured workspace still touched the Studio: %d new actions", got-before)
	}
}

func TestEnsureStudioCreatesWithBothReferencesAndRetriesUnresolved(t *testing.T) {
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(),
		studioSharedServiceTemplate("browser"),
	)
	c := asclient.NewFromDynamic(dynamicClient)
	server := &Server{}
	id := identity{clusterID: "ensure-studio-create-" + t.Name()}
	t.Cleanup(func() { studioEnsured.Delete(id.clusterID) })

	server.ensureStudio(context.Background(), c, id)
	if ref := studioServiceRef(t, c, "browser"); ref["name"] != studioBrowserInstanceName {
		t.Fatalf("browser resourceRef = %#v on the created Studio", ref)
	}
	if ref := studioServiceRef(t, c, "search"); len(ref) != 0 {
		t.Fatalf("search resourceRef = %#v although searxng is not resolvable", ref)
	}
	if _, done := studioEnsured.Load(id.clusterID); done {
		t.Fatal("workspace marked ensured although search is unresolved")
	}

	if err := dynamicClient.Tracker().Add(studioSharedServiceTemplate("searxng")); err != nil {
		t.Fatal(err)
	}
	server.ensureStudio(context.Background(), c, id)
	if ref := studioServiceRef(t, c, "search"); ref["name"] != studioSearchInstanceName {
		t.Fatalf("search resourceRef = %#v after the template appeared", ref)
	}
	if _, done := studioEnsured.Load(id.clusterID); !done {
		t.Fatal("workspace not marked ensured once search resolved")
	}
}
