// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package provision

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
	"strings"
	"time"
	"unicode/utf8"
)

// Scaffold fetch — ported from app-studio's project_scaffold.go. Templates pin
// a tag-locked scaffold repo (spec.development.scaffold {repository, ref});
// its tree IS the workspace layout (web/, api/ …), so paths land verbatim.

const (
	scaffoldMaxFiles      = 400
	scaffoldMaxTotalBytes = 8 << 20
	scaffoldMaxFileBytes  = 1 << 20
	scaffoldMaxArchive    = 16 << 20
)

var scaffoldHTTPClient = &http.Client{Timeout: 60 * time.Second}

// scaffoldArchiveURLs derives candidate tarball URLs for repo@ref, tried in
// order. GitHub goes through codeload (tags, then branches, then raw ref);
// other hosts use the gitea/forgejo archive convention.
func ScaffoldArchiveURLs(repo, ref string) ([]string, error) {
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

// fetchScaffold downloads and unpacks the scaffold: text files only, archive
// root stripped, VCS/docs metadata excluded, size-capped.
func FetchScaffold(ctx context.Context, repo, ref string) ([]File, error) {
	urls, err := ScaffoldArchiveURLs(repo, ref)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, u := range urls {
		files, err := fetchScaffoldArchive(ctx, u)
		if err == nil {
			return files, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func fetchScaffoldArchive(ctx context.Context, archiveURL string) ([]File, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := scaffoldHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", archiveURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: status %d", archiveURL, resp.StatusCode)
	}
	gz, err := gzip.NewReader(io.LimitReader(resp.Body, scaffoldMaxArchive))
	if err != nil {
		return nil, fmt.Errorf("decompressing %s: %w", archiveURL, err)
	}
	defer gz.Close()

	var (
		files []File
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
		if name == "" || strings.HasPrefix(name, "..") || scaffoldSkippedPath(name) {
			continue
		}
		if len(files) >= scaffoldMaxFiles || total >= scaffoldMaxTotalBytes || hdr.Size > scaffoldMaxFileBytes {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, scaffoldMaxFileBytes+1))
		if err != nil {
			return nil, fmt.Errorf("reading %s from %s: %w", name, archiveURL, err)
		}
		if len(data) > scaffoldMaxFileBytes || !utf8.Valid(data) {
			continue
		}
		files = append(files, File{Path: name, Content: string(data)})
		total += len(data)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("archive %s contained no usable text files", archiveURL)
	}
	return files, nil
}

// scaffoldSkippedPath drops what belongs to the scaffold repository rather
// than to the project seeded from it: its licence and its README (the git host
// writes a fresh one on autoInit).
//
// `.github/` is deliberately KEPT. Every scaffold ships its own build workflow
// — smoke test, then a Railpack image per component pushed to
// ghcr.io/<owner>/<repo>/<component> tagged sha-<commit> — so a seeded project
// has working CI from its first commit and promotion has digests to pin.
// Committing under `.github/workflows/` requires the `workflow` scope on the
// Code provider connection; without it the git host refuses the whole commit,
// which the git checkpoint reports in those words.
func scaffoldSkippedPath(p string) bool {
	if p == "LICENSE" || p == "README.md" {
		return true
	}
	return strings.HasPrefix(p, ".git/")
}
