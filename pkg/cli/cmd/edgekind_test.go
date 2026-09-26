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

package cmd

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	railgridclient "github.com/railgrid/railgrid/pkg/client"
)

func TestGetEdgeByNameFindsMacOSServer(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("get", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		get := action.(clienttesting.GetAction)
		if get.GetResource() != railgridclient.MacOSServerGVR {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: get.GetResource().Group, Resource: get.GetResource().Resource}, get.GetName())
		}
		return true, &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": railgridclient.MacOSServerGVR.GroupVersion().String(),
			"kind":       "MacOSServer",
			"metadata":   map[string]interface{}{"name": get.GetName()},
		}}, nil
	})

	edge, gvr, err := getEdgeByName(context.Background(), dyn, "macbook-01")
	if err != nil {
		t.Fatalf("getEdgeByName: %v", err)
	}
	if gvr != railgridclient.MacOSServerGVR {
		t.Fatalf("GVR = %v, want %v", gvr, railgridclient.MacOSServerGVR)
	}
	if edge.GetKind() != "MacOSServer" || edge.GetName() != "macbook-01" {
		t.Fatalf("edge identity = %s/%s, want MacOSServer/macbook-01", edge.GetKind(), edge.GetName())
	}
}

func TestListAllEdgesDerivesKindFromMacOSServerGVR(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		railgridclient.KubernetesClusterGVR: "KubernetesClusterList",
		railgridclient.LinuxServerGVR:       "LinuxServerList",
		railgridclient.MacOSServerGVR:       "MacOSServerList",
	})
	dyn.PrependReactor("list", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		list := action.(clienttesting.ListAction)
		if list.GetResource() != railgridclient.MacOSServerGVR {
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
	if got := items[0].GetAPIVersion(); got != railgridclient.MacOSServerGVR.GroupVersion().String() {
		t.Fatalf("derived apiVersion = %q, want %q", got, railgridclient.MacOSServerGVR.GroupVersion().String())
	}
}

// fakeEdgeDyn serves a Get for exactly the (GVR, name) pairs listed, and
// NotFound for everything else — enough to exercise name resolution across
// kinds without a real edges API.
func fakeEdgeDyn(t *testing.T, edges map[schema.GroupVersionResource][]string) *dynamicfake.FakeDynamicClient {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("get", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		get := action.(clienttesting.GetAction)
		gvr := get.GetResource()
		for _, name := range edges[gvr] {
			if name != get.GetName() {
				continue
			}
			return true, &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": gvr.GroupVersion().String(),
				"kind":       railgridclient.EdgeKindForType(railgridclient.EdgeTypeForGVR(gvr)),
				"metadata":   map[string]interface{}{"name": name},
			}}, nil
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, get.GetName())
	})
	return dyn
}

func TestParseEdgeRef(t *testing.T) {
	for _, tc := range []struct {
		ref     string
		name    string
		gvr     schema.GroupVersionResource
		wantErr bool
	}{
		{ref: "minis", name: "minis"},
		{ref: "server/minis", name: "minis", gvr: railgridclient.LinuxServerGVR},
		{ref: "linuxserver/minis", name: "minis", gvr: railgridclient.LinuxServerGVR},
		{ref: "LinuxServer/minis", name: "minis", gvr: railgridclient.LinuxServerGVR},
		{ref: "linuxservers/minis", name: "minis", gvr: railgridclient.LinuxServerGVR},
		{ref: "kubernetes/minis", name: "minis", gvr: railgridclient.KubernetesClusterGVR},
		{ref: "macos/macbook", name: "macbook", gvr: railgridclient.MacOSServerGVR},
		{ref: "bogus/minis", wantErr: true},
		{ref: "server/", wantErr: true},
	} {
		name, gvr, err := parseEdgeRef(tc.ref)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseEdgeRef(%q) = %q/%v, want an error", tc.ref, name, gvr)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseEdgeRef(%q): %v", tc.ref, err)
			continue
		}
		if name != tc.name || gvr != tc.gvr {
			t.Errorf("parseEdgeRef(%q) = %q/%v, want %q/%v", tc.ref, name, gvr, tc.name, tc.gvr)
		}
	}
}

// A name used by both a cluster and a server is not resolvable on its own: the
// CLI must say so instead of silently acting on whichever kind it probes first.
func TestGetEdgeByNameRejectsAmbiguousName(t *testing.T) {
	dyn := fakeEdgeDyn(t, map[schema.GroupVersionResource][]string{
		railgridclient.KubernetesClusterGVR: {"minis"},
		railgridclient.LinuxServerGVR:       {"minis"},
	})

	_, _, err := getEdgeByName(context.Background(), dyn, "minis")
	if err == nil {
		t.Fatal("getEdgeByName resolved an ambiguous name, want an error")
	}
	for _, want := range []string{"ambiguous", "kubernetes/minis", "server/minis"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestGetEdgeByNameQualifiedPicksTheKind(t *testing.T) {
	dyn := fakeEdgeDyn(t, map[schema.GroupVersionResource][]string{
		railgridclient.KubernetesClusterGVR: {"minis"},
		railgridclient.LinuxServerGVR:       {"minis"},
	})

	edge, gvr, err := getEdgeByName(context.Background(), dyn, "server/minis")
	if err != nil {
		t.Fatalf("getEdgeByName: %v", err)
	}
	if gvr != railgridclient.LinuxServerGVR {
		t.Fatalf("GVR = %v, want %v", gvr, railgridclient.LinuxServerGVR)
	}
	if edge.GetName() != "minis" {
		t.Fatalf("name = %q, want the qualifier stripped to %q", edge.GetName(), "minis")
	}
}

// `railgrid ssh minis` must reach the LinuxServer even though a
// KubernetesCluster of the same name exists (the bug: SSH refused, telling the
// user to run 'railgrid connect' for a host it cannot kubectl into).
func TestGetEdgeByNamePreferringPicksTheServerForSSH(t *testing.T) {
	dyn := fakeEdgeDyn(t, map[schema.GroupVersionResource][]string{
		railgridclient.KubernetesClusterGVR: {"minis"},
		railgridclient.LinuxServerGVR:       {"minis"},
	})

	_, gvr, err := getEdgeByNamePreferring(context.Background(), dyn, "minis",
		railgridclient.LinuxServerGVR, railgridclient.MacOSServerGVR)
	if err != nil {
		t.Fatalf("getEdgeByNamePreferring: %v", err)
	}
	if gvr != railgridclient.LinuxServerGVR {
		t.Fatalf("GVR = %v, want %v", gvr, railgridclient.LinuxServerGVR)
	}
}

// The mirror case: kubectl access prefers the cluster.
func TestGetEdgeByNamePreferringPicksTheClusterForKubeconfig(t *testing.T) {
	dyn := fakeEdgeDyn(t, map[schema.GroupVersionResource][]string{
		railgridclient.KubernetesClusterGVR: {"minis"},
		railgridclient.LinuxServerGVR:       {"minis"},
	})

	_, gvr, err := getEdgeByNamePreferring(context.Background(), dyn, "minis", railgridclient.KubernetesClusterGVR)
	if err != nil {
		t.Fatalf("getEdgeByNamePreferring: %v", err)
	}
	if gvr != railgridclient.KubernetesClusterGVR {
		t.Fatalf("GVR = %v, want %v", gvr, railgridclient.KubernetesClusterGVR)
	}
}

// A unique name outside the preference list still resolves, so the caller can
// answer with its own "that edge is the wrong kind" message.
func TestGetEdgeByNamePreferringReturnsTheOnlyMatch(t *testing.T) {
	dyn := fakeEdgeDyn(t, map[schema.GroupVersionResource][]string{
		railgridclient.KubernetesClusterGVR: {"dev-cluster"},
	})

	_, gvr, err := getEdgeByNamePreferring(context.Background(), dyn, "dev-cluster",
		railgridclient.LinuxServerGVR, railgridclient.MacOSServerGVR)
	if err != nil {
		t.Fatalf("getEdgeByNamePreferring: %v", err)
	}
	if gvr != railgridclient.KubernetesClusterGVR {
		t.Fatalf("GVR = %v, want %v", gvr, railgridclient.KubernetesClusterGVR)
	}
}

func TestGetEdgeByNameNotFound(t *testing.T) {
	dyn := fakeEdgeDyn(t, nil)

	_, _, err := getEdgeByName(context.Background(), dyn, "nope")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want a not-found error", err)
	}
}

func TestDuplicateEdgeNames(t *testing.T) {
	items := []unstructured.Unstructured{
		{Object: map[string]interface{}{"kind": "KubernetesCluster", "metadata": map[string]interface{}{"name": "minis"}}},
		{Object: map[string]interface{}{"kind": "LinuxServer", "metadata": map[string]interface{}{"name": "minis"}}},
		{Object: map[string]interface{}{"kind": "LinuxServer", "metadata": map[string]interface{}{"name": "lenovo"}}},
	}
	dupes := duplicateEdgeNames(items)
	if !dupes["minis"] || dupes["lenovo"] {
		t.Fatalf("duplicateEdgeNames = %v, want only minis", dupes)
	}
}
