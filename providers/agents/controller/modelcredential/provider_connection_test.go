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
	"fmt"
	"reflect"
	"slices"
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
	return newCredentialTestClient(objects, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*agentsv1alpha1.ModelProviderConnection); ok {
			obj.SetUID("connection-uid")
		}
		return c.Create(ctx, obj, opts...)
	}})
}

func newCredentialTestClient(objects []client.Object, hooks ...interceptor.Funcs) client.WithWatch {
	builder := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.ModelCredential{}, &agentsv1alpha1.ModelProviderConnection{})
	if len(hooks) > 0 {
		builder = builder.WithInterceptorFuncs(hooks[0])
	}
	return builder.WithObjects(objects...).Build()
}

func ownedConnectionSecret(owner *agentsv1alpha1.ModelProviderConnection, name, uid, resourceVersion string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       llm.SecretNamespace,
			UID:             types.UID(uid),
			ResourceVersion: resourceVersion,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: agentsv1alpha1.SchemeGroupVersion.String(),
				Kind:       "ModelProviderConnection",
				Name:       owner.Name,
				UID:        owner.UID,
			}},
		},
		Data: data,
	}
}

func rotatedConnectionFixture(t *testing.T, sharing string) (*agentsv1alpha1.ModelProviderConnection, *corev1.Secret, []client.Object) {
	t.Helper()
	connection := &agentsv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "account-a", UID: "connection-a", Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}},
		Spec: agentsv1alpha1.ModelProviderConnectionSpec{
			Provider:  "openai",
			BaseURL:   "https://example.test/v1",
			SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "current-key"},
		},
	}
	previous := ownedConnectionSecret(connection, "previous-key", "previous-key-uid", "1", map[string][]byte{"apiKey": []byte("previous")})
	current := secret(true, map[string][]byte{"apiKey": []byte("current")})
	current.Name = connection.Spec.SecretRef.Name
	objects := []client.Object{connection, previous, current}
	switch sharing {
	case "connection":
		objects = append(objects, &agentsv1alpha1.ModelProviderConnection{
			ObjectMeta: metav1.ObjectMeta{Name: "account-b", UID: "connection-b"},
			Spec:       agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: previous.Name}},
		})
	case "direct-model":
		objects = append(objects, &agentsv1alpha1.ModelCredential{
			ObjectMeta: metav1.ObjectMeta{Name: "direct-model"},
			Spec:       agentsv1alpha1.ModelCredentialSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: previous.Name}, Model: "one"},
		})
	case "unshared":
	default:
		t.Fatalf("unknown previous Secret sharing case %q", sharing)
	}
	return connection, previous, objects
}

func assertRotatedSecretState(t *testing.T, c client.Client, previous *corev1.Secret, connection *agentsv1alpha1.ModelProviderConnection, sharing string) *corev1.Secret {
	t.Helper()
	var got corev1.Secret
	err := c.Get(t.Context(), client.ObjectKeyFromObject(previous), &got)
	if sharing == "unshared" {
		if !apierrors.IsNotFound(err) {
			t.Fatalf("unshared previous Secret was not removed: %v", err)
		}
		return nil
	}
	if err != nil {
		t.Fatalf("previous Secret still referenced by %s was deleted: %v", sharing, err)
	}
	if string(got.Data["apiKey"]) != "previous" {
		t.Fatal("previous Secret data changed")
	}
	for _, owner := range got.OwnerReferences {
		if owner.UID == connection.UID && owner.Kind == "ModelProviderConnection" {
			t.Fatal("rotated connection still owns a shared previous Secret")
		}
	}
	return &got
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
					t.Fatal("connection-only legacy source was retained after its owned Secret transferred", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if got.Spec.ConnectionRef != legacy.Name || got.Spec.Model != modelID || got.Spec.SecretRef.Name != "" || got.Spec.BaseURL != "" {
					t.Fatalf("model binding was not preserved: %+v", got.Spec)
				}
				if hasCredentialSecretProtectionFinalizer(&got) {
					if _, err := (&Reconciler{Manager: fakeManager{c: c}}).Reconcile(t.Context(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: legacy.Name}}, ClusterName: testCluster}); err != nil {
						t.Fatal(err)
					}
					if err := c.Get(t.Context(), types.NamespacedName{Name: legacy.Name}, &got); err != nil {
						t.Fatal(err)
					}
					if hasCredentialSecretProtectionFinalizer(&got) {
						t.Fatal("converted binding retained the legacy Secret protection finalizer")
					}
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
	key := ownedConnectionSecret(connection, "shared", "key-uid", "1", map[string][]byte{"apiKey": []byte("keep-me")})
	model := &agentsv1alpha1.ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "model"}, Spec: agentsv1alpha1.ModelCredentialSpec{ConnectionRef: connection.Name, Model: "selected"}}
	c := migrationClient(connection, key, model)
	if err := c.Delete(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(connection), connection); err != nil {
		t.Fatal(err)
	}
	r := &ProviderConnectionReconciler{Reconciler: Reconciler{Now: func() time.Time { return now }}}
	if _, err := r.finalizeConnection(t.Context(), c, connection); err != nil {
		t.Fatal(err)
	}
	var preserved corev1.Secret
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &preserved); err != nil {
		t.Fatal("deletion removed an in-use key", err)
	}
	if err := c.Delete(t.Context(), model); err != nil {
		t.Fatal(err)
	}
	if _, err := r.finalizeConnection(t.Context(), c, connection); err != nil {
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
	if _, err := r.finalizeConnection(t.Context(), c, connection); err != nil {
		t.Fatal(err)
	}
	var stored corev1.Secret
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &stored); err != nil || len(stored.OwnerReferences) != 0 {
		t.Fatal("external Secret was changed", err)
	}
}

func TestConnectionAdoptsRandomKeyOnlyWithMatchingGeneratedID(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	connection := &agentsv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "account",
			UID:         "connection",
			Annotations: map[string]string{agentsv1alpha1.GeneratedKeySecretIDAnnotation: id},
		},
		Spec: agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: agentsv1alpha1.GeneratedModelCredentialSecretName(id)}},
	}
	key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: connection.Spec.SecretRef.Name, Namespace: llm.SecretNamespace,
		Annotations: map[string]string{agentsv1alpha1.GeneratedKeySecretIDAnnotation: id},
	}}
	c := migrationClient(connection, key)
	if err := adoptConnectionSecret(t.Context(), c, connection); err != nil {
		t.Fatal(err)
	}
	var got corev1.Secret
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &got); err != nil {
		t.Fatal(err)
	}
	if !ownsConnectionSecret(&got, connection) {
		t.Fatalf("matching generated ID did not establish exact owner reference: %+v", got.OwnerReferences)
	}
	got.OwnerReferences = nil
	got.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] = "fedcba9876543210fedcba9876543210"
	if err := c.Update(t.Context(), &got); err != nil {
		t.Fatal(err)
	}
	if err := adoptConnectionSecret(t.Context(), c, connection); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &got); err != nil {
		t.Fatal(err)
	}
	if ownsConnectionSecret(&got, connection) {
		t.Fatal("random Secret with a mismatched generated ID was adopted")
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

func TestActiveConnectionCleanupKeepsCurrentAndReservedKeys(t *testing.T) {
	connection := &agentsv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "account-a",
			UID:         "connection-a",
			Finalizers:  []string{agentsv1alpha1.ModelProviderConnectionFinalizer},
			Annotations: map[string]string{agentsv1alpha1.PendingKeySecretAnnotation: "reserved-key"},
		},
		Spec: agentsv1alpha1.ModelProviderConnectionSpec{
			Provider:  "openai",
			BaseURL:   "https://example.test/v1",
			SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "active-key"},
		},
	}
	key := func(name string) *corev1.Secret {
		return ownedConnectionSecret(connection, name, name+"-uid", "1", map[string][]byte{"apiKey": []byte("test-key")})
	}
	active, reserved, previous := key("active-key"), key("reserved-key"), key("previous-key")
	c := migrationClient(connection, active, reserved, previous)
	r := &ProviderConnectionReconciler{Reconciler: Reconciler{
		Manager: fakeManager{c: c},
		Now:     func() time.Time { return now },
		Probe:   func(context.Context, llm.Profile) ([]string, time.Duration, error) { return []string{"one"}, 0, nil },
	}}
	if _, err := r.Reconcile(t.Context(), mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: connection.Name}},
		ClusterName: testCluster,
	}); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []*corev1.Secret{active, reserved} {
		if err := c.Get(t.Context(), client.ObjectKeyFromObject(keep), &corev1.Secret{}); err != nil {
			t.Fatalf("active or reserved connection key %q was removed: %v", keep.Name, err)
		}
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(previous), &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("previous unshared connection key was not cleaned up: %v", err)
	}
}

func TestConnectionDeletionIgnoresPendingKeyReservation(t *testing.T) {
	connection := &agentsv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "account-a",
			UID:         "connection-a",
			Finalizers:  []string{agentsv1alpha1.ModelProviderConnectionFinalizer},
			Annotations: map[string]string{agentsv1alpha1.PendingKeySecretAnnotation: "reserved-key"},
		},
		Spec: agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "active-key"}},
	}
	active := ownedConnectionSecret(connection, "active-key", "active-uid", "1", map[string][]byte{"apiKey": []byte("active")})
	reserved := ownedConnectionSecret(connection, "reserved-key", "reserved-uid", "1", map[string][]byte{"apiKey": []byte("reserved")})
	c := migrationClient(connection, active, reserved)
	if err := c.Delete(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	var deleting agentsv1alpha1.ModelProviderConnection
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(connection), &deleting); err != nil {
		t.Fatal(err)
	}
	r := &ProviderConnectionReconciler{}
	if _, err := r.finalizeConnection(t.Context(), c, &deleting); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(connection), &deleting); !apierrors.IsNotFound(err) {
		t.Fatalf("connection finalizer did not complete deletion: %v", err)
	}
	for _, key := range []*corev1.Secret{active, reserved} {
		if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &corev1.Secret{}); !apierrors.IsNotFound(err) {
			t.Fatalf("owned connection key %q survived owner deletion: %v", key.Name, err)
		}
	}
}

type migrationConflictClient struct {
	client.Client
	failed bool
}

func (c *migrationConflictClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if credential, ok := obj.(*agentsv1alpha1.ModelCredential); ok && credential.Spec.ConnectionRef != "" && !c.failed {
		c.failed = true
		return apierrors.NewConflict(agentsv1alpha1.Resource("modelcredentials"), obj.GetName(), fmt.Errorf("injected concurrent edit"))
	}
	return c.Client.Update(ctx, obj, opts...)
}
func TestMigrationRecoversThroughReconcileAfterConflict(t *testing.T) {
	legacy := credential()
	legacy.UID = "legacy-review"
	legacy.Spec.Model = "existing-model"
	key := secret(true, map[string][]byte{"apiKey": []byte("test-only")})
	key.OwnerReferences = []metav1.OwnerReference{{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential", Name: legacy.Name, UID: legacy.UID}}
	backing := migrationClient(legacy, key)
	c := &migrationConflictClient{Client: backing}
	r := &ProviderConnectionReconciler{MigrateLegacy: true, Reconciler: Reconciler{Manager: fakeManager{c: c}, Now: func() time.Time { return now }, Probe: func(context.Context, llm.Profile) ([]string, time.Duration, error) {
		return []string{"existing-model"}, 0, nil
	}}}
	req := mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: legacy.Name}}, ClusterName: testCluster}
	if _, err := r.Reconcile(t.Context(), req); err == nil {
		t.Fatal("expected injected conflict")
	}
	for i := 0; i < 4; i++ {
		if _, err := r.Reconcile(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	}
	var stored agentsv1alpha1.ModelCredential
	if err := backing.Get(t.Context(), client.ObjectKeyFromObject(legacy), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.ConnectionRef == "" {
		t.Fatalf("migration never resumed after conflict: legacy remains direct, endpoint=%q", stored.Spec.BaseURL)
	}
}
func TestPartialConnectionOnlyMigrationCompletesThroughReconcile(t *testing.T) {
	legacy := credential()
	legacy.UID = "legacy-review"
	legacy.Spec.Model = ""
	connection := &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: legacy.Name, UID: "already-created", Annotations: map[string]string{migratedFrom: string(legacy.UID)}, Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}}, Spec: agentsv1alpha1.ModelProviderConnectionSpec{Provider: legacy.Spec.Provider, BaseURL: legacy.Spec.BaseURL, SecretRef: legacy.Spec.SecretRef}}
	key := secret(true, map[string][]byte{"apiKey": []byte("test-only")})
	key.UID = "migration-key"
	key.ResourceVersion = "1"
	c := migrationClient(legacy, connection, key)
	r := &ProviderConnectionReconciler{MigrateLegacy: true, Reconciler: Reconciler{Manager: fakeManager{c: c}, Now: func() time.Time { return now }, Probe: func(context.Context, llm.Profile) ([]string, time.Duration, error) { return nil, 0, nil }}}
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(t.Context(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: legacy.Name}}, ClusterName: testCluster}); err != nil {
			t.Fatal(err)
		}
	}
	var stored agentsv1alpha1.ModelCredential
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(legacy), &stored); !apierrors.IsNotFound(err) {
		t.Fatal("legacy connection-only source survives every reconciliation")
	}
}

func TestMigrationWaitsForReservedKeyThenTransfersGeneratedKeyIdentity(t *testing.T) {
	const secretID = "0123456789abcdef0123456789abcdef"
	legacy := credential()
	legacy.UID = "legacy-pending-key"
	legacy.Annotations = map[string]string{agentsv1alpha1.PendingKeySecretAnnotation: "railgrid-agents-model-" + secretID}
	legacy.Spec.SecretRef.Name = "railgrid-agents-model-old"
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:            agentsv1alpha1.GeneratedModelCredentialSecretName(secretID),
		Namespace:       llm.SecretNamespace,
		UID:             "generated-key",
		ResourceVersion: "1",
		Annotations:     map[string]string{agentsv1alpha1.GeneratedKeySecretIDAnnotation: secretID},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential", Name: legacy.Name, UID: legacy.UID,
		}},
	}, Data: map[string][]byte{"apiKey": []byte("generated-key")}}
	c := migrationClient(legacy, secret)
	if err := migrateCredential(t.Context(), c, legacy.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	var untouched agentsv1alpha1.ModelCredential
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(legacy), &untouched); err != nil {
		t.Fatal(err)
	}
	if untouched.Spec.ConnectionRef != "" {
		t.Fatal("migration converted a ModelCredential while its key Secret reservation was pending")
	}
	var absent agentsv1alpha1.ModelProviderConnection
	if err := c.Get(t.Context(), types.NamespacedName{Name: legacy.Name}, &absent); !apierrors.IsNotFound(err) {
		t.Fatalf("migration created a connection before key activation: %v", err)
	}

	delete(untouched.Annotations, agentsv1alpha1.PendingKeySecretAnnotation)
	untouched.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] = secretID
	untouched.Spec.SecretRef.Name = secret.Name
	if err := c.Update(t.Context(), &untouched); err != nil {
		t.Fatal(err)
	}
	if err := migrateCredential(t.Context(), c, &untouched); err != nil {
		t.Fatal(err)
	}
	var connection agentsv1alpha1.ModelProviderConnection
	if err := c.Get(t.Context(), types.NamespacedName{Name: legacy.Name}, &connection); err != nil {
		t.Fatal(err)
	}
	if connection.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] != secretID {
		t.Fatalf("migration did not copy generated key identity to connection: %v", connection.Annotations)
	}
	var transferred corev1.Secret
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(secret), &transferred); err != nil {
		t.Fatal(err)
	}
	if len(transferred.OwnerReferences) != 1 || transferred.OwnerReferences[0].Kind != "ModelProviderConnection" || transferred.OwnerReferences[0].Name != connection.Name || transferred.OwnerReferences[0].UID != connection.UID {
		t.Fatalf("generated key ownership was not transferred to the connection: %+v", transferred.OwnerReferences)
	}
}

func TestTerminatingPartialMigrationCanReleaseConnectionOnlySource(t *testing.T) {
	legacy := credential()
	legacy.UID = "legacy-partial"
	legacy.Spec.Model = ""
	connection := &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: legacy.Name, UID: "connection-partial", Annotations: map[string]string{migratedFrom: string(legacy.UID)}, Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}}, Spec: agentsv1alpha1.ModelProviderConnectionSpec{Provider: legacy.Spec.Provider, BaseURL: legacy.Spec.BaseURL, SecretRef: legacy.Spec.SecretRef}}
	key := secret(true, map[string][]byte{"apiKey": []byte("test-only")})
	key.UID = "partial-migration-key"
	key.ResourceVersion = "1"
	key.OwnerReferences = []metav1.OwnerReference{{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential", Name: legacy.Name, UID: legacy.UID}}
	c := migrationClient(legacy, connection, key)
	if err := c.Delete(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	r := &ProviderConnectionReconciler{MigrateLegacy: true, Reconciler: Reconciler{Manager: fakeManager{c: c}, Now: func() time.Time { return now }}}
	request := mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: legacy.Name}}, ClusterName: testCluster}
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	var got agentsv1alpha1.ModelProviderConnection
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(connection), &got); !apierrors.IsNotFound(err) {
		t.Fatal("terminating migration remained stuck", err)
	}
}

func TestConnectionDeletionPreservesSharedSecrets(t *testing.T) {
	for _, sharing := range []string{"connection", "other-owner", "direct-model"} {
		t.Run(sharing, func(t *testing.T) {
			connection := &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: "account-a", UID: "a", Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}}, Spec: agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "shared"}}}
			key := ownedConnectionSecret(connection, "shared", "key", "1", map[string][]byte{"apiKey": []byte("keep")})
			objects := []client.Object{connection, key}
			switch sharing {
			case "connection":
				objects = append(objects, &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: "account-b", UID: "b"}, Spec: agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: key.Name}}}, &agentsv1alpha1.ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "model-b"}, Spec: agentsv1alpha1.ModelCredentialSpec{ConnectionRef: "account-b", Model: "one"}})
			case "other-owner":
				key.OwnerReferences = append(key.OwnerReferences, metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "external", UID: "other"})
			case "direct-model":
				objects = append(objects, &agentsv1alpha1.ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: "legacy"}, Spec: agentsv1alpha1.ModelCredentialSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: key.Name}, Model: "one"}})
			}
			c := migrationClient(objects...)
			if err := c.Delete(t.Context(), connection); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(connection), connection); err != nil {
				t.Fatal(err)
			}
			r := &ProviderConnectionReconciler{}
			if _, err := r.finalizeConnection(t.Context(), c, connection); err != nil {
				t.Fatal(err)
			}
			var stored corev1.Secret
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &stored); err != nil {
				t.Fatal("shared key deleted", err)
			}
			if string(stored.Data["apiKey"]) != "keep" {
				t.Fatal("key changed")
			}
			for _, owner := range stored.OwnerReferences {
				if owner.UID == connection.UID {
					t.Fatal("departing connection still owns shared key")
				}
			}
			if sharing == "other-owner" && len(stored.OwnerReferences) != 1 {
				t.Fatal("external owner removed")
			}
		})
	}
}

func TestConnectionRotationCleansUpPreviousOwnedSecrets(t *testing.T) {
	for _, sharing := range []string{"connection", "direct-model", "unshared"} {
		t.Run(sharing, func(t *testing.T) {
			connection, previous, objects := rotatedConnectionFixture(t, sharing)
			c := migrationClient(objects...)
			r := &ProviderConnectionReconciler{Reconciler: Reconciler{
				Manager: fakeManager{c: c},
				Now:     func() time.Time { return now },
				Probe:   func(context.Context, llm.Profile) ([]string, time.Duration, error) { return []string{"one"}, 0, nil },
			}}
			request := mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: connection.Name}}, ClusterName: testCluster}
			if _, err := r.Reconcile(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			assertRotatedSecretState(t, c, previous, connection, sharing)
		})
	}
}

func TestConnectionDeletionAfterSecretRefRotationFindsPreviousOwnedSecrets(t *testing.T) {
	for _, sharing := range []string{"connection", "direct-model", "unshared"} {
		t.Run(sharing, func(t *testing.T) {
			connection, previous, objects := rotatedConnectionFixture(t, sharing)
			c := migrationClient(objects...)
			if err := c.Delete(t.Context(), connection); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(connection), connection); err != nil {
				t.Fatal(err)
			}
			r := &ProviderConnectionReconciler{}
			if _, err := r.finalizeConnection(t.Context(), c, connection); err != nil {
				t.Fatal(err)
			}

			got := assertRotatedSecretState(t, c, previous, connection, sharing)
			if sharing == "connection" && len(got.OwnerReferences) != 0 {
				t.Fatalf("unexpected owner references remain: %+v", got.OwnerReferences)
			}
		})
	}
}

func TestRotatedConnectionSecretDeleteUsesUIDAndResourceVersion(t *testing.T) {
	connection := &agentsv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "account-a", UID: "connection-a", Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}},
		Spec:       agentsv1alpha1.ModelProviderConnectionSpec{Provider: "openai", BaseURL: "https://example.test/v1", SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "current-key"}},
	}
	previous := ownedConnectionSecret(connection, "previous-key", "previous-key-uid", "secret-rv", nil)
	current := secret(true, map[string][]byte{"apiKey": []byte("current")})
	current.Name = connection.Spec.SecretRef.Name
	base := migrationClient(connection, previous, current)
	watchable, ok := base.(client.WithWatch)
	if !ok {
		t.Fatal("fake client does not support interceptor wrapping")
	}
	var observed metav1.Preconditions
	c := interceptor.NewClient(watchable, interceptor.Funcs{Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		if obj.GetName() == previous.Name {
			options := (&client.DeleteOptions{}).ApplyOptions(opts)
			if options.Preconditions != nil {
				observed = *options.Preconditions.DeepCopy()
			}
		}
		return cl.Delete(ctx, obj, opts...)
	}})
	r := &ProviderConnectionReconciler{Reconciler: Reconciler{
		Manager: fakeManager{c: c},
		Now:     func() time.Time { return now },
		Probe:   func(context.Context, llm.Profile) ([]string, time.Duration, error) { return []string{"one"}, 0, nil },
	}}
	request := mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: connection.Name}}, ClusterName: testCluster}
	if _, err := r.Reconcile(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if observed.UID == nil || *observed.UID != previous.UID || observed.ResourceVersion == nil || *observed.ResourceVersion != previous.ResourceVersion {
		t.Fatalf("rotated Secret delete preconditions = %+v, want UID %q and resourceVersion %q", observed, previous.UID, previous.ResourceVersion)
	}
}

func TestConnectionFinalizerRequeuesWhileRotatedSecretIsTerminating(t *testing.T) {
	connection := &agentsv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "account-a", UID: "connection-a", Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}},
		Spec:       agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "rotated-to"}},
	}
	previous := ownedConnectionSecret(connection, "rotated-from", "previous-key-uid", "1", nil)
	previous.Finalizers = []string{"test/hold-secret-deletion"}
	c := migrationClient(connection, previous)
	if err := c.Delete(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	r := &ProviderConnectionReconciler{Reconciler: Reconciler{Manager: fakeManager{c: c}, Now: func() time.Time { return now }}}
	request := mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: connection.Name}}, ClusterName: testCluster}
	result, err := r.Reconcile(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != backoffInitial {
		t.Fatalf("requeue after = %s, want %s while the rotated Secret terminates", result.RequeueAfter, backoffInitial)
	}
	var got agentsv1alpha1.ModelProviderConnection
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(connection), &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Finalizers, agentsv1alpha1.ModelProviderConnectionFinalizer) {
		t.Fatal("connection finalizer was removed before its Secret disappeared")
	}
}

func TestMigrationRetryPreservesConflictingEdits(t *testing.T) {
	for _, side := range []string{"source", "destination"} {
		t.Run(side, func(t *testing.T) {
			legacy := credential()
			legacy.UID = "source"
			legacy.Spec.Model = "existing-model"
			key := secret(true, map[string][]byte{"apiKey": []byte("unchanged")})
			backing := migrationClient(legacy, key)
			fault := &migrationConflictClient{Client: backing}
			if err := migrateCredential(t.Context(), fault, legacy); err == nil {
				t.Fatal("expected source update conflict")
			}
			var source agentsv1alpha1.ModelCredential
			var destination agentsv1alpha1.ModelProviderConnection
			if err := backing.Get(t.Context(), client.ObjectKeyFromObject(legacy), &source); err != nil {
				t.Fatal(err)
			}
			if err := backing.Get(t.Context(), client.ObjectKeyFromObject(legacy), &destination); err != nil {
				t.Fatal(err)
			}
			if side == "source" {
				source.Spec.BaseURL = "https://changed.example/v1"
				source.Spec.SecretRef.Name = "rotated-secret"
				if err := backing.Update(t.Context(), &source); err != nil {
					t.Fatal(err)
				}
			} else {
				destination.Spec.BaseURL = "https://changed.example/v1"
				destination.Spec.SecretRef.Name = "rotated-secret"
				if err := backing.Update(t.Context(), &destination); err != nil {
					t.Fatal(err)
				}
			}
			expectedSource := source.Spec
			expectedDestination := destination.Spec
			if err := migrateCredential(t.Context(), backing, &source); err == nil {
				t.Fatal("configuration drift was silently discarded")
			}
			if err := backing.Get(t.Context(), client.ObjectKeyFromObject(legacy), &source); err != nil {
				t.Fatal(err)
			}
			if err := backing.Get(t.Context(), client.ObjectKeyFromObject(legacy), &destination); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(source.Spec, expectedSource) || !reflect.DeepEqual(destination.Spec, expectedDestination) {
				t.Fatal("conflicting source or destination was overwritten")
			}
			destination.Spec = agentsv1alpha1.ModelProviderConnectionSpec{Provider: source.Spec.Provider, BaseURL: source.Spec.BaseURL, SecretRef: source.Spec.SecretRef, SecretKey: source.Spec.SecretKey, AnthropicWorkspaceID: source.Spec.AnthropicWorkspaceID}
			if err := backing.Update(t.Context(), &destination); err != nil {
				t.Fatal(err)
			}
			if err := migrateCredential(t.Context(), backing, &source); err != nil {
				t.Fatal(err)
			}
			if err := backing.Get(t.Context(), client.ObjectKeyFromObject(legacy), &source); err != nil {
				t.Fatal(err)
			}
			if source.Spec.ConnectionRef != destination.Name || source.Spec.Model != "existing-model" {
				t.Fatal("migration did not resume after conflict resolution")
			}
		})
	}
}

func TestMigrationComparisonAcceptsServerDefaults(t *testing.T) {
	base := agentsv1alpha1.ModelProviderConnectionSpec{BaseURL: "https://example.test/v1", SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "key"}}
	defaulted := base
	defaulted.Provider = agentsv1alpha1.ModelProviderOpenAICompatible
	defaulted.SecretKey = agentsv1alpha1.DefaultModelSecretKey
	if !sameConnectionConfiguration(base, defaulted) {
		t.Fatal("API defaults blocked migration retry")
	}
	defaulted.SecretKey = "different"
	if sameConnectionConfiguration(base, defaulted) {
		t.Fatal("key configuration change ignored")
	}
}

func TestLegacyMigrationRequiresExplicitEnable(t *testing.T) {
	legacy := credential()
	legacy.UID = "legacy-migration-gate"
	key := secret(true, map[string][]byte{"apiKey": []byte("retained-key")})
	c := migrationClient(legacy, key)
	r := &ProviderConnectionReconciler{Reconciler: Reconciler{Manager: fakeManager{c: c}, Now: func() time.Time { return now }}}
	req := mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: legacy.Name}}, ClusterName: testCluster}
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	var connection agentsv1alpha1.ModelProviderConnection
	if err := c.Get(t.Context(), types.NamespacedName{Name: legacy.Name}, &connection); !apierrors.IsNotFound(err) {
		t.Fatalf("disabled migration created connection: %v", err)
	}
	var stored agentsv1alpha1.ModelCredential
	if err := c.Get(t.Context(), types.NamespacedName{Name: legacy.Name}, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.ConnectionRef != "" || stored.UID != legacy.UID {
		t.Fatalf("disabled migration changed source: %+v", stored)
	}
	r.MigrateLegacy = true
	if _, err := r.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), types.NamespacedName{Name: legacy.Name}, &connection); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), types.NamespacedName{Name: legacy.Name}, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.ConnectionRef != connection.Name || stored.UID != legacy.UID {
		t.Fatalf("enabled migration did not retain source identity: %+v", stored)
	}
}
