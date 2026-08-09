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

package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ProviderSkillPackage is the framework-neutral representation of one
// provider-declared inline App Studio package. API and HTTP layers adapt their
// provider catalog DTOs into this type; the source never accepts a URL or
// runtime credential.
type ProviderSkillPackage struct {
	ProviderName string
	PackageName  string
	Version      string
	Digest       string
	Skill        string
	Resources    []ProviderSkillResource
}

// ProviderSkillResource is one package-relative supporting file.
type ProviderSkillResource struct {
	Path    string
	Content string
}

const (
	// ProviderSkillSourceMaxProviderAggregateBytes mirrors the hub's per-provider
	// CatalogEntry publication bound. A source receives flattened packages from
	// multiple provider entries, so this is tracked independently per provider.
	ProviderSkillSourceMaxProviderAggregateBytes = 512 << 10
	// ProviderSkillSourceMaxAggregateBytes bounds the flattened provider catalog
	// globally. It matches App Studio's default catalog aggregate and the hub
	// fetch envelope rather than one provider's publication bound.
	ProviderSkillSourceMaxAggregateBytes = DefaultMaxAggregateBytes
)

type providerSkillSource struct {
	packages []providerSkillPackageEntry
	byPath   map[string]providerSkillPackageEntry
	limits   Limits
}

type providerSkillPackageEntry struct {
	packagePath string
	packageData Package
	resources   map[string][]byte
}

// NewProviderSkillSource creates a system-scoped, in-memory source from
// provider catalog packages. Invalid or colliding packages are retained only
// as bounded, source-relative warnings during List; valid sibling packages are
// still available. The returned source copies all input content.
func NewProviderSkillSource(packages []ProviderSkillPackage) (Source, error) {
	return newProviderSkillSource(packages)
}

func newProviderSkillSource(packages []ProviderSkillPackage) (Source, error) {
	limits := DefaultLimits()
	if len(packages) > HardMaxPackages {
		packages = packages[:HardMaxPackages]
	}
	source := &providerSkillSource{limits: limits, byPath: make(map[string]providerSkillPackageEntry, len(packages))}
	var aggregateBytes int64
	providerAggregateBytes := make(map[string]int64)
	for _, packageValue := range packages {
		entry, err := normalizeProviderSkillPackage(packageValue, limits)
		if err != nil {
			// Invalid package declarations are intentionally deferred to List so
			// the source can report a bounded warning and continue with siblings.
			entry.packagePath = providerSkillWarningPath(packageValue)
			entry.packageData = Package{Path: entry.packagePath}
		}
		if entry.packagePath != "" {
			// Keep the first deterministic package for a path. Duplicate package
			// identities are retained as invalid entries so List can emit a
			// bounded warning rather than silently merging provider artifacts.
			if _, exists := source.byPath[entry.packagePath]; exists {
				entry.packageData = Package{Path: entry.packagePath}
				source.packages = append(source.packages, entry)
				continue
			}
			if entry.packageData.SkillContent != nil {
				packageBytes := int64(len(entry.packageData.SkillContent))
				for _, content := range entry.resources {
					packageBytes += int64(len(content))
				}
				providerBytes := providerAggregateBytes[packageValue.ProviderName]
				if providerBytes+packageBytes > ProviderSkillSourceMaxProviderAggregateBytes || aggregateBytes+packageBytes > ProviderSkillSourceMaxAggregateBytes {
					entry.packageData = Package{Path: entry.packagePath}
					entry.resources = nil
				} else {
					providerAggregateBytes[packageValue.ProviderName] = providerBytes + packageBytes
					aggregateBytes += packageBytes
				}
			}
			source.byPath[entry.packagePath] = entry
			source.packages = append(source.packages, entry)
		}
	}
	sort.SliceStable(source.packages, func(i, j int) bool { return source.packages[i].packagePath < source.packages[j].packagePath })
	return source, nil
}

var errProviderSkillUnavailable = errors.New("provider skill catalog unavailable")

type providerSkillUnavailableSource struct{}

// NewProviderSkillUnavailableSource returns a system source whose List call
// fails with a sanitized error. Catalog.Load isolates this optional source and
// records its bounded source_list_failed warning while retaining other sources.
func NewProviderSkillUnavailableSource() Source { return providerSkillUnavailableSource{} }

func (providerSkillUnavailableSource) Scope() Scope { return ScopeSystem }

func (providerSkillUnavailableSource) List(ctx context.Context, _ int) (PackageList, error) {
	if err := ctx.Err(); err != nil {
		return PackageList{}, err
	}
	return PackageList{}, errProviderSkillUnavailable
}

func (providerSkillUnavailableSource) ReadResource(ctx context.Context, _, _ string, _ ResourceReadOptions) (ResourceReadResult, error) {
	if err := ctx.Err(); err != nil {
		return ResourceReadResult{}, err
	}
	return ResourceReadResult{}, ErrResourceNotFound
}

func (s *providerSkillSource) Scope() Scope { return ScopeSystem }

func (s *providerSkillSource) List(ctx context.Context, maxPackages int) (PackageList, error) {
	if err := ctx.Err(); err != nil {
		return PackageList{}, err
	}
	limit := maxPackages
	if limit <= 0 || limit > s.limits.MaxPackages {
		limit = s.limits.MaxPackages
	}
	packages := make([]Package, 0, min(limit, len(s.packages)))
	warnings := make([]Warning, 0)
	for _, entry := range s.packages {
		if err := ctx.Err(); err != nil {
			return PackageList{}, err
		}
		if entry.packageData.SkillContent == nil {
			warnings = appendBoundedWarning(warnings, Warning{Scope: ScopeSystem, PackagePath: entry.packagePath, Code: "provider_skill_invalid", Message: "provider skill package is invalid"}, s.limits.MaxWarnings)
			continue
		}
		if len(packages) >= limit {
			break
		}
		packageCopy := entry.packageData
		packageCopy.SkillContent = append([]byte(nil), packageCopy.SkillContent...)
		packageCopy.Resources = append([]ResourceRef(nil), packageCopy.Resources...)
		packages = append(packages, packageCopy)
	}
	return PackageList{Packages: packages, Warnings: warnings, Truncated: len(s.packages) > len(packages)}, nil
}

func (s *providerSkillSource) ReadResource(ctx context.Context, packagePath, resourcePath string, opts ResourceReadOptions) (ResourceReadResult, error) {
	if err := ctx.Err(); err != nil {
		return ResourceReadResult{}, err
	}
	packagePath, err := cleanPublicPackagePath(packagePath)
	if err != nil {
		return ResourceReadResult{}, err
	}
	resourcePath, err = cleanPublicResourcePath(resourcePath)
	if err != nil {
		return ResourceReadResult{}, err
	}
	if resourcePath == "SKILL.md" {
		return ResourceReadResult{}, fmt.Errorf("SKILL.md is not a supporting resource")
	}
	entry, ok := s.byPath[packagePath]
	if !ok || entry.packageData.SkillContent == nil {
		return ResourceReadResult{}, ErrResourceNotFound
	}
	data, ok := entry.resources[resourcePath]
	if !ok {
		return ResourceReadResult{}, ErrResourceNotFound
	}
	bounded, err := boundedReadOptions(opts, s.limits.MaxResourceRead)
	if err != nil {
		return ResourceReadResult{}, err
	}
	if bounded.Offset > int64(len(data)) {
		return ResourceReadResult{Path: resourcePath, Size: int64(len(data)), Offset: bounded.Offset}, nil
	}
	start := bounded.Offset
	end := start + int64(bounded.Limit)
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	result := ResourceReadResult{Path: resourcePath, Content: append([]byte(nil), data[start:end]...), Size: int64(len(data)), Offset: start}
	if end < int64(len(data)) {
		result.Truncated = true
		result.NextOffset = end
	}
	return result, nil
}

func normalizeProviderSkillPackage(packageValue ProviderSkillPackage, limits Limits) (providerSkillPackageEntry, error) {
	providerName := strings.TrimSpace(packageValue.ProviderName)
	if providerName == "" || providerName != packageValue.ProviderName || !providerSkillIdentity(providerName) {
		return providerSkillPackageEntry{}, fmt.Errorf("provider name is invalid")
	}
	packageName := strings.TrimSpace(packageValue.PackageName)
	if packageName == "" || packageName != packageValue.PackageName {
		return providerSkillPackageEntry{}, fmt.Errorf("package name is invalid")
	}
	packagePath, err := cleanPublicPackagePath(providerSkillPackagePath(providerName, packageName))
	if err != nil || !strings.HasPrefix(packagePath, "providers/") {
		return providerSkillPackageEntry{}, fmt.Errorf("package path is invalid")
	}
	version := strings.TrimSpace(packageValue.Version)
	if version == "" || version != packageValue.Version || len([]byte(version)) > 64 || containsControlChars(version) {
		return providerSkillPackageEntry{}, fmt.Errorf("package version is invalid")
	}
	if len([]byte(packageValue.Skill)) == 0 || len([]byte(packageValue.Skill)) > DefaultMaxSkillBytes || !utf8.ValidString(packageValue.Skill) || strings.ContainsRune(packageValue.Skill, '\x00') {
		return providerSkillPackageEntry{}, fmt.Errorf("skill document is invalid or oversized")
	}
	if len(packageValue.Resources) > HardMaxResourceCount {
		return providerSkillPackageEntry{}, fmt.Errorf("resource count exceeds its bound")
	}
	resources := make(map[string][]byte, len(packageValue.Resources))
	refs := make([]ResourceRef, 0, len(packageValue.Resources))
	total := len([]byte(packageValue.Skill))
	for _, resource := range packageValue.Resources {
		resourcePath, pathErr := cleanPublicResourcePath(resource.Path)
		if pathErr != nil || resourcePath == "SKILL.md" || strings.HasSuffix(resourcePath, "/SKILL.md") {
			return providerSkillPackageEntry{}, fmt.Errorf("resource path is invalid")
		}
		if _, exists := resources[resourcePath]; exists {
			return providerSkillPackageEntry{}, fmt.Errorf("resource path is duplicated")
		}
		if len([]byte(resource.Content)) > HardMaxResourceBytes || !utf8.ValidString(resource.Content) || strings.ContainsRune(resource.Content, '\x00') {
			return providerSkillPackageEntry{}, fmt.Errorf("resource content is invalid or oversized")
		}
		total += len([]byte(resource.Content))
		if total > HardMaxAggregateBytes {
			return providerSkillPackageEntry{}, fmt.Errorf("package aggregate exceeds its bound")
		}
		resources[resourcePath] = []byte(resource.Content)
		refs = append(refs, ResourceRef{Path: resourcePath, Size: int64(len([]byte(resource.Content)))})
	}
	if !validProviderSkillDigest(packageValue.Digest) {
		return providerSkillPackageEntry{}, fmt.Errorf("package digest is invalid")
	}
	computed, err := providerSkillPackageDigest(packageValue)
	if err != nil {
		return providerSkillPackageEntry{}, err
	}
	if computed != packageValue.Digest {
		return providerSkillPackageEntry{}, fmt.Errorf("package digest does not match content")
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Path < refs[j].Path })
	return providerSkillPackageEntry{
		packagePath: packagePath,
		packageData: Package{Path: packagePath, SkillContent: []byte(packageValue.Skill), Resources: refs, Version: version, Digest: packageValue.Digest},
		resources:   resources,
	}, nil
}

func providerSkillPackagePath(providerName, packageName string) string {
	return "providers/" + providerName + "/" + packageName
}

func providerSkillWarningPath(packageValue ProviderSkillPackage) string {
	providerName := strings.TrimSpace(packageValue.ProviderName)
	packageName := strings.TrimSpace(packageValue.PackageName)
	if providerName == "" || packageName == "" || !providerSkillIdentity(providerName) || strings.ContainsRune(packageName, '\\') || strings.ContainsRune(packageName, ':') {
		return "providers/invalid"
	}
	path, err := cleanPublicPackagePath(providerSkillPackagePath(providerName, packageName))
	if err != nil || !strings.HasPrefix(path, "providers/") {
		return "providers/invalid"
	}
	return path
}

func providerSkillIdentity(value string) bool {
	if len([]byte(value)) > 128 || strings.ContainsRune(value, '/') || strings.ContainsRune(value, '\\') || strings.ContainsRune(value, ':') {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func containsControlChars(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func validProviderSkillDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// ProviderSkillPackageDigest is exported for API adapters and tests that need
// to reproduce the hub's canonical artifact digest without importing hub code.
func ProviderSkillPackageDigest(packageValue ProviderSkillPackage) (string, error) {
	return providerSkillPackageDigest(packageValue)
}

func providerSkillPackageDigest(packageValue ProviderSkillPackage) (string, error) {
	type canonicalResource struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	resources := make([]canonicalResource, 0, len(packageValue.Resources))
	for _, resource := range packageValue.Resources {
		resources = append(resources, canonicalResource{Path: resource.Path, Content: resource.Content})
	}
	sort.SliceStable(resources, func(i, j int) bool {
		if resources[i].Path != resources[j].Path {
			return resources[i].Path < resources[j].Path
		}
		return resources[i].Content < resources[j].Content
	})
	envelope := struct {
		PackageName string              `json:"packageName"`
		Version     string              `json:"version"`
		Skill       string              `json:"skill"`
		Resources   []canonicalResource `json:"resources,omitempty"`
	}{PackageName: packageValue.PackageName, Version: packageValue.Version, Skill: packageValue.Skill, Resources: resources}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
