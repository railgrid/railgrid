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

package mcpserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	railgridv1alpha1 "github.com/railgrid/railgrid/apis/railgrid/v1alpha1"
)

func readyServer(now time.Time) *railgridv1alpha1.MCPServer {
	now = now.Truncate(time.Second)
	srv := newServer("default", false)
	srv.Generation = 1
	srv.Status.Phase = railgridv1alpha1.MCPServerPhaseReady
	refreshed := metav1.NewTime(now)
	srv.Status.ToolsRefreshedTime = &refreshed
	srv.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "EndpointReady", Message: "endpoint provisioned", ObservedGeneration: 1, LastTransitionTime: metav1.NewTime(now.Add(-time.Hour))}}
	return srv
}

func TestToolsRefreshDeadline(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		mutate  func(*railgridv1alpha1.MCPServer)
		elapsed time.Duration
		want    time.Duration
	}{
		{name: "restart recovers remaining deadline", elapsed: 20 * time.Second, want: 40 * time.Second},
		{name: "duplicate event does not extend deadline", elapsed: 59 * time.Second, want: time.Second},
		{name: "deadline reached", elapsed: time.Minute},
		{name: "overdue", elapsed: 2 * time.Minute},
		{name: "spec changed", mutate: func(s *railgridv1alpha1.MCPServer) { s.Generation++ }},
		{name: "initial discovery", mutate: func(s *railgridv1alpha1.MCPServer) { s.Status.ToolsRefreshedTime = nil }},
		{name: "recovering from error", mutate: func(s *railgridv1alpha1.MCPServer) { s.Status.Phase = railgridv1alpha1.MCPServerPhaseError }},
		{name: "not ready", mutate: func(s *railgridv1alpha1.MCPServer) { s.Status.Conditions[0].Status = metav1.ConditionFalse }},
		{name: "missing condition", mutate: func(s *railgridv1alpha1.MCPServer) { s.Status.Conditions = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := readyServer(now)
			if tc.mutate != nil {
				tc.mutate(srv)
			}
			if got := toolsRefreshDelay(srv, now.Add(tc.elapsed)); got != tc.want {
				t.Fatalf("delay = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStatusConvergesWithoutWrites(t *testing.T) {
	ctx := context.Background()
	srv := readyServer(time.Now())
	originalTransition := srv.Status.Conditions[0].LastTransitionTime
	scheme := runtime.NewScheme()
	if err := railgridv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	writes := 0
	fail := false
	updateErr := errors.New("status update failed")
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(srv).WithObjects(srv).WithInterceptorFuncs(interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subresource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			writes++
			if fail {
				return updateErr
			}
			return c.SubResource(subresource).Update(ctx, obj, opts...)
		},
	}).Build()
	if err := c.Get(ctx, client.ObjectKeyFromObject(srv), srv); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		before := srv.DeepCopy()
		setCondition(&srv.Status.Conditions, "Ready", metav1.ConditionTrue, "EndpointReady", "endpoint provisioned", srv.Generation)
		if err := updateStatusIfChanged(ctx, c, before, srv); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 0 {
		t.Fatalf("unchanged status produced %d writes", writes)
	}
	if !srv.Status.Conditions[0].LastTransitionTime.Equal(&originalTransition) {
		t.Fatal("unchanged readiness reset transition time")
	}
	before := srv.DeepCopy()
	srv.Status.Phase = railgridv1alpha1.MCPServerPhaseError
	setCondition(&srv.Status.Conditions, "Ready", metav1.ConditionFalse, "ProvisioningFailed", "failed", srv.Generation)
	if err := updateStatusIfChanged(ctx, c, before, srv); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("changed status produced %d writes", writes)
	}
	got := &railgridv1alpha1.MCPServer{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(srv), got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != railgridv1alpha1.MCPServerPhaseError {
		t.Fatal("changed phase was not persisted")
	}
	if !meta.FindStatusCondition(got.Status.Conditions, "Ready").LastTransitionTime.After(originalTransition.Time) {
		t.Fatal("real transition did not advance time")
	}
	fail = true
	before = srv.DeepCopy()
	srv.Status.URL = "https://changed.example"
	if err := updateStatusIfChanged(ctx, c, before, srv); !errors.Is(err, updateErr) {
		t.Fatalf("update error = %v", err)
	}
}

func TestConditionMetadataDoesNotResetTransition(t *testing.T) {
	srv := readyServer(time.Now())
	original := srv.Status.Conditions[0].LastTransitionTime
	setCondition(&srv.Status.Conditions, "Ready", metav1.ConditionTrue, "NewReason", "new detail", 2)
	cond := meta.FindStatusCondition(srv.Status.Conditions, "Ready")
	if cond.Reason != "NewReason" || cond.Message != "new detail" || cond.ObservedGeneration != 2 {
		t.Fatalf("condition metadata not updated: %+v", cond)
	}
	if !cond.LastTransitionTime.Equal(&original) {
		t.Fatal("metadata-only change reset transition time")
	}
}
