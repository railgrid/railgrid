// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"bytes"
	"embed"
	"io/fs"
	"strings"
	"time"

	"github.com/railgrid/provider-kuery/queryapi"
)

// portalFSEmbed embeds the Vite build output. The portal/ subdirectory holds a
// standalone npm project (Vite + TypeScript); see portal/README.md.
//
// `all:` so dotfiles (.gitkeep) are bundled too — without that the embed
// would fail at compile time when the dist/ directory exists but is empty.
// Run `npm --prefix portal install && npm --prefix portal run build` (or
// `make build-kuery-provider` from the repo root) to populate dist/
// before `go build`; the Makefile target chains the two.
//
//go:embed all:portal/dist
var portalFSEmbed embed.FS

// portalFS returns the embedded bundle rooted at the dist directory, with the
// QuerySpec JSON Schema overlaid onto it as a file.
//
// The schema is a static asset beside the bundle, not a route of its own: the
// provider's closed route list has no class for "one more JSON document"
// (docs/provider-connectivity-contract.md §"Pillar 2 route classes"), and the
// hub's UI proxy already routes any path whose last segment contains a "." to
// this binary, so /query-schema.json reaches the browser exactly like
// cytoscape.min.js does. Overlaying it from the Go constant rather than
// copying it into portal/public keeps one source of truth: the bytes the
// savedview reconciler validates against are the bytes the editor completes
// from.
func portalFS() (fs.FS, error) {
	dist, err := fs.Sub(portalFSEmbed, "portal/dist")
	if err != nil {
		return nil, err
	}
	return overlayFS{
		FS:   dist,
		name: strings.TrimPrefix(queryapi.SchemaPath, "/"),
		data: []byte(queryapi.QuerySpecSchema),
	}, nil
}

// overlayFS serves one in-memory file in front of another fs.FS.
type overlayFS struct {
	fs.FS
	name string
	data []byte
}

func (o overlayFS) Open(name string) (fs.File, error) {
	if name == o.name {
		return &memFile{name: name, Reader: bytes.NewReader(o.data)}, nil
	}
	return o.FS.Open(name) //nolint:wrapcheck // pass the fs error through, including fs.ErrNotExist
}

// memFile is the overlaid file. Read comes from the embedded bytes.Reader.
type memFile struct {
	name string
	*bytes.Reader
}

func (f *memFile) Stat() (fs.FileInfo, error) { return memInfo{name: f.name, size: f.Size()}, nil }
func (f *memFile) Close() error               { return nil }

type memInfo struct {
	name string
	size int64
}

func (i memInfo) Name() string       { return i.name }
func (i memInfo) Size() int64        { return i.size }
func (i memInfo) Mode() fs.FileMode  { return 0o444 }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return false }
func (i memInfo) Sys() any           { return nil }
