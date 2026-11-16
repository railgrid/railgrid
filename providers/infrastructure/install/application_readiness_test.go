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
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
)

// The application template is Ready only when the tiers that serve a request
// are. kro reports a resource ready as soon as it exists unless the resource
// says otherwise, so without these the instance read Ready while both app
// tiers sat in ImagePullBackOff and every request through the gate 502'd.
func TestApplicationTiersGateReadiness(t *testing.T) {
	raw, err := fs.ReadFile(seedTemplatesFS, "templates/application.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var tmpl infrav1alpha1.Template
	if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
		t.Fatal(err)
	}
	var backend map[string]any
	if err := json.Unmarshal(tmpl.Spec.BackendConfig.Raw, &backend); err != nil {
		t.Fatalf("decode backendConfig: %v", err)
	}
	readyWhen := map[string]string{}
	resources, _ := backend["resources"].([]any)
	for _, rawResource := range resources {
		resource, _ := rawResource.(map[string]any)
		id, _ := resource["id"].(string)
		exprs, _ := resource["readyWhen"].([]any)
		if len(exprs) == 1 {
			readyWhen[id], _ = exprs[0].(string)
		}
	}

	rolledOut := func(replicas, ready int64) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"generation": int64(3)},
			"spec":     map[string]any{"replicas": int64(2)},
			"status": map[string]any{
				"observedGeneration": int64(3),
				"replicas":           replicas,
				"updatedReplicas":    replicas,
				"readyReplicas":      ready,
				"availableReplicas":  ready,
			},
		}
	}
	for _, id := range []string{"dbStatefulSet", "apiDeployment", "webDeployment", "gateDeployment"} {
		t.Run(id, func(t *testing.T) {
			expr, ok := readyWhen[id]
			if !ok {
				t.Fatalf("%s declares no readyWhen", id)
			}
			expr = strings.TrimSpace(expr)
			if !strings.HasPrefix(expr, "${") || !strings.HasSuffix(expr, "}") {
				t.Fatalf("readyWhen %q is not one ${...} expression", expr)
			}
			env, err := cel.NewEnv(cel.Variable(id, cel.DynType))
			if err != nil {
				t.Fatal(err)
			}
			ast, issues := env.Compile(strings.TrimSuffix(strings.TrimPrefix(expr, "${"), "}"))
			if issues != nil && issues.Err() != nil {
				t.Fatalf("compile %q: %v", expr, issues.Err())
			}
			program, err := env.Program(ast)
			if err != nil {
				t.Fatal(err)
			}
			eval := func(obj map[string]any) bool {
				out, _, err := program.Eval(map[string]any{id: obj})
				if err != nil {
					// kro treats an expression it cannot evaluate yet (a
					// status field not written) as not ready.
					return false
				}
				return out.Value() == true
			}
			if !eval(rolledOut(2, 2)) {
				t.Error("fully rolled-out tier is not ready")
			}
			if eval(rolledOut(2, 0)) {
				t.Error("tier with no ready replicas (ImagePullBackOff) is ready")
			}
			stale := rolledOut(2, 2)
			stale["status"].(map[string]any)["observedGeneration"] = int64(2)
			if eval(stale) {
				t.Error("tier whose status describes an older generation is ready")
			}
			fresh := rolledOut(2, 2)
			delete(fresh, "status")
			if eval(fresh) {
				t.Error("tier with no status yet is ready")
			}
		})
	}
}
