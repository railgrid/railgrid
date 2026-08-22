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

package api

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestProjectEinoAssistantSnapshotPublishesActiveCodingEnvironment(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetSandbox(&projectAssistantRunSandbox{
		target: projectDevelopmentSyncTargetInfo{Components: map[string]projectTemplateComponent{
			projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."},
		}},
		metadata: projectAssistantRunSandboxMetadata{
			Status:        "active",
			Template:      projectAssistantRunSandboxDefaultTemplate,
			HardExpiresAt: time.Now().UTC().Add(time.Hour),
		},
	})

	snapshot := projectEinoAssistantSnapshot(context.Background(), projectAssistantRunRequest{}, state)
	environment := snapshot.CodingEnvironment
	if environment == nil {
		t.Fatal("active universal sandbox did not publish codingEnvironment")
	}
	if environment.Kind != "assistant-run-sandbox" || environment.Status != "ready" ||
		environment.Template != projectAssistantRunSandboxDefaultTemplate || environment.WorkspaceRoot != "." ||
		environment.ExecComponent != projectAssistantRunSandboxWorkspaceVerb || environment.SourcePersistence != "project-workspace" ||
		environment.NetworkExposure != "internal" || environment.PublicPreview {
		t.Fatalf("codingEnvironment = %#v", environment)
	}
	if want := []string{"go", "node", "python"}; !reflect.DeepEqual(environment.Toolchains, want) {
		t.Fatalf("toolchains = %#v, want %#v", environment.Toolchains, want)
	}

	clone := cloneProjectEinoAssistantSessionSnapshot(&snapshot)
	clone.CodingEnvironment.Toolchains[0] = "changed"
	if snapshot.CodingEnvironment.Toolchains[0] != "go" {
		t.Fatalf("codingEnvironment toolchains were not deep-cloned: %#v", snapshot.CodingEnvironment.Toolchains)
	}
}

func TestProjectEinoAssistantCodingEnvironmentFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		sandbox *projectAssistantRunSandbox
	}{
		{name: "missing"},
		{name: "closed", sandbox: &projectAssistantRunSandbox{
			target: projectDevelopmentSyncTargetInfo{Components: map[string]projectTemplateComponent{
				projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."},
			}},
			metadata: projectAssistantRunSandboxMetadata{Status: "closed", Template: projectAssistantRunSandboxDefaultTemplate},
		}},
		{name: "wrong template", sandbox: &projectAssistantRunSandbox{
			target: projectDevelopmentSyncTargetInfo{Components: map[string]projectTemplateComponent{
				projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."},
			}},
			metadata: projectAssistantRunSandboxMetadata{Status: "active", Template: "other"},
		}},
		{name: "wrong root", sandbox: &projectAssistantRunSandbox{
			target: projectDevelopmentSyncTargetInfo{Components: map[string]projectTemplateComponent{
				projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "src"},
			}},
			metadata: projectAssistantRunSandboxMetadata{Status: "active", Template: projectAssistantRunSandboxDefaultTemplate},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newProjectEinoAssistantRunState()
			state.SetSandbox(tt.sandbox)
			if environment := projectEinoAssistantCodingEnvironmentForRun(state); environment != nil {
				t.Fatalf("codingEnvironment = %#v, want omitted", environment)
			}
		})
	}
}
