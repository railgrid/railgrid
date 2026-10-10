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

package identity

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestDynamicOwnerProbeRejectsDeletingOwnerAndReturnsRevision(t *testing.T) {
	owner := Owner{
		Provider: "agents", Kind: "Agent", Group: "agents.railgrid.ai",
		Version: "v1alpha1", Resource: "agents", Name: "scheduler", UID: "agent-uid",
	}
	active := dynamicOwnerFixture(owner, "", 7, "opaque-active")
	probe := NewOwnerProbeForClient(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), active))
	found, revision, err := probe.Observe(context.Background(), "cluster", owner)
	if err != nil {
		t.Fatalf("Observe active owner: %v", err)
	}
	if !found || revision.UID != owner.UID || revision.Generation != 7 || revision.ResourceVersion != "opaque-active" {
		t.Fatalf("active observation = (found=%v, %#v), want owner UID, generation 7, opaque resourceVersion", found, revision)
	}

	deleting := dynamicOwnerFixture(owner, metav1.Now().Format(time.RFC3339), 8, "opaque-deleting")
	probe = NewOwnerProbeForClient(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), deleting))
	found, revision, err = probe.Observe(context.Background(), "cluster", owner)
	if err != nil {
		t.Fatalf("Observe deleting owner: %v", err)
	}
	if found {
		t.Fatalf("deleting owner was treated as live: %#v", revision)
	}
	legacyFound, _, err := probe.Exists(context.Background(), "cluster", owner)
	if err != nil || legacyFound {
		t.Fatalf("Exists deleting owner = (%v, %v), want (false, nil)", legacyFound, err)
	}
}

func dynamicOwnerFixture(owner Owner, deletionTimestamp string, generation int64, resourceVersion string) *unstructured.Unstructured {
	metadata := map[string]any{
		"name": owner.Name, "uid": owner.UID,
		"generation": generation, "resourceVersion": resourceVersion,
	}
	if deletionTimestamp != "" {
		metadata["deletionTimestamp"] = deletionTimestamp
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": owner.Group + "/" + owner.Version,
		"kind":       owner.Kind,
		"metadata":   metadata,
	}}
}
