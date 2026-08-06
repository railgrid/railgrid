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

// Package skills contains the framework-neutral App Studio skill catalog.
//
// The package deliberately models only bounded, declarative skill metadata and
// content. Eino-specific authority fields (context, agent, and model) are
// rejected while loading instead of being interpreted by this package.
package skills

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Scope identifies the trust/domain scope from which a skill was loaded.
// System entries are ordered before project entries in every catalog snapshot.
type Scope string

const (
	ScopeSystem  Scope = "system"
	ScopeProject Scope = "project"
)

// Limits bounds discovery, parsing, and resource materialization. Zero values
// are replaced with Defaults. Values above their corresponding hard maximum
// are clamped, so a caller cannot accidentally disable the safety limits.
type Limits struct {
	MaxPackages         int
	MaxWarnings         int
	MaxSkillBytes       int
	MaxAggregateBytes   int
	MaxResourceBytes    int
	MaxResourceCount    int
	MaxResourceRead     int
	MaxProjectFileCount int
}

const (
	DefaultMaxPackages         = 64
	DefaultMaxWarnings         = 64
	DefaultMaxSkillBytes       = 32 << 10
	DefaultMaxAggregateBytes   = 4 << 20
	DefaultMaxResourceBytes    = 64 << 10
	DefaultMaxResourceCount    = 256
	DefaultMaxResourceRead     = 64 << 10
	DefaultMaxProjectFileCount = 500

	HardMaxPackages         = 256
	HardMaxWarnings         = 256
	HardMaxSkillBytes       = 256 << 10
	HardMaxAggregateBytes   = 16 << 20
	HardMaxResourceBytes    = 1 << 20
	HardMaxResourceCount    = 1000
	HardMaxResourceRead     = 64 << 10
	HardMaxProjectFileCount = 500
)

// DefaultLimits returns the bounded defaults used by NewCatalog.
func DefaultLimits() Limits {
	return Limits{
		MaxPackages:         DefaultMaxPackages,
		MaxWarnings:         DefaultMaxWarnings,
		MaxSkillBytes:       DefaultMaxSkillBytes,
		MaxAggregateBytes:   DefaultMaxAggregateBytes,
		MaxResourceBytes:    DefaultMaxResourceBytes,
		MaxResourceCount:    DefaultMaxResourceCount,
		MaxResourceRead:     DefaultMaxResourceRead,
		MaxProjectFileCount: DefaultMaxProjectFileCount,
	}
}

func (l Limits) bounded() Limits {
	d := DefaultLimits()
	if l.MaxPackages <= 0 {
		l.MaxPackages = d.MaxPackages
	}
	if l.MaxWarnings <= 0 {
		l.MaxWarnings = d.MaxWarnings
	}
	if l.MaxSkillBytes <= 0 {
		l.MaxSkillBytes = d.MaxSkillBytes
	}
	if l.MaxAggregateBytes <= 0 {
		l.MaxAggregateBytes = d.MaxAggregateBytes
	}
	if l.MaxResourceBytes <= 0 {
		l.MaxResourceBytes = d.MaxResourceBytes
	}
	if l.MaxResourceCount <= 0 {
		l.MaxResourceCount = d.MaxResourceCount
	}
	if l.MaxResourceRead <= 0 {
		l.MaxResourceRead = d.MaxResourceRead
	}
	if l.MaxProjectFileCount <= 0 {
		l.MaxProjectFileCount = d.MaxProjectFileCount
	}
	if l.MaxPackages > HardMaxPackages {
		l.MaxPackages = HardMaxPackages
	}
	if l.MaxWarnings > HardMaxWarnings {
		l.MaxWarnings = HardMaxWarnings
	}
	if l.MaxSkillBytes > HardMaxSkillBytes {
		l.MaxSkillBytes = HardMaxSkillBytes
	}
	if l.MaxAggregateBytes > HardMaxAggregateBytes {
		l.MaxAggregateBytes = HardMaxAggregateBytes
	}
	if l.MaxResourceBytes > HardMaxResourceBytes {
		l.MaxResourceBytes = HardMaxResourceBytes
	}
	if l.MaxResourceCount > HardMaxResourceCount {
		l.MaxResourceCount = HardMaxResourceCount
	}
	if l.MaxResourceRead > HardMaxResourceRead {
		l.MaxResourceRead = HardMaxResourceRead
	}
	if l.MaxProjectFileCount > HardMaxProjectFileCount {
		l.MaxProjectFileCount = HardMaxProjectFileCount
	}
	return l
}

// Warning is a bounded, public-safe load warning. Paths are source-relative;
// no store roots or host paths are ever included.
type Warning struct {
	Scope       Scope  `json:"scope"`
	PackagePath string `json:"packagePath,omitempty"`
	Code        string `json:"code"`
	Message     string `json:"message"`
}

// ResourceRef describes a supporting resource before it is materialized into
// a snapshot. Path is relative to the containing skill package.
type ResourceRef struct {
	Path string `json:"path"`
	Size int64  `json:"size,omitempty"`
}

// Package is one source package. SkillContent is the raw SKILL.md document;
// resources are package-relative and contain no filesystem locators.
type Package struct {
	Path         string        `json:"path"`
	SkillContent []byte        `json:"-"`
	Resources    []ResourceRef `json:"resources,omitempty"`
	// Enabled is optional for sources that do not carry activation metadata.
	// EnabledSet distinguishes the absent (backwards-compatible enabled) state
	// from an explicit disabled package.
	Enabled    bool   `json:"enabled,omitempty"`
	EnabledSet bool   `json:"-"`
	Version    string `json:"version,omitempty"`
}

// PackageList is returned by a Source. Warnings are already source-relative
// and are merged into the catalog warning budget.
type PackageList struct {
	Packages  []Package
	Warnings  []Warning
	Truncated bool
}

// ResourceReadOptions bounds one supporting-resource read. Offset and Limit
// are bytes, not runes. A zero Limit uses the catalog default.
type ResourceReadOptions struct {
	Offset int64
	Limit  int
}

// ResourceReadResult is a public-safe, paginated resource response. Content is
// copied for each call and can therefore not mutate the immutable snapshot.
type ResourceReadResult struct {
	Path       string `json:"path"`
	Content    []byte `json:"content,omitempty"`
	Size       int64  `json:"size"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"nextOffset,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// Resource is immutable metadata for one supporting resource.
type Resource struct {
	Path   string `json:"path"`
	Size   int64  `json:"size,omitempty"`
	Digest string `json:"sha256,omitempty"`
}

// Entry is one parsed skill in a Snapshot. PackagePath is relative to the
// source root. QualifiedName is stable and is the lookup key when names
// collide across system/project scopes (or within one scope).
type Entry struct {
	Name          string     `json:"name"`
	QualifiedName string     `json:"qualifiedName"`
	Description   string     `json:"description"`
	Scope         Scope      `json:"scope"`
	PackagePath   string     `json:"packagePath"`
	PackageName   string     `json:"packageName,omitempty"`
	Enabled       bool       `json:"enabled"`
	Editable      bool       `json:"editable"`
	Version       string     `json:"version,omitempty"`
	Content       string     `json:"content"`
	ContentDigest string     `json:"contentSha256"`
	Digest        string     `json:"sha256"`
	Resources     []Resource `json:"resources,omitempty"`
}

// Snapshot is an immutable catalog result. Catalog.Load returns a deep copy;
// callers can safely retain or mutate their copy without changing subsequent
// snapshots. The unexported maps back resource reads with copied bytes.
type Snapshot struct {
	Entries        []Entry   `json:"entries"`
	Warnings       []Warning `json:"warnings,omitempty"`
	CatalogDigest  string    `json:"catalogSha256"`
	ContentDigest  string    `json:"contentSha256"`
	ResourceDigest string    `json:"resourceSha256"`

	resources       map[string]map[string]snapshotResource
	byName          map[string]int
	maxResourceRead int
}

type snapshotResource struct {
	data []byte
	size int64
}

// Source loads source-relative skill packages and bounded supporting
// resources. Implementations must never expose host paths through Package or
// Warning values.
type Source interface {
	Scope() Scope
	List(context.Context, int) (PackageList, error)
	ReadResource(context.Context, string, string, ResourceReadOptions) (ResourceReadResult, error)
}

// Catalog combines one or more bounded sources into deterministic snapshots.
type Catalog struct {
	sources []Source
	limits  Limits
}

// CatalogOptions configures a catalog. Sources are sorted by Scope and then by
// their deterministic package order; callers should generally provide one
// system and one project source.
type CatalogOptions struct {
	Sources []Source
	Limits  Limits
}

var (
	// ErrSkillNotFound is returned when a snapshot lookup does not resolve a
	// name or qualified name.
	ErrSkillNotFound = errors.New("skill not found")
	// ErrResourceNotFound is returned for an unknown package-relative resource.
	ErrResourceNotFound = errors.New("skill resource not found")
)

// NewCatalog validates and returns a read-only catalog configuration.
func NewCatalog(opts CatalogOptions) (*Catalog, error) {
	limits := opts.Limits.bounded()
	sources := append([]Source(nil), opts.Sources...)
	for _, source := range sources {
		if source == nil {
			return nil, errors.New("skill source cannot be nil")
		}
		if source.Scope() != ScopeSystem && source.Scope() != ScopeProject {
			return nil, fmt.Errorf("unsupported skill source scope %q", source.Scope())
		}
	}
	sort.SliceStable(sources, func(i, j int) bool {
		return scopeRank(sources[i].Scope()) < scopeRank(sources[j].Scope())
	})
	return &Catalog{sources: sources, limits: limits}, nil
}

func scopeRank(scope Scope) int {
	if scope == ScopeSystem {
		return 0
	}
	return 1
}

func cloneWarnings(in []Warning) []Warning {
	return append([]Warning(nil), in...)
}

func cloneResources(in []Resource) []Resource {
	return append([]Resource(nil), in...)
}

func cloneEntries(in []Entry) []Entry {
	out := make([]Entry, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Resources = cloneResources(in[i].Resources)
	}
	return out
}

func (s Snapshot) clone() Snapshot {
	out := Snapshot{
		Entries:         cloneEntries(s.Entries),
		Warnings:        cloneWarnings(s.Warnings),
		CatalogDigest:   s.CatalogDigest,
		ContentDigest:   s.ContentDigest,
		ResourceDigest:  s.ResourceDigest,
		resources:       make(map[string]map[string]snapshotResource, len(s.resources)),
		byName:          make(map[string]int, len(s.byName)),
		maxResourceRead: s.maxResourceRead,
	}
	for name, index := range s.byName {
		out.byName[name] = index
	}
	for qualified, resources := range s.resources {
		copied := make(map[string]snapshotResource, len(resources))
		for path, resource := range resources {
			copied[path] = snapshotResource{data: append([]byte(nil), resource.data...), size: resource.size}
		}
		out.resources[qualified] = copied
	}
	return out
}

// EnabledOnly returns an immutable snapshot containing only packages that are
// active for model turns. Management callers should retain the full snapshot
// so disabled entries remain visible and editable through the project API.
func (s Snapshot) EnabledOnly() Snapshot {
	out := Snapshot{Warnings: cloneWarnings(s.Warnings), maxResourceRead: s.maxResourceRead, resources: make(map[string]map[string]snapshotResource), byName: make(map[string]int)}
	for _, entry := range s.Entries {
		if !entry.Enabled {
			continue
		}
		index := len(out.Entries)
		entry.Resources = cloneResources(entry.Resources)
		out.Entries = append(out.Entries, entry)
		out.byName[entry.QualifiedName] = index
		if resources, ok := s.resources[entry.QualifiedName]; ok {
			copied := make(map[string]snapshotResource, len(resources))
			for resourcePath, resource := range resources {
				copied[resourcePath] = snapshotResource{data: append([]byte(nil), resource.data...), size: resource.size}
			}
			out.resources[entry.QualifiedName] = copied
		}
	}
	buildSnapshotAliases(&out)
	computeSnapshotDigests(&out)
	return out
}

func cleanPublicPackagePath(raw string) (string, error) {
	return cleanRelative(raw, "package path")
}

// ValidatePackagePath validates a package-relative identity supplied by a
// management caller without exposing the internal path helper.
func ValidatePackagePath(raw string) (string, error) { return cleanPublicPackagePath(raw) }

func cleanPublicResourcePath(raw string) (string, error) {
	return cleanRelative(raw, "resource path")
}

// ValidateResourcePath validates a supporting-resource locator supplied by a
// management caller.
func ValidateResourcePath(raw string) (string, error) { return cleanPublicResourcePath(raw) }

func cleanRelative(raw, label string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" || raw == "." {
		return "", fmt.Errorf("%s cannot be empty", label)
	}
	if strings.HasPrefix(raw, "/") || strings.ContainsRune(raw, '\x00') {
		return "", fmt.Errorf("%s must be relative", label)
	}
	if len(raw) >= 2 && raw[1] == ':' {
		return "", fmt.Errorf("%s must be relative", label)
	}
	parts := strings.Split(raw, "/")
	for _, part := range parts {
		if part == ".." {
			return "", fmt.Errorf("%s cannot contain ..", label)
		}
		if part == "" {
			return "", fmt.Errorf("%s contains an empty segment", label)
		}
		if part == "." {
			return "", fmt.Errorf("%s is not clean", label)
		}
	}
	clean := path.Clean(raw)
	if clean == "" || clean == "." || clean != raw {
		return "", fmt.Errorf("%s is not clean", label)
	}
	return clean, nil
}
