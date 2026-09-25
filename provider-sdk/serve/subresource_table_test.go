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

package serve

import (
	"net/http"
	"strings"
	"testing"
)

const fixtureManifest = `
apiVersion: providers.railgrid.ai/v1alpha1
kind: CatalogEntry
metadata: {name: fixture}
spec:
  dataPlane:
    verbs:
    - resource: linuxservers
      verb: addon-credentials
    - resource: services
      verb: proxy
  actions:
  - id: mint-clone-token/v1
    boundResource: {resource: repositories}
  - id: preview/v2
    boundResource: {resource: factorylines}
`

func TestSubresourcesFromCatalogEntryMirrorsTheDeclaration(t *testing.T) {
	table, err := SubresourcesFromCatalogEntry([]byte(fixtureManifest))
	if err != nil {
		t.Fatalf("SubresourcesFromCatalogEntry: %v", err)
	}
	if len(table) != 4 {
		t.Fatalf("table = %v, want 4 coordinates", table)
	}
	if r := table["linuxservers/addon-credentials"]; r.Action || r.Version != "" {
		t.Fatalf("verb route = %+v", r)
	}
	if r := table["repositories/mint-clone-token"]; !r.Action || r.Version != "v1" {
		t.Fatalf("action route = %+v, want action at v1", r)
	}
	if r := table["factorylines/preview"]; !r.Action || r.Version != "v2" {
		t.Fatalf("versioned action route = %+v", r)
	}
	// The table is what New accepts, unchanged.
	if _, err := New(Options{Readiness: okHandler(), DataPlane: okHandler(), Actions: okHandler(), Subresources: table}); err != nil {
		t.Fatalf("New rejected the derived table: %v", err)
	}
}

func TestSubresourcesFromCatalogEntryRefusesWhatKcpWouldRefuse(t *testing.T) {
	for name, manifest := range map[string]string{
		"underscore in a verb":    "spec:\n  dataPlane:\n    verbs:\n    - {resource: repositories, verb: mint_token}\n",
		"status as a verb":        "spec:\n  dataPlane:\n    verbs:\n    - {resource: instances, verb: status}\n",
		"action with no version":  "spec:\n  actions:\n  - id: preview\n    boundResource: {resource: factorylines}\n",
		"underscore in an action": "spec:\n  actions:\n  - id: update_thing/v1\n    boundResource: {resource: boards}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := SubresourcesFromCatalogEntry([]byte(manifest))
			if err == nil {
				t.Fatal("an invalid declaration was accepted")
			}
			if !strings.Contains(err.Error(), "/") {
				t.Fatalf("error does not name the coordinate: %v", err)
			}
		})
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}
