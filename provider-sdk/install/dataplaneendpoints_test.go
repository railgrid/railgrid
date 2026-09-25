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

package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/railgrid/provider-sdk/dataplaneendpoints"
)

const (
	testDataPlaneExport = "edges.providers.railgrid.ai"
	testDataPlaneURL    = "http://edges.railgrid-provider-edges.svc.cluster.local:8088"
)

func dataPlaneClient(t *testing.T, seed ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	t.Helper()
	objs := make([]runtime.Object, 0, len(seed))
	for _, o := range seed {
		objs = append(objs, o)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			crdGVR:                    "CustomResourceDefinitionList",
			dataPlaneEndpointSliceGVR: dataplaneendpoints.Kind + "List",
		}, objs...)
}

func sliceEndpoints(t *testing.T, cl *dynamicfake.FakeDynamicClient) []any {
	t.Helper()
	got, err := cl.Resource(dataPlaneEndpointSliceGVR).Get(context.Background(), testDataPlaneExport, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get slice: %v", err)
	}
	endpoints, _, err := unstructured.NestedSlice(got.Object, "status", "endpoints")
	if err != nil {
		t.Fatalf("read status.endpoints: %v", err)
	}
	return endpoints
}

// The shape is the whole contract: kcp reads the object unstructured and takes
// status.endpoints[] to be a list of its own Endpoint values.
func TestEnsureDataPlaneEndpointSlicePublishesTheURL(t *testing.T) {
	cl := dataPlaneClient(t)
	if err := EnsureDataPlaneEndpointSlice(context.Background(), cl, testDataPlaneExport, testDataPlaneURL); err != nil {
		t.Fatalf("EnsureDataPlaneEndpointSlice: %v", err)
	}
	endpoints := sliceEndpoints(t, cl)
	if len(endpoints) != 1 {
		t.Fatalf("endpoints = %v, want exactly one", endpoints)
	}
	endpoint, _ := endpoints[0].(map[string]any)
	if endpoint["url"] != testDataPlaneURL {
		t.Errorf("url = %v, want %q", endpoint["url"], testDataPlaneURL)
	}
	// Without matchAll, kcp adopts a lone URL only when it already carries the
	// serving shard's own prefix, and a railgrid provider serves the whole
	// installation from one address.
	shards, _ := endpoint["shards"].(map[string]any)
	if shards["matchAll"] != true {
		t.Errorf("shards = %v, want matchAll", shards)
	}
}

// init is re-run on every upgrade; the second run must be a no-op, and a
// changed address must land.
func TestEnsureDataPlaneEndpointSliceIsIdempotentAndUpdates(t *testing.T) {
	ctx := context.Background()
	cl := dataPlaneClient(t)
	for range 2 {
		if err := EnsureDataPlaneEndpointSlice(ctx, cl, testDataPlaneExport, testDataPlaneURL); err != nil {
			t.Fatalf("EnsureDataPlaneEndpointSlice: %v", err)
		}
	}
	if endpoints := sliceEndpoints(t, cl); len(endpoints) != 1 {
		t.Fatalf("endpoints = %v, want exactly one after two runs", endpoints)
	}
	const moved = "http://edges.other.svc.cluster.local:8088"
	if err := EnsureDataPlaneEndpointSlice(ctx, cl, testDataPlaneExport, moved); err != nil {
		t.Fatalf("EnsureDataPlaneEndpointSlice (moved): %v", err)
	}
	endpoint, _ := sliceEndpoints(t, cl)[0].(map[string]any)
	if endpoint["url"] != moved {
		t.Errorf("url = %v, want the new address", endpoint["url"])
	}
}

// An empty URL would publish a slice kcp resolves to nothing, so the subresource
// would 404 with no error anywhere. Fail at init instead.
func TestEnsureDataPlaneEndpointSliceRefusesAnEmptyURL(t *testing.T) {
	err := EnsureDataPlaneEndpointSlice(context.Background(), dataPlaneClient(t), testDataPlaneExport, "")
	if err == nil || !strings.Contains(err.Error(), "DataPlaneURL") {
		t.Fatalf("err = %v, want a refusal naming the option", err)
	}
}

// The name is derived, never looked up: that is what lets apiexportgen write
// the reference into every entry from manifest.yaml alone.
func TestSliceNameIsTheExportName(t *testing.T) {
	if got := dataplaneendpoints.SliceName(testDataPlaneExport); got != testDataPlaneExport {
		t.Errorf("SliceName = %q, want %q", got, testDataPlaneExport)
	}
}

// The CRD provider-sdk ships must actually be the kind the reference names, and
// must carry the status subresource the slice is written through.
func TestEmbeddedCRDMatchesTheReferencedKind(t *testing.T) {
	var crd struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Group string `json:"group"`
			Names struct {
				Kind   string `json:"kind"`
				Plural string `json:"plural"`
			} `json:"names"`
			Scope    string `json:"scope"`
			Versions []struct {
				Name         string `json:"name"`
				Subresources struct {
					Status *map[string]any `json:"status"`
				} `json:"subresources"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(dataplaneendpoints.CRDYAML(), &crd); err != nil {
		t.Fatalf("parsing the embedded CRD: %v", err)
	}
	if crd.Metadata.Name != dataplaneendpoints.CRDName {
		t.Errorf("name = %q, want %q", crd.Metadata.Name, dataplaneendpoints.CRDName)
	}
	if crd.Spec.Group != dataplaneendpoints.Group || crd.Spec.Names.Kind != dataplaneendpoints.Kind {
		t.Errorf("group/kind = %s/%s", crd.Spec.Group, crd.Spec.Names.Kind)
	}
	if crd.Spec.Names.Plural != dataplaneendpoints.Resource {
		t.Errorf("plural = %q, want %q", crd.Spec.Names.Plural, dataplaneendpoints.Resource)
	}
	if crd.Spec.Scope != "Cluster" {
		t.Errorf("scope = %q; kcp resolves the reference cluster-scoped in the export's workspace", crd.Spec.Scope)
	}
	if len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Name != dataplaneendpoints.Version {
		t.Fatalf("versions = %+v", crd.Spec.Versions)
	}
	if crd.Spec.Versions[0].Subresources.Status == nil {
		t.Error("the status subresource is missing; install writes the URL through it")
	}
}

func TestExportDeclaresSubresources(t *testing.T) {
	export := func(names ...string) *unstructured.Unstructured {
		resources := make([]any, 0, len(names))
		for _, name := range names {
			resources = append(resources, map[string]any{"name": name, "group": "edges.railgrid.ai"})
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"resources": resources},
		}}
	}
	if ExportDeclaresSubresources(export("linuxservers", "services")) {
		t.Error("an export with only real resources needs no slice")
	}
	if !ExportDeclaresSubresources(export("linuxservers", "linuxservers/ssh")) {
		t.Error("an export carrying a custom subresource does need one")
	}
}

// The URL is not a new flag: spec.backend.url IS the address the hub already
// proxies to, rendered per environment by the provider's own chart.
func TestCatalogEntryBackendURLReadsTheAddressTheHubProxiesTo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalogentry.yaml")
	body := "apiVersion: providers.railgrid.ai/v1alpha1\nkind: CatalogEntry\nmetadata:\n  name: edges\nspec:\n  backend:\n    url: " + testDataPlaneURL + "\n    healthPath: /readyz\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	got, err := CatalogEntryBackendURL(path)
	if err != nil {
		t.Fatalf("CatalogEntryBackendURL: %v", err)
	}
	if got != testDataPlaneURL {
		t.Errorf("url = %q, want %q", got, testDataPlaneURL)
	}
}

// A custom subresource entry names the schema of the verb's "<Verb>Request"
// kind, shipped beside the stored kinds' so a claimer's virtual workspace can
// resolve it; ApplyAPIExport waits for it exactly as for any other.
func TestApplyAPIExportWaitsForASubresourceSchema(t *testing.T) {
	cl := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			apiResourceSchemaGVR: "APIResourceSchemaList",
			apiExportGVR:         "APIExportList",
		},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apis.kcp.io/v1alpha1",
			"kind":       "APIResourceSchema",
			"metadata":   map[string]any{"name": "v1.linuxservers.edges.railgrid.ai"},
		}},
	)
	export := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIExport",
		"metadata":   map[string]any{"name": testDataPlaneExport},
		"spec": map[string]any{"resources": []any{
			map[string]any{"name": "linuxservers", "group": "edges.railgrid.ai", "schema": "v1.linuxservers.edges.railgrid.ai"},
			map[string]any{"name": "linuxservers/ssh", "group": "edges.railgrid.ai", "schema": "v1.ssh.edges.railgrid.ai"},
		}},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := ApplyAPIExport(ctx, cl, export)
	if err == nil || !strings.Contains(err.Error(), "v1.ssh.edges.railgrid.ai") {
		t.Fatalf("err = %v, want a missing verb-schema error", err)
	}

	if _, err := cl.Resource(apiResourceSchemaGVR).Create(context.Background(), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIResourceSchema",
		"metadata":   map[string]any{"name": "v1.ssh.edges.railgrid.ai"},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyAPIExport(context.Background(), cl, export); err != nil {
		t.Fatalf("ApplyAPIExport with the verb schema shipped: %v", err)
	}
}

// A CRD that kcp has just accepted reads back with status.conditions: null
// until the establishing controller fills it. Seen against kcp-dev/kcp#4388 in
// the provider e2e: the install step failed with a NestedSlice type error on
// that null instead of polling on.
func TestWaitForCRDEstablishedPollsThroughANullConditions(t *testing.T) {
	crd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": dataplaneendpoints.CRDName},
		"status":     map[string]any{"conditions": nil},
	}}
	cl := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList"}, crd)

	// After the first read the controller catches up.
	gets := 0
	cl.PrependReactor("get", "customresourcedefinitions", func(action clienttesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets < 2 {
			return false, nil, nil
		}
		established := crd.DeepCopy()
		established.Object["status"] = map[string]any{"conditions": []any{
			map[string]any{"type": "Established", "status": "True"},
		}}
		return true, established, nil
	})

	if err := waitForCRDEstablished(context.Background(), cl, dataplaneendpoints.CRDName); err != nil {
		t.Fatalf("waitForCRDEstablished: %v", err)
	}
	if gets < 2 {
		t.Fatalf("expected the wait to poll past the null conditions, got %d reads", gets)
	}
}
