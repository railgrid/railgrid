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
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsscheme "github.com/railgrid/provider-agents/scheme"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func TestLegacySecretDeletionFailureRetainsParentForRetry(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "exclusive", true: "shared"}[shared], func(t *testing.T) {
			owner := credential()
			owner.UID = "legacy-uid"
			owner.Finalizers = []string{agentsv1alpha1.ModelCredentialSecretProtectionFinalizer}
			key := secret(true, map[string][]byte{"apiKey": []byte("test-only")})
			key.UID = "secret-uid"
			key.ResourceVersion = "1"
			objects := []client.Object{owner, key}
			if shared {
				objects = append(objects, &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: "other"}, Spec: agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: key.Name}}})
			}
			failures := 0
			c := fake.NewClientBuilder().WithScheme(agentsscheme.NewScheme()).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*corev1.Secret); ok {
					failures++
					if failures == 1 {
						return errors.New("transient Secret delete error")
					}
				}
				return c.Delete(ctx, obj, opts...)
			}}).Build()
			if err := c.Delete(t.Context(), owner); err != nil {
				t.Fatal(err)
			}
			r := &Reconciler{Manager: fakeManager{c: c}}
			req := mcreconcile.Request{Request: reconcile.Request{NamespacedName: client.ObjectKeyFromObject(owner)}, ClusterName: testCluster}
			_, err := r.Reconcile(t.Context(), req)
			if !shared {
				if err == nil {
					t.Fatal("expected Secret delete failure")
				}
				var pending agentsv1alpha1.ModelCredential
				if err := c.Get(t.Context(), client.ObjectKeyFromObject(owner), &pending); err != nil {
					t.Fatalf("parent lost before key deletion: %v", err)
				}
				if _, err := r.Reconcile(t.Context(), req); err != nil {
					t.Fatalf("retry: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err := c.Get(t.Context(), client.ObjectKeyFromObject(owner), &agentsv1alpha1.ModelCredential{}); !apierrors.IsNotFound(err) {
				t.Fatalf("finalized parent remains: %v", err)
			}
			err = c.Get(t.Context(), client.ObjectKeyFromObject(key), &corev1.Secret{})
			if shared && err != nil {
				t.Fatalf("shared legacy key deleted: %v", err)
			}
			if !shared && !apierrors.IsNotFound(err) {
				t.Fatalf("key not deleted on retry: %v", err)
			}
		})
	}
}
