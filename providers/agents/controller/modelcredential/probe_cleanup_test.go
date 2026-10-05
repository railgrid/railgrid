// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package modelcredential

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/railgrid/provider-sdk/claimscope"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/llm"
	agentsscheme "github.com/railgrid/provider-agents/scheme"
)

const (
	probeTestID     = "probe-123"
	probeTestName   = "model-check-probe-123"
	probeTestSecret = "model-check-secret-probe-123"
)

func probePair(created time.Time, expires, uid string) (*agentsv1alpha1.ModelCredential, *corev1.Secret) {
	labels := claimscope.OwnerLabels(agentsclient.ProviderName)
	labels[credentialProbeLabel] = "true"
	annotations := map[string]string{credentialProbeIDAnnotation: probeTestID}
	if expires != "" {
		annotations[credentialProbeExpiresAnnotation] = expires
	}
	cred := &agentsv1alpha1.ModelCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name: probeTestName, UID: types.UID(uid), Generation: 1,
			CreationTimestamp: metav1.NewTime(created), Labels: labels, Annotations: annotations,
		},
		Spec: agentsv1alpha1.ModelCredentialSpec{
			Provider:  llm.ProviderOpenAICompatible,
			BaseURL:   "https://fixture.invalid/v1",
			SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: probeTestSecret},
		},
	}
	secretLabels := claimscope.OwnerLabels(agentsclient.ProviderName)
	secretLabels[credentialProbeLabel] = "true"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: probeTestSecret, Namespace: llm.SecretNamespace, UID: "secret-uid",
			Labels:      secretLabels,
			Annotations: map[string]string{credentialProbeIDAnnotation: probeTestID},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: agentsv1alpha1.SchemeGroupVersion.String(),
				Kind:       "ModelCredential",
				Name:       cred.Name,
				UID:        cred.UID,
			}},
		},
		Data: map[string][]byte{"apiKey": []byte("test-only-key")},
	}
	return cred, secret
}

func probeTestClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.ModelCredential{}).
		WithObjects(objs...).
		Build()
}

func probeReconciler(c client.Client, at time.Time, probe Prober) *Reconciler {
	return &Reconciler{
		Manager: fakeManager{c: c},
		Probe:   probe,
		Now:     func() time.Time { return at },
	}
}

func reconcileProbe(t *testing.T, r *Reconciler, clusterName multicluster.ClusterName) (reconcile.Result, error) {
	t.Helper()
	return r.Reconcile(t.Context(), mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: probeTestName}},
		ClusterName: clusterName,
	})
}

func getProbeCredential(t *testing.T, c client.Client) (*agentsv1alpha1.ModelCredential, error) {
	t.Helper()
	var got agentsv1alpha1.ModelCredential
	err := c.Get(t.Context(), types.NamespacedName{Name: probeTestName}, &got)
	return &got, err
}

func getProbeSecret(t *testing.T, c client.Client) (*corev1.Secret, error) {
	t.Helper()
	var got corev1.Secret
	err := c.Get(t.Context(), types.NamespacedName{Namespace: llm.SecretNamespace, Name: probeTestSecret}, &got)
	return &got, err
}

func TestCredentialProbeDeadlineUsesServerMaximumAndOnlyShortens(t *testing.T) {
	created := now.Add(-3 * time.Minute)
	maximum := created.Add(credentialProbeMaxAge)
	for _, tc := range []struct {
		name    string
		expires string
		want    time.Time
	}{
		{name: "legacy without expiry annotation", want: maximum},
		{name: "malformed annotation falls back to cap", expires: "yesterday-ish", want: maximum},
		{name: "future annotation cannot extend maximum", expires: now.Add(time.Hour).Format(time.RFC3339), want: maximum},
		{name: "earlier annotation shortens maximum", expires: now.Add(time.Minute).Format(time.RFC3339), want: now.Add(time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cred, _ := probePair(created, tc.expires, "credential-uid")
			if got := credentialProbeDeadline(cred, now); !got.Equal(tc.want) {
				t.Fatalf("deadline = %s, want %s", got, tc.want)
			}
		})
	}
	cred, _ := probePair(created, "", "credential-uid")
	cred.CreationTimestamp = metav1.Time{}
	if got := credentialProbeDeadline(cred, now); !got.Equal(now) {
		t.Fatalf("missing server creation timestamp deadline = %s, want immediate cleanup at %s", got, now)
	}
}

func TestMarkedProbeWaitsForPersistedDeadlineAndRestartRecoversIt(t *testing.T) {
	created := now.Add(-3 * time.Minute)
	cred, secret := probePair(created, "", "credential-uid") // legacy probe, no expiry annotation
	c := probeTestClient(t, cred, secret)
	probeCalls := 0
	probe := func(context.Context, string, string) ([]string, time.Duration, error) {
		probeCalls++
		return nil, 0, errors.New("temporary credentials must skip readiness polling")
	}

	first := probeReconciler(c, now, probe)
	res, err := reconcileProbe(t, first, testCluster)
	if err != nil {
		t.Fatal(err)
	}
	if want := 7 * time.Minute; res.RequeueAfter != want {
		t.Fatalf("first requeue = %s, want %s to server-derived expiry", res.RequeueAfter, want)
	}
	if probeCalls != 0 {
		t.Fatalf("temporary credential was readiness-probed %d times", probeCalls)
	}
	if _, err := getProbeCredential(t, c); err != nil {
		t.Fatalf("credential deleted before expiry: %v", err)
	}

	// A fresh reconciler has no in-memory deadline state; it must derive the
	// same remaining lifetime from creationTimestamp after a controller restart.
	restarted := probeReconciler(c, now.Add(6*time.Minute), probe)
	res, err = reconcileProbe(t, restarted, testCluster)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Minute; res.RequeueAfter != want {
		t.Fatalf("restart requeue = %s, want %s", res.RequeueAfter, want)
	}

	restarted.Now = func() time.Time { return created.Add(credentialProbeMaxAge) }
	res, err = reconcileProbe(t, restarted, testCluster)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expired cleanup requeue = %s, want none", res.RequeueAfter)
	}
	if _, err := getProbeCredential(t, c); !apierrors.IsNotFound(err) {
		t.Fatalf("expired credential still exists, error = %v", err)
	}
	if _, err := getProbeSecret(t, c); !apierrors.IsNotFound(err) {
		t.Fatalf("expired Secret still exists, error = %v", err)
	}
	if probeCalls != 0 {
		t.Fatalf("temporary credential was readiness-probed %d times", probeCalls)
	}
}

func TestExpiredProbeDeletesOnlyItsOwnedSecretFirstWithUIDAndResourceVersion(t *testing.T) {
	cred, secret := probePair(now.Add(-11*time.Minute), "", "credential-uid")
	base := probeTestClient(t, cred, secret)
	var deleted []string
	var observedPreconditions []metav1.Preconditions
	c := interceptor.NewClient(base, interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			deleteOptions := (&client.DeleteOptions{}).ApplyOptions(opts)
			if deleteOptions.Preconditions == nil {
				t.Errorf("Delete %T %s lacked preconditions", obj, obj.GetName())
			} else {
				observedPreconditions = append(observedPreconditions, *deleteOptions.Preconditions.DeepCopy())
				if deleteOptions.Preconditions.UID == nil || *deleteOptions.Preconditions.UID != obj.GetUID() {
					t.Errorf("Delete %T %s did not use its observed UID", obj, obj.GetName())
				}
				if deleteOptions.Preconditions.ResourceVersion == nil || *deleteOptions.Preconditions.ResourceVersion != obj.GetResourceVersion() {
					t.Errorf("Delete %T %s did not use its observed resourceVersion", obj, obj.GetName())
				}
			}
			switch obj.(type) {
			case *corev1.Secret:
				deleted = append(deleted, "secret")
			case *agentsv1alpha1.ModelCredential:
				deleted = append(deleted, "credential")
			default:
				t.Errorf("unexpected delete %T", obj)
			}
			return cl.Delete(ctx, obj, opts...)
		},
	})
	probeCalls := 0
	res, err := reconcileProbe(t, probeReconciler(c, now, func(context.Context, string, string) ([]string, time.Duration, error) {
		probeCalls++
		return nil, 0, nil
	}), testCluster)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != 0 || probeCalls != 0 {
		t.Fatalf("result = %+v, probe calls = %d; expired probes must clean up without readiness calls", res, probeCalls)
	}
	if fmt.Sprint(deleted) != "[credential secret]" {
		t.Fatalf("delete request order = %v, want [credential secret] (finalizer preserves actual disappearance order)", deleted)
	}
	if len(observedPreconditions) != 2 {
		t.Fatalf("observed %d deletion precondition sets, want two", len(observedPreconditions))
	}
	if _, err := getProbeCredential(t, base); !apierrors.IsNotFound(err) {
		t.Fatalf("credential remains after cleanup: %v", err)
	}
	if _, err := getProbeSecret(t, base); !apierrors.IsNotFound(err) {
		t.Fatalf("Secret remains after cleanup: %v", err)
	}
}

func TestExpiredProbeWithMissingSecretCleansUpPartialCreate(t *testing.T) {
	cred, _ := probePair(now.Add(-11*time.Minute), "", "credential-uid")
	c := probeTestClient(t, cred)
	probeCalls := 0
	_, err := reconcileProbe(t, probeReconciler(c, now, func(context.Context, string, string) ([]string, time.Duration, error) {
		probeCalls++
		return nil, 0, nil
	}), testCluster)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := getProbeCredential(t, c); !apierrors.IsNotFound(err) {
		t.Fatalf("partial-create credential remains: %v", err)
	}
	if probeCalls != 0 {
		t.Fatalf("partial-create credential readiness-probed %d times", probeCalls)
	}
}

func TestExpiredProbeRefusesMismatchedSecretAndRecordsSafeStatus(t *testing.T) {
	cred, secret := probePair(now.Add(-11*time.Minute), "", "credential-uid")
	secret.Annotations[credentialProbeIDAnnotation] = "other-probe"
	c := probeTestClient(t, cred, secret)
	probeCalls := 0
	_, err := reconcileProbe(t, probeReconciler(c, now, func(context.Context, string, string) ([]string, time.Duration, error) {
		probeCalls++
		return nil, 0, nil
	}), testCluster)
	if err == nil {
		t.Fatal("mismatched Secret ownership must report a retryable cleanup error")
	}
	got, getErr := getProbeCredential(t, c)
	if getErr != nil {
		t.Fatalf("credential was removed after ownership mismatch: %v", getErr)
	}
	ready := condition(t, got, agentsv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != credentialProbeCleanupBlocked {
		t.Fatalf("Ready = %s/%s, want False/%s", ready.Status, ready.Reason, credentialProbeCleanupBlocked)
	}
	if ready.Message == "" || strings.Contains(ready.Message, "test-only-key") {
		t.Fatalf("cleanup status is missing safe recovery text: %q", ready.Message)
	}
	if _, err := getProbeSecret(t, c); err != nil {
		t.Fatalf("mismatched Secret was removed: %v", err)
	}
	if probeCalls != 0 {
		t.Fatalf("marked probe was readiness-probed %d times", probeCalls)
	}
}

func TestExpiredProbeDeleteConflictsKeepCredentialRetryable(t *testing.T) {
	for _, failKind := range []string{"secret", "credential"} {
		t.Run(failKind, func(t *testing.T) {
			cred, secret := probePair(now.Add(-11*time.Minute), "", "credential-uid")
			base := probeTestClient(t, cred, secret)
			failed := false
			c := interceptor.NewClient(base, interceptor.Funcs{
				Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					match := false
					switch obj.(type) {
					case *corev1.Secret:
						match = failKind == "secret"
					case *agentsv1alpha1.ModelCredential:
						match = failKind == "credential"
					}
					if match && !failed {
						failed = true
						return apierrors.NewConflict(schema.GroupResource{Resource: failKind}, obj.GetName(), errors.New("concurrent update"))
					}
					return cl.Delete(ctx, obj, opts...)
				},
			})
			r := probeReconciler(c, now, nil)
			if _, err := reconcileProbe(t, r, testCluster); err == nil {
				t.Fatal("first cleanup should return the API conflict for controller retry")
			}
			if _, err := getProbeCredential(t, base); err != nil {
				t.Fatalf("credential not retained as retry anchor: %v", err)
			}
			if failKind == "secret" || failKind == "credential" {
				if _, err := getProbeSecret(t, base); err != nil {
					t.Fatalf("Secret disappeared before cleanup delete succeeded: %v", err)
				}
			}

			if _, err := reconcileProbe(t, r, testCluster); err != nil {
				t.Fatalf("retry cleanup: %v", err)
			}
			if _, err := getProbeCredential(t, base); !apierrors.IsNotFound(err) {
				t.Fatalf("credential remained after retry: %v", err)
			}
			if _, err := getProbeSecret(t, base); !apierrors.IsNotFound(err) {
				t.Fatalf("Secret remained after retry: %v", err)
			}
		})
	}
}

func TestMarkerEditConflictBeforeDeleteLeavesSecretAndReleasesReservation(t *testing.T) {
	cred, secret := probePair(now.Add(-11*time.Minute), "", "credential-uid")
	base := probeTestClient(t, cred, secret)
	conflicted := false
	c := interceptor.NewClient(base, interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, isCredential := obj.(*agentsv1alpha1.ModelCredential); isCredential && !conflicted {
				conflicted = true
				var live agentsv1alpha1.ModelCredential
				if err := base.Get(ctx, types.NamespacedName{Name: probeTestName}, &live); err != nil {
					return err
				}
				delete(live.Labels, credentialProbeLabel)
				delete(live.Annotations, credentialProbeIDAnnotation)
				if err := base.Update(ctx, &live); err != nil {
					return err
				}
				return apierrors.NewConflict(schema.GroupResource{Group: agentsv1alpha1.SchemeGroupVersion.Group, Resource: "modelcredentials"}, obj.GetName(), errors.New("probe marker changed"))
			}
			return cl.Delete(ctx, obj, opts...)
		},
	})
	r := probeReconciler(c, now, nil)
	if _, err := reconcileProbe(t, r, testCluster); err == nil {
		t.Fatal("a marker edit racing the deletion CAS must return a retryable conflict")
	}
	if _, err := getProbeSecret(t, base); err != nil {
		t.Fatalf("Secret was touched before delete CAS succeeded: %v", err)
	}
	reserved, err := getProbeCredential(t, base)
	if err != nil {
		t.Fatal(err)
	}
	if !reserved.DeletionTimestamp.IsZero() {
		t.Fatal("conflicted delete unexpectedly became irreversible")
	}
	if !hasCredentialProbeCleanupFinalizer(reserved) {
		t.Fatal("reservation finalizer should remain until the next reconcile releases it")
	}

	if _, err := reconcileProbe(t, r, testCluster); err != nil {
		t.Fatalf("release stale reservation: %v", err)
	}
	updated, err := getProbeCredential(t, base)
	if err != nil {
		t.Fatal(err)
	}
	if hasCredentialProbeCleanupFinalizer(updated) {
		t.Fatal("cleanup reservation was not released after the marker changed")
	}
	if _, err := getProbeSecret(t, base); err != nil {
		t.Fatalf("Secret was touched after marker edit: %v", err)
	}
}

func TestFinalizingProbeUsesReservationAndCleansImmediatelyBeforeExpiry(t *testing.T) {
	cred, secret := probePair(now.Add(-time.Minute), "", "credential-uid")
	cred.Finalizers = []string{credentialProbeCleanupFinalizer}
	cred.Annotations[credentialProbeCleanupIDAnnotation] = probeTestID
	cred.Annotations[credentialProbeCleanupSecretAnnotation] = probeTestSecret
	cred.DeletionTimestamp = &metav1.Time{Time: now.Add(-time.Second)}
	// These fields may have changed after deletion began. The durable cleanup
	// reservation is the identity the controller must continue to honor.
	delete(cred.Labels, credentialProbeLabel)
	delete(cred.Annotations, credentialProbeIDAnnotation)
	cred.Spec.SecretRef.Name = "later-edit"
	c := probeTestClient(t, cred, secret)
	if _, err := reconcileProbe(t, probeReconciler(c, now, nil), testCluster); err != nil {
		t.Fatal(err)
	}
	if _, err := getProbeCredential(t, c); !apierrors.IsNotFound(err) {
		t.Fatalf("finalizing probe was retained until its expiry: %v", err)
	}
	if _, err := getProbeSecret(t, c); !apierrors.IsNotFound(err) {
		t.Fatalf("reserved Secret was not cleaned: %v", err)
	}
}

func TestFinalizerWaitsUntilSecretWithFinalizerIsActuallyGone(t *testing.T) {
	cred, secret := probePair(now.Add(-11*time.Minute), "", "credential-uid")
	secret.Finalizers = []string{"test.example/hold"}
	base := probeTestClient(t, cred, secret)
	r := probeReconciler(base, now, nil)
	if _, err := reconcileProbe(t, r, testCluster); err != nil {
		t.Fatal(err)
	}
	terminating, err := getProbeCredential(t, base)
	if err != nil {
		t.Fatalf("ModelCredential should anchor cleanup until Secret is gone: %v", err)
	}
	if terminating.DeletionTimestamp.IsZero() || !hasCredentialProbeCleanupFinalizer(terminating) {
		t.Fatalf("ModelCredential is not held finalizing: %+v", terminating.ObjectMeta)
	}
	terminatingSecret, err := getProbeSecret(t, base)
	if err != nil {
		t.Fatal(err)
	}
	if terminatingSecret.DeletionTimestamp.IsZero() {
		t.Fatal("Secret delete did not begin")
	}

	// The finalizer update emits a Secret event; manually reconcile that watch
	// delivery after the Secret is actually removed.
	terminatingSecret.Finalizers = nil
	if err := base.Update(t.Context(), terminatingSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := getProbeSecret(t, base); !apierrors.IsNotFound(err) {
		t.Fatalf("Secret remains after releasing its last finalizer: %v", err)
	}
	if _, err := reconcileProbe(t, r, testCluster); err != nil {
		t.Fatal(err)
	}
	if _, err := getProbeCredential(t, base); !apierrors.IsNotFound(err) {
		t.Fatalf("ModelCredential finalizer remained after Secret disappearance: %v", err)
	}
}

func TestProbeCleanupUsesOnlyTheRequestTenantClient(t *testing.T) {
	credentialA, secretA := probePair(now.Add(-11*time.Minute), "", "tenant-a-uid")
	credentialB, secretB := probePair(now.Add(-11*time.Minute), "", "tenant-b-uid")
	clientA := probeTestClient(t, credentialA, secretA)
	clientB := probeTestClient(t, credentialB, secretB)
	r := &Reconciler{
		Manager: fakeManager{clusters: map[multicluster.ClusterName]client.Client{
			"tenant-a": clientA,
			"tenant-b": clientB,
		}},
		Now: func() time.Time { return now },
	}
	if _, err := reconcileProbe(t, r, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := getProbeCredential(t, clientA); !apierrors.IsNotFound(err) {
		t.Fatalf("tenant-a probe was not cleaned: %v", err)
	}
	if _, err := getProbeSecret(t, clientA); !apierrors.IsNotFound(err) {
		t.Fatalf("tenant-a Secret was not cleaned: %v", err)
	}
	if _, err := getProbeCredential(t, clientB); err != nil {
		t.Fatalf("tenant-b credential was touched by tenant-a reconcile: %v", err)
	}
	if _, err := getProbeSecret(t, clientB); err != nil {
		t.Fatalf("tenant-b Secret was touched by tenant-a reconcile: %v", err)
	}
}

func TestUnmarkedOldCredentialStillUsesNormalReadinessProbe(t *testing.T) {
	cred := credential()
	cred.CreationTimestamp = metav1.NewTime(now.Add(-24 * time.Hour))
	cred.Labels = map[string]string{claimscope.OwnerLabel: agentsclient.ProviderName}
	c := probeTestClient(t, cred, secret(true, map[string][]byte{"apiKey": []byte("k")}))
	probed := false
	r := probeReconciler(c, now, func(context.Context, string, string) ([]string, time.Duration, error) {
		probed = true
		return []string{"gpt-4o"}, time.Millisecond, nil
	})
	res, err := reconcileProbeNamed(t, r, testCluster, testCred)
	if err != nil {
		t.Fatal(err)
	}
	if !probed || res.RequeueAfter != readyResync {
		t.Fatalf("unmarked credential probed=%t requeue=%s; expected normal readiness", probed, res.RequeueAfter)
	}
	if err := c.Get(t.Context(), types.NamespacedName{Name: testCred}, &agentsv1alpha1.ModelCredential{}); err != nil {
		t.Fatalf("unmarked saved credential was removed: %v", err)
	}
}

func reconcileProbeNamed(t *testing.T, r *Reconciler, clusterName multicluster.ClusterName, name string) (reconcile.Result, error) {
	t.Helper()
	return r.Reconcile(t.Context(), mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: name}},
		ClusterName: clusterName,
	})
}
