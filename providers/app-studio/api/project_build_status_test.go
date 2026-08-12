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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/yaml"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/tenant"
)

// packageCR builds a minimal Code provider Package CR (as the crawler writes
// it) for one component, with a single published version.
func packageCR(packageName, imageRepository, digest string, tags ...string) unstructured.Unstructured {
	tagList := make([]any, 0, len(tags))
	for _, t := range tags {
		tagList = append(tagList, t)
	}
	return packageCRWithVersions(packageName, imageRepository, map[string]any{"digest": digest, "tags": tagList})
}

func packageCRWithVersions(packageName, imageRepository string, versions ...map[string]any) unstructured.Unstructured {
	versionList := make([]any, 0, len(versions))
	for _, version := range versions {
		versionList = append(versionList, version)
	}
	return unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"packageName":     packageName,
			"imageRepository": imageRepository,
			"versions":        versionList,
		},
	}}
}

func packageCRForRepository(repositoryRef, packageName, imageRepository, digest string, tags ...string) unstructured.Unstructured {
	pkg := packageCR(packageName, imageRepository, digest, tags...)
	pkg.SetLabels(map[string]string{codeLabelRepository: repositoryRef})
	pkg.Object["spec"] = map[string]any{"repositoryRef": repositoryRef}
	return pkg
}

func repositoryCommitForBuildTest(name, labelRepository, specRepository, phase, commitSHA string, createdAt time.Time) *unstructured.Unstructured {
	commit := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":              name,
			"creationTimestamp": createdAt.UTC().Format(time.RFC3339Nano),
			"labels":            map[string]any{codeLabelRepository: labelRepository},
		},
		"spec": map[string]any{"repositoryRef": specRepository},
		"status": map[string]any{
			"phase":     phase,
			"commitSHA": commitSHA,
		},
	}}
	commit.SetAPIVersion(codeSchemeGroupVersion.String())
	commit.SetKind("RepositoryCommit")
	return commit
}

func projectBuildPackageForTest(repositoryRef, component, imageRepository string, versions ...map[string]any) *unstructured.Unstructured {
	pkg := packageCRWithVersions(repositoryRef+"/"+component, imageRepository, versions...)
	pkg.SetAPIVersion(codeSchemeGroupVersion.String())
	pkg.SetKind("Package")
	pkg.SetName(repositoryRef + "-" + component)
	pkg.SetLabels(map[string]string{codeLabelRepository: repositoryRef})
	pkg.Object["spec"] = map[string]any{"repositoryRef": repositoryRef}
	return &pkg
}

func newProjectBuildProvenanceClient(project *aiv1alpha1.Project, commits []*unstructured.Unstructured, packages []*unstructured.Unstructured) *asclient.Client {
	projectRaw, _ := json.Marshal(project)
	projectObject := &unstructured.Unstructured{Object: map[string]any{}}
	_ = json.Unmarshal(projectRaw, &projectObject.Object)
	projectObject.SetAPIVersion(aiv1alpha1.SchemeGroupVersion.String())
	projectObject.SetKind("Project")
	objects := make([]runtime.Object, 0, 2+len(commits)+len(packages))
	objects = append(objects, applicationTemplateObject(), projectObject)
	for _, commit := range commits {
		objects = append(objects, commit)
	}
	for _, pkg := range packages {
		objects = append(objects, pkg)
	}
	return asclient.NewFromDynamic(fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			asclient.ProjectGVR:      "ProjectList",
			templatesGVR:             "TemplateList",
			codeRepositoryCommitsGVR: "RepositoryCommitList",
			codePackagesGVR:          "PackageList",
		},
		objects...,
	))
}

func TestFindPackageForComponentMatchesSuffix(t *testing.T) {
	items := []unstructured.Unstructured{
		packageCR("rainbow/frontend", "ghcr.io/acme/rainbow/frontend", "sha256:aaa", "sha-abc"),
		packageCR("rainbow/backend", "ghcr.io/acme/rainbow/backend", "sha256:bbb", "sha-abc"),
	}
	pkg := findPackageForComponent(items, "backend")
	if pkg == nil {
		t.Fatal("backend package not found")
	}
	name, _, _ := unstructured.NestedString(pkg.Object, "status", "packageName")
	if name != "rainbow/backend" {
		t.Fatalf("matched %q, want rainbow/backend", name)
	}
	if findPackageForComponent(items, "worker") != nil {
		t.Fatal("worker should have no package")
	}
}

func TestFindPackageForComponentInRepositoryRejectsCrossRepositoryPackages(t *testing.T) {
	spoofed := packageCR("repo-b/app", "ghcr.io/acme/repo-b/app", "sha256:spoof", "sha-spoof")
	spoofed.SetLabels(map[string]string{codeLabelRepository: "repo-a"})
	spoofed.Object["spec"] = map[string]any{"repositoryRef": "repo-b"}
	items := []unstructured.Unstructured{
		spoofed,
		packageCRForRepository("repo-a", "repo-a/app", "ghcr.io/acme/repo-a/app", "sha256:aaa", "sha-aaa"),
		packageCRForRepository("repo-b", "repo-b/app", "ghcr.io/acme/repo-b/app", "sha256:bbb", "sha-bbb"),
	}
	pkg := findPackageForComponentInRepository(items, "app", "repo-a")
	if pkg == nil {
		t.Fatal("repo-a app package not found")
	}
	image, _, _ := unstructured.NestedString(pkg.Object, "status", "imageRepository")
	if image != "ghcr.io/acme/repo-a/app" {
		t.Fatalf("matched image repository = %q, want repo-a image", image)
	}
	if pkg := findPackageForComponentInRepository(items[0:1], "app", "repo-a"); pkg != nil {
		t.Fatalf("package with mismatched spec.repositoryRef cross-selected for repo-a: %#v", pkg.Object)
	}
	if pkg := findPackageForComponentInRepository(items[2:], "app", "repo-a"); pkg != nil {
		t.Fatalf("repo-b package cross-selected for repo-a: %#v", pkg.Object)
	}
}

func TestResolveProjectComponentImagesKeepsPackagesBoundToProjectRepository(t *testing.T) {
	spoofed := packageCR("repo-b/app", "ghcr.io/acme/repo-b/app", "sha256:spoof", "sha-spoof")
	spoofed.SetLabels(map[string]string{codeLabelRepository: "repo-a"})
	spoofed.Object["spec"] = map[string]any{"repositoryRef": "repo-b"}
	packages := []unstructured.Unstructured{
		spoofed,
		packageCRForRepository("repo-a", "repo-a/app", "ghcr.io/acme/repo-a/app", "sha256:aaa", "sha-current"),
		packageCRForRepository("repo-b", "repo-b/app", "ghcr.io/acme/repo-b/app", "sha256:bbb", "sha-bbb"),
	}
	commit := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":   "commit-current",
			"labels": map[string]any{codeLabelRepository: "repo-a"},
		},
		"spec":   map[string]any{"repositoryRef": "repo-a"},
		"status": map[string]any{"phase": "Succeeded", "commitSHA": "current"},
	}}
	listYAML, err := yaml.Marshal(packages)
	if err != nil {
		t.Fatalf("marshal packages: %v", err)
	}
	commitYAML, err := yaml.Marshal([]unstructured.Unstructured{commit})
	if err != nil {
		t.Fatalf("marshal repository commits: %v", err)
	}
	const selector = codeLabelRepository + "=repo-a"
	graphql := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode GraphQL request: %v", err)
		}
		if req.Variables["labelSelector"] != selector {
			t.Fatalf("labelSelector variable = %#v, want %q", req.Variables["labelSelector"], selector)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "RepositoryCommitsYaml") {
			_, _ = fmt.Fprintf(w, `{"data":{"code_faros_sh":{"v1alpha1":{"RepositoryCommitsYaml":%q}}}}`, string(commitYAML))
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":{"code_faros_sh":{"v1alpha1":{"PackagesYaml":%q}}}}`, string(listYAML))
	}))
	t.Cleanup(graphql.Close)
	scope, err := tenant.NewGraphQLClient(graphql.URL, false).For("cluster-id", "caller-token")
	if err != nil {
		t.Fatalf("create GraphQL scope: %v", err)
	}
	project := &aiv1alpha1.Project{Spec: aiv1alpha1.ProjectSpec{
		Repository: &aiv1alpha1.ProjectRepositoryBinding{RepositoryRef: "repo-a"},
	}}
	images, err := (&Server{}).resolveProjectComponentImages(
		context.Background(),
		asclient.NewFromGraphQL(scope),
		project,
		[]projectBuildComponent{{Name: "app"}},
	)
	if err != nil {
		t.Fatalf("resolve component images: %v", err)
	}
	if got := images["app"].Image; got != "ghcr.io/acme/repo-a/app@sha256:aaa" {
		t.Fatalf("resolved app image = %q, want repo-a image", got)
	}
}

func TestCurrentProjectRepositoryCommitSHASelectsNewestSuccessfulScopedCommit(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	old := repositoryCommitForBuildTest("old", "repo-a", "repo-a", "Succeeded", "commit-old", base.Add(-5*time.Hour))
	newestSuccessful := repositoryCommitForBuildTest("newest-success", "repo-a", "repo-a", "Succeeded", "commit-current", base.Add(-4*time.Hour))
	failed := repositoryCommitForBuildTest("newer-failed", "repo-a", "repo-a", "Failed", "commit-failed", base.Add(-3*time.Hour))
	running := repositoryCommitForBuildTest("newer-running", "repo-a", "repo-a", "Running", "commit-running", base.Add(-2*time.Hour))
	labelMismatch := repositoryCommitForBuildTest("label-mismatch", "repo-b", "repo-a", "Succeeded", "commit-other-label", base.Add(-30*time.Minute))
	specMismatch := repositoryCommitForBuildTest("spec-mismatch", "repo-a", "repo-b", "Succeeded", "commit-other-spec", base.Add(-15*time.Minute))

	if repositoryCommitBelongsToRepository(labelMismatch, "repo-a") || repositoryCommitBelongsToRepository(specMismatch, "repo-a") {
		t.Fatal("cross-repository label/spec mismatch was accepted")
	}
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{codeRepositoryCommitsGVR: "RepositoryCommitList"},
		old, newestSuccessful, failed, running, labelMismatch, specMismatch,
	)
	got, err := currentProjectRepositoryCommitSHA(
		context.Background(),
		asclient.NewFromDynamic(dynamicClient),
		"repo-a",
	)
	if err != nil {
		t.Fatalf("currentProjectRepositoryCommitSHA: %v", err)
	}
	if got != "commit-current" {
		t.Fatalf("current commit SHA = %q, want newest successful nonempty scoped commit", got)
	}
}

func TestCurrentProjectRepositoryCommitSHANewestSuccessfulEmptySHAFailsClosed(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	older := repositoryCommitForBuildTest("older-success", "repo-a", "repo-a", "Succeeded", "commit-old", base.Add(-time.Hour))
	newestEmpty := repositoryCommitForBuildTest("newest-empty", "repo-a", "repo-a", "Succeeded", "", base)
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{codeRepositoryCommitsGVR: "RepositoryCommitList"},
		older, newestEmpty,
	)
	got, err := currentProjectRepositoryCommitSHA(context.Background(), asclient.NewFromDynamic(dynamicClient), "repo-a")
	if err != nil {
		t.Fatalf("currentProjectRepositoryCommitSHA: %v", err)
	}
	if got != "" {
		t.Fatalf("current commit SHA = %q, want empty for newest successful record with empty SHA", got)
	}
}

func TestPackageVersionForCommitIgnoresNewerUnrelatedVersion(t *testing.T) {
	pkg := packageCRWithVersions(
		"rainbow/app",
		"ghcr.io/acme/rainbow/app",
		map[string]any{"digest": "sha256:newer", "tags": []any{"latest", "sha-unrelated"}},
		map[string]any{"digest": "sha256:reviewed", "tags": []any{"sha-reviewed-commit"}},
	)
	digest, tag := packageVersionForCommit(&pkg, "reviewed-commit")
	if digest != "sha256:reviewed" || tag != "sha-reviewed-commit" {
		t.Fatalf("version for reviewed commit = %q/%q, want sha256:reviewed/sha-reviewed-commit", digest, tag)
	}
}

func TestPackageVersionForCommitMissingBuildDoesNotFallBackToNewest(t *testing.T) {
	pkg := packageCRWithVersions(
		"rainbow/app",
		"ghcr.io/acme/rainbow/app",
		map[string]any{"digest": "sha256:newer", "tags": []any{"latest", "sha-unrelated"}},
	)
	digest, tag := packageVersionForCommit(&pkg, "reviewed-commit")
	if digest != "" || tag != "" {
		t.Fatalf("missing reviewed commit version = %q/%q, want empty", digest, tag)
	}
	status, missing := statusFor([]projectBuildComponent{{Name: "app"}}, map[string]componentImageRef{})
	if status != "none" || missing != 1 {
		t.Fatalf("missing reviewed commit status = %q/%d, want none/1", status, missing)
	}
}

// componentsFromImages applies checkProjectBuild's built/incomplete/none logic
// over a resolved image map, so the status decision is tested without the live
// package-list round-trip.
func statusFor(components []projectBuildComponent, images map[string]componentImageRef) (status string, missing int) {
	built := 0
	for _, comp := range components {
		if img, ok := images[comp.Name]; ok && img.Image != "" {
			built++
		} else {
			missing++
		}
	}
	switch {
	case built == len(components):
		return "built", missing
	case built > 0:
		return "incomplete", missing
	default:
		return "none", missing
	}
}

func TestBuildStatusDecision(t *testing.T) {
	components := projectBuildComponents(applicationTemplateInfo()) // frontend + backend
	all := map[string]componentImageRef{
		"frontend": {Image: "ghcr.io/acme/rainbow/frontend@sha256:aaa"},
		"backend":  {Image: "ghcr.io/acme/rainbow/backend@sha256:bbb"},
	}
	if s, _ := statusFor(components, all); s != "built" {
		t.Fatalf("status = %q, want built", s)
	}
	partial := map[string]componentImageRef{"backend": {Image: "ghcr.io/acme/rainbow/backend@sha256:bbb"}}
	if s, m := statusFor(components, partial); s != "incomplete" || m != 1 {
		t.Fatalf("status = %q missing = %d, want incomplete/1", s, m)
	}
	if s, m := statusFor(components, map[string]componentImageRef{}); s != "none" || m != 2 {
		t.Fatalf("status = %q missing = %d, want none/2", s, m)
	}
}
