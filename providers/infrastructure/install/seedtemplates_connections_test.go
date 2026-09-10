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

package install

import (
	"context"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/instancespec"
)

// TestWorkloadTemplatesValidateConnections runs the connections input of the
// workload templates through the same contract the instance controller
// applies (defaulting + openapi + CEL): omitted defaults to unconnected
// slots, real instance names pass, and anything that could not be the
// values.name of a sibling instance (and so could not name its Secret) is
// rejected before it reaches the runtime cluster.
func TestWorkloadTemplatesValidateConnections(t *testing.T) {
	for _, tc := range []struct {
		file string
		base map[string]any
	}{
		{file: "simple-webapp.yaml", base: map[string]any{"name": "web", "image": "nginx:latest"}},
		{file: "worker.yaml", base: map[string]any{"name": "wrk", "image": "busybox:stable"}},
		{file: "cron-job.yaml", base: map[string]any{"name": "job", "image": "busybox:stable"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := fs.ReadFile(seedTemplatesFS, "templates/"+tc.file)
			if err != nil {
				t.Fatal(err)
			}
			var tmpl infrav1alpha1.Template
			if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
				t.Fatalf("decode: %v", err)
			}
			contract, err := instancespec.NewContract(&tmpl)
			if err != nil {
				t.Fatalf("compile contract: %v", err)
			}
			values := func(connections any) map[string]any {
				out := map[string]any{}
				for k, v := range tc.base {
					out[k] = v
				}
				if connections != nil {
					out["connections"] = connections
				}
				return out
			}

			// Omitted: the object and both slots default to "" (unconnected),
			// which is what the RGD's name/optional expressions branch on.
			defaulted, errs := contract.ValidateAndDefault(context.Background(), values(nil))
			if len(errs) != 0 {
				t.Fatalf("values without connections rejected: %v", errs)
			}
			if got, want := defaulted["connections"], map[string]any{"database": "", "cache": ""}; !reflect.DeepEqual(got, want) {
				t.Fatalf("defaulted connections = %#v, want %#v", got, want)
			}
			// A partial object still gets the other slot defaulted.
			defaulted, errs = contract.ValidateAndDefault(context.Background(), values(map[string]any{"database": "todo-db"}))
			if len(errs) != 0 {
				t.Fatalf("connections.database=todo-db rejected: %v", errs)
			}
			if got, want := defaulted["connections"], map[string]any{"database": "todo-db", "cache": ""}; !reflect.DeepEqual(got, want) {
				t.Fatalf("defaulted connections = %#v, want %#v", got, want)
			}

			for _, ok := range []map[string]any{
				{"database": "todo-db", "cache": "cache"},
				{"database": "", "cache": ""},
				{"cache": "a"},
				{"database": strings.Repeat("d", 50)},
			} {
				if _, errs := contract.ValidateAndDefault(context.Background(), values(ok)); len(errs) != 0 {
					t.Errorf("connections %v rejected: %v", ok, errs)
				}
			}
			for _, bad := range []map[string]any{
				{"database": "Todo_DB"},
				{"database": "-db"},
				{"database": "db-"},
				{"database": "todo-db-db-credentials/other"},
				{"cache": "my cache"},
				{"database": strings.Repeat("d", 51)},
				{"cache": strings.Repeat("c", 64)},
				{"database": int64(5)},
			} {
				if _, errs := contract.ValidateAndDefault(context.Background(), values(bad)); len(errs) == 0 {
					t.Errorf("connections %v accepted; want a validation error", bad)
				}
			}
		})
	}
}
