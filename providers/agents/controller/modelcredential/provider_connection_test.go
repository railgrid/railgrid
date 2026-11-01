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

package modelcredential

import (
	"context"
	"reflect"
	"testing"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
	agentsscheme "github.com/railgrid/provider-agents/scheme"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func migrationClient(objects ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(agentsscheme.NewScheme()).WithStatusSubresource(&agentsv1alpha1.ModelCredential{}, &agentsv1alpha1.ModelProviderConnection{}).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*agentsv1alpha1.ModelProviderConnection); ok {
			obj.SetUID("connection-uid")
		}
		return c.Create(ctx, obj, opts...)
	}}).Build()
}
func TestMigrationPreservesModelNamesAndSecretData(t *testing.T) {
	for _, modelID := range []string{"model-one", ""} {
		t.Run(modelID, func(t *testing.T) {
			legacy := credential()
			legacy.UID = "legacy-uid"
			legacy.Spec.Model = modelID
			key := secret(true, map[string][]byte{"apiKey": []byte("unchanged-key")})
			key.OwnerReferences = []metav1.OwnerReference{{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential", Name: legacy.Name, UID: legacy.UID}}
			before := key.DeepCopy()
			binding := &agentsv1alpha1.ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "other-model"}, Spec: agentsv1alpha1.ModelCredentialSpec{ConnectionRef: legacy.Name, Model: "model-two"}}
			c := migrationClient(legacy, key, binding)
			if err := migrateCredential(t.Context(), c, legacy); err != nil {
				t.Fatal(err)
			}
			var connection agentsv1alpha1.ModelProviderConnection
			if err := c.Get(t.Context(), types.NamespacedName{Name: legacy.Name}, &connection); err != nil {
				t.Fatal(err)
			}
			if connection.Spec.BaseURL != legacy.Spec.BaseURL && modelID == "" {
				t.Fatal("endpoint changed")
			}
			var stored corev1.Secret
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &stored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Data, stored.Data) {
				t.Fatal("migration rewrote key data")
			}
			if len(stored.OwnerReferences) != 1 || stored.OwnerReferences[0].Kind != "ModelProviderConnection" || stored.OwnerReferences[0].UID != connection.UID {
				t.Fatalf("wrong key owner: %+v", stored.OwnerReferences)
			}
			var got agentsv1alpha1.ModelCredential
			err := c.Get(t.Context(), types.NamespacedName{Name: legacy.Name}, &got)
			if modelID == "" {
				if !apierrors.IsNotFound(err) {
					t.Fatal("connection-only legacy object was retained")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if got.Spec.ConnectionRef != legacy.Name || got.Spec.Model != modelID || got.Spec.SecretRef.Name != "" || got.Spec.BaseURL != "" {
					t.Fatalf("model binding was not preserved: %+v", got.Spec)
				}
			}
			var child agentsv1alpha1.ModelCredential
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(binding), &child); err != nil {
				t.Fatal(err)
			}
			resolved, err := agentsv1alpha1.ResolveModelConnection(&child, &connection)
			if err != nil || resolved.Spec.Model != "model-two" {
				t.Fatal("existing references did not survive migration", err)
			}
		})
	}
}
func TestMigrationCollisionPreservesExistingObjects(t *testing.T) {
	legacy := credential()
	legacy.UID = "legacy"
	connection := &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: legacy.Name, UID: "independent"}}
	c := migrationClient(legacy, connection)
	if err := migrateCredential(t.Context(), c, legacy); err == nil {
		t.Fatal("overwrote independent connection")
	}
	var got agentsv1alpha1.ModelCredential
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(legacy), &got); err != nil || got.Spec.ConnectionRef != "" {
		t.Fatal("legacy source was changed")
	}
}
func TestMigrationResumesAfterCreatingConnection(t *testing.T) {
	legacy := credential()
	legacy.UID = "legacy"
	connection := &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: legacy.Name, UID: "connection", Annotations: map[string]string{migratedFrom: string(legacy.UID)}}, Spec: agentsv1alpha1.ModelProviderConnectionSpec{BaseURL: legacy.Spec.BaseURL, SecretRef: legacy.Spec.SecretRef}}
	c := migrationClient(legacy, connection)
	if err := migrateCredential(t.Context(), c, legacy); err != nil {
		t.Fatal(err)
	}
	var got agentsv1alpha1.ModelCredential
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(legacy), &got); err != nil || got.Spec.ConnectionRef != connection.Name {
		t.Fatal("partial migration did not resume", err)
	}
}
func TestDirectDeletionProtectsSharedKeyUntilModelsAreRemoved(t *testing.T) {
	connection := &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: "account", UID: "account-uid", Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}}, Spec: agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "shared"}}}
	key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: llm.SecretNamespace, UID: "key-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection", Name: connection.Name, UID: connection.UID}}}, Data: map[string][]byte{"apiKey": []byte("keep-me")}}
	model := &agentsv1alpha1.ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "model"}, Spec: agentsv1alpha1.ModelCredentialSpec{ConnectionRef: connection.Name, Model: "selected"}}
	c := migrationClient(connection, key, model)
	if err := c.Delete(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(connection), connection); err != nil {
		t.Fatal(err)
	}
	r := &ProviderConnectionReconciler{Reconciler: Reconciler{Now: func() time.Time { return now }}}
	if err := r.finalizeConnection(t.Context(), c, connection); err != nil {
		t.Fatal(err)
	}
	var preserved corev1.Secret
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &preserved); err != nil {
		t.Fatal("deletion removed an in-use key", err)
	}
	if err := c.Delete(t.Context(), model); err != nil {
		t.Fatal(err)
	}
	if err := r.finalizeConnection(t.Context(), c, connection); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &preserved); !apierrors.IsNotFound(err) {
		t.Fatal("owned key was not cleaned up", err)
	}
}
func TestConnectionNeverAdoptsOrDeletesExternalSecret(t *testing.T) {
	connection := &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: "account", UID: "connection", Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}}, Spec: agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "tenant-supplied"}}}
	key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tenant-supplied", Namespace: llm.SecretNamespace}, Data: map[string][]byte{"apiKey": []byte("external")}}
	c := migrationClient(connection, key)
	if err := adoptConnectionSecret(t.Context(), c, connection); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(connection), connection); err != nil {
		t.Fatal(err)
	}
	r := &ProviderConnectionReconciler{}
	if err := r.finalizeConnection(t.Context(), c, connection); err != nil {
		t.Fatal(err)
	}
	var stored corev1.Secret
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &stored); err != nil || len(stored.OwnerReferences) != 0 {
		t.Fatal("external Secret was changed", err)
	}
}
func TestProviderConnectionOwnsDiscoveryAndReadiness(t *testing.T) {
	connection := &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: "account", UID: "account", Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}}, Spec: agentsv1alpha1.ModelProviderConnectionSpec{Provider: "openai", BaseURL: "https://provider.example/v1", SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: testSecret}}}
	key := secret(true, map[string][]byte{"apiKey": []byte("test-key")})
	c := migrationClient(connection, key)
	r := &ProviderConnectionReconciler{Reconciler: Reconciler{Manager: fakeManager{c: c}, Now: func() time.Time { return now }, Probe: func(_ context.Context, p llm.Profile) ([]string, time.Duration, error) {
		if p.APIKey != "test-key" || p.BaseURL != connection.Spec.BaseURL {
			t.Fatal("wrong connection profile")
		}
		return []string{"one", "two"}, 0, nil
	}}}
	if _, err := r.Reconcile(t.Context(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: connection.Name}}, ClusterName: testCluster}); err != nil {
		t.Fatal(err)
	}
	var got agentsv1alpha1.ModelProviderConnection
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(connection), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Models) != 2 {
		t.Fatalf("missing discovery: %+v", got.Status)
	}
}
