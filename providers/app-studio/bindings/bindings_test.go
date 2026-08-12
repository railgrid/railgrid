/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bindings

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

func testProject() *aiv1alpha1.Project {
	return &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")},
	}
}

func testBinding() aiv1alpha1.ProjectProviderBindingSpec {
	return aiv1alpha1.ProjectProviderBindingSpec{
		Name:     "development",
		Provider: "infrastructure",
		Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			Name:       "demo-dev",
			APIVersion: "infrastructure.faros.sh/v1alpha1",
			Kind:       "Application",
			Resource:   "applications",
		},
		Values: runtime.RawExtension{Raw: []byte(`{"farosMode":"development","webImage":"x"}`)},
	}
}

func TestDesiredIsSelfContained(t *testing.T) {
	p := testProject()
	want, gvr, err := Desired(p, testBinding())
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	if gvr.Group != "infrastructure.faros.sh" || gvr.Resource != "applications" || gvr.Version != "v1alpha1" {
		t.Fatalf("gvr = %v", gvr)
	}
	if want.GetName() != "demo-dev" {
		t.Fatalf("name = %q, want demo-dev", want.GetName())
	}
	if want.GetLabels()[ProjectLabel] != "demo" {
		t.Fatalf("labels = %v, want %s=demo", want.GetLabels(), ProjectLabel)
	}
	owners := want.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "Project" || owners[0].Name != "demo" {
		t.Fatalf("ownerReferences = %+v", owners)
	}
	mode, _, _ := unstructured.NestedString(want.Object, "spec", "farosMode")
	if mode != "development" {
		t.Fatalf("spec.farosMode = %q", mode)
	}
}

func TestDesiredNameFallbacks(t *testing.T) {
	p := testProject()

	b := testBinding()
	b.ResourceRef.Name = ""
	b.Values = runtime.RawExtension{Raw: []byte(`{"name":"explicit"}`)}
	want, _, err := Desired(p, b)
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	if want.GetName() != "explicit" {
		t.Fatalf("name = %q, want explicit (values fallback)", want.GetName())
	}

	b.Values = runtime.RawExtension{}
	want, _, err = Desired(p, b)
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	if want.GetName() != "demo-development" {
		t.Fatalf("name = %q, want demo-development (project-binding fallback)", want.GetName())
	}
}

func TestDesiredInvalidBinding(t *testing.T) {
	p := testProject()

	b := testBinding()
	b.ResourceRef = nil
	if _, _, err := Desired(p, b); !IsInvalidBinding(err) {
		t.Fatalf("nil resourceRef: err = %v, want InvalidBindingError", err)
	}

	b = testBinding()
	b.Values = runtime.RawExtension{Raw: []byte(`{broken`)}
	if _, _, err := Desired(p, b); !IsInvalidBinding(err) {
		t.Fatalf("broken values: err = %v, want InvalidBindingError", err)
	}

	b = testBinding()
	b.ResourceRef.Resource = ""
	if _, _, err := Desired(p, b); !IsInvalidBinding(err) {
		t.Fatalf("missing resource: err = %v, want InvalidBindingError", err)
	}
}

func TestApplyActionsOverlayReplacesReservedValuesAndClearsTransport(t *testing.T) {
	input := map[string]any{
		"ordinary":                "preserved",
		ActionsExchangeURLField:   "stale-exchange",
		ActionsBaseURLField:       "stale-base",
		ActionsCABundleField:      "stale-ca",
		ActionsTenantPathField:    "stale-tenant",
		"farosActionsFutureField": "stale-future",
		"farosActions":            "stale-prefix",
	}
	overlay := ActionsOverlay{
		ActionsIdentity: ActionsIdentity{
			TenantPath:  "root:faros:tenants:org:workspace",
			Org:         "org",
			Workspace:   "workspace",
			Project:     "demo",
			ProjectUID:  "uid-1",
			Environment: "development",
			Instance:    "demo-dev",
		},
		ExchangeURL: "https://actions.example/api/provider-actions/workload/exchange",
		BaseURL:     "https://actions.example/services/providers/app-studio",
		CABundle:    "public-ca",
	}

	got := ApplyActionsOverlay(input, overlay)
	if got["ordinary"] != "preserved" {
		t.Fatalf("ordinary value = %v, want preserved", got["ordinary"])
	}
	for key, want := range map[string]string{
		ActionsExchangeURLField: overlay.ExchangeURL,
		ActionsBaseURLField:     overlay.BaseURL,
		ActionsCABundleField:    overlay.CABundle,
		ActionsTenantPathField:  overlay.TenantPath,
		ActionsOrgField:         overlay.Org,
		ActionsWorkspaceField:   overlay.Workspace,
		ActionsProjectField:     overlay.Project,
		ActionsProjectUIDField:  overlay.ProjectUID,
		ActionsEnvironmentField: overlay.Environment,
		ActionsInstanceField:    overlay.Instance,
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %q", key, got[key], want)
		}
	}
	for _, key := range []string{"farosActionsFutureField", "farosActions"} {
		if _, found := got[key]; found {
			t.Errorf("reserved unknown field %s survived: %v", key, got[key])
		}
	}
	if input[ActionsExchangeURLField] != "stale-exchange" || input["ordinary"] != "preserved" {
		t.Fatalf("input was mutated: %v", input)
	}

	actionless := overlay
	actionless.ExchangeURL = ""
	actionless.BaseURL = ""
	actionless.CABundle = ""
	got = ApplyActionsOverlay(input, actionless)
	for key, want := range map[string]string{
		ActionsTenantPathField:  actionless.TenantPath,
		ActionsOrgField:         actionless.Org,
		ActionsWorkspaceField:   actionless.Workspace,
		ActionsProjectField:     actionless.Project,
		ActionsProjectUIDField:  actionless.ProjectUID,
		ActionsEnvironmentField: actionless.Environment,
		ActionsInstanceField:    actionless.Instance,
	} {
		if got[key] != want {
			t.Errorf("actionless %s = %v, want identity %q", key, got[key], want)
		}
	}
	for _, key := range []string{ActionsExchangeURLField, ActionsBaseURLField, ActionsCABundleField} {
		if _, found := got[key]; found {
			t.Errorf("actionless transport field %s survived: %v", key, got[key])
		}
	}
}

func TestMergeProviderSpecPreservesComputedFieldsAndClearsStaleActions(t *testing.T) {
	observed := map[string]any{
		"name": "demo-dev",
		"expose": map[string]any{
			"hostnamePrefix": "demo",
			"fqdn":           "demo-tenant.apps.example",
			"providerFlag":   "keep",
		},
		"credentialsSecretName": "demo-dev-credentials",
		"providerComputed":      "keep-top-level",
		ActionsExchangeURLField: "https://stale.example/exchange",
		"farosActionsFuture":    "stale-future",
	}
	desired := map[string]any{
		"expose": map[string]any{
			"hostnamePrefix": "new-prefix",
		},
		"providerInput": "explicit",
	}
	got := MergeProviderSpec(observed, desired)

	expose, ok := got["expose"].(map[string]any)
	if !ok {
		t.Fatalf("expose = %#v, want map", got["expose"])
	}
	for key, want := range map[string]any{
		"hostnamePrefix": "new-prefix",
		"fqdn":           "demo-tenant.apps.example",
		"providerFlag":   "keep",
	} {
		if expose[key] != want {
			t.Errorf("expose[%q] = %#v, want %#v", key, expose[key], want)
		}
	}
	for key, want := range map[string]any{
		"credentialsSecretName": "demo-dev-credentials",
		"providerComputed":      "keep-top-level",
		"providerInput":         "explicit",
	} {
		if got[key] != want {
			t.Errorf("spec[%q] = %#v, want %#v", key, got[key], want)
		}
	}
	for key := range observed {
		if strings.HasPrefix(key, ActionsFieldPrefix) {
			if _, found := got[key]; found {
				t.Errorf("stale reserved field %q survived merge: %#v", key, got[key])
			}
		}
	}
	if observed[ActionsExchangeURLField] != "https://stale.example/exchange" {
		t.Fatal("MergeProviderSpec mutated observed input")
	}

	// A subsequent explicit update changes only the requested nested input and
	// remains stable once the provider has accepted it.
	updated := MergeProviderSpec(got, map[string]any{"expose": map[string]any{"hostnamePrefix": "final"}})
	if updated["expose"].(map[string]any)["fqdn"] != "demo-tenant.apps.example" {
		t.Fatalf("explicit update dropped computed fqdn: %#v", updated["expose"])
	}
}

func TestPhase(t *testing.T) {
	cases := []struct {
		name   string
		status map[string]any
		want   string
	}{
		{"explicit phase", map[string]any{"phase": "Ready"}, "Ready"},
		{"ready condition", map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}, "Ready"},
		{"unready condition", map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False"}}}, "Pending"},
		{"active state", map[string]any{"state": "ACTIVE"}, "Ready"},
		{"nothing", map[string]any{}, ""},
	}
	for _, tc := range cases {
		obj := &unstructured.Unstructured{Object: map[string]any{"status": tc.status}}
		if got := Phase(obj); got != tc.want {
			t.Errorf("%s: Phase = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStatusFromObjectNotFoundIsPending(t *testing.T) {
	st := StatusFromObject(testBinding(), nil)
	if st.Phase != "Pending" || st.Name != "development" || st.Provider != "infrastructure" {
		t.Fatalf("status = %+v", st)
	}
}

func TestMergeEnvironmentStatusesPreservesUnmanaged(t *testing.T) {
	existing := []aiv1alpha1.ProjectEnvironmentStatus{
		{Name: "development", Phase: "Pending"},
		{Name: "other", Phase: "Ready"},
	}
	live := []aiv1alpha1.ProjectEnvironmentStatus{
		{Name: "development", Phase: "Ready"},
		{Name: "production", Phase: "Pending"},
	}
	out := MergeEnvironmentStatuses(existing, live)
	if len(out) != 3 {
		t.Fatalf("merged = %+v, want 3 entries", out)
	}
	if out[0].Name != "development" || out[0].Phase != "Ready" {
		t.Fatalf("development not overlaid: %+v", out[0])
	}
	if out[1].Name != "other" || out[1].Phase != "Ready" {
		t.Fatalf("unmanaged entry not preserved in order: %+v", out[1])
	}
}
