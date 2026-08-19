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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	asclient "github.com/faroshq/provider-app-studio/client"
)

func releaseCommitForTest(name, repositoryRef, phase, sha string, created time.Time) *unstructured.Unstructured {
	commit := repositoryCommitForBuildTest(name, repositoryRef, repositoryRef, phase, sha, created)
	_ = unstructured.SetNestedField(commit.Object, "https://github.com/acme/shop/commit/"+sha, "status", "commitURL")
	_ = unstructured.SetNestedField(commit.Object, "message-"+name, "spec", "message")
	_ = unstructured.SetNestedField(commit.Object, created.Add(time.Minute).UTC().Format(time.RFC3339), "status", "completedAt")
	return commit
}

func TestProjectReleasesOrdersEvidenceAndComputesLiveFromProductionImages(t *testing.T) {
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	oldSHA := strings.Repeat("1", 40)
	newSHA := strings.Repeat("2", 40)
	partialSHA := strings.Repeat("3", 40)
	project := projectForPromoteWithRepository("shop", "repo-a")
	oldImages := map[string]string{
		"frontendImage": "ghcr.io/acme/shop/frontend@sha256:old-front",
		"backendImage":  "ghcr.io/acme/shop/backend@sha256:old-back",
	}
	binding, err := projectTemplateProdBinding(project, applicationTemplateForPromote(), oldImages, map[string]any{"access": "public"})
	if err != nil {
		t.Fatalf("production binding: %v", err)
	}
	upsertProjectProductionBinding(project, binding)

	commits := []*unstructured.Unstructured{
		releaseCommitForTest("old", "repo-a", "Succeeded", oldSHA, base),
		releaseCommitForTest("new", "repo-a", "Succeeded", newSHA, base.Add(2*time.Hour)),
		releaseCommitForTest("partial", "repo-a", "Succeeded", partialSHA, base.Add(3*time.Hour)),
		releaseCommitForTest("failed", "repo-a", "Failed", strings.Repeat("4", 40), base.Add(4*time.Hour)),
		releaseCommitForTest("empty", "repo-a", "Succeeded", "", base.Add(5*time.Hour)),
	}
	spoofed := releaseCommitForTest("spoofed", "repo-b", "Succeeded", strings.Repeat("5", 40), base.Add(6*time.Hour))
	spoofed.SetLabels(map[string]string{codeLabelRepository: "repo-a"})
	commits = append(commits, spoofed)
	packages := []*unstructured.Unstructured{
		projectBuildPackageForTest("repo-a", "frontend", "ghcr.io/acme/shop/frontend",
			map[string]any{"digest": "sha256:new-front", "tags": []any{"sha-" + newSHA}},
			map[string]any{"digest": "sha256:old-front", "tags": []any{"sha-" + oldSHA}},
			map[string]any{"digest": "sha256:partial-front", "tags": []any{"sha-" + partialSHA}}),
		projectBuildPackageForTest("repo-a", "backend", "ghcr.io/acme/shop/backend",
			map[string]any{"digest": "sha256:new-back", "tags": []any{"sha-" + newSHA}},
			map[string]any{"digest": "sha256:old-back", "tags": []any{"sha-" + oldSHA}}),
	}
	client := newProjectBuildProvenanceClient(project, commits, packages)
	persisted, err := client.Projects().Get(context.Background(), project.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	response, err := (&Server{}).projectReleases(context.Background(), client, persisted)
	if err != nil {
		t.Fatalf("projectReleases: %v", err)
	}
	if len(response.Items) != 3 {
		t.Fatalf("release count = %d, want 3 (failed/empty/cross-repository commits must be filtered): %#v", len(response.Items), response.Items)
	}
	if response.Items[0].CommitSHA != partialSHA || response.Items[1].CommitSHA != newSHA || response.Items[2].CommitSHA != oldSHA {
		t.Fatalf("release order = %#v, want newest repository history first", response.Items)
	}
	bySHA := make(map[string]projectReleaseView, len(response.Items))
	for _, item := range response.Items {
		bySHA[item.CommitSHA] = item
	}
	if !bySHA[newSHA].Deployable || bySHA[newSHA].Live {
		t.Fatalf("new release = %#v, want deployable and not live while old images are configured", bySHA[newSHA])
	}
	if !bySHA[oldSHA].Deployable || !bySHA[oldSHA].Live {
		t.Fatalf("old release = %#v, want deployable and live", bySHA[oldSHA])
	}
	if bySHA[oldSHA].ReleaseID == "" || bySHA[newSHA].ReleaseID == "" || bySHA[partialSHA].ReleaseID != "" {
		t.Fatalf("release IDs = old %q, new %q, partial %q; want IDs only for deployable releases", bySHA[oldSHA].ReleaseID, bySHA[newSHA].ReleaseID, bySHA[partialSHA].ReleaseID)
	}
	partial := bySHA[partialSHA]
	if partial.Deployable || len(partial.Missing) != 1 || partial.Missing[0] != "backend" {
		t.Fatalf("partial release = %#v, want backend missing and not deployable", partial)
	}
}

func TestPromoteProjectSelectedHistoricalCommitResolvesFreshArtifacts(t *testing.T) {
	base := metav1.Now().Time
	oldSHA := strings.Repeat("1", 40)
	newSHA := strings.Repeat("2", 40)
	failedSHA := strings.Repeat("3", 40)
	project := projectForPromoteWithRepository("shop", "repo-a")
	currentImages := map[string]string{
		"frontendImage": "ghcr.io/acme/shop/frontend@sha256:new-front",
		"backendImage":  "ghcr.io/acme/shop/backend@sha256:new-back",
	}
	binding, err := projectTemplateProdBinding(project, applicationTemplateForPromote(), currentImages, map[string]any{"access": "public"})
	if err != nil {
		t.Fatalf("initial production binding: %v", err)
	}
	upsertProjectProductionBinding(project, binding)
	commits := []*unstructured.Unstructured{
		releaseCommitForTest("old", "repo-a", "Succeeded", oldSHA, base.Add(-time.Hour)),
		releaseCommitForTest("new", "repo-a", "Succeeded", newSHA, base),
		releaseCommitForTest("failed", "repo-a", "Failed", failedSHA, base.Add(time.Minute)),
	}
	packages := []*unstructured.Unstructured{
		projectBuildPackageForTest("repo-a", "frontend", "ghcr.io/acme/shop/frontend",
			map[string]any{"digest": "sha256:new-front", "tags": []any{"sha-" + newSHA}},
			map[string]any{"digest": "sha256:old-front", "tags": []any{"sha-" + oldSHA}}),
		projectBuildPackageForTest("repo-a", "backend", "ghcr.io/acme/shop/backend",
			map[string]any{"digest": "sha256:new-back", "tags": []any{"sha-" + newSHA}},
			map[string]any{"digest": "sha256:old-back", "tags": []any{"sha-" + oldSHA}}),
	}
	client := newProjectBuildProvenanceClient(project, commits, packages)
	persisted, err := client.Projects().Get(context.Background(), project.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	releases, err := (&Server{}).projectReleases(context.Background(), client, persisted)
	if err != nil || len(releases.Items) != 2 {
		t.Fatalf("release history before promotion = %#v, err=%v", releases, err)
	}
	var oldReleaseID string
	for _, release := range releases.Items {
		if release.CommitSHA == oldSHA {
			oldReleaseID = release.ReleaseID
		}
	}
	if oldReleaseID == "" {
		t.Fatal("missing historical release ID")
	}
	if _, _, missingEvidenceErr := (&Server{}).promoteProjectWithSelection(context.Background(), client, identity{}, persisted, nil, nil, oldSHA, true); missingEvidenceErr == nil || !strings.Contains(missingEvidenceErr.Error(), "releaseID is required") {
		t.Fatalf("promotion without releaseID error = %v, want release evidence validation", missingEvidenceErr)
	}
	updated, response, err := (&Server{}).promoteProjectWithSelection(context.Background(), client, identity{}, persisted, nil, nil, oldSHA, true, oldReleaseID)
	if err != nil {
		t.Fatalf("historical promotion: %v", err)
	}
	if response.CommitSHA != oldSHA {
		t.Fatalf("response commitSHA = %q, want %s", response.CommitSHA, oldSHA)
	}
	production := findProjectProductionBinding(updated)
	if production == nil {
		t.Fatal("historical promotion removed production binding")
	}
	values := projectBindingValues(production)
	if values["frontendImage"] != "ghcr.io/acme/shop/frontend@sha256:old-front" || values["backendImage"] != "ghcr.io/acme/shop/backend@sha256:old-back" {
		t.Fatalf("selected release images = %#v, want old immutable digests", values)
	}
	if values["access"] != "public" {
		t.Fatalf("existing production value access = %#v, want preserved", values["access"])
	}
	if response.ReleaseID != oldReleaseID {
		t.Fatalf("response releaseID = %q, want %q", response.ReleaseID, oldReleaseID)
	}

	for _, invalid := range []string{failedSHA, strings.Repeat("9", 40), ""} {
		_, _, invalidErr := (&Server{}).promoteProjectWithSelection(context.Background(), client, identity{}, updated, nil, nil, invalid, true, oldReleaseID)
		if invalidErr == nil || !strings.Contains(invalidErr.Error(), "commitSHA is required") && !strings.Contains(invalidErr.Error(), "not a successful commit") {
			t.Fatalf("selected commit %q error = %v, want validation rejection", invalid, invalidErr)
		}
	}

	// Repointing one commit tag changes the server's fresh component evidence;
	// the previously observed release ID must no longer authorize promotion.
	frontend, err := client.Resource(codePackageResource, "").Get(context.Background(), "repo-a-frontend", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get frontend package: %v", err)
	}
	versions, found, err := unstructured.NestedSlice(frontend.Object, "status", "versions")
	if err != nil || !found || len(versions) < 2 {
		t.Fatalf("frontend versions = %#v, found=%v, err=%v", versions, found, err)
	}
	version, ok := versions[1].(map[string]any)
	if !ok {
		t.Fatalf("frontend historical version = %#v, want object", versions[1])
	}
	version["digest"] = "sha256:repointed-front"
	if err := unstructured.SetNestedSlice(frontend.Object, versions, "status", "versions"); err != nil {
		t.Fatalf("set repointed frontend version: %v", err)
	}
	if _, err := client.Resource(codePackageResource, "").Update(context.Background(), frontend, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update repointed frontend package: %v", err)
	}
	_, _, staleErr := (&Server{}).promoteProjectWithSelection(context.Background(), client, identity{}, updated, nil, nil, oldSHA, true, oldReleaseID)
	if staleErr == nil || !strings.Contains(staleErr.Error(), "release evidence is stale") {
		t.Fatalf("repointed tag promotion error = %v, want stale release evidence rejection", staleErr)
	}
}

func TestPromoteProjectSelectedHistoricalCommitRejectsPartialArtifacts(t *testing.T) {
	project := projectForPromoteWithRepository("shop", "repo-a")
	commitSHA := strings.Repeat("1", 40)
	commit := releaseCommitForTest("old", "repo-a", "Succeeded", commitSHA, metav1.Now().Time)
	client := newProjectBuildProvenanceClient(project, []*unstructured.Unstructured{commit}, []*unstructured.Unstructured{
		projectBuildPackageForTest("repo-a", "frontend", "ghcr.io/acme/shop/frontend", map[string]any{"digest": "sha256:front", "tags": []any{"sha-" + commitSHA}}),
	})
	persisted, err := client.Projects().Get(context.Background(), project.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	_, _, err = (&Server{}).promoteProjectWithSelection(context.Background(), client, identity{}, persisted, nil, nil, commitSHA, true, "sha256:stale")
	if err == nil || !strings.Contains(err.Error(), "not ready to promote") {
		t.Fatalf("partial historical promotion error = %v, want not-ready validation", err)
	}
}

func TestPromoteProjectHandlerRejectsExplicitCommitWithoutReleaseEvidence(t *testing.T) {
	project := projectForPromoteWithRepository("shop", "repo-a")
	client := newProjectBuildProvenanceClient(project, nil, nil)
	server := &Server{projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/shop/promote", strings.NewReader(`{"commitSHA":"1111111111111111111111111111111111111111"}`))
	request = mux.SetURLVars(request, map[string]string{"project": "shop"})
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org-a:workspace-a")
	request.Header.Set("X-Faros-Cluster", "cluster-a")
	response := httptest.NewRecorder()
	server.promoteProjectHandler(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "releaseID is required") {
		t.Fatalf("handler response = %d %s, want release evidence validation", response.Code, response.Body.String())
	}
}

func TestProjectReleaseHandlersReturnAndPromoteExactEvidence(t *testing.T) {
	project := projectForPromoteWithRepository("shop", "repo-a")
	commitSHA := strings.Repeat("a", 40)
	commit := releaseCommitForTest("release", "repo-a", "Succeeded", commitSHA, metav1.Now().Time)
	packages := []*unstructured.Unstructured{
		projectBuildPackageForTest("repo-a", "frontend", "ghcr.io/acme/shop/frontend", map[string]any{"digest": "sha256:front", "tags": []any{"sha-" + commitSHA}}),
		projectBuildPackageForTest("repo-a", "backend", "ghcr.io/acme/shop/backend", map[string]any{"digest": "sha256:back", "tags": []any{"sha-" + commitSHA}}),
	}
	client := newProjectBuildProvenanceClient(project, []*unstructured.Unstructured{commit}, packages)
	server := &Server{projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/projects/shop/releases", nil)
	getRequest = mux.SetURLVars(getRequest, map[string]string{"project": "shop"})
	getRequest.Header.Set("X-Faros-Tenant", "root:faros:tenants:org-a:workspace-a")
	getRequest.Header.Set("X-Faros-Cluster", "cluster-a")
	getResponse := httptest.NewRecorder()
	server.getProjectReleases(getResponse, getRequest)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET releases response = %d %s", getResponse.Code, getResponse.Body.String())
	}
	var releases projectReleasesResponse
	if err := json.NewDecoder(getResponse.Body).Decode(&releases); err != nil {
		t.Fatalf("decode GET releases: %v", err)
	}
	if len(releases.Items) != 1 || releases.Items[0].CommitSHA != commitSHA || releases.Items[0].ReleaseID == "" {
		t.Fatalf("GET releases = %#v, want exact release evidence", releases.Items)
	}

	body, err := json.Marshal(projectPromoteRequest{CommitSHA: &commitSHA, ReleaseID: &releases.Items[0].ReleaseID})
	if err != nil {
		t.Fatalf("marshal promote request: %v", err)
	}
	postRequest := httptest.NewRequest(http.MethodPost, "/api/projects/shop/promote", bytes.NewReader(body))
	postRequest = mux.SetURLVars(postRequest, map[string]string{"project": "shop"})
	postRequest.Header.Set("X-Faros-Tenant", "root:faros:tenants:org-a:workspace-a")
	postRequest.Header.Set("X-Faros-Cluster", "cluster-a")
	postResponse := httptest.NewRecorder()
	server.promoteProjectHandler(postResponse, postRequest)
	if postResponse.Code != http.StatusOK {
		t.Fatalf("POST promote response = %d %s", postResponse.Code, postResponse.Body.String())
	}
	var promoted projectPromoteResponse
	if err := json.NewDecoder(postResponse.Body).Decode(&promoted); err != nil {
		t.Fatalf("decode POST promote: %v", err)
	}
	if promoted.CommitSHA != commitSHA || promoted.ReleaseID != releases.Items[0].ReleaseID || promoted.RolloutRevision == "" {
		t.Fatalf("POST promote = %#v, want selected commit/release identity and rollout revision", promoted)
	}
}

func TestProjectReleaseMatchesProductionRequiresConfiguredImageEquality(t *testing.T) {
	p := projectForPromote("shop")
	if projectReleaseMatchesProduction(p, []projectBuildCheckComponent{{Name: "frontend", ImageInput: "frontendImage", Built: true, Image: "image@sha256:a"}}) {
		t.Fatal("release without production binding reported live")
	}
	binding, err := projectTemplateProdBinding(p, applicationTemplateForPromote(), map[string]string{"frontendImage": "image@sha256:a"}, nil)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	upsertProjectProductionBinding(p, binding)
	if !projectReleaseMatchesProduction(p, []projectBuildCheckComponent{{Name: "frontend", ImageInput: "frontendImage", Built: true, Image: "image@sha256:a"}}) {
		t.Fatal("matching configured image did not report live")
	}
	if projectReleaseMatchesProduction(p, []projectBuildCheckComponent{{Name: "frontend", ImageInput: "frontendImage", Built: true, Image: "image@sha256:b"}}) {
		t.Fatal("different configured image reported live")
	}
}
