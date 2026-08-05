// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"strings"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/client"
)

// Built images.
//
// Nobody should have to paste an image reference into the ship panel. The
// scaffold's CI publishes one image per component tagged sha-<commit>, the
// code provider crawls the registry into Package records, and this resolves
// the two against the commit the workspace is actually on.
//
// Resolving BY COMMIT is what makes the digest tether exact rather than
// approximate: an image is offered only when it was built from the tree that
// is in git, so promotion cannot ship something untraceable. "The build for
// this commit has not published yet" is a real answer, and a better one than
// quietly offering an older image.

// buildTagPrefix is what the scaffolds' workflow tags an image with. The
// multi-arch index carries the bare tag; per-arch manifests carry suffixed
// variants (…-linux-arm64), which must not be promoted — they would pin
// production to one architecture.
const buildTagPrefix = "sha-"

// builtImage is one component's published image for a revision.
type builtImage struct {
	Component string
	// Ref is the pullable reference, pinned by digest.
	Ref string
}

// resolveBuiltImages maps component name → the image built from revision.
// Components with no published image for that commit are simply absent.
func resolveBuiltImages(ctx context.Context, cl *client.Client, p *vibev1alpha1.Project, revision string) map[string]string {
	if p == nil || p.Spec.Repository == nil || p.Spec.Repository.RepositoryRef == "" || revision == "" {
		return nil
	}
	packages, err := cl.ListPackages(ctx)
	if err != nil {
		return nil
	}
	return matchBuiltImages(packages, p.Spec.Repository.RepositoryRef, revision)
}

// matchBuiltImages is the pure half: pick each component's digest for the
// revision out of the workspace's packages.
func matchBuiltImages(packages []client.Package, repository, revision string) map[string]string {
	want := buildTagPrefix + revision
	out := map[string]string{}
	for _, pkg := range packages {
		if pkg.Repository != repository || pkg.ImageRepository == "" {
			continue
		}
		component := componentOfPackage(pkg.Name)
		if component == "" {
			continue
		}
		for _, v := range pkg.Versions {
			if v.Digest == "" {
				continue
			}
			for _, tag := range v.Tags {
				// Exact match only: sha-<commit>-linux-arm64 is a per-arch
				// manifest, not the image to run.
				if tag == want {
					out[component] = pkg.ImageRepository + "@" + v.Digest
				}
			}
		}
	}
	return out
}

// componentOfPackage reads the component out of a "<repo>/<component>"
// package name. A package with no slash belongs to the repository as a whole
// and names no component.
func componentOfPackage(name string) string {
	i := strings.LastIndex(name, "/")
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return name[i+1:]
}
