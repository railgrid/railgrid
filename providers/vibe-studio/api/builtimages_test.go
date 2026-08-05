// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"

	"github.com/faroshq/provider-vibe-studio/client"
)

const testSHA = "f00e9e7f801d2602ddc05466557d244c48a14cb6"

func testPackages() []client.Package {
	return []client.Package{
		{
			Repository: "tinder-clone", Name: "tinder-clone/api",
			ImageRepository: "ghcr.io/mjudeikis/tinder-clone/api",
			Versions: []client.PackageVersion{
				{Digest: "sha256:aaa", Tags: []string{"sha-" + testSHA, "latest"}},
				// Per-arch manifests carry a suffixed variant of the same tag.
				{Digest: "sha256:arm", Tags: []string{"sha-" + testSHA + "-linux-arm64", "latest-linux-arm64"}},
				{Digest: "sha256:old", Tags: []string{"sha-1111111111111111111111111111111111111111"}},
			},
		},
		{
			Repository: "tinder-clone", Name: "tinder-clone/web",
			ImageRepository: "ghcr.io/mjudeikis/tinder-clone/web",
			Versions: []client.PackageVersion{
				{Digest: "sha256:bbb", Tags: []string{"sha-" + testSHA, "latest"}},
			},
		},
		{ // another project's repository entirely
			Repository: "slides-page", Name: "slides-page/app",
			ImageRepository: "ghcr.io/mjudeikis/slides-page/app",
			Versions:        []client.PackageVersion{{Digest: "sha256:ccc", Tags: []string{"sha-" + testSHA}}},
		},
	}
}

func TestMatchBuiltImagesPinsTheCommitsDigest(t *testing.T) {
	got := matchBuiltImages(testPackages(), "tinder-clone", testSHA)
	if len(got) != 2 {
		t.Fatalf("images = %v, want api and web", got)
	}
	if got["api"] != "ghcr.io/mjudeikis/tinder-clone/api@sha256:aaa" {
		t.Errorf("api = %q, want the multi-arch index digest, not the arm64 manifest", got["api"])
	}
	if got["web"] != "ghcr.io/mjudeikis/tinder-clone/web@sha256:bbb" {
		t.Errorf("web = %q", got["web"])
	}
}

func TestMatchBuiltImagesIgnoresOtherRepositories(t *testing.T) {
	if got := matchBuiltImages(testPackages(), "tinder-clone", testSHA); got["app"] != "" {
		t.Errorf("another repository's package leaked in: %v", got)
	}
}

func TestMatchBuiltImagesRefusesAnUnbuiltCommit(t *testing.T) {
	// The tether: no image for this tree means nothing is offered, rather
	// than an older image being passed off as this commit's.
	if got := matchBuiltImages(testPackages(), "tinder-clone", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); len(got) != 0 {
		t.Errorf("images = %v, want none for a commit with no build", got)
	}
}

func TestComponentOfPackage(t *testing.T) {
	cases := map[string]string{
		"tinder-clone/api": "api",
		"repo/web":         "web",
		"repo":             "", // repository-wide package, names no component
		"repo/":            "",
	}
	for in, want := range cases {
		if got := componentOfPackage(in); got != want {
			t.Errorf("componentOfPackage(%q) = %q, want %q", in, got, want)
		}
	}
}
