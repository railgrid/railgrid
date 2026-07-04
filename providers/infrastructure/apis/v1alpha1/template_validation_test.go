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

package v1alpha1

import (
	"strings"
	"testing"
)

func devComponent(workspacePath string) TemplateDevelopmentComponent {
	return TemplateDevelopmentComponent{
		WorkspacePath: workspacePath,
		DevImage:      "${kedge.devImage.node}",
		StartCommand:  "npm run dev",
	}
}

func TestValidateDevelopment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spec    TemplateSpec
		wantErr string // substring; empty means valid
	}{
		{
			name: "no development block is valid",
			spec: TemplateSpec{},
		},
		{
			name: "single root component",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{"app": devComponent(".")},
			}},
		},
		{
			name: "multi component with distinct paths",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{
					"frontend": devComponent("web"),
					"backend":  devComponent("api"),
				},
			}},
		},
		{
			name: "bad component name",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{"Front_End": devComponent("web")},
			}},
			wantErr: "must match",
		},
		{
			name: "absolute workspace path",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{"web": devComponent("/web")},
			}},
			wantErr: "must be relative",
		},
		{
			name: "workspace escape",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{"web": devComponent("../web")},
			}},
			wantErr: "escapes the workspace",
		},
		{
			name: "duplicate workspace path",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{
					"a": devComponent("web"),
					"b": devComponent("web/"),
				},
			}},
			wantErr: "duplicates",
		},
		{
			name: "prefix overlap",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{
					"a": devComponent("web"),
					"b": devComponent("web/admin"),
				},
			}},
			wantErr: "is a prefix of",
		},
		{
			name: "root path with siblings",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{
					"a": devComponent("."),
					"b": devComponent("api"),
				},
			}},
			wantErr: "claims the workspace root",
		},
		{
			name: "missing start command",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{
					"web": {WorkspacePath: ".", DevImage: "${kedge.devImage.node}"},
				},
			}},
			wantErr: "startCommand is required",
		},
		{
			name: "reload rule without command",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{
					"web": {
						WorkspacePath: ".",
						DevImage:      "${kedge.devImage.node}",
						StartCommand:  "npm run dev",
						Reload: &TemplateDevelopmentReload{Rules: []TemplateDevelopmentReloadRule{
							{Paths: []string{"package.json"}},
						}},
					},
				},
			}},
			wantErr: "command is required",
		},
		{
			name: "data-plane component without development component",
			spec: TemplateSpec{
				Development: &TemplateDevelopment{
					Components: map[string]TemplateDevelopmentComponent{"frontend": devComponent("web")},
				},
				DataPlane: &TemplateDataPlane{
					Components: map[string]TemplateDataPlaneComponent{
						"backend": {Endpoints: map[string]TemplateDataPlaneEndpoint{"sync": {ServicePath: "status.x", Port: "control"}}},
					},
				},
			},
			wantErr: "no matching spec.development.components entry",
		},
		{
			name: "data-plane with neither endpoints nor components",
			spec: TemplateSpec{
				DataPlane: &TemplateDataPlane{},
			},
			wantErr: "neither endpoints nor components",
		},
		{
			name: "matching data-plane and development components",
			spec: TemplateSpec{
				Development: &TemplateDevelopment{
					Components: map[string]TemplateDevelopmentComponent{"frontend": devComponent("web")},
				},
				DataPlane: &TemplateDataPlane{
					Endpoints: map[string]TemplateDataPlaneEndpoint{"status": {FromStatus: true}},
					Components: map[string]TemplateDataPlaneComponent{
						"frontend": {Endpoints: map[string]TemplateDataPlaneEndpoint{"sync": {ServicePath: "status.x", Port: "control"}}},
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.ValidateDevelopment()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateDevelopment() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateDevelopment() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}
