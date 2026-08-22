// Copyright 2026 The Faros Authors.
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

package instance

import (
	"context"
	"errors"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRemoveInstanceFinalizerRetriesConflictAndPreservesOtherFinalizers(t *testing.T) {
	const (
		name      = "sandbox"
		namespace = "tenant"
	)

	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": instanceGVK.GroupVersion().String(),
		"kind":       instanceGVK.Kind,
		"metadata": map[string]any{
			"name":       name,
			"namespace":  namespace,
			"finalizers": []any{"other-controller.example/finalizer", finalizer, "another-controller.example/finalizer"},
		},
	}}
	instance.SetGroupVersionKind(instanceGVK)

	scheme := runtime.NewScheme()
	client := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()
	conflicting := &conflictOnceClient{Client: client, remaining: 1}
	if err := removeInstanceFinalizer(context.Background(), conflicting, types.NamespacedName{Name: name, Namespace: namespace}); err != nil {
		t.Fatalf("removeInstanceFinalizer() error = %v", err)
	}
	if conflicting.updates != 2 {
		t.Fatalf("Update calls = %d, want one conflict followed by one successful update", conflicting.updates)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(instanceGVK)
	if err := client.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, got); err != nil {
		t.Fatalf("get updated instance: %v", err)
	}
	wantFinalizers := []string{"other-controller.example/finalizer", "another-controller.example/finalizer"}
	if !reflect.DeepEqual(got.GetFinalizers(), wantFinalizers) {
		t.Fatalf("finalizers = %#v, want %#v", got.GetFinalizers(), wantFinalizers)
	}
}

func TestRemoveInstanceFinalizerSucceedsWhenAlreadyAbsent(t *testing.T) {
	const name = "sandbox"
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": instanceGVK.GroupVersion().String(),
		"kind":       instanceGVK.Kind,
		"metadata": map[string]any{
			"name":       name,
			"finalizers": []any{"other-controller.example/finalizer"},
		},
	}}
	instance.SetGroupVersionKind(instanceGVK)

	client := clientfake.NewClientBuilder().WithObjects(instance).Build()
	if err := removeInstanceFinalizer(context.Background(), client, types.NamespacedName{Name: name}); err != nil {
		t.Fatalf("removeInstanceFinalizer() error = %v", err)
	}
}

func TestRemoveInstanceFinalizerSucceedsWhenObjectIsGone(t *testing.T) {
	client := clientfake.NewClientBuilder().Build()
	if err := removeInstanceFinalizer(context.Background(), client, types.NamespacedName{Name: "gone"}); err != nil {
		t.Fatalf("removeInstanceFinalizer() error = %v", err)
	}
}

type conflictOnceClient struct {
	ctrlclient.Client
	remaining int
	updates   int
}

func (c *conflictOnceClient) Update(ctx context.Context, obj ctrlclient.Object, opts ...ctrlclient.UpdateOption) error {
	c.updates++
	if c.remaining > 0 {
		c.remaining--
		return apierrors.NewConflict(instanceGVK.GroupVersion().WithResource("instances").GroupResource(), obj.GetName(), errors.New("injected update conflict"))
	}
	return c.Client.Update(ctx, obj, opts...)
}

var _ ctrlclient.Client = (*conflictOnceClient)(nil)
