/*
Copyright 2026 The Faros Authors.

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

package cmd

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	farosclient "github.com/faroshq/faros/pkg/client"
)

func TestGetEdgeByNameFindsMacOSServer(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("get", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		get := action.(clienttesting.GetAction)
		if get.GetResource() != farosclient.MacOSServerGVR {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: get.GetResource().Group, Resource: get.GetResource().Resource}, get.GetName())
		}
		return true, &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": farosclient.MacOSServerGVR.GroupVersion().String(),
			"kind":       "MacOSServer",
			"metadata":   map[string]interface{}{"name": get.GetName()},
		}}, nil
	})

	edge, gvr, err := getEdgeByName(context.Background(), dyn, "macbook-01")
	if err != nil {
		t.Fatalf("getEdgeByName: %v", err)
	}
	if gvr != farosclient.MacOSServerGVR {
		t.Fatalf("GVR = %v, want %v", gvr, farosclient.MacOSServerGVR)
	}
	if edge.GetKind() != "MacOSServer" || edge.GetName() != "macbook-01" {
		t.Fatalf("edge identity = %s/%s, want MacOSServer/macbook-01", edge.GetKind(), edge.GetName())
	}
}

func TestListAllEdgesDerivesKindFromMacOSServerGVR(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		farosclient.KubernetesClusterGVR: "KubernetesClusterList",
		farosclient.LinuxServerGVR:       "LinuxServerList",
		farosclient.MacOSServerGVR:       "MacOSServerList",
	})
	dyn.PrependReactor("list", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		list := action.(clienttesting.ListAction)
		if list.GetResource() != farosclient.MacOSServerGVR {
			return true, &unstructured.UnstructuredList{}, nil
		}
		// Dynamic responses can omit TypeMeta on list items. The source GVR must
		// still determine the displayed kind.
		return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{{Object: map[string]interface{}{
			"metadata": map[string]interface{}{"name": "macbook-01"},
		}}}}, nil
	})

	items, err := listAllEdges(context.Background(), dyn)
	if err != nil {
		t.Fatalf("listAllEdges: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d edges, want 1", len(items))
	}
	if got := items[0].GetKind(); got != "MacOSServer" {
		t.Fatalf("derived kind = %q, want MacOSServer", got)
	}
	if got := items[0].GetAPIVersion(); got != farosclient.MacOSServerGVR.GroupVersion().String() {
		t.Fatalf("derived apiVersion = %q, want %q", got, farosclient.MacOSServerGVR.GroupVersion().String())
	}
}
