// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

const ScreenshotsName = "screenshots.json"
const MaxScreenshotsBytes = 12 << 20
const screenshotDirectory = "artifacts/screenshots"

var screenshotName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.(png|jpg|jpeg)$`)

// Screenshot is a bounded image exported separately from the Git snapshot.
type Screenshot struct {
	Path      string `json:"path"`
	MediaType string `json:"mediaType"`
	Data      []byte `json:"data"`
}

func exportScreenshots(cfg Config, request StartRequest, workdir string) (*gitResultArtifact, error) {
	// OpenRoot prevents symlink traversal outside the worktree even during races.
	root, err := os.OpenRoot(workdir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var directoryInfo os.FileInfo
	for _, p := range []string{"artifacts", screenshotDirectory} {
		info, err := root.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		directoryInfo = info
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("screenshot directory must not be a symlink: %s", p)
		}
	}
	screenshots, err := root.OpenRoot(screenshotDirectory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = screenshots.Close() }()
	openedInfo, err := screenshots.Stat(".")
	if err != nil || !os.SameFile(directoryInfo, openedInfo) {
		return nil, errors.New("screenshot directory changed during export")
	}
	dir, err := screenshots.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(9)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 8 {
		return nil, errors.New("at most 8 screenshots are supported")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var images []Screenshot
	total := 0
	for _, entry := range entries {
		if !screenshotName.MatchString(entry.Name()) || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("invalid screenshot file %q", entry.Name())
		}
		path := screenshotDirectory + "/" + entry.Name()
		before, err := screenshots.Lstat(entry.Name())
		if err != nil || !before.Mode().IsRegular() {
			return nil, errors.New("screenshot must be a regular file")
		}
		f, err := screenshots.Open(entry.Name())
		if err != nil {
			return nil, err
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
			_ = f.Close()
			return nil, errors.New("screenshot must be a regular file")
		}
		data, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		total += len(data)
		if len(data) == 0 || len(data) > 2<<20 || total > 8<<20 {
			return nil, errors.New("screenshots exceed limits: 2 MiB each, 8 MiB total")
		}
		media := http.DetectContentType(data)
		if media != "image/png" && media != "image/jpeg" {
			return nil, fmt.Errorf("screenshot %s is not PNG or JPEG", path)
		}
		images = append(images, Screenshot{Path: path, MediaType: media, Data: data})
	}
	if len(images) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(images)
	if err != nil {
		return nil, err
	}
	if cfg.MaxArtifactSize > 0 && int64(len(data)) > cfg.MaxArtifactSize {
		return nil, errors.New("screenshot artifact exceeds runner limit")
	}
	dirPath := filepath.Join(cfg.StateDir, "artifacts", request.AttemptID)
	f, err := os.CreateTemp(dirPath, ".screenshots-*.json")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return nil, err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(name)
		return nil, err
	}
	item, err := makeGitResultArtifact(ScreenshotsName, name, "application/json")
	if err != nil {
		_ = os.Remove(name)
		return nil, err
	}
	return &item, nil
}
