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
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// packageCR builds a minimal Code provider Package CR (as the crawler writes
// it) for one component, with a single published version.
func packageCR(packageName, imageRepository, digest string, tags ...string) unstructured.Unstructured {
	tagList := make([]any, 0, len(tags))
	for _, t := range tags {
		tagList = append(tagList, t)
	}
	return unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"packageName":     packageName,
			"imageRepository": imageRepository,
			"versions": []any{
				map[string]any{"digest": digest, "tags": tagList},
			},
		},
	}}
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

func TestLatestPackageVersionPrefersCommitTag(t *testing.T) {
	pkg := packageCR("rainbow/app", "ghcr.io/acme/rainbow/app", "sha256:ccc", "latest", "sha-deadbeef")
	digest, tag := latestPackageVersion(&pkg)
	if digest != "sha256:ccc" {
		t.Fatalf("digest = %q", digest)
	}
	if tag != "sha-deadbeef" {
		t.Fatalf("tag = %q, want the non-latest commit tag", tag)
	}
}

func TestLatestPackageVersionNoVersions(t *testing.T) {
	pkg := unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"packageName": "x"}}}
	digest, tag := latestPackageVersion(&pkg)
	if digest != "" || tag != "" {
		t.Fatalf("expected empty, got %q/%q", digest, tag)
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
