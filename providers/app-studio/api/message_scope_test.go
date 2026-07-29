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

import "github.com/faroshq/provider-app-studio/store"

// testProjectMessageScope keeps legacy test fixtures explicit without
// weakening production scope derivation. The UID is stable for a fixture name
// and intentionally distinct from the mutable project name.
func testProjectMessageScope(orgUUID, workspaceUUID, projectName string) store.Scope {
	return store.Scope{
		OrgUUID:       orgUUID,
		WorkspaceUUID: workspaceUUID,
		ProjectName:   projectName,
		ProjectUID:    "test-project-uid-" + projectName,
	}
}
