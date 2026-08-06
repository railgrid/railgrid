// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	appskills "github.com/faroshq/provider-app-studio/skills"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectSkillActivationRejectsConcurrentStaleMetadata(t *testing.T) {
	router, files := newEvaluationSkillRouter(t)
	request := evaluationSkillRequest
	create := evaluationServeSkill(router, request(http.MethodPost, "/api/projects/demo/assistant/skills/project", `{"packageName":"review/demo","name":"demo","description":"initial","instructions":"initial body"}`))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}

	responses := make([]int, 2)
	var wait sync.WaitGroup
	for index := range responses {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response := evaluationServeSkill(router, request(http.MethodPost, "/api/projects/demo/assistant/skills/activation", `{"id":"project:demo","enabled":false}`))
			responses[index] = response.Code
		}(index)
	}
	wait.Wait()
	statusCount := map[int]int{}
	for _, status := range responses {
		statusCount[status]++
	}
	if statusCount[http.StatusOK] != 1 || statusCount[http.StatusConflict] != 1 {
		t.Fatalf("concurrent activation statuses = %#v, want one success and one conflict", statusCount)
	}

	metadata, _, err := appskills.ReadProjectMetadata(context.Background(), files, workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-demo"})
	if err != nil {
		t.Fatal(err)
	}
	activation, ok := metadata.Packages["review/demo"]
	if !ok || activation.Enabled {
		t.Fatalf("activation metadata after race = %#v, want one committed disabled policy", metadata.Packages)
	}
}

func TestProjectSkillLifecycleKeepsPackageAndMetadataTogether(t *testing.T) {
	router, files := newEvaluationSkillRouter(t)
	request := evaluationSkillRequest
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-demo"}
	ctx := context.Background()
	beforeRevision, err := files.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	create := evaluationServeSkill(router, request(http.MethodPost, "/api/projects/demo/assistant/skills/project", `{"packageName":"review/demo","name":"demo","description":"initial","instructions":"initial body","resources":[{"path":"old.txt","content":"old resource"}]}`))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	assertFileContent := func(path, want string) {
		t.Helper()
		file, readErr := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: path})
		if readErr != nil || file.Content != want {
			t.Fatalf("file %q = %q, err=%v; want %q", path, file.Content, readErr, want)
		}
	}
	initialSkill := mustReadFile(t, files, ctx, scope, ".agents/skills/review/demo/SKILL.md")
	if !strings.Contains(string(initialSkill), "name: demo") || !strings.Contains(string(initialSkill), "description: initial") || !strings.HasSuffix(string(initialSkill), "initial body") {
		t.Fatalf("created SKILL.md = %q", initialSkill)
	}
	assertFileContent(".agents/skills/review/demo/old.txt", "old resource")
	metadata, _, err := appskills.ReadProjectMetadata(ctx, files, scope)
	if err != nil || !metadata.Packages["review/demo"].Enabled {
		t.Fatalf("metadata after create = %#v, err=%v", metadata, err)
	}
	afterCreateRevision, err := files.SourceRevision(ctx, scope)
	if err != nil || afterCreateRevision != beforeRevision+1 {
		t.Fatalf("source revision after create = %d, err=%v; want %d", afterCreateRevision, err, beforeRevision+1)
	}

	var created projectAssistantSkillDetail
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	updateBody := `{"packageName":"review/demo","name":"demo","description":"updated","instructions":"updated body","resources":[{"path":"new.txt","content":"new resource"}],"expectedDigest":"` + created.Digest + `"}`
	updateBeforeRevision, _ := files.SourceRevision(ctx, scope)
	update := evaluationServeSkill(router, request(http.MethodPut, "/api/projects/demo/assistant/skills/project/review/demo", updateBody))
	if update.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", update.Code, update.Body.String())
	}
	updatedSkill := mustReadFile(t, files, ctx, scope, ".agents/skills/review/demo/SKILL.md")
	if !strings.Contains(string(updatedSkill), "name: demo") || !strings.Contains(string(updatedSkill), "description: updated") || !strings.HasSuffix(string(updatedSkill), "updated body") {
		t.Fatalf("updated SKILL.md = %q", updatedSkill)
	}
	assertFileContent(".agents/skills/review/demo/new.txt", "new resource")
	if _, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: ".agents/skills/review/demo/old.txt"}); err == nil {
		t.Fatal("removed resource survived update")
	}
	metadata, _, err = appskills.ReadProjectMetadata(ctx, files, scope)
	if err != nil || !metadata.Packages["review/demo"].Enabled {
		t.Fatalf("metadata after update = %#v, err=%v", metadata, err)
	}
	afterUpdateRevision, err := files.SourceRevision(ctx, scope)
	if err != nil || afterUpdateRevision != updateBeforeRevision+1 {
		t.Fatalf("source revision after update = %d, err=%v; want %d", afterUpdateRevision, err, updateBeforeRevision+1)
	}

	var updated projectAssistantSkillDetail
	if err := json.Unmarshal(update.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	deleteBeforeRevision, _ := files.SourceRevision(ctx, scope)
	deleted := evaluationServeSkill(router, request(http.MethodDelete, "/api/projects/demo/assistant/skills/project/review/demo?expectedDigest="+updated.Digest, ""))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	for _, path := range []string{".agents/skills/review/demo/SKILL.md", ".agents/skills/review/demo/new.txt"} {
		if _, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: path}); err == nil {
			t.Fatalf("deleted package file %q survived delete", path)
		}
	}
	metadata, _, err = appskills.ReadProjectMetadata(ctx, files, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := metadata.Packages["review/demo"]; exists {
		t.Fatalf("metadata retained deleted package: %#v", metadata.Packages)
	}
	afterDeleteRevision, err := files.SourceRevision(ctx, scope)
	if err != nil || afterDeleteRevision != deleteBeforeRevision+1 {
		t.Fatalf("source revision after delete = %d, err=%v; want %d", afterDeleteRevision, err, deleteBeforeRevision+1)
	}
	if strings.Contains(string(mustReadFile(t, files, ctx, scope, appskills.ProjectMetadataPath)), "review/demo") {
		t.Fatal("deleted package identity remains in metadata bytes")
	}
}

func mustReadFile(t *testing.T, files *workspace.FileStore, ctx context.Context, scope workspace.Scope, path string) []byte {
	t.Helper()
	file, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	return []byte(file.Content)
}
