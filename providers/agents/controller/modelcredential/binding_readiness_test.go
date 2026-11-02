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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
	agentsscheme "github.com/railgrid/provider-agents/scheme"
)

func readyConnection(name string, generation int64) *agentsv1alpha1.ModelProviderConnection {
	return &agentsv1alpha1.ModelProviderConnection{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: generation},
		Spec: agentsv1alpha1.ModelProviderConnectionSpec{
			Provider: agentsv1alpha1.ModelProviderOpenAICompatible,
			BaseURL:  "https://api.example.test/v1",
			SecretRef: agentsv1alpha1.ModelCredentialSecretRef{
				Name: testSecret,
			},
		},
		Status: agentsv1alpha1.ModelCredentialStatus{
			ObservedGeneration: generation,
			Conditions: []metav1.Condition{{
				Type:               agentsv1alpha1.ConditionReady,
				Status:             metav1.ConditionTrue,
				Reason:             agentsv1alpha1.ReasonReady,
				ObservedGeneration: generation,
				LastTransitionTime: metav1.NewTime(now),
			}},
		},
	}
}

func modelBinding(name, connectionName, model string) *agentsv1alpha1.ModelCredential {
	return &agentsv1alpha1.ModelCredential{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 7},
		Spec: agentsv1alpha1.ModelCredentialSpec{
			ConnectionRef: connectionName,
			Model:         model,
		},
	}
}

func newBindingReadinessReconciler(t *testing.T, objects ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.ModelCredential{}, &agentsv1alpha1.ModelProviderConnection{}).
		WithObjects(objects...).
		Build()
	return &Reconciler{Manager: fakeManager{c: c}, Now: func() time.Time { return now }}, c
}

func reconcileBinding(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(t.Context(), mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: name}},
		ClusterName: multicluster.ClusterName(testCluster),
	})
	if err != nil {
		t.Fatalf("reconcile binding: %v", err)
	}
}

func readBinding(t *testing.T, c client.Client, name string) *agentsv1alpha1.ModelCredential {
	t.Helper()
	var got agentsv1alpha1.ModelCredential
	if err := c.Get(t.Context(), types.NamespacedName{Name: name}, &got); err != nil {
		t.Fatalf("reading binding: %v", err)
	}
	return &got
}

func TestReconcileModelBindingReadiness(t *testing.T) {
	tests := []struct {
		name            string
		connection      *agentsv1alpha1.ModelProviderConnection
		model           string
		wantConnection  metav1.ConditionStatus
		wantReason      string
		wantModel       metav1.ConditionStatus
		wantReady       metav1.ConditionStatus
		wantMessagePart string
	}{
		{
			name:           "ready connection and configured model",
			connection:     readyConnection("shared-openai", 3),
			model:          "gpt-5",
			wantConnection: metav1.ConditionTrue,
			wantModel:      metav1.ConditionTrue,
			wantReady:      metav1.ConditionTrue,
		},
		{
			name:            "missing connection",
			model:           "gpt-5",
			wantConnection:  metav1.ConditionFalse,
			wantReason:      reasonConnectionUnavailable,
			wantModel:       metav1.ConditionTrue,
			wantReady:       metav1.ConditionFalse,
			wantMessagePart: "does not exist",
		},
		{
			name: "deleting connection",
			connection: func() *agentsv1alpha1.ModelProviderConnection {
				connection := readyConnection("shared-openai", 3)
				connection.DeletionTimestamp = &metav1.Time{Time: now}
				connection.Finalizers = []string{"hold-for-test"}
				return connection
			}(),
			model:           "gpt-5",
			wantConnection:  metav1.ConditionFalse,
			wantReason:      reasonConnectionUnavailable,
			wantModel:       metav1.ConditionTrue,
			wantReady:       metav1.ConditionFalse,
			wantMessagePart: "deleting",
		},
		{
			name: "stale connection generation",
			connection: func() *agentsv1alpha1.ModelProviderConnection {
				connection := readyConnection("shared-openai", 3)
				connection.Generation = 4
				return connection
			}(),
			model:           "gpt-5",
			wantConnection:  metav1.ConditionFalse,
			wantReason:      reasonConnectionNotReady,
			wantModel:       metav1.ConditionTrue,
			wantReady:       metav1.ConditionFalse,
			wantMessagePart: "stale",
		},
		{
			name:            "model is not configured",
			connection:      readyConnection("shared-openai", 3),
			wantConnection:  metav1.ConditionTrue,
			wantModel:       metav1.ConditionFalse,
			wantReady:       metav1.ConditionFalse,
			wantMessagePart: "spec.model",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding := modelBinding("binding", "shared-openai", test.model)
			objects := []client.Object{binding}
			if test.connection != nil {
				objects = append(objects, test.connection)
			}
			r, c := newBindingReadinessReconciler(t, objects...)
			reconcileBinding(t, r, binding.Name)

			got := readBinding(t, c, binding.Name)
			connectionCondition := condition(t, got, conditionConnectionReady)
			modelCondition := condition(t, got, conditionModelConfigured)
			readyCondition := condition(t, got, agentsv1alpha1.ConditionReady)
			if connectionCondition.Status != test.wantConnection || (test.wantReason != "" && connectionCondition.Reason != test.wantReason) {
				t.Fatalf("ConnectionReady = %s/%s, want %s/%s", connectionCondition.Status, connectionCondition.Reason, test.wantConnection, test.wantReason)
			}
			if modelCondition.Status != test.wantModel {
				t.Fatalf("ModelConfigured = %s, want %s", modelCondition.Status, test.wantModel)
			}
			if readyCondition.Status != test.wantReady {
				t.Fatalf("Ready = %s (%s): %s, want %s", readyCondition.Status, readyCondition.Reason, readyCondition.Message, test.wantReady)
			}
			for _, cond := range []metav1.Condition{connectionCondition, modelCondition, readyCondition} {
				if cond.ObservedGeneration != binding.Generation {
					t.Errorf("%s observedGeneration = %d, want %d", cond.Type, cond.ObservedGeneration, binding.Generation)
				}
			}
			if got.Status.ObservedGeneration != binding.Generation {
				t.Errorf("status.observedGeneration = %d, want %d", got.Status.ObservedGeneration, binding.Generation)
			}
			if test.wantMessagePart != "" && !strings.Contains(readyCondition.Message, test.wantMessagePart) {
				t.Errorf("Ready message %q does not contain %q", readyCondition.Message, test.wantMessagePart)
			}
		})
	}
}

func TestModelBindingEventMappings(t *testing.T) {
	connection := readyConnection("shared-openai", 3)
	connection.UID = types.UID("connection-uid")
	connection.Finalizers = []string{agentsv1alpha1.ModelProviderConnectionFinalizer}
	matchingBinding := modelBinding("matching", connection.Name, "gpt-5")
	otherBinding := modelBinding("other", "another-connection", "gpt-5")
	sec := secret(true, map[string][]byte{"apiKey": []byte("first-key")})
	sec.Name = "tenant-managed-shared-key"
	connection.Spec.SecretRef.Name = sec.Name
	r, c := newBindingReadinessReconciler(t, connection, matchingBinding, otherBinding, sec)
	cl := fakeCluster{c: c}

	connectionRequests := connectionRequestsForSecret(t.Context(), cl, sec)
	if len(connectionRequests) != 1 || connectionRequests[0].Name != connection.Name {
		t.Fatalf("Secret event requests = %v, want only connection %q", connectionRequests, connection.Name)
	}

	modelRequests := modelRequestsForConnection(t.Context(), cl, connection)
	if len(modelRequests) != 1 || modelRequests[0].Name != matchingBinding.Name {
		t.Fatalf("connection event requests = %v, want only binding %q", modelRequests, matchingBinding.Name)
	}

	// Treat the Secret event's mapped request as a requeue and verify the health
	// reconciler reads the newly rotated key without taking ownership of it.
	var observedKeys []string
	connectionReconciler := &ProviderConnectionReconciler{Reconciler: Reconciler{
		Manager: r.Manager,
		Now:     func() time.Time { return now },
		Probe: func(_ context.Context, profile llm.Profile) ([]string, time.Duration, error) {
			observedKeys = append(observedKeys, profile.APIKey)
			return []string{"gpt-5"}, time.Millisecond, nil
		},
	}}
	connectionRequest := func() mcreconcile.Request {
		return mcreconcile.Request{
			Request:     connectionRequests[0],
			ClusterName: multicluster.ClusterName(testCluster),
		}
	}
	if _, err := connectionReconciler.Reconcile(t.Context(), connectionRequest()); err != nil {
		t.Fatalf("initial connection health reconcile: %v", err)
	}
	var healthy agentsv1alpha1.ModelProviderConnection
	if err := c.Get(t.Context(), types.NamespacedName{Name: connection.Name}, &healthy); err != nil {
		t.Fatal(err)
	}
	if healthy.Status.ObservedGeneration != healthy.Generation || !meta.IsStatusConditionTrue(healthy.Status.Conditions, agentsv1alpha1.ConditionReady) {
		t.Fatalf("connection health was not published for the current generation: %+v", healthy.Status)
	}
	if len(healthy.Status.Models) != 1 || healthy.Status.Models[0] != "gpt-5" {
		t.Fatalf("connection models = %v, want discovered gpt-5", healthy.Status.Models)
	}
	var storedSecret corev1.Secret
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: sec.Namespace, Name: sec.Name}, &storedSecret); err != nil {
		t.Fatal(err)
	}
	storedSecret.Data["apiKey"] = []byte("rotated-key")
	if err := c.Update(t.Context(), &storedSecret); err != nil {
		t.Fatalf("rotating secret: %v", err)
	}
	rotatedRequests := connectionRequestsForSecret(t.Context(), cl, &storedSecret)
	if len(rotatedRequests) != 1 || rotatedRequests[0].Name != connection.Name {
		t.Fatalf("rotated Secret event requests = %v, want connection %q", rotatedRequests, connection.Name)
	}
	if _, err := connectionReconciler.Reconcile(t.Context(), connectionRequest()); err != nil {
		t.Fatalf("connection health reconcile after rotation: %v", err)
	}
	if len(observedKeys) != 2 || observedKeys[0] != "first-key" || observedKeys[1] != "rotated-key" {
		t.Fatalf("probe keys = %v, want initial and rotated Secret values", observedKeys)
	}
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: sec.Namespace, Name: sec.Name}, &storedSecret); err != nil {
		t.Fatal(err)
	}
	if len(storedSecret.OwnerReferences) != 0 {
		t.Fatalf("connection reconciler adopted the shared Secret: %+v", storedSecret.OwnerReferences)
	}
}
