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
		DevImage:      "${faros.devImage.node}",
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
				Build:      &TemplateDevelopmentBuild{WorkflowPath: ".github/workflows/build.yaml"},
			}},
		},
		{
			name: "workflow path yml is valid",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{"app": devComponent(".")},
				Build:      &TemplateDevelopmentBuild{WorkflowPath: ".github/workflows/release.yml"},
			}},
		},
		{
			name: "workflow path is required when build declared",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{"app": devComponent(".")},
				Build:      &TemplateDevelopmentBuild{},
			}},
			wantErr: "workflowPath: is required",
		},
		{
			name: "workflow path must be repository relative",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{"app": devComponent(".")},
				Build:      &TemplateDevelopmentBuild{WorkflowPath: "/.github/workflows/build.yaml"},
			}},
			wantErr: "directly under .github/workflows",
		},
		{
			name: "workflow path must be directly under workflows",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{"app": devComponent(".")},
				Build:      &TemplateDevelopmentBuild{WorkflowPath: ".github/workflows/nested/build.yaml"},
			}},
			wantErr: "directly under .github/workflows",
		},
		{
			name: "workflow path rejects traversal",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{"app": devComponent(".")},
				Build:      &TemplateDevelopmentBuild{WorkflowPath: ".github/workflows/../build.yaml"},
			}},
			wantErr: "directly under .github/workflows",
		},
		{
			name: "workflow path requires yaml extension",
			spec: TemplateSpec{Development: &TemplateDevelopment{
				Components: map[string]TemplateDevelopmentComponent{"app": devComponent(".")},
				Build:      &TemplateDevelopmentBuild{WorkflowPath: ".github/workflows/build.json"},
			}},
			wantErr: "directly under .github/workflows",
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
					"web": {WorkspacePath: ".", DevImage: "${faros.devImage.node}"},
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
						DevImage:      "${faros.devImage.node}",
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
			name: "exec requires development component",
			spec: TemplateSpec{
				DataPlane: &TemplateDataPlane{
					Components: map[string]TemplateDataPlaneComponent{
						"backend": {Endpoints: map[string]TemplateDataPlaneEndpoint{"sync": {ServicePath: "status.x", Port: "control"}}, Exec: &TemplateDataPlaneExec{}},
					},
				},
			},
			wantErr: "exec requires spec.development.components",
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
