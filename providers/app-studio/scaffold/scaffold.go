/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package scaffold fetches a template's starter code and lays it into a
// project workspace. A dev-capable Template pins a tag-locked scaffold repo
// (spec.development.scaffold {repository, ref}); its tree IS the workspace
// layout (web/, api/ …), so paths land verbatim. This makes a new project open
// on a runnable placeholder instead of an empty directory.
package scaffold

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	maxFiles      = 400
	maxTotalBytes = 8 << 20
	maxFileBytes  = 1 << 20
	maxArchive    = 16 << 20
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

// ArchiveURLs derives candidate tarball URLs for repo@ref, tried in order.
// GitHub goes through codeload (tags, then branches, then raw ref); other
// hosts use the gitea/forgejo archive convention.
func ArchiveURLs(repo, ref string) ([]string, error) {
	repo = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(repo), "/"), ".git")
	if !strings.Contains(repo, "://") {
		repo = "https://" + repo
	}
	u, err := url.Parse(repo)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("scaffold repository %q is not a valid URL", repo)
	}
	if u.Host == "github.com" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 2 {
			return nil, fmt.Errorf("github scaffold repository %q must be owner/repo", repo)
		}
		base := "https://codeload.github.com/" + parts[0] + "/" + parts[1] + "/tar.gz/"
		if strings.TrimSpace(ref) == "" {
			return []string{base + "refs/heads/main"}, nil
		}
		esc := url.PathEscape(ref)
		return []string{base + "refs/tags/" + esc, base + "refs/heads/" + esc, base + esc}, nil
	}
	r := ref
	if strings.TrimSpace(r) == "" {
		r = "main"
	}
	return []string{repo + "/archive/" + url.PathEscape(r) + ".tar.gz"}, nil
}

// Fetch downloads and unpacks the scaffold: text files only, archive root
// stripped, VCS/docs metadata excluded, size-capped.
func Fetch(ctx context.Context, repo, ref string) ([]workspace.File, error) {
	urls, err := ArchiveURLs(repo, ref)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, u := range urls {
		files, err := fetchArchive(ctx, u)
		if err == nil {
			return files, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func fetchArchive(ctx context.Context, archiveURL string) ([]workspace.File, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", archiveURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: status %d", archiveURL, resp.StatusCode)
	}
	gz, err := gzip.NewReader(io.LimitReader(resp.Body, maxArchive))
	if err != nil {
		return nil, fmt.Errorf("decompressing %s: %w", archiveURL, err)
	}
	defer gz.Close()

	var (
		files []workspace.File
		total int
	)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", archiveURL, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		// Strip the archive root directory (<repo>-<ref>/); rootless entries skip.
		slash := strings.Index(name, "/")
		if slash < 0 {
			continue
		}
		name = name[slash+1:]
		if name == "" || strings.HasPrefix(name, "..") || skippedPath(name) {
			continue
		}
		if len(files) >= maxFiles || total >= maxTotalBytes || hdr.Size > maxFileBytes {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxFileBytes+1))
		if err != nil {
			return nil, fmt.Errorf("reading %s from %s: %w", name, archiveURL, err)
		}
		if len(data) > maxFileBytes || !utf8.Valid(data) {
			continue
		}
		files = append(files, workspace.File{Path: name, Content: string(data)})
		total += len(data)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("archive %s contained no usable text files", archiveURL)
	}
	return files, nil
}

// skippedPath drops what belongs to the scaffold repository rather than to the
// project seeded from it: its licence and its README (the git host writes a
// fresh one on autoInit).
//
// `.github/` is deliberately KEPT — every scaffold ships its own build
// workflow so a seeded project has working CI from its first commit and
// promotion has image digests to pin.
func skippedPath(p string) bool {
	if p == "LICENSE" || p == "README.md" {
		return true
	}
	return strings.HasPrefix(p, ".git/")
}

// CheckLayout verifies the scaffold matches the template: at least one file
// falls under a declared component's workspace directory (a component that
// claims "." — the whole workspace — always passes). componentPaths maps
// component name → its workspacePath.
func CheckLayout(componentPaths map[string]string, files []workspace.File) error {
	if len(files) == 0 || len(componentPaths) == 0 {
		return nil
	}
	for _, wp := range componentPaths {
		if strings.TrimSpace(wp) == "." || strings.TrimSpace(wp) == "" {
			return nil // a root component claims everything
		}
	}
	for _, f := range files {
		for _, wp := range componentPaths {
			prefix := strings.TrimSuffix(wp, "/") + "/"
			if strings.HasPrefix(f.Path, prefix) {
				return nil
			}
		}
	}
	dirs := map[string]bool{}
	for _, f := range files {
		if i := strings.Index(f.Path, "/"); i > 0 {
			dirs[f.Path[:i]] = true
		}
	}
	have := make([]string, 0, len(dirs))
	for d := range dirs {
		have = append(have, d)
	}
	sort.Strings(have)
	want := make([]string, 0, len(componentPaths))
	for name, wp := range componentPaths {
		want = append(want, fmt.Sprintf("%s (%s/)", name, strings.TrimSuffix(wp, "/")))
	}
	sort.Strings(want)
	return fmt.Errorf("scaffold layout does not match the template: none of its %d files fall under any component directory. "+
		"Template components: %s. Scaffold top-level directories: %s",
		len(files), strings.Join(want, ", "), strings.Join(have, ", "))
}
