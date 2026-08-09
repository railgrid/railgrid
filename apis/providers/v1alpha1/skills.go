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

package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// ProviderAssistantSkillMaxCount bounds one CatalogEntry's package list.
	ProviderAssistantSkillMaxCount = 64
	// ProviderAssistantSkillMaxDocumentBytes bounds one raw SKILL.md document.
	ProviderAssistantSkillMaxDocumentBytes = 32 << 10
	// ProviderAssistantSkillMaxResourceCount bounds one package's resources.
	ProviderAssistantSkillMaxResourceCount = 64
	// ProviderAssistantSkillMaxResourceBytes bounds one supporting resource.
	ProviderAssistantSkillMaxResourceBytes = 64 << 10
	// ProviderAssistantSkillMaxAggregateBytes bounds one package's total body and
	// resource bytes. It is deliberately lower than the aggregate catalog bound
	// enforced by App Studio's skill source.
	ProviderAssistantSkillMaxAggregateBytes = 4 << 20
	// ProviderAssistantSkillsMaxAggregateBytes bounds all raw SKILL.md and
	// supporting-resource bytes in one CatalogEntry. It keeps the authenticated
	// provider catalog response bounded even when every package is individually
	// valid.
	ProviderAssistantSkillsMaxAggregateBytes = 512 << 10
)

var providerAssistantSkillDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// ValidateProviderAssistantSkills validates list-level uniqueness and bounds.
// The hub controller also validates each package independently so one malformed
// package can be omitted without dropping valid sibling packages.
func ValidateProviderAssistantSkills(skills []ProviderAssistantSkillSpec) error {
	if len(skills) > ProviderAssistantSkillMaxCount {
		return fmt.Errorf("assistantSkills exceeds %d packages", ProviderAssistantSkillMaxCount)
	}
	seen := make(map[string]struct{}, len(skills))
	var aggregateBytes int64
	for index, skill := range skills {
		if err := ValidateProviderAssistantSkill(skill); err != nil {
			return fmt.Errorf("assistantSkills[%d] (%q): %w", index, skill.PackageName, err)
		}
		if _, exists := seen[skill.PackageName]; exists {
			return fmt.Errorf("assistantSkills[%d] (%q): duplicate packageName", index, skill.PackageName)
		}
		seen[skill.PackageName] = struct{}{}
		aggregateBytes += int64(len([]byte(skill.Skill)))
		for _, resource := range skill.Resources {
			aggregateBytes += int64(len([]byte(resource.Content)))
		}
		if aggregateBytes > ProviderAssistantSkillsMaxAggregateBytes {
			return fmt.Errorf("assistantSkills aggregate exceeds %d bytes", ProviderAssistantSkillsMaxAggregateBytes)
		}
	}
	return nil
}

// ValidateProviderAssistantSkill validates one inline provider skill package.
// It intentionally validates only the declarative artifact: no URL, runtime,
// credential, tool, model, or authority field exists in this contract.
func ValidateProviderAssistantSkill(skill ProviderAssistantSkillSpec) error {
	packageName := strings.TrimSpace(skill.PackageName)
	if packageName == "" || packageName != skill.PackageName {
		return fmt.Errorf("packageName must be a clean relative path")
	}
	if len([]byte(packageName)) > 128 || !validProviderSkillPath(packageName) {
		return fmt.Errorf("packageName must be a bounded relative path")
	}
	version := strings.TrimSpace(skill.Version)
	if version == "" || version != skill.Version {
		return fmt.Errorf("version is required and must not contain surrounding whitespace")
	}
	if len([]byte(version)) > 64 || containsControl(version) {
		return fmt.Errorf("version is invalid or exceeds 64 bytes")
	}
	if len([]byte(skill.Skill)) == 0 {
		return fmt.Errorf("skill is required")
	}
	if len([]byte(skill.Skill)) > ProviderAssistantSkillMaxDocumentBytes {
		return fmt.Errorf("skill exceeds %d bytes", ProviderAssistantSkillMaxDocumentBytes)
	}
	if !utf8.ValidString(skill.Skill) || strings.ContainsRune(skill.Skill, '\x00') {
		return fmt.Errorf("skill must be valid UTF-8 without NUL bytes")
	}
	if len(skill.Resources) > ProviderAssistantSkillMaxResourceCount {
		return fmt.Errorf("resources exceeds %d files", ProviderAssistantSkillMaxResourceCount)
	}
	seenPaths := make(map[string]struct{}, len(skill.Resources))
	aggregate := len([]byte(skill.Skill))
	for index, resource := range skill.Resources {
		path := strings.TrimSpace(resource.Path)
		if path == "" || path != resource.Path || len([]byte(path)) > 256 || !validProviderSkillResourcePath(path) {
			return fmt.Errorf("resources[%d].path is invalid", index)
		}
		if _, exists := seenPaths[path]; exists {
			return fmt.Errorf("resources[%d].path is duplicated", index)
		}
		seenPaths[path] = struct{}{}
		if len([]byte(resource.Content)) > ProviderAssistantSkillMaxResourceBytes {
			return fmt.Errorf("resources[%d].content exceeds %d bytes", index, ProviderAssistantSkillMaxResourceBytes)
		}
		if !utf8.ValidString(resource.Content) || strings.ContainsRune(resource.Content, '\x00') {
			return fmt.Errorf("resources[%d].content must be valid UTF-8 without NUL bytes", index)
		}
		aggregate += len([]byte(resource.Content))
		if aggregate > ProviderAssistantSkillMaxAggregateBytes {
			return fmt.Errorf("skill package exceeds %d aggregate bytes", ProviderAssistantSkillMaxAggregateBytes)
		}
	}
	if !providerAssistantSkillDigestPattern.MatchString(skill.Digest) {
		return fmt.Errorf("digest must match sha256:<64 lowercase hex digits>")
	}
	digest, err := ProviderAssistantSkillDigest(skill)
	if err != nil {
		return err
	}
	if skill.Digest != digest {
		return fmt.Errorf("digest %q does not match canonical package (want %s)", skill.Digest, digest)
	}
	return nil
}

// ProviderAssistantSkillDigest computes a deterministic sha256 digest over the
// package identity, version, raw SKILL.md bytes, and resources sorted by path.
// Resource content is included directly; callers cannot substitute a remote
// URL, credential, or mutable runtime reference for an inline artifact.
func ProviderAssistantSkillDigest(skill ProviderAssistantSkillSpec) (string, error) {
	if strings.TrimSpace(skill.PackageName) == "" {
		return "", fmt.Errorf("packageName is required")
	}
	if strings.TrimSpace(skill.Version) == "" {
		return "", fmt.Errorf("version is required")
	}
	if len([]byte(skill.Skill)) == 0 {
		return "", fmt.Errorf("skill is required")
	}
	type canonicalResource struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	resources := make([]canonicalResource, 0, len(skill.Resources))
	for _, resource := range skill.Resources {
		resources = append(resources, canonicalResource(resource))
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
	}{PackageName: skill.PackageName, Version: skill.Version, Skill: skill.Skill, Resources: resources}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal skill digest payload: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validProviderSkillPath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\x00') || strings.ContainsRune(value, '\\') {
		return false
	}
	if len(value) >= 2 && value[1] == ':' {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || part == "SKILL.md" || containsControl(part) {
			return false
		}
	}
	return true
}

func validProviderSkillResourcePath(value string) bool {
	if !validProviderSkillPath(value) || value == "SKILL.md" {
		return false
	}
	return !strings.HasSuffix(value, "/SKILL.md")
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
