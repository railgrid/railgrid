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
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/faroshq/provider-app-studio/workspace"
)

const defaultProjectSkillsRoot = ".agents/skills"

// ProjectSkillsRoot is the only workspace root managed by project skill
// lifecycle operations. Callers must treat it as an opaque relative prefix.
const ProjectSkillsRoot = defaultProjectSkillsRoot

// ProjectMetadataPath stores activation/distribution metadata outside any
// skill package. It is deliberately a workspace-relative path so it can never
// expose a provider host path through a catalog response.
const ProjectMetadataPath = defaultProjectSkillsRoot + "/.kedge-catalog.json"

const projectMetadataVersion = 1

// Activation records are keyed by package-relative identity, not the
// collision-prone qualified catalog ID. Version and Digest are informational
// provenance retained for management views; activation itself is the only
// mutable policy field.
type Activation struct {
	Enabled bool   `json:"enabled"`
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

// ProjectMetadata is the bounded persisted policy for one project skill root.
type ProjectMetadata struct {
	Version  int                   `json:"version"`
	Packages map[string]Activation `json:"packages,omitempty"`
	// System contains per-project activation overrides for bundled skills.
	// It is intentionally separate from Packages: a project package path is
	// user-controlled and may otherwise collide with a bundled package path.
	// Bundled package content remains immutable; only this project-local policy
	// is mutable.
	System map[string]Activation `json:"system,omitempty"`
}

// ProjectSource reads project skills through the App Studio FileStore. It
// never resolves or accepts an OS path from callers; all paths are cleaned
// relative to Root and passed through FileStore's scope and symlink checks.
type ProjectSource struct {
	store  *workspace.FileStore
	scope  workspace.Scope
	root   string
	limits Limits
}

// NewProjectSource creates a project-scoped source rooted at .agents/skills.
func NewProjectSource(store *workspace.FileStore, scope workspace.Scope) (*ProjectSource, error) {
	return NewProjectSourceWithRoot(store, scope, defaultProjectSkillsRoot)
}

// NewProjectSourceWithRoot creates a project-scoped source under a clean,
// workspace-relative skills root (for example, "skills" or ".agents/skills").
func NewProjectSourceWithRoot(store *workspace.FileStore, scope workspace.Scope, root string) (*ProjectSource, error) {
	if store == nil {
		return nil, fmt.Errorf("project workspace store is not configured")
	}
	if root == "" {
		root = defaultProjectSkillsRoot
	}
	root, err := cleanPublicResourcePath(root)
	if err != nil {
		return nil, err
	}
	return &ProjectSource{store: store, scope: scope, root: root, limits: DefaultLimits()}, nil
}

func defaultProjectMetadata() ProjectMetadata {
	return ProjectMetadata{Version: projectMetadataVersion, Packages: map[string]Activation{}, System: map[string]Activation{}}
}

// ReadProjectMetadata reads activation state. A missing metadata file means
// all project packages are enabled for backwards compatibility. Malformed
// metadata is returned as an error so callers can fail closed.
func ReadProjectMetadata(ctx context.Context, store *workspace.FileStore, scope workspace.Scope) (ProjectMetadata, string, error) {
	metadata := defaultProjectMetadata()
	if store == nil {
		return metadata, "", errors.New("project workspace store is not configured")
	}
	file, err := store.ReadFile(ctx, scope, workspace.ReadOptions{Path: ProjectMetadataPath, MaxBytes: workspace.MaxReadMaxBytes})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return metadata, "", nil
		}
		return metadata, "", err
	}
	if file.Binary || file.Truncated || len(file.Content) == 0 {
		return metadata, file.Version, errors.New("project skill metadata is invalid")
	}
	var decoded ProjectMetadata
	if err := json.Unmarshal([]byte(file.Content), &decoded); err != nil {
		return metadata, file.Version, errors.New("project skill metadata is invalid")
	}
	if decoded.Version != projectMetadataVersion || projectMetadataEntryCount(decoded) > DefaultMaxPackages*4 {
		return metadata, file.Version, errors.New("project skill metadata version or size is unsupported")
	}
	if decoded.Packages == nil {
		decoded.Packages = map[string]Activation{}
	}
	if decoded.System == nil {
		decoded.System = map[string]Activation{}
	}
	if err := validateProjectMetadataActivations(decoded.Packages, false); err != nil {
		return metadata, file.Version, err
	}
	if err := validateProjectMetadataActivations(decoded.System, true); err != nil {
		return metadata, file.Version, err
	}
	return decoded, file.Version, nil
}

// EncodeProjectMetadata validates and serializes activation policy for a
// managed workspace transaction. Keeping encoding here makes lifecycle
// callers use the same bounds and canonical JSON as the ordinary metadata
// writer without performing a second, non-transactional mutation.
func EncodeProjectMetadata(metadata ProjectMetadata) (string, error) {
	if metadata.Version == 0 {
		metadata.Version = projectMetadataVersion
	}
	if metadata.Version != projectMetadataVersion || projectMetadataEntryCount(metadata) > DefaultMaxPackages*4 {
		return "", errors.New("project skill metadata version or size is unsupported")
	}
	if metadata.Packages == nil {
		metadata.Packages = map[string]Activation{}
	}
	if metadata.System == nil {
		metadata.System = map[string]Activation{}
	}
	if err := validateProjectMetadataActivations(metadata.Packages, false); err != nil {
		return "", err
	}
	if err := validateProjectMetadataActivations(metadata.System, true); err != nil {
		return "", err
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func projectMetadataEntryCount(metadata ProjectMetadata) int {
	return len(metadata.Packages) + len(metadata.System)
}

func validateProjectMetadataActivations(activations map[string]Activation, system bool) error {
	for rawPath, activation := range activations {
		clean, err := cleanPublicPackagePath(rawPath)
		if err != nil || clean != rawPath || len([]byte(clean)) > workspace.MaxProjectPathBytes || strings.Contains(clean, "/.kedge-") || strings.HasPrefix(clean, ".kedge-") {
			if system {
				return errors.New("project skill metadata contains an invalid bundled package identity")
			}
			return errors.New("project skill metadata contains an invalid package identity")
		}
		if len([]byte(activation.Version)) > 128 || len([]byte(activation.Digest)) > 128 {
			return errors.New("project skill metadata contains oversized provenance")
		}
	}
	return nil
}

// WriteProjectMetadata persists metadata using a create/replace operation.
// The expected version is the complete-read version returned by
// ReadProjectMetadata; callers can retry on a workspace conflict.
func WriteProjectMetadata(ctx context.Context, store *workspace.FileStore, scope workspace.Scope, metadata ProjectMetadata, expectedVersion string) (workspace.MutationResult, error) {
	if store == nil {
		return workspace.MutationResult{}, errors.New("project workspace store is not configured")
	}
	raw, err := EncodeProjectMetadata(metadata)
	if err != nil {
		return workspace.MutationResult{}, err
	}
	if expectedVersion == "" {
		return store.CreateFile(ctx, scope, workspace.CreateOptions{Path: ProjectMetadataPath, Content: string(raw)})
	}
	return store.ReplaceFile(ctx, scope, workspace.ReplaceOptions{Path: ProjectMetadataPath, Content: string(raw), ExpectedVersion: expectedVersion})
}

// NewProjectSourceForSkillsRoot is a descriptive alias retained for callers
// that want to make the source root explicit at the call site.
func NewProjectSourceForSkillsRoot(store *workspace.FileStore, scope workspace.Scope, root string) (*ProjectSource, error) {
	return NewProjectSourceWithRoot(store, scope, root)
}

func (s *ProjectSource) Scope() Scope { return ScopeProject }

func (s *ProjectSource) List(ctx context.Context, maxPackages int) (PackageList, error) {
	if err := ctx.Err(); err != nil {
		return PackageList{}, err
	}
	limit := maxPackages
	if limit <= 0 || limit > s.limits.MaxPackages {
		limit = s.limits.MaxPackages
	}
	fileLimit := s.limits.MaxProjectFileCount
	if fileLimit < limit {
		fileLimit = limit
	}
	if fileLimit > workspace.MaxListLimit {
		fileLimit = workspace.MaxListLimit
	}
	listing, err := s.store.ListFiles(ctx, s.scope, workspace.ListOptions{Limit: fileLimit})
	if err != nil {
		return PackageList{}, fmt.Errorf("list project skill files: %w", err)
	}
	warnings := make([]Warning, 0)
	metadata, _, metadataErr := ReadProjectMetadata(ctx, s.store, s.scope)
	metadataInvalid := metadataErr != nil
	if metadataErr != nil {
		warnings = appendBoundedWarning(warnings, Warning{Scope: ScopeProject, Code: "activation_metadata_invalid", Message: "project skill activation metadata is invalid; packages are disabled"}, s.limits.MaxWarnings)
	}
	if listing.Truncated {
		warnings = appendBoundedWarning(warnings, Warning{Scope: ScopeProject, Code: "project_file_limit", Message: "project skill file listing reached its bound"}, s.limits.MaxWarnings)
	}
	packages := make(map[string]*Package)
	resourceFiles := make([]workspace.FileInfo, 0)
	skillPaths := make(map[string]string)
	for _, info := range listing.Files {
		if err := ctx.Err(); err != nil {
			return PackageList{}, err
		}
		relative, ok := relativeToRoot(info.Path, s.root)
		if !ok {
			continue
		}
		if relative == "SKILL.md" || strings.HasSuffix(relative, "/SKILL.md") {
			packagePath := path.Dir(relative)
			if packagePath == "." {
				warnings = appendBoundedWarning(warnings, Warning{Scope: ScopeProject, PackagePath: relative, Code: "invalid_package_path", Message: "project SKILL.md must be inside a package directory"}, s.limits.MaxWarnings)
				continue
			}
			packagePath, cleanErr := cleanPublicPackagePath(packagePath)
			if cleanErr != nil {
				warnings = appendBoundedWarning(warnings, Warning{Scope: ScopeProject, PackagePath: packagePath, Code: "invalid_package_path", Message: "project skill package path is invalid"}, s.limits.MaxWarnings)
				continue
			}
			if _, exists := skillPaths[packagePath]; exists {
				warnings = appendBoundedWarning(warnings, Warning{Scope: ScopeProject, PackagePath: packagePath, Code: "duplicate_skill_file", Message: "project skill package contains duplicate SKILL.md entries"}, s.limits.MaxWarnings)
				continue
			}
			skillPaths[packagePath] = path.Join(s.root, relative)
			continue
		}
		packagePath := path.Dir(relative)
		if packagePath == "." {
			continue
		}
		packagePath, cleanErr := cleanPublicPackagePath(packagePath)
		if cleanErr != nil {
			continue
		}
		resourceFiles = append(resourceFiles, info)
	}
	resourceInfos := make(map[string][]workspace.FileInfo)
	for _, info := range resourceFiles {
		relative, ok := relativeToRoot(info.Path, s.root)
		if !ok {
			continue
		}
		best := ""
		for packagePath := range skillPaths {
			if relative == packagePath || strings.HasPrefix(relative, packagePath+"/") {
				if len(packagePath) > len(best) {
					best = packagePath
				}
			}
		}
		if best != "" {
			resourceInfos[best] = append(resourceInfos[best], info)
		}
	}
	orderedSkillPaths := make([]string, 0, len(skillPaths))
	for packagePath := range skillPaths {
		orderedSkillPaths = append(orderedSkillPaths, packagePath)
	}
	sort.Strings(orderedSkillPaths)
	for _, packagePath := range orderedSkillPaths {
		skillPath := skillPaths[packagePath]
		content, readErr := s.store.ReadFile(ctx, s.scope, workspace.ReadOptions{Path: skillPath, MaxBytes: s.limits.MaxSkillBytes})
		if readErr != nil || content.Binary || content.Truncated {
			warnings = appendBoundedWarning(warnings, Warning{Scope: ScopeProject, PackagePath: packagePath, Code: "skill_read_failed", Message: "project SKILL.md could not be read"}, s.limits.MaxWarnings)
			continue
		}
		packageEntry := &Package{Path: packagePath, SkillContent: []byte(content.Content), Enabled: true}
		if metadataInvalid {
			packageEntry.EnabledSet = true
			packageEntry.Enabled = false
		} else if activation, exists := metadata.Packages[packagePath]; exists {
			packageEntry.EnabledSet = true
			if activationMatchesPackageVersion(*packageEntry, activation) {
				packageEntry.Enabled = activation.Enabled
			} else {
				packageEntry.Enabled = false
				warnings = appendBoundedWarning(warnings, Warning{Scope: ScopeProject, PackagePath: packagePath, Code: "activation_stale", Message: "skill activation metadata is stale; package is disabled"}, s.limits.MaxWarnings)
			}
		}
		for _, info := range resourceInfos[packagePath] {
			relative, ok := relativeToRoot(info.Path, s.root)
			if !ok {
				continue
			}
			resourcePath := strings.TrimPrefix(relative, packagePath+"/")
			resourcePath, pathErr := cleanPublicResourcePath(resourcePath)
			if pathErr != nil || resourcePath == "SKILL.md" {
				continue
			}
			packageEntry.Resources = append(packageEntry.Resources, ResourceRef{Path: resourcePath, Size: info.Size})
		}
		packages[packagePath] = packageEntry
	}
	packagePaths := make([]string, 0, len(packages))
	for packagePath := range packages {
		packagePaths = append(packagePaths, packagePath)
	}
	sort.Strings(packagePaths)
	ordered := make([]Package, 0, min(len(packages), limit))
	for _, packagePath := range packagePaths {
		if len(ordered) >= limit {
			break
		}
		packageEntry := packages[packagePath]
		sort.Slice(packageEntry.Resources, func(i, j int) bool { return packageEntry.Resources[i].Path < packageEntry.Resources[j].Path })
		if len(packageEntry.Resources) > s.limits.MaxResourceCount {
			packageEntry.Resources = packageEntry.Resources[:s.limits.MaxResourceCount]
			warnings = appendBoundedWarning(warnings, Warning{Scope: ScopeProject, PackagePath: packageEntry.Path, Code: "resource_limit", Message: "supporting-resource listing reached its bound"}, s.limits.MaxWarnings)
		}
		ordered = append(ordered, *packageEntry)
	}
	return PackageList{Packages: ordered, Warnings: warnings, Truncated: len(packages) > len(ordered)}, nil
}

func relativeToRoot(filePath, root string) (string, bool) {
	filePath = strings.TrimPrefix(strings.ReplaceAll(filePath, "\\", "/"), "./")
	root = strings.TrimPrefix(strings.ReplaceAll(root, "\\", "/"), "./")
	if root == "." || root == "" {
		return filePath, true
	}
	prefix := root + "/"
	if !strings.HasPrefix(filePath, prefix) {
		return "", false
	}
	relative := strings.TrimPrefix(filePath, prefix)
	if relative == "" || strings.HasPrefix(relative, "../") {
		return "", false
	}
	return relative, true
}

func (s *ProjectSource) ReadResource(ctx context.Context, packagePath, resourcePath string, opts ResourceReadOptions) (ResourceReadResult, error) {
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
	bounded, err := boundedReadOptions(opts, s.limits.MaxResourceRead)
	if err != nil {
		return ResourceReadResult{}, err
	}
	maxBytes := bounded.Limit
	if bounded.Offset > 0 {
		maxBytes += int(bounded.Offset)
	}
	if maxBytes > workspace.MaxReadMaxBytes {
		maxBytes = workspace.MaxReadMaxBytes
	}
	file, err := s.store.ReadFile(ctx, s.scope, workspace.ReadOptions{Path: path.Join(s.root, packagePath, resourcePath), MaxBytes: maxBytes})
	if err != nil {
		return ResourceReadResult{}, ErrResourceNotFound
	}
	if file.Binary {
		return ResourceReadResult{}, fmt.Errorf("binary supporting resources are not exposed")
	}
	data := []byte(file.Content)
	if bounded.Offset > int64(len(data)) {
		return ResourceReadResult{Path: resourcePath, Size: file.Size, Offset: bounded.Offset}, nil
	}
	end := int64(len(data))
	if remaining := bounded.Offset + int64(bounded.Limit); end > remaining {
		end = remaining
	}
	chunk := append([]byte(nil), data[bounded.Offset:end]...)
	truncated := file.Truncated || end < file.Size
	result := ResourceReadResult{Path: resourcePath, Content: chunk, Size: file.Size, Offset: bounded.Offset, Truncated: truncated}
	if truncated {
		result.NextOffset = bounded.Offset + int64(len(chunk))
	}
	return result, nil
}
