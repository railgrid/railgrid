// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package modelcredential

import (
	"context"
	"errors"
	"testing"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func protectedLegacyCredential() *agentsv1alpha1.ModelCredential {
	cred := credential()
	cred.UID = "legacy-a"
	cred.ResourceVersion = "1"
	cred.Finalizers = []string{agentsv1alpha1.ModelCredentialSecretProtectionFinalizer}
	return cred
}

func legacyOwnedSecret(owner *agentsv1alpha1.ModelCredential) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            owner.Spec.SecretRef.Name,
			Namespace:       llm.SecretNamespace,
			UID:             "secret-uid",
			ResourceVersion: "1",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: agentsv1alpha1.SchemeGroupVersion.String(),
				Kind:       "ModelCredential",
				Name:       owner.Name,
				UID:        owner.UID,
			}},
		},
		Data: map[string][]byte{"apiKey": []byte("shared-test-key")},
	}
}

func protectionClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	return newCredentialTestClient(objects)
}

func reconcileCredential(t *testing.T, c client.Client, name string, readers ...client.Reader) (ctrl.Result, error) {
	t.Helper()
	var reader client.Reader
	if len(readers) > 0 {
		reader = readers[0]
	}
	r := &Reconciler{Manager: fakeManager{c: c, reader: reader}, Now: func() time.Time { return now }}
	return r.Reconcile(t.Context(), mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: name}},
		ClusterName: testCluster,
	})
}

func TestCredentialProtectionFinalizerPrecedesSecretAdoption(t *testing.T) {
	cred := credential()
	cred.UID = "new-direct-credential"
	key := secret(true, map[string][]byte{"apiKey": []byte("test-only")})
	var sawProtectedCredential bool
	base := newCredentialTestClient([]client.Object{cred, key}, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		if _, ok := obj.(*corev1.Secret); ok {
			var stored agentsv1alpha1.ModelCredential
			if err := c.Get(ctx, client.ObjectKeyFromObject(cred), &stored); err != nil || !hasCredentialSecretProtectionFinalizer(&stored) {
				t.Fatalf("Secret ownership changed before the credential protection finalizer persisted: %v", err)
			}
		}
		if err := c.Update(ctx, obj, opts...); err != nil {
			return err
		}
		if _, ok := obj.(*agentsv1alpha1.ModelCredential); ok && hasCredentialSecretProtectionFinalizer(obj.(*agentsv1alpha1.ModelCredential)) {
			var stored agentsv1alpha1.ModelCredential
			if err := c.Get(ctx, client.ObjectKeyFromObject(obj), &stored); err == nil && hasCredentialSecretProtectionFinalizer(&stored) {
				sawProtectedCredential = true
			}
		}
		return nil
	}})
	r := &Reconciler{
		Manager: fakeManager{c: base},
		Now:     func() time.Time { return now },
		Probe:   func(context.Context, llm.Profile) ([]string, time.Duration, error) { return []string{"one"}, 0, nil },
	}
	if _, err := r.Reconcile(t.Context(), mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: cred.Name}},
		ClusterName: testCluster,
	}); err != nil {
		t.Fatal(err)
	}
	if !sawProtectedCredential {
		t.Fatal("the reconciler did not persist the protection finalizer before adopting the Secret")
	}
}

func TestHarnessCredentialKeepsSecretProtectionLifecycle(t *testing.T) {
	cred := harnessCredential(agentsv1alpha1.ModelProviderClaudeCode)
	cred.UID = "harness-credential"
	key := secret(true, map[string][]byte{"oauthToken": []byte("test-only-token")})
	key.UID = "harness-secret"
	key.ResourceVersion = "1"
	c := protectionClient(t, cred, key)
	r := &Reconciler{
		Manager: fakeManager{c: c},
		Now:     func() time.Time { return now },
		Probe:   refuseProbe(t),
	}
	request := mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: cred.Name}}, ClusterName: testCluster}
	if _, err := r.Reconcile(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	var ready agentsv1alpha1.ModelCredential
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(cred), &ready); err != nil {
		t.Fatal(err)
	}
	if !hasCredentialSecretProtectionFinalizer(&ready) {
		t.Fatal("harness credential did not retain protection before owning its Secret")
	}
	if err := c.Delete(t.Context(), &ready); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(cred), &ready); !apierrors.IsNotFound(err) {
		t.Fatalf("harness credential did not finish deletion: %v", err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("unshared harness Secret remains after deletion: %v", err)
	}
}

func TestRawCredentialDeleteDetachesSharedSecretBeforeOwnerRemoval(t *testing.T) {
	for _, sharing := range []string{"connection", "direct-credential", "external-owner"} {
		t.Run(sharing, func(t *testing.T) {
			owner := protectedLegacyCredential()
			key := legacyOwnedSecret(owner)
			objects := []client.Object{owner, key}
			switch sharing {
			case "connection":
				objects = append(objects, &agentsv1alpha1.ModelProviderConnection{
					ObjectMeta: metav1.ObjectMeta{Name: "new-account", UID: "connection-b"},
					Spec:       agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: key.Name}},
				})
			case "direct-credential":
				objects = append(objects, &agentsv1alpha1.ModelCredential{
					ObjectMeta: metav1.ObjectMeta{Name: "new-model", UID: "direct-b"},
					Spec:       agentsv1alpha1.ModelCredentialSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: key.Name}},
				})
			case "external-owner":
				external := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "external-owner", Namespace: llm.SecretNamespace, UID: "external-owner-uid"}}
				objects = append(objects, external)
				key.OwnerReferences = append(key.OwnerReferences, metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: external.Name, UID: external.UID})
			}
			base := protectionClient(t, objects...)
			var detachedBeforeCredentialRelease bool
			c := interceptor.NewClient(base, interceptor.Funcs{Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if updated, ok := obj.(*agentsv1alpha1.ModelCredential); ok && !updated.DeletionTimestamp.IsZero() && !hasCredentialSecretProtectionFinalizer(updated) {
					var stored corev1.Secret
					if err := cl.Get(ctx, client.ObjectKeyFromObject(key), &stored); err != nil {
						t.Fatalf("Secret could not be checked before releasing the credential: %v", err)
					}
					if hasLegacyCredentialOwner(&stored, owner) {
						t.Fatal("credential protection finalizer was removed before detaching its Secret owner reference")
					}
					detachedBeforeCredentialRelease = true
				}
				return cl.Update(ctx, obj, opts...)
			}})

			// A raw Kubernetes DELETE sets deletionTimestamp and waits because
			// the credential already carries its durable protection finalizer.
			if err := c.Delete(t.Context(), owner); err != nil {
				t.Fatal(err)
			}
			var terminating agentsv1alpha1.ModelCredential
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(owner), &terminating); err != nil {
				t.Fatalf("raw DELETE bypassed the credential finalizer: %v", err)
			}
			if terminating.DeletionTimestamp.IsZero() {
				t.Fatal("raw DELETE did not enter finalization")
			}
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &corev1.Secret{}); err != nil {
				t.Fatalf("Secret disappeared before finalization: %v", err)
			}

			if _, err := reconcileCredential(t, c, owner.Name); err != nil {
				t.Fatal(err)
			}
			if !detachedBeforeCredentialRelease {
				t.Fatal("credential was removed without first detaching its Secret owner reference")
			}
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(owner), &terminating); !apierrors.IsNotFound(err) {
				t.Fatalf("credential finalizer did not complete deletion: %v", err)
			}
			var stored corev1.Secret
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &stored); err != nil {
				t.Fatalf("shared Secret was deleted with its legacy owner: %v", err)
			}
			if string(stored.Data["apiKey"]) != "shared-test-key" {
				t.Fatal("shared key data changed")
			}
			if hasLegacyCredentialOwner(&stored, owner) {
				t.Fatal("the removed credential is still an owner, so Kubernetes GC could delete the shared Secret")
			}
			if sharing == "external-owner" && len(stored.OwnerReferences) != 1 {
				t.Fatalf("external owner was not preserved: %+v", stored.OwnerReferences)
			}
		})
	}
}

func TestCredentialFinalizerUsesLiveAPIReaderForSharedConsumers(t *testing.T) {
	owner := protectedLegacyCredential()
	key := legacyOwnedSecret(owner)
	connection := &agentsv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "new-account", UID: "connection-b"},
		Spec:       agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: key.Name}},
	}
	// The cached client has not observed B yet, while the APIReader has. This
	// is the ordering that can happen when B's create is accepted just before a
	// raw DELETE of A and the source's delete event reaches another replica.
	cached := protectionClient(t, owner.DeepCopy(), key.DeepCopy())
	reader := protectionClient(t, owner.DeepCopy(), key.DeepCopy(), connection)
	if err := cached.Delete(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileCredential(t, cached, owner.Name, reader); err != nil {
		t.Fatal(err)
	}
	var preserved corev1.Secret
	if err := cached.Get(t.Context(), client.ObjectKeyFromObject(key), &preserved); err != nil {
		t.Fatalf("finalizer deleted a Secret referenced by a connection missing from the cache: %v", err)
	}
	if hasLegacyCredentialOwner(&preserved, owner) {
		t.Fatal("finalizer did not detach the legacy owner after finding the live connection")
	}
}

func TestRawCredentialDeleteRemovesOnlyUnsharedOwnedSecret(t *testing.T) {
	owner := protectedLegacyCredential()
	key := legacyOwnedSecret(owner)
	var observed metav1.Preconditions
	base := protectionClient(t, owner, key)
	c := interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		if obj.GetName() == key.Name && obj.GetNamespace() == key.Namespace {
			options := (&client.DeleteOptions{}).ApplyOptions(opts)
			if options.Preconditions != nil {
				observed = *options.Preconditions.DeepCopy()
			}
		}
		return cl.Delete(ctx, obj, opts...)
	}})
	if err := c.Delete(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileCredential(t, c, owner.Name); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("unshared owned Secret remains: %v", err)
	}
	if observed.UID == nil || *observed.UID != key.UID || observed.ResourceVersion == nil || *observed.ResourceVersion != key.ResourceVersion {
		t.Fatalf("Secret delete preconditions = %+v, want UID %q and resourceVersion %q", observed, key.UID, key.ResourceVersion)
	}
}

func TestActiveCredentialCleanupKeepsCurrentAndReservedKeys(t *testing.T) {
	owner := protectedLegacyCredential()
	owner.Spec.SecretRef.Name = "active-key"
	owner.Annotations = map[string]string{agentsv1alpha1.PendingKeySecretAnnotation: "reserved-key"}
	owned := func(name string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: llm.SecretNamespace, UID: types.UID(name + "-uid"), ResourceVersion: "1",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential", Name: owner.Name, UID: owner.UID,
			}},
		}, Data: map[string][]byte{"apiKey": []byte("test-only")}}
	}
	active, reserved, previous := owned("active-key"), owned("reserved-key"), owned("previous-key")
	c := protectionClient(t, owner, active, reserved, previous)
	r := &Reconciler{
		Manager: fakeManager{c: c},
		Now:     func() time.Time { return now },
		Probe:   func(context.Context, llm.Profile) ([]string, time.Duration, error) { return []string{"one"}, 0, nil },
	}
	if _, err := r.Reconcile(t.Context(), mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: owner.Name}},
		ClusterName: testCluster,
	}); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []*corev1.Secret{active, reserved} {
		if err := c.Get(t.Context(), client.ObjectKeyFromObject(keep), &corev1.Secret{}); err != nil {
			t.Fatalf("active or reserved key %q was removed: %v", keep.Name, err)
		}
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(previous), &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("previous unshared owned key was not cleaned up: %v", err)
	}
}

func TestCredentialDeletionIgnoresPendingKeyReservation(t *testing.T) {
	owner := protectedLegacyCredential()
	owner.Spec.SecretRef.Name = "active-key"
	owner.Annotations = map[string]string{agentsv1alpha1.PendingKeySecretAnnotation: "reserved-key"}
	active, reserved := legacyOwnedSecret(owner), legacyOwnedSecret(owner)
	active.Name, reserved.Name = "active-key", "reserved-key"
	c := protectionClient(t, owner, active, reserved)
	if err := c.Delete(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileCredential(t, c, owner.Name); err != nil {
		t.Fatal(err)
	}
	var remaining agentsv1alpha1.ModelCredential
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(owner), &remaining); !apierrors.IsNotFound(err) {
		t.Fatalf("credential finalizer did not complete deletion: %v", err)
	}
	for _, key := range []*corev1.Secret{active, reserved} {
		if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &corev1.Secret{}); !apierrors.IsNotFound(err) {
			t.Fatalf("owned key %q survived owner deletion: %v", key.Name, err)
		}
	}
}

func TestCredentialFinalizerWaitsForSecretDeletionAndRetries(t *testing.T) {
	owner := protectedLegacyCredential()
	key := legacyOwnedSecret(owner)
	key.Finalizers = []string{"test/hold-secret-deletion"}
	c := protectionClient(t, owner, key)
	if err := c.Delete(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	result, err := reconcileCredential(t, c, owner.Name)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != backoffInitial {
		t.Fatalf("requeue = %s, want %s while the Secret deletion is pending", result.RequeueAfter, backoffInitial)
	}
	var terminatingCredential agentsv1alpha1.ModelCredential
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(owner), &terminatingCredential); err != nil {
		t.Fatal(err)
	}
	if !hasCredentialSecretProtectionFinalizer(&terminatingCredential) {
		t.Fatal("credential protection finalizer was removed while its Secret still exists")
	}
	var terminatingSecret corev1.Secret
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &terminatingSecret); err != nil || terminatingSecret.DeletionTimestamp.IsZero() {
		t.Fatalf("Secret deletion did not start: %v", err)
	}
	terminatingSecret.Finalizers = nil
	if err := c.Update(t.Context(), &terminatingSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileCredential(t, c, owner.Name); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(owner), &terminatingCredential); !apierrors.IsNotFound(err) {
		t.Fatalf("credential was not removed after its Secret disappeared: %v", err)
	}
}

func TestCredentialFinalizerRetainsGuardOnSharedSecretUpdateConflict(t *testing.T) {
	owner := protectedLegacyCredential()
	key := legacyOwnedSecret(owner)
	connection := &agentsv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "new-account", UID: "connection-b"},
		Spec:       agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: key.Name}},
	}
	base := protectionClient(t, owner, key, connection)
	conflict := true
	c := interceptor.NewClient(base, interceptor.Funcs{Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		if _, ok := obj.(*corev1.Secret); ok && conflict {
			conflict = false
			return apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, obj.GetName(), errors.New("concurrent Secret replacement"))
		}
		return cl.Update(ctx, obj, opts...)
	}})
	if err := c.Delete(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileCredential(t, c, owner.Name); !apierrors.IsConflict(err) {
		t.Fatalf("first reconciliation error = %v, want conflict", err)
	}
	var stillTerminating agentsv1alpha1.ModelCredential
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(owner), &stillTerminating); err != nil {
		t.Fatal(err)
	}
	if !hasCredentialSecretProtectionFinalizer(&stillTerminating) {
		t.Fatal("credential guard was removed after a failed Secret update")
	}
	var stillShared corev1.Secret
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &stillShared); err != nil || !hasLegacyCredentialOwner(&stillShared, owner) {
		t.Fatalf("failed cleanup changed the Secret owner reference: %v", err)
	}
	if _, err := reconcileCredential(t, c, owner.Name); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &stillShared); err != nil {
		t.Fatal(err)
	}
	if hasLegacyCredentialOwner(&stillShared, owner) {
		t.Fatal("retry did not detach the legacy owner from the shared Secret")
	}
}

func TestCredentialCleanupMatchesOwnerUIDExactly(t *testing.T) {
	owner := protectedLegacyCredential()
	owner.UID = "replacement-uid"
	key := legacyOwnedSecret(owner)
	key.OwnerReferences[0].UID = "old-uid"
	key.OwnerReferences = append(key.OwnerReferences, metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "other", UID: "other-uid"})
	c := protectionClient(t, owner, key)
	if err := c.Delete(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileCredential(t, c, owner.Name); err != nil {
		t.Fatal(err)
	}
	var stored corev1.Secret
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(key), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.OwnerReferences) != 2 || stored.OwnerReferences[0].UID != "old-uid" || stored.OwnerReferences[1].UID != "other-uid" {
		t.Fatalf("cleanup changed references for a different credential UID: %+v", stored.OwnerReferences)
	}
}
