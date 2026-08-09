// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type catalogSkillArtifact struct {
	PackageName string
	Version     string
	Digest      string
	Skill       string
	Reference   string
}

func TestDatabricksAssistantSkillArtifactsAreVersionedAndMirrored(t *testing.T) {
	skill := mustReadDatabricksSkillFile(t, "skills/databricks-app-integration/SKILL.md")
	reference := mustReadDatabricksSkillFile(t, "skills/databricks-app-integration/references/action-contract.md")

	if !strings.Contains(skill, "integration alias is an SDK selector only") ||
		!strings.Contains(skill, "exact Table name supplied by the grant") ||
		!strings.Contains(skill, "Make at most one schema probe in this assistant turn") ||
		!strings.Contains(skill, "`describe_table` and MCP `query_table` are not an application schema-") {
		t.Fatal("canonical skill is missing the alias/tableRef or one-probe authority contract")
	}

	manifest := mustExtractDatabricksCatalogSkill(t, "manifest.yaml", 8, "\n      resources:", "\n  apiExport:")
	chart := mustExtractDatabricksCatalogSkill(t, "deploy/chart/templates/catalogentry.yaml", 12, "\n          resources:", "\n      apiExport:")
	for name, artifact := range map[string]catalogSkillArtifact{"manifest": manifest, "chart": chart} {
		t.Run(name, func(t *testing.T) {
			if artifact.PackageName != "databricks-app-integration" {
				t.Fatalf("packageName = %q, want databricks-app-integration", artifact.PackageName)
			}
			if artifact.Version != "1.0.1" {
				t.Fatalf("version = %q, want 1.0.1", artifact.Version)
			}
			if artifact.Skill != skill {
				t.Fatalf("embedded skill does not mirror canonical SKILL.md")
			}
			if artifact.Reference != reference {
				t.Fatalf("embedded reference does not mirror canonical action-contract.md")
			}
			if got := databricksSkillDigest(artifact.PackageName, artifact.Version, artifact.Skill, artifact.Reference); got != artifact.Digest {
				t.Fatalf("digest = %q, want computed content digest %q", artifact.Digest, got)
			}
		})
	}
	if manifest.Digest != chart.Digest || manifest.Skill != chart.Skill || manifest.Reference != chart.Reference {
		t.Fatal("manifest and chart assistant skill artifacts drifted")
	}
}

func mustReadDatabricksSkillFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func mustExtractDatabricksCatalogSkill(t *testing.T, path string, skillIndent int, resourceMarker, apiExportMarker string) catalogSkillArtifact {
	t.Helper()
	content := mustReadDatabricksSkillFile(t, path)
	assistantStart := strings.Index(content, "\n  assistantSkills:")
	if assistantStart < 0 {
		assistantStart = strings.Index(content, "\n      assistantSkills:")
	}
	if assistantStart < 0 {
		t.Fatalf("%s has no assistantSkills block", path)
	}
	content = content[assistantStart:]
	packageName := extractDatabricksCatalogValue(t, path, content, "packageName: \"")
	version := extractDatabricksCatalogValue(t, path, content, "version: \"")
	digest := extractDatabricksCatalogValue(t, path, content, "digest: \"")

	skillMarker := "skill: |\n"
	skillStart := strings.Index(content, skillMarker)
	if skillStart < 0 {
		t.Fatalf("%s has no inline skill", path)
	}
	skillStart += len(skillMarker)
	skillEnd := strings.Index(content[skillStart:], resourceMarker)
	if skillEnd < 0 {
		t.Fatalf("%s has no resource boundary after skill", path)
	}
	skillEnd += skillStart
	skill := dedentDatabricksCatalogBlock(content[skillStart:skillEnd], skillIndent)

	referenceMarker := "content: |\n"
	referenceStart := strings.Index(content[skillEnd:], referenceMarker)
	if referenceStart < 0 {
		t.Fatalf("%s has no inline reference", path)
	}
	referenceStart += skillEnd + len(referenceMarker)
	referenceEnd := strings.Index(content[referenceStart:], apiExportMarker)
	if referenceEnd < 0 {
		t.Fatalf("%s has no API export boundary after reference", path)
	}
	referenceEnd += referenceStart
	reference := dedentDatabricksCatalogBlock(content[referenceStart:referenceEnd], skillIndent+4)

	return catalogSkillArtifact{PackageName: packageName, Version: version, Digest: digest, Skill: skill, Reference: reference}
}

func extractDatabricksCatalogValue(t *testing.T, path, content, marker string) string {
	t.Helper()
	start := strings.Index(content, marker)
	if start < 0 {
		t.Fatalf("%s has no %q", path, marker)
	}
	start += len(marker)
	end := strings.IndexByte(content[start:], '"')
	if end < 0 {
		t.Fatalf("%s has unterminated %q", path, marker)
	}
	return content[start : start+end]
}

func dedentDatabricksCatalogBlock(content string, indent int) string {
	prefix := strings.Repeat(" ", indent)
	var out strings.Builder
	for _, line := range strings.SplitAfter(content, "\n") {
		if strings.TrimSpace(line) == "" {
			out.WriteString(line)
			continue
		}
		if !strings.HasPrefix(line, prefix) {
			panic("catalog block line has less indentation than its YAML scalar")
		}
		out.WriteString(strings.TrimPrefix(line, prefix))
	}
	return out.String()
}

func databricksSkillDigest(packageName, version, skill, reference string) string {
	type canonicalResource struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	envelope := struct {
		PackageName string              `json:"packageName"`
		Version     string              `json:"version"`
		Skill       string              `json:"skill"`
		Resources   []canonicalResource `json:"resources,omitempty"`
	}{
		PackageName: packageName,
		Version:     version,
		Skill:       skill,
		Resources:   []canonicalResource{{Path: "references/action-contract.md", Content: reference}},
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}
