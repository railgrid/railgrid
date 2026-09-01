/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	"github.com/faroshq/provider-app-studio/store"
)

func binding(name string) aiv1alpha1.ProjectProviderBindingSpec {
	return aiv1alpha1.ProjectProviderBindingSpec{
		Name:     name,
		Provider: "infrastructure",
		Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			APIVersion: "infrastructure.faros.sh/v1alpha1",
			Kind:       "Instance",
			Resource:   "instances",
		},
		Values: runtime.RawExtension{Raw: []byte(`{}`)},
	}
}

func actionsDevelopmentBinding(values string) aiv1alpha1.ProjectProviderBindingSpec {
	b := binding(projectDevelopmentBindingName)
	b.Provider = projectDevelopmentProvider
	b.ResourceRef.Name = "demo-dev"
	b.Values = runtime.RawExtension{Raw: []byte(values)}
	return b
}

// providerBindings must select provider-resource bindings from EVERY
// environment — promotion appends an artifact-mode production binding and
// relies on this reconciler to provision it (the HTTP layer no longer does).
func TestProviderBindingsSpansAllEnvironmentModes(t *testing.T) {
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{
				{
					Name:     "development",
					Mode:     aiv1alpha1.ProjectEnvironmentModeLive,
					Bindings: []aiv1alpha1.ProjectProviderBindingSpec{binding("development")},
				},
				{
					Name:     "production",
					Mode:     aiv1alpha1.ProjectEnvironmentModeArtifact,
					Bindings: []aiv1alpha1.ProjectProviderBindingSpec{binding("production")},
				},
				{
					// No resourceRef → not lifecycled.
					Name: "empty",
					Mode: aiv1alpha1.ProjectEnvironmentModeLive,
					Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
						Name: "unbound", Provider: "infrastructure",
						Kind: aiv1alpha1.ProjectBindingKindProviderResource,
					}},
				},
			},
		},
	}
	got := providerBindings(p)
	if len(got) != 2 {
		t.Fatalf("providerBindings = %d envs, want 2 (development + production)", len(got))
	}
	if got[0].spec.Name != "development" || got[1].spec.Name != "production" {
		t.Fatalf("selected envs = %s, %s", got[0].spec.Name, got[1].spec.Name)
	}
}

func TestFinalizeProjectAttachmentStorageWithoutInstanceBinding(t *testing.T) {
	ctx := context.Background()
	now := metav1.Now()
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{
		Name:              "demo",
		UID:               "project-uid",
		Annotations:       map[string]string{bindings.OrgUUIDAnnotation: "org", bindings.WorkspaceUUIDAnnotation: "workspace"},
		Finalizers:        []string{store.AttachmentStorageFinalizer},
		DeletionTimestamp: &now,
	}}
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project).Build()
	attachments := store.NewMemoryStore()
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	digest := sha256.Sum256(data)
	expires := time.Now().UTC().Add(time.Hour)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "project-uid"}
	if _, err := attachments.CreateAttachment(ctx, scope, store.Attachment{
		ID: "att-1", ActorID: "alice", Filename: "screen.png", ContentType: "image/png", SizeBytes: int64(len(data)),
		SHA256: hex.EncodeToString(digest[:]), Draft: true, CreatedAt: time.Now().UTC(), ExpiresAt: &expires, Data: data,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Reconciler{Attachments: attachments}).finalize(ctx, client, project, "cluster-a"); err != nil {
		t.Fatal(err)
	}
	if controllerutil.ContainsFinalizer(project, store.AttachmentStorageFinalizer) {
		t.Fatal("attachment finalizer was not released after storage cleanup")
	}
	if _, err := attachments.GetAttachment(ctx, scope, "att-1"); !errors.Is(err, store.ErrAttachmentNotFound) {
		t.Fatalf("attachment after finalizer cleanup = %v", err)
	}
}

func TestFinalizeProjectAttachmentStorageReleasesUnscopedLegacyFinalizer(t *testing.T) {
	ctx := context.Background()
	now := metav1.Now()
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{
		Name:              "metadata-incomplete",
		UID:               "project-uid",
		Finalizers:        []string{store.AttachmentStorageFinalizer},
		DeletionTimestamp: &now,
	}}
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project).Build()
	if _, err := (&Reconciler{Attachments: store.NewMemoryStore()}).finalize(ctx, fakeClient, project, "cluster-a"); err != nil {
		t.Fatalf("finalize metadata-incomplete project: %v", err)
	}
	if controllerutil.ContainsFinalizer(project, store.AttachmentStorageFinalizer) {
		t.Fatal("unscoped legacy attachment finalizer was retained")
	}
	stored := &aiv1alpha1.Project{}
	if err := fakeClient.Get(ctx, client.ObjectKey{Name: project.Name}, stored); err != nil {
		if !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		return
	}
	if controllerutil.ContainsFinalizer(stored, store.AttachmentStorageFinalizer) {
		t.Fatal("unscoped legacy attachment finalizer persisted in the API")
	}
}

func TestAttachmentScopeForProjectRequiresTenantMetadata(t *testing.T) {
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid"}}
	if _, ok := attachmentScopeForProject(project); ok {
		t.Fatal("attachment scope resolved without tenant annotations")
	}
	project.Annotations = map[string]string{
		bindings.OrgUUIDAnnotation:       "org",
		bindings.WorkspaceUUIDAnnotation: "workspace",
	}
	scope, ok := attachmentScopeForProject(project)
	if !ok || scope.OrgUUID != "org" || scope.WorkspaceUUID != "workspace" || scope.ProjectName != project.Name || scope.ProjectUID != string(project.UID) {
		t.Fatalf("attachment scope = %#v, ok=%v", scope, ok)
	}
}

func TestReconcileDevelopmentPreviewPolicy(t *testing.T) {
	projectWith := func(mode aiv1alpha1.ProjectSharingMode, values string, observedURL string) *aiv1alpha1.Project {
		p := &aiv1alpha1.Project{
			Spec: aiv1alpha1.ProjectSpec{
				Sharing: aiv1alpha1.ProjectSharingSpec{Preview: aiv1alpha1.ProjectPreviewSharingPolicy{Mode: mode}},
				Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
					Name:     projectDevelopmentEnvironmentName,
					Bindings: []aiv1alpha1.ProjectProviderBindingSpec{actionsDevelopmentBinding(values)},
				}},
			},
		}
		if observedURL != "" {
			p.Status.Environments = []aiv1alpha1.ProjectEnvironmentStatus{{
				Name:     projectDevelopmentEnvironmentName,
				Bindings: []aiv1alpha1.ProjectProviderBindingStatus{{Name: projectDevelopmentBindingName, URL: observedURL}},
			}}
		}
		return p
	}
	for _, tc := range []struct {
		name       string
		project    *aiv1alpha1.Project
		wantMode   aiv1alpha1.ProjectSharingMode
		wantAccess string
		wantFound  bool
		wantChange bool
	}{
		{name: "new private binding", project: projectWith(aiv1alpha1.ProjectSharingModePrivate, `{"access":"public"}`, ""), wantMode: aiv1alpha1.ProjectSharingModePrivate, wantAccess: "private", wantFound: true, wantChange: true},
		{name: "explicit public", project: projectWith(aiv1alpha1.ProjectSharingModePublic, `{"access":"private"}`, ""), wantMode: aiv1alpha1.ProjectSharingModePublic, wantAccess: "public", wantFound: true, wantChange: true},
		{name: "legacy missing with URL", project: projectWith("", `{}`, "https://preview.example"), wantMode: aiv1alpha1.ProjectSharingModePrivate, wantAccess: "private", wantFound: true, wantChange: true},
		{name: "legacy shared", project: projectWith(aiv1alpha1.ProjectSharingModeShared, `{"access":"private"}`, ""), wantMode: aiv1alpha1.ProjectSharingModePrivate, wantAccess: "private", wantFound: true, wantChange: true},
		{name: "internal template", project: projectWith("", `{}`, ""), wantMode: aiv1alpha1.ProjectSharingModePrivate, wantFound: false, wantChange: true},
		{name: "already converged", project: projectWith(aiv1alpha1.ProjectSharingModePrivate, `{"access":"private"}`, ""), wantMode: aiv1alpha1.ProjectSharingModePrivate, wantAccess: "private", wantFound: true, wantChange: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := reconcileDevelopmentPreviewPolicy(tc.project)
			if err != nil {
				t.Fatal(err)
			}
			if changed != tc.wantChange {
				t.Fatalf("changed = %t, want %t", changed, tc.wantChange)
			}
			if got := tc.project.Spec.Sharing.Preview.Mode; got != tc.wantMode {
				t.Fatalf("mode = %q, want %q", got, tc.wantMode)
			}
			values, err := bindings.Values(tc.project.Spec.Environments[0].Bindings[0])
			if err != nil {
				t.Fatal(err)
			}
			got, found := values[bindings.PreviewAccessField]
			if found != tc.wantFound || (found && got != tc.wantAccess) {
				t.Fatalf("access = %v, found=%t; want %q, found=%t", got, found, tc.wantAccess, tc.wantFound)
			}
		})
	}
}

func TestEqualSpecAndMetaDetectsDrift(t *testing.T) {
	base := func() *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "infrastructure.faros.sh/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]any{
				"name":   "demo-dev",
				"labels": map[string]any{"app-studio.faros.sh/project": "demo"},
			},
			"spec": map[string]any{"webImage": "x"},
			// Instance-owned fields must not count as drift.
			"status": map[string]any{"phase": "Ready"},
		}}
	}

	same := base()
	if !equalSpecAndMeta(base(), same) {
		t.Fatal("identical objects reported as drifted")
	}

	specDrift := base()
	specDrift.Object["spec"] = map[string]any{"webImage": "y"}
	if equalSpecAndMeta(base(), specDrift) {
		t.Fatal("spec drift not detected")
	}

	labelDrift := base()
	labelDrift.SetLabels(map[string]string{"other": "label"})
	if equalSpecAndMeta(base(), labelDrift) {
		t.Fatal("label drift not detected")
	}

	statusOnly := base()
	statusOnly.Object["status"] = map[string]any{"phase": "Failed"}
	if !equalSpecAndMeta(base(), statusOnly) {
		t.Fatal("status-only difference must not count as drift")
	}
}

func TestEnsureInstanceDeepMergesComputedFieldsAndRetriesConflict(t *testing.T) {
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec: aiv1alpha1.ProjectSpec{
			Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"},
		},
	}
	b := binding(projectDevelopmentBindingName)
	b.ResourceRef.Name = "demo-dev"
	b.Values = runtime.RawExtension{Raw: []byte(`{
		"name":"demo-dev",
		"expose":{"hostnamePrefix":"desired"},
		"nested":{"input":"new"}
	}`)}
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": b.ResourceRef.APIVersion,
		"kind":       b.ResourceRef.Kind,
		"metadata": map[string]any{
			"name":   "demo-dev",
			"labels": map[string]any{bindings.ProjectLabel: "demo"},
		},
		"spec": map[string]any{
			"template": "application",
			"values": map[string]any{
				"name": "demo-dev",
				"expose": map[string]any{
					"hostnamePrefix": "old",
					"fqdn":           "provider-computed.example",
					"providerField":  "preserve",
				},
				"credentialsSecretName": "demo-dev-credentials",
				"nested": map[string]any{
					"input":    "old",
					"computed": "preserve-nested",
				},
				bindings.ActionsExchangeURLField: "https://stale.example/exchange",
				"farosActionsFutureField":        "stale",
			},
		},
	}}
	instance.SetGroupVersionKind(instance.GroupVersionKind())
	var updates int
	c := fake.NewClientBuilder().WithObjects(instance).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, underlying client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			updates++
			if updates == 1 {
				latest := &unstructured.Unstructured{}
				latest.SetGroupVersionKind(instance.GroupVersionKind())
				if err := underlying.Get(ctx, client.ObjectKey{Name: "demo-dev"}, latest); err != nil {
					return err
				}
				values, _, err := unstructured.NestedMap(latest.Object, "spec", "values")
				if err != nil {
					return err
				}
				values["expose"].(map[string]any)["fqdn"] = "fresh-provider-computed.example"
				if err := unstructured.SetNestedMap(latest.Object, values, "spec", "values"); err != nil {
					return err
				}
				if err := underlying.Update(ctx, latest); err != nil {
					return err
				}
				return apierrors.NewConflict(schemaGroupResourceForTest(), "demo-dev", fmt.Errorf("the object has been modified"))
			}
			return underlying.Update(ctx, obj)
		},
	}).Build()

	got, err := (&Reconciler{}).ensureInstance(context.Background(), c, p, b)
	if err != nil {
		t.Fatalf("ensureInstance after conflict: %v", err)
	}
	if got == nil {
		t.Fatal("ensureInstance returned nil object")
	}
	if updates != 2 {
		t.Fatalf("Update calls = %d, want bounded fresh retry (2)", updates)
	}

	stored := &unstructured.Unstructured{}
	stored.SetGroupVersionKind(instance.GroupVersionKind())
	if err := c.Get(context.Background(), client.ObjectKey{Name: "demo-dev"}, stored); err != nil {
		t.Fatalf("get converged instance: %v", err)
	}
	spec, _, err := unstructured.NestedMap(stored.Object, "spec", "values")
	if err != nil {
		t.Fatalf("get spec.values: %v", err)
	}
	if tmplName, _, _ := unstructured.NestedString(stored.Object, "spec", "template"); tmplName != "application" {
		t.Fatalf("spec.template = %q, want application", tmplName)
	}
	expose := spec["expose"].(map[string]any)
	if expose["hostnamePrefix"] != "desired" || expose["fqdn"] != "fresh-provider-computed.example" || expose["providerField"] != "preserve" {
		t.Fatalf("merged expose = %#v, want desired input + fresh/unknown provider fields", expose)
	}
	if spec["credentialsSecretName"] != "demo-dev-credentials" || spec["nested"].(map[string]any)["computed"] != "preserve-nested" {
		t.Fatalf("computed fields were lost: %#v", spec)
	}
	for key := range spec {
		if strings.HasPrefix(key, bindings.ActionsFieldPrefix) {
			t.Fatalf("stale Provider Actions field %q survived: %#v", key, spec[key])
		}
	}

	// A converged retry is a no-op. An explicit desired update still changes
	// only its requested field and leaves provider-computed fields intact.
	if _, err := (&Reconciler{}).ensureInstance(context.Background(), c, p, b); err != nil {
		t.Fatalf("second ensureInstance: %v", err)
	}
	if updates != 2 {
		t.Fatalf("converged ensure made an Update call: %d", updates)
	}
	b.Values = runtime.RawExtension{Raw: []byte(`{"name":"demo-dev","expose":{"hostnamePrefix":"final"},"nested":{"input":"explicit"}}`)}
	if _, err := (&Reconciler{}).ensureInstance(context.Background(), c, p, b); err != nil {
		t.Fatalf("explicit desired update: %v", err)
	}
	if updates != 3 {
		t.Fatalf("explicit desired update calls = %d, want 3", updates)
	}
}

// Keep the conflict test independent of provider-specific API discovery.
func schemaGroupResourceForTest() schema.GroupResource {
	return schema.GroupResource{Group: "infrastructure.faros.sh", Resource: "applications"}
}

func TestResolveLogicalClusterPathFromAppStudioBinding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cluster  string
		path     string
		multiple bool
		want     string
		wantErr  string
	}{
		{name: "success", cluster: "cluster-a", path: "root:faros:tenants:org:workspace", want: "root:faros:tenants:org:workspace"},
		{name: "cluster mismatch", cluster: "cluster-b", path: "root:faros:tenants:org:workspace", wantErr: "does not match request cluster"},
		{name: "missing path", cluster: "cluster-a", wantErr: "no kcp.io/path annotation"},
		{name: "multiple bindings", cluster: "cluster-a", path: "root:faros:tenants:org:workspace", multiple: true, wantErr: "multiple APIBindings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newBinding := func(name string) *apisv1alpha2.APIBinding {
				annotations := map[string]string{"kcp.io/cluster": tc.cluster}
				if tc.path != "" {
					annotations["kcp.io/path"] = tc.path
				}
				return &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations},
					Spec: apisv1alpha2.APIBindingSpec{Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{Name: appStudioAPIExportName, Path: appStudioAPIExportPath},
					}},
				}
			}
			scheme := runtime.NewScheme()
			if err := apisv1alpha2.AddToScheme(scheme); err != nil {
				t.Fatalf("add APIBinding scheme: %v", err)
			}
			objects := []client.Object{newBinding("app-studio")}
			if tc.multiple {
				objects = append(objects, newBinding("app-studio-duplicate"))
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

			path, err := resolveLogicalClusterPath(t.Context(), c, "cluster-a")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveLogicalClusterPath: %v", err)
			}
			if path != tc.want {
				t.Fatalf("path = %q, want %q", path, tc.want)
			}
		})
	}
}

func TestOverlayDevelopmentBindingUsesAuthoritativeConfigAndClearsRevokedTransport(t *testing.T) {
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid-1"},
		Spec: aiv1alpha1.ProjectSpec{Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
			Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
				Kind:           aiv1alpha1.ProjectBindingKindProviderReference,
				AllowedActions: []aiv1alpha1.ProjectProviderActionSpec{{Name: "query", Revoked: false}},
			}},
		}}},
	}
	binding := actionsDevelopmentBinding(`{
		"name":"demo-dev",
		"farosActionsExchangeURL":"https://stale.example/api/provider-actions/workload/exchange",
		"farosActionsBaseURL":"https://stale.example/services/providers/app-studio",
		"farosActionsCABundle":"stale-ca",
		"farosActionsTenantPath":"stale-tenant",
		"farosActionsProject":"stale-project"
	}`)
	r := &Reconciler{Actions: bindings.ActionsRuntimeConfig{
		ExternalURL: "https://actions.example",
		CABundle:    "authoritative-ca",
	}}

	updated, err := r.overlayDevelopmentBinding(p, binding, "root:faros:tenants:authoritative-org:authoritative-workspace")
	if err != nil {
		t.Fatalf("active overlay: %v", err)
	}
	var values map[string]any
	if err := json.Unmarshal(updated.Values.Raw, &values); err != nil {
		t.Fatalf("decode active overlay: %v", err)
	}
	for key, want := range map[string]string{
		bindings.ActionsExchangeURLField: "https://actions.example/api/provider-actions/workload/exchange",
		bindings.ActionsBaseURLField:     "https://actions.example/services/providers/app-studio",
		bindings.ActionsCABundleField:    "authoritative-ca",
		bindings.ActionsTenantPathField:  "root:faros:tenants:authoritative-org:authoritative-workspace",
		bindings.ActionsOrgField:         "authoritative-org",
		bindings.ActionsWorkspaceField:   "authoritative-workspace",
		bindings.ActionsProjectField:     "demo",
		bindings.ActionsProjectUIDField:  "uid-1",
		bindings.ActionsEnvironmentField: projectDevelopmentEnvironmentName,
		bindings.ActionsInstanceField:    "demo-dev",
	} {
		if values[key] != want {
			t.Errorf("active %s = %v, want %q", key, values[key], want)
		}
	}

	p.Spec.Environments[0].Bindings[0].AllowedActions[0].Revoked = true
	if bindings.HasActiveProviderActionGrant(p) {
		t.Fatal("revoked test grant is still active")
	}
	updated, err = r.overlayDevelopmentBinding(p, binding, "root:faros:tenants:authoritative-org:authoritative-workspace")
	if err != nil {
		t.Fatalf("revoked overlay: %v", err)
	}
	values = nil
	if err := json.Unmarshal(updated.Values.Raw, &values); err != nil {
		t.Fatalf("decode revoked overlay: %v", err)
	}
	for _, key := range []string{bindings.ActionsExchangeURLField, bindings.ActionsBaseURLField, bindings.ActionsCABundleField} {
		if _, found := values[key]; found {
			t.Errorf("revoked transport field %s survived: %v", key, values[key])
		}
	}
	if values[bindings.ActionsTenantPathField] != "root:faros:tenants:authoritative-org:authoritative-workspace" {
		t.Fatalf("revoked tenant path = %v, want authoritative identity", values[bindings.ActionsTenantPathField])
	}
}

func TestActionsTenantPathRejectsConflictingProjectAnnotations(t *testing.T) {
	for _, tc := range []struct {
		name        string
		annotations map[string]string
		want        string
	}{
		{
			name: "organization",
			annotations: map[string]string{
				bindings.OrgUUIDAnnotation:       "stale-org",
				bindings.WorkspaceUUIDAnnotation: "workspace",
			},
			want: "organization annotation",
		},
		{
			name: "workspace",
			annotations: map[string]string{
				bindings.OrgUUIDAnnotation:       "org",
				bindings.WorkspaceUUIDAnnotation: "stale-workspace",
			},
			want: "workspace annotation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &aiv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Annotations: tc.annotations},
				Spec: aiv1alpha1.ProjectSpec{Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
					Name:     projectDevelopmentEnvironmentName,
					Bindings: []aiv1alpha1.ProjectProviderBindingSpec{actionsDevelopmentBinding(`{}`)},
				}}},
			}
			r := &Reconciler{ResolveTenantPath: func(context.Context, client.Client, string) (string, error) {
				return "root:faros:tenants:org:workspace", nil
			}}
			_, err := r.actionsTenantPath(context.Background(), nil, p, providerBindings(p), "cluster-a")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("actionsTenantPath error = %v, want substring %q", err, tc.want)
			}
		})
	}
}
