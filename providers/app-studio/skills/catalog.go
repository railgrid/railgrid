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
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Load builds one immutable snapshot from the configured catalog.
func (c *Catalog) Load(ctx context.Context) (Snapshot, error) {
	if c == nil {
		return Snapshot{}, errors.New("skill catalog is not configured")
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	warnings := make([]Warning, 0)
	candidates := make([]loadedCandidate, 0)
	aggregateBytes := 0
	for _, source := range c.sources {
		list, err := source.List(ctx, c.limits.MaxPackages)
		if err != nil {
			if ctx.Err() != nil {
				return Snapshot{}, ctx.Err()
			}
			warnings = appendBoundedWarning(warnings, Warning{Scope: source.Scope(), Code: "source_list_failed", Message: "skill source could not be listed"}, c.limits.MaxWarnings)
			continue
		}
		warnings = appendBoundedWarnings(warnings, list.Warnings, c.limits.MaxWarnings)
		if list.Truncated {
			warnings = appendBoundedWarning(warnings, Warning{Scope: source.Scope(), Code: "package_limit", Message: "skill package listing reached its bound"}, c.limits.MaxWarnings)
		}
		for _, packageEntry := range list.Packages {
			if len(candidates) >= c.limits.MaxPackages {
				break
			}
			candidate, candidateWarnings, ok := c.loadCandidate(ctx, source, packageEntry)
			warnings = appendBoundedWarnings(warnings, candidateWarnings, c.limits.MaxWarnings)
			if ok {
				candidateBytes := loadedCandidateSize(candidate)
				if aggregateBytes+candidateBytes > c.limits.MaxAggregateBytes {
					warnings = appendBoundedWarning(warnings, Warning{Scope: source.Scope(), PackagePath: candidate.entry.PackagePath, Code: "catalog_limit", Message: "catalog content reached its aggregate bound"}, c.limits.MaxWarnings)
					continue
				}
				candidates = append(candidates, candidate)
				aggregateBytes += candidateBytes
			}
		}
	}
	assignQualifiedNames(candidates)
	snapshot := Snapshot{Warnings: cloneWarnings(warnings), resources: make(map[string]map[string]snapshotResource), byName: make(map[string]int), maxResourceRead: c.limits.MaxResourceRead}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		if len(snapshot.Entries) >= c.limits.MaxPackages {
			break
		}
		entry := candidate.entry
		entry.Resources = cloneResources(candidate.entry.Resources)
		index := len(snapshot.Entries)
		snapshot.Entries = append(snapshot.Entries, entry)
		resourceMap := make(map[string]snapshotResource, len(candidate.resources))
		for resourcePath, data := range candidate.resources {
			resourceMap[resourcePath] = snapshotResource{data: append([]byte(nil), data.data...), size: data.size}
		}
		snapshot.resources[entry.QualifiedName] = resourceMap
		snapshot.byName[entry.QualifiedName] = index
	}
	// Build aliases only after the aggregate limit has filtered entries.
	buildSnapshotAliases(&snapshot)
	computeSnapshotDigests(&snapshot)
	return snapshot.clone(), nil
}

// Build is a convenience wrapper around NewCatalog and Load.
func Build(ctx context.Context, opts CatalogOptions) (Snapshot, error) {
	catalog, err := NewCatalog(opts)
	if err != nil {
		return Snapshot{}, err
	}
	return catalog.Load(ctx)
}

// Snapshot is an alias for Load for call sites that describe the result rather
// than the operation.
func (c *Catalog) Snapshot(ctx context.Context) (Snapshot, error) {
	return c.Load(ctx)
}

// LoadSnapshot is a convenience alias for Build.
func LoadSnapshot(ctx context.Context, opts CatalogOptions) (Snapshot, error) {
	return Build(ctx, opts)
}

type loadedCandidate struct {
	entry     Entry
	resources map[string]snapshotResource
}

func (c *Catalog) loadCandidate(ctx context.Context, source Source, packageEntry Package) (loadedCandidate, []Warning, bool) {
	warnings := make([]Warning, 0)
	packagePath, err := cleanPublicPackagePath(packageEntry.Path)
	if err != nil {
		return loadedCandidate{}, []Warning{{Scope: source.Scope(), Code: "invalid_package_path", Message: "skill package path is invalid"}}, false
	}
	parsed, err := ParseSkill(packageEntry.SkillContent, c.limits)
	if err != nil {
		return loadedCandidate{}, []Warning{{Scope: source.Scope(), PackagePath: packagePath, Code: "invalid_skill", Message: sanitizeParseWarning(err)}}, false
	}
	contentDigest := digestBytes([]byte(parsed.Content))
	resources := make(map[string]snapshotResource)
	resourceMetadata := make([]Resource, 0, len(packageEntry.Resources))
	seenResources := make(map[string]struct{})
	resourceBytes := 0
	baseEntryBytes := entrySize(Entry{
		Name:        parsed.Name,
		Description: parsed.Description,
		Scope:       source.Scope(),
		PackagePath: packagePath,
		Content:     parsed.Content,
	})
	for _, resourceRef := range packageEntry.Resources {
		if len(resourceMetadata) >= c.limits.MaxResourceCount {
			warnings = appendBoundedWarning(warnings, Warning{Scope: source.Scope(), PackagePath: packagePath, Code: "resource_limit", Message: "supporting-resource count reached its bound"}, c.limits.MaxWarnings)
			break
		}
		resourcePath, cleanErr := cleanPublicResourcePath(resourceRef.Path)
		if cleanErr != nil || resourcePath == "SKILL.md" {
			continue
		}
		if _, exists := seenResources[resourcePath]; exists {
			continue
		}
		seenResources[resourcePath] = struct{}{}
		metadata := Resource{Path: resourcePath, Size: resourceRef.Size}
		read, readErr := source.ReadResource(ctx, packagePath, resourcePath, ResourceReadOptions{Limit: c.limits.MaxResourceBytes})
		if readErr != nil {
			warnings = appendBoundedWarning(warnings, Warning{Scope: source.Scope(), PackagePath: packagePath, Code: "resource_read_failed", Message: "supporting resource could not be read"}, c.limits.MaxWarnings)
			resourceMetadata = append(resourceMetadata, metadata)
			continue
		}
		if read.Truncated {
			warnings = appendBoundedWarning(warnings, Warning{Scope: source.Scope(), PackagePath: packagePath, Code: "resource_too_large", Message: "supporting resource exceeds the catalog bound"}, c.limits.MaxWarnings)
			resourceMetadata = append(resourceMetadata, metadata)
			continue
		}
		if baseEntryBytes+resourceBytes+len(read.Content) > c.limits.MaxAggregateBytes {
			warnings = appendBoundedWarning(warnings, Warning{Scope: source.Scope(), PackagePath: packagePath, Code: "resource_aggregate_limit", Message: "supporting resources reached the catalog aggregate bound"}, c.limits.MaxWarnings)
			break
		}
		metadata.Size = read.Size
		metadata.Digest = digestBytes(read.Content)
		resourceMetadata = append(resourceMetadata, metadata)
		resources[resourcePath] = snapshotResource{data: append([]byte(nil), read.Content...), size: read.Size}
		resourceBytes += len(read.Content)
	}
	sort.Slice(resourceMetadata, func(i, j int) bool { return resourceMetadata[i].Path < resourceMetadata[j].Path })
	entry := Entry{
		Name:          parsed.Name,
		PackageName:   parsed.Name,
		Description:   parsed.Description,
		Scope:         source.Scope(),
		PackagePath:   packagePath,
		Enabled:       true,
		Editable:      source.Scope() == ScopeProject,
		Version:       contentDigest,
		Content:       parsed.Content,
		ContentDigest: contentDigest,
		Resources:     resourceMetadata,
	}
	if packageEntry.EnabledSet {
		entry.Enabled = packageEntry.Enabled
	}
	if source.Scope() == ScopeSystem {
		entry.Editable = false
		if !packageEntry.EnabledSet {
			entry.Enabled = true
		}
	}
	if version := strings.TrimSpace(packageEntry.Version); version != "" {
		entry.Version = version
	}
	entry.Digest = digestEntry(entry)
	if digest := strings.TrimSpace(packageEntry.Digest); digest != "" {
		entry.Digest = digest
	}
	return loadedCandidate{entry: entry, resources: resources}, warnings, true
}

func assignQualifiedNames(candidates []loadedCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if scopeRank(candidates[i].entry.Scope) != scopeRank(candidates[j].entry.Scope) {
			return scopeRank(candidates[i].entry.Scope) < scopeRank(candidates[j].entry.Scope)
		}
		if candidates[i].entry.Name != candidates[j].entry.Name {
			return candidates[i].entry.Name < candidates[j].entry.Name
		}
		if candidates[i].entry.PackagePath != candidates[j].entry.PackagePath {
			return candidates[i].entry.PackagePath < candidates[j].entry.PackagePath
		}
		return candidates[i].entry.ContentDigest < candidates[j].entry.ContentDigest
	})
	byName := make(map[string][]int)
	for index := range candidates {
		byName[candidates[index].entry.Name] = append(byName[candidates[index].entry.Name], index)
	}
	for name, indices := range byName {
		if len(indices) == 1 {
			entry := &candidates[indices[0]].entry
			entry.QualifiedName = string(entry.Scope) + ":" + name
			continue
		}
		used := make(map[string]int)
		for _, index := range indices {
			entry := &candidates[index].entry
			base := string(entry.Scope) + ":" + name
			if used[base] > 0 || countScopeName(candidates, indices, entry.Scope, name) > 1 {
				base += "@" + strconv.Itoa(len([]byte(entry.PackagePath))) + "=" + entry.PackagePath
			}
			used[base]++
			if used[base] > 1 {
				base += "#" + strconv.Itoa(used[base])
			}
			entry.QualifiedName = base
		}
	}
	resolveQualifiedNameCollisions(candidates)
}

// resolveQualifiedNameCollisions protects the public lookup map from names
// that happen to resemble another entry's generated suffix. The fallback
// encoding is length-prefixed and therefore injective for the source, name,
// package path, and deterministic duplicate ordinal.
func resolveQualifiedNameCollisions(candidates []loadedCandidate) {
	byID := make(map[string][]int)
	for index := range candidates {
		byID[candidates[index].entry.QualifiedName] = append(byID[candidates[index].entry.QualifiedName], index)
	}
	for _, indices := range byID {
		if len(indices) < 2 {
			continue
		}
		sort.SliceStable(indices, func(i, j int) bool {
			left, right := candidates[indices[i]].entry, candidates[indices[j]].entry
			if left.PackagePath != right.PackagePath {
				return left.PackagePath < right.PackagePath
			}
			if left.ContentDigest != right.ContentDigest {
				return left.ContentDigest < right.ContentDigest
			}
			return candidates[indices[i]].entry.Description < candidates[indices[j]].entry.Description
		})
		for ordinal, index := range indices {
			entry := &candidates[index].entry
			entry.QualifiedName = injectiveQualifiedName(*entry, ordinal+1)
		}
	}
}

func injectiveQualifiedName(entry Entry, ordinal int) string {
	return "skill:" + lengthPrefix(string(entry.Scope)) + "/" +
		lengthPrefix(entry.Name) + "/" + lengthPrefix(entry.PackagePath) + "/" + strconv.Itoa(ordinal)
}

func lengthPrefix(value string) string {
	return strconv.Itoa(len([]byte(value))) + "=" + value
}

func countScopeName(candidates []loadedCandidate, indices []int, scope Scope, name string) int {
	count := 0
	for _, index := range indices {
		if candidates[index].entry.Scope == scope && candidates[index].entry.Name == name {
			count++
		}
	}
	return count
}

func buildSnapshotAliases(snapshot *Snapshot) {
	ambiguous := make(map[string]struct{})
	canonical := make(map[string]int, len(snapshot.Entries))
	for index := range snapshot.Entries {
		canonical[snapshot.Entries[index].QualifiedName] = index
	}
	addAlias := func(alias string, index int) {
		if owner, ok := canonical[alias]; ok && owner != index {
			// Canonical qualified IDs always win over convenience aliases.
			return
		}
		if _, seen := ambiguous[alias]; seen {
			return
		}
		if existing, ok := snapshot.byName[alias]; !ok {
			snapshot.byName[alias] = index
		} else if existing != index {
			delete(snapshot.byName, alias)
			ambiguous[alias] = struct{}{}
		}
	}
	for index := range snapshot.Entries {
		entry := snapshot.Entries[index]
		snapshot.byName[entry.QualifiedName] = index
		scopeName := string(entry.Scope) + ":" + entry.Name
		addAlias(scopeName, index)
	}
	nameCounts := make(map[string]int)
	for _, entry := range snapshot.Entries {
		nameCounts[entry.Name]++
	}
	for index, entry := range snapshot.Entries {
		if nameCounts[entry.Name] == 1 {
			addAlias(entry.Name, index)
		}
	}
}

// Find resolves a qualified name, scoped name ("system:name" or
// "project:name"), or an unqualified name when it is unambiguous.
func (s Snapshot) Find(name string) (Entry, bool) {
	index, ok := s.byName[strings.TrimSpace(name)]
	if !ok || index < 0 || index >= len(s.Entries) {
		return Entry{}, false
	}
	entry := s.Entries[index]
	entry.Resources = cloneResources(entry.Resources)
	return entry, true
}

// Get resolves a skill and returns ErrSkillNotFound when the name is unknown
// or ambiguous.
func (s Snapshot) Get(name string) (Entry, error) {
	entry, ok := s.Find(name)
	if !ok {
		return Entry{}, ErrSkillNotFound
	}
	return entry, nil
}

// ReadResource returns one bounded page from an immutable snapshot resource.
// Paths are package-relative; traversal, absolute paths, and SKILL.md are
// rejected. A resource too large for the snapshot is unavailable rather than
// exposing a partial, mutable source read.
func (s Snapshot) ReadResource(ctx context.Context, name, resourcePath string, opts ResourceReadOptions) (ResourceReadResult, error) {
	if err := ctx.Err(); err != nil {
		return ResourceReadResult{}, err
	}
	entry, ok := s.Find(name)
	if !ok {
		return ResourceReadResult{}, ErrSkillNotFound
	}
	resourcePath, err := cleanPublicResourcePath(resourcePath)
	if err != nil {
		return ResourceReadResult{}, err
	}
	if resourcePath == "SKILL.md" {
		return ResourceReadResult{}, fmt.Errorf("SKILL.md is not a supporting resource")
	}
	resource, found := s.resources[entry.QualifiedName][resourcePath]
	if !found {
		return ResourceReadResult{}, ErrResourceNotFound
	}
	defaultLimit := s.maxResourceRead
	if defaultLimit <= 0 {
		defaultLimit = DefaultMaxResourceRead
	}
	limit, err := boundedReadOptions(opts, defaultLimit)
	if err != nil {
		return ResourceReadResult{}, err
	}
	if limit.Limit > defaultLimit {
		return ResourceReadResult{}, fmt.Errorf("resource read limit exceeds %d bytes", defaultLimit)
	}
	if resource.size < int64(len(resource.data)) {
		resource.size = int64(len(resource.data))
	}
	if limit.Offset > resource.size {
		return ResourceReadResult{Path: resourcePath, Size: resource.size, Offset: limit.Offset}, nil
	}
	end := int64(len(resource.data))
	if max := limit.Offset + int64(limit.Limit); end > max {
		end = max
	}
	content := append([]byte(nil), resource.data[limit.Offset:end]...)
	result := ResourceReadResult{Path: resourcePath, Content: content, Size: resource.size, Offset: limit.Offset}
	if end < resource.size {
		result.Truncated = true
		result.NextOffset = end
	}
	return result, nil
}

func boundedReadOptions(opts ResourceReadOptions, defaultLimit int) (ResourceReadOptions, error) {
	if opts.Offset < 0 {
		return ResourceReadOptions{}, errors.New("resource offset cannot be negative")
	}
	if opts.Limit <= 0 {
		opts.Limit = defaultLimit
	}
	if opts.Limit > HardMaxResourceRead {
		return ResourceReadOptions{}, fmt.Errorf("resource read limit exceeds %d bytes", HardMaxResourceRead)
	}
	if opts.Offset > int64(HardMaxResourceBytes) {
		return ResourceReadOptions{}, fmt.Errorf("resource offset exceeds bounded catalog size")
	}
	return opts, nil
}

func appendBoundedWarning(warnings []Warning, warning Warning, max int) []Warning {
	if len(warnings) >= max {
		return warnings
	}
	warning.Scope = normalizedScope(warning.Scope)
	warning.PackagePath = publicWarningPath(warning.PackagePath)
	warning.Code = boundedWarningCode(warning.Code)
	warning.Message = boundedWarningMessage(warning.Message)
	return append(warnings, warning)
}

func appendBoundedWarnings(warnings, additions []Warning, max int) []Warning {
	for _, warning := range additions {
		warnings = appendBoundedWarning(warnings, warning, max)
	}
	return warnings
}

func normalizedScope(scope Scope) Scope {
	if scope == ScopeProject {
		return ScopeProject
	}
	return ScopeSystem
}

func publicWarningPath(path string) string {
	if path == "" {
		return ""
	}
	clean, err := cleanPublicPackagePath(path)
	if err != nil {
		return ""
	}
	return clean
}

func boundedWarningCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "load_failed"
	}
	if len(code) > 64 {
		return code[:64]
	}
	return code
}

func boundedWarningMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "skill entry could not be loaded"
	}
	if strings.ContainsAny(message, `/\\`) || (len(message) >= 2 && message[1] == ':') {
		return "skill entry could not be loaded"
	}
	if len(message) > 256 {
		return message[:256]
	}
	return message
}

func sanitizeParseWarning(err error) string {
	message := strings.TrimSpace(err.Error())
	if strings.Contains(message, "frontmatter") {
		return boundedWarningMessage(message)
	}
	return "skill document is invalid"
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestEntry(entry Entry) string {
	var builder strings.Builder
	writeDigestField(&builder, entry.Name)
	writeDigestField(&builder, entry.Description)
	writeDigestField(&builder, string(entry.Scope))
	writeDigestField(&builder, entry.PackagePath)
	writeDigestField(&builder, strconv.FormatBool(entry.Enabled))
	writeDigestField(&builder, strconv.FormatBool(entry.Editable))
	writeDigestField(&builder, entry.Version)
	writeDigestField(&builder, entry.ContentDigest)
	for _, resource := range entry.Resources {
		writeDigestField(&builder, resource.Path)
		writeDigestField(&builder, strconv.FormatInt(resource.Size, 10))
		writeDigestField(&builder, resource.Digest)
	}
	return digestBytes([]byte(builder.String()))
}

func writeDigestField(builder *strings.Builder, value string) {
	builder.WriteString(strconv.Itoa(len(value)))
	builder.WriteByte(':')
	builder.WriteString(value)
	builder.WriteByte('\n')
}

func entrySize(entry Entry) int {
	total := len(entry.Name) + len(entry.Description) + len(entry.Content) + len(entry.PackagePath)
	for _, resource := range entry.Resources {
		total += len(resource.Path) + len(resource.Digest) + 32
	}
	return total
}

func loadedCandidateSize(candidate loadedCandidate) int {
	total := entrySize(candidate.entry)
	for _, resource := range candidate.resources {
		total += len(resource.data)
	}
	return total
}

func computeSnapshotDigests(snapshot *Snapshot) {
	var content, resources, catalog strings.Builder
	for _, entry := range snapshot.Entries {
		writeDigestField(&content, entry.QualifiedName)
		writeDigestField(&content, entry.ContentDigest)
		writeDigestField(&catalog, entry.QualifiedName)
		writeDigestField(&catalog, entry.Description)
		writeDigestField(&catalog, string(entry.Scope))
		writeDigestField(&catalog, entry.PackagePath)
		writeDigestField(&catalog, strconv.FormatBool(entry.Enabled))
		writeDigestField(&catalog, strconv.FormatBool(entry.Editable))
		writeDigestField(&catalog, entry.Version)
		writeDigestField(&catalog, entry.ContentDigest)
		for _, resource := range entry.Resources {
			writeDigestField(&resources, entry.QualifiedName)
			writeDigestField(&resources, resource.Path)
			writeDigestField(&resources, resource.Digest)
			writeDigestField(&resources, strconv.FormatInt(resource.Size, 10))
			writeDigestField(&catalog, resource.Path)
			writeDigestField(&catalog, resource.Digest)
			writeDigestField(&catalog, strconv.FormatInt(resource.Size, 10))
		}
	}
	snapshot.ContentDigest = digestBytes([]byte(content.String()))
	snapshot.ResourceDigest = digestBytes([]byte(resources.String()))
	writeDigestField(&catalog, snapshot.ContentDigest)
	writeDigestField(&catalog, snapshot.ResourceDigest)
	snapshot.CatalogDigest = digestBytes([]byte(catalog.String()))
}
