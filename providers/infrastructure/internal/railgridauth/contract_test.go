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

package railgridauth

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func applicationTemplate(t *testing.T) *infrav1alpha1.Template {
	t.Helper()
	raw, err := os.ReadFile("../../install/templates/application.yaml")
	if err != nil {
		t.Fatalf("read application template: %v", err)
	}
	var tmpl infrav1alpha1.Template
	if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
		t.Fatalf("decode application template: %v", err)
	}
	return &tmpl
}

func TestValidateApplicationTemplateRailgridAuthContract(t *testing.T) {
	tmpl := applicationTemplate(t)
	if tmpl.Spec.RailgridAuth == nil || tmpl.Spec.RailgridAuth.Version != infrav1alpha1.CapabilityRailgridAuthV1 {
		t.Fatalf("application Railgrid Auth declaration = %+v, want version v1", tmpl.Spec.RailgridAuth)
	}
	if err := Validate(tmpl); err != nil {
		t.Fatalf("Validate(application) = %v, want nil", err)
	}
}

func TestValidateRejectsRailgridAuthWiringDrift(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*testing.T, *infrav1alpha1.Template)
		wantErr string
	}{
		{
			name: "non-KRO backend",
			mutate: func(_ *testing.T, tmpl *infrav1alpha1.Template) {
				tmpl.Spec.Backend = "other"
			},
			wantErr: "requires spec.backend=kro",
		},
		{
			name: "gate opt-in removed",
			mutate: func(t *testing.T, tmpl *infrav1alpha1.Template) {
				setGraphEnv(t, tmpl, "gateDeployment", "RAILGRID_ACCESS_PROXY_RAILGRID_AUTH", "false")
			},
			wantErr: "canonical KRO access-proxy Deployment",
		},
		{
			name: "reversed gate opt-in",
			mutate: func(t *testing.T, tmpl *infrav1alpha1.Template) {
				setGraphEnv(t, tmpl, "gateDeployment", "RAILGRID_ACCESS_PROXY_RAILGRID_AUTH", `${!schema.spec.railgridAuth ? "true" : "false"}`)
			},
			wantErr: "canonical KRO access-proxy Deployment",
		},
		{
			name: "gate issuer empty",
			mutate: func(t *testing.T, tmpl *infrav1alpha1.Template) {
				setGraphEnv(t, tmpl, "gateDeployment", "RAILGRID_AUTH_ISSUER", "")
			},
			wantErr: "canonical KRO access-proxy Deployment",
		},
		{
			name: "gate audience changed to a constant",
			mutate: func(t *testing.T, tmpl *infrav1alpha1.Template) {
				setGraphEnv(t, tmpl, "gateDeployment", "RAILGRID_AUTH_AUDIENCE", "static-app")
			},
			wantErr: "canonical KRO access-proxy Deployment",
		},
		{
			name: "gate resource is not a Deployment",
			mutate: func(t *testing.T, tmpl *infrav1alpha1.Template) {
				setGraphKind(t, tmpl, "gateDeployment", "Service")
			},
			wantErr: "canonical KRO access-proxy Deployment",
		},
		{
			name: "gate instance marker changed",
			mutate: func(t *testing.T, tmpl *infrav1alpha1.Template) {
				setGraphEnv(t, tmpl, "gateDeployment", "RAILGRID_ACCESS_PROXY_INSTANCE_GROUP", "apps")
			},
			wantErr: "canonical KRO access-proxy Deployment",
		},
		{
			name: "api identity audience removed",
			mutate: func(t *testing.T, tmpl *infrav1alpha1.Template) {
				setGraphEnv(t, tmpl, "apiDeployment", "RAILGRID_AUTH_AUDIENCE", "")
			},
			wantErr: "api development component's KRO Deployment",
		},
		{
			name: "api origin empty",
			mutate: func(t *testing.T, tmpl *infrav1alpha1.Template) {
				setGraphEnv(t, tmpl, "apiDeployment", "RAILGRID_AUTH_ORIGIN", "")
			},
			wantErr: "api development component's KRO Deployment",
		},
		{
			name: "api resource is not a Deployment",
			mutate: func(t *testing.T, tmpl *infrav1alpha1.Template) {
				setGraphKind(t, tmpl, "apiDeployment", "Service")
			},
			wantErr: "api development component's KRO Deployment",
		},
		{
			name: "api workload id no longer follows component mapping",
			mutate: func(t *testing.T, tmpl *infrav1alpha1.Template) {
				setGraphResourceID(t, tmpl, "apiDeployment", "backendDeployment")
			},
			wantErr: "api development component's KRO Deployment",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := applicationTemplate(t)
			tc.mutate(t, tmpl)
			err := Validate(tmpl)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func setGraphEnv(t *testing.T, tmpl *infrav1alpha1.Template, resourceID, envName, value string) {
	t.Helper()
	config := graphConfig(t, tmpl)
	resources := config["resources"].([]any)
	for _, rawResource := range resources {
		resource := rawResource.(map[string]any)
		if resource["id"] != resourceID {
			continue
		}
		deployment := resource["template"].(map[string]any)
		containers := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
		for _, rawContainer := range containers {
			container := rawContainer.(map[string]any)
			envs := container["env"].([]any)
			for _, rawEnv := range envs {
				env := rawEnv.(map[string]any)
				if env["name"] == envName {
					env["value"] = value
					writeGraphConfig(t, tmpl, config)
					return
				}
			}
		}
	}
	t.Fatalf("resource %q has no env %q", resourceID, envName)
}

func setGraphResourceID(t *testing.T, tmpl *infrav1alpha1.Template, oldID, newID string) {
	t.Helper()
	config := graphConfig(t, tmpl)
	for _, rawResource := range config["resources"].([]any) {
		resource := rawResource.(map[string]any)
		if resource["id"] == oldID {
			resource["id"] = newID
			writeGraphConfig(t, tmpl, config)
			return
		}
	}
	t.Fatalf("resource id %q was not found", oldID)
}

func setGraphKind(t *testing.T, tmpl *infrav1alpha1.Template, resourceID, kind string) {
	t.Helper()
	config := graphConfig(t, tmpl)
	for _, rawResource := range config["resources"].([]any) {
		resource := rawResource.(map[string]any)
		if resource["id"] == resourceID {
			resource["template"].(map[string]any)["kind"] = kind
			writeGraphConfig(t, tmpl, config)
			return
		}
	}
	t.Fatalf("resource id %q was not found", resourceID)
}

func graphConfig(t *testing.T, tmpl *infrav1alpha1.Template) map[string]any {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal(tmpl.Spec.BackendConfig.Raw, &config); err != nil {
		t.Fatalf("decode backend config: %v", err)
	}
	return config
}

func writeGraphConfig(t *testing.T, tmpl *infrav1alpha1.Template, config map[string]any) {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal backend config: %v", err)
	}
	tmpl.Spec.BackendConfig.Raw = raw
}

func TestValidateReportsExpectedExpressionWithoutLeakingActualValue(t *testing.T) {
	for _, id := range []string{"gateDeployment", "apiDeployment"} {
		t.Run(id, func(t *testing.T) {
			tmpl := applicationTemplate(t)
			setGraphEnv(t, tmpl, id, "RAILGRID_AUTH_ISSUER", "secret-incorrect-value")
			err := Validate(tmpl)
			if err == nil {
				t.Fatal("expected wiring error")
			}
			for _, want := range []string{id, "RAILGRID_AUTH_ISSUER", canonicalAuthIssuer} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not identify %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "secret-incorrect-value") {
				t.Fatalf("error exposes actual env value: %v", err)
			}
		})
	}
}

func TestValidateRejectsExpressionWhitespaceDrift(t *testing.T) {
	tmpl := applicationTemplate(t)
	setGraphEnv(t, tmpl, "apiDeployment", "RAILGRID_AUTH_ISSUER", strings.Replace(canonicalAuthIssuer, " != ", "!=", 1))
	err := Validate(tmpl)
	if err == nil || !strings.Contains(err.Error(), "RAILGRID_AUTH_ISSUER must exactly equal "+canonicalAuthIssuer) {
		t.Fatalf("Validate() = %v, want exact expression guidance", err)
	}
}
