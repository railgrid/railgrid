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

package restapi

import (
	"testing"

	"k8s.io/utils/ptr"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

func TestWorkspaceAssistantStaleTab(t *testing.T) {
	prefs := &tenancyv1alpha1.UserPreferences{}
	const workspace = "workspace"
	initial := workspaceAssistantUpdate{Agent: ptr.To("scout"), FollowContext: ptr.To(true), Initialize: true}
	old, changed, conflict := updateWorkspaceAssistant(prefs, workspace, initial, false)
	if !changed || conflict {
		t.Fatalf("initial migration changed=%v conflict=%v", changed, conflict)
	}

	updateWorkspaceAssistant(prefs, workspace, workspaceAssistantUpdate{Agent: ptr.To("")}, true)
	staleSave := workspaceAssistantUpdate{
		Agent: old.Agent, FollowContext: ptr.To(false), Expected: &workspaceAssistantResponse{Preference: old},
	}
	for _, request := range []workspaceAssistantUpdate{staleSave, {Agent: old.Agent, FollowContext: ptr.To(false)}} {
		current, changed, conflict := updateWorkspaceAssistant(prefs, workspace, request, false)
		if changed || !conflict || current.Agent == nil || *current.Agent != "" || !current.FollowContext {
			t.Fatalf("stale full save changed=%v conflict=%v preference=%+v", changed, conflict, current)
		}
	}

	current, changed, conflict := updateWorkspaceAssistant(prefs, workspace, workspaceAssistantUpdate{FollowContext: ptr.To(false)}, true)
	if !changed || conflict || current.Agent == nil || *current.Agent != "" || current.FollowContext {
		t.Fatalf("context patch changed=%v conflict=%v preference=%+v", changed, conflict, current)
	}
	current, changed, conflict = updateWorkspaceAssistant(prefs, workspace, initial, false)
	if changed || conflict || current.Agent == nil || *current.Agent != "" || current.FollowContext {
		t.Fatalf("legacy migration changed=%v conflict=%v preference=%+v", changed, conflict, current)
	}
}
