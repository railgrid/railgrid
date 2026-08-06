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
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	maxSkillNameBytes        = 64
	maxSkillDescriptionBytes = 1024
)

// ParsedSkill is the framework-neutral representation of a SKILL.md file.
// Content contains only the markdown body; YAML authority-bearing fields are
// intentionally not represented.
type ParsedSkill struct {
	Name        string
	Description string
	Content     string
}

// ParseSkill parses bounded YAML frontmatter and a markdown body. The only
// supported frontmatter fields are name and description. Non-empty Eino
// authority fields (context, agent, model) are rejected to prevent a catalog
// load from silently granting execution authority.
func ParseSkill(data []byte, limits Limits) (ParsedSkill, error) {
	limits = limits.bounded()
	if len(data) > limits.MaxSkillBytes {
		return ParsedSkill{}, fmt.Errorf("skill document exceeds %d bytes", limits.MaxSkillBytes)
	}
	frontmatter, body, err := splitFrontmatter(data)
	if err != nil {
		return ParsedSkill{}, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(frontmatter, &document); err != nil {
		return ParsedSkill{}, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return ParsedSkill{}, errors.New("YAML frontmatter must be a mapping")
	}
	values, err := yamlMappingValues(document.Content[0])
	if err != nil {
		return ParsedSkill{}, err
	}
	for _, field := range []string{"context", "agent", "model"} {
		if node, ok := values[field]; ok && yamlNodeNonEmpty(node) {
			return ParsedSkill{}, fmt.Errorf("unsupported authority-bearing frontmatter field %q", field)
		}
	}
	name := strings.TrimSpace(yamlScalarString(values["name"]))
	rawDescription := yamlScalarString(values["description"])
	description := strings.TrimSpace(rawDescription)
	if name == "" {
		return ParsedSkill{}, errors.New("frontmatter name is required")
	}
	if description == "" {
		return ParsedSkill{}, errors.New("frontmatter description is required")
	}
	if err := validateSkillName(name); err != nil {
		return ParsedSkill{}, err
	}
	if len([]byte(name)) > maxSkillNameBytes {
		return ParsedSkill{}, fmt.Errorf("skill name exceeds %d bytes", maxSkillNameBytes)
	}
	if len([]byte(description)) > maxSkillDescriptionBytes {
		return ParsedSkill{}, fmt.Errorf("skill description exceeds %d bytes", maxSkillDescriptionBytes)
	}
	if err := validateSkillDescription(rawDescription); err != nil {
		return ParsedSkill{}, err
	}
	return ParsedSkill{Name: name, Description: description, Content: string(body)}, nil
}

// Parse is a short alias for ParseSkill for callers that already have a
// SKILL.md document in memory.
func Parse(data []byte, limits Limits) (ParsedSkill, error) {
	return ParseSkill(data, limits)
}

func splitFrontmatter(data []byte) ([]byte, []byte, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	firstEnd := bytes.IndexByte(data, '\n')
	if firstEnd < 0 || strings.TrimSpace(strings.TrimSuffix(string(data[:firstEnd]), "\r")) != "---" {
		return nil, nil, errors.New("SKILL.md must start with YAML frontmatter delimiter")
	}
	frontmatterStart := firstEnd + 1
	for lineStart := frontmatterStart; lineStart <= len(data); {
		lineEnd := bytes.IndexByte(data[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(data)
		} else {
			lineEnd += lineStart
		}
		line := strings.TrimSpace(strings.TrimSuffix(string(data[lineStart:lineEnd]), "\r"))
		if line != "---" {
			if lineEnd >= len(data) {
				break
			}
			lineStart = lineEnd + 1
			continue
		}
		bodyStart := lineEnd
		if bodyStart < len(data) {
			bodyStart++
		}
		frontmatter := data[frontmatterStart:lineStart]
		body := data[bodyStart:]
		return frontmatter, body, nil
	}
	return nil, nil, errors.New("SKILL.md frontmatter closing delimiter not found")
}

func yamlMappingValues(mapping *yaml.Node) (map[string]*yaml.Node, error) {
	values := make(map[string]*yaml.Node, len(mapping.Content)/2)
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, errors.New("frontmatter keys must be strings")
		}
		if _, exists := values[key.Value]; exists {
			return nil, fmt.Errorf("duplicate frontmatter field %q", key.Value)
		}
		values[key.Value] = mapping.Content[index+1]
	}
	return values, nil
}

func yamlNodeNonEmpty(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.ScalarNode {
		if node.Tag == "!!null" {
			return false
		}
		return strings.TrimSpace(node.Value) != ""
	}
	return len(node.Content) > 0
}

func yamlScalarString(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag == "!!null" {
		return ""
	}
	return node.Value
}

func validateSkillDescription(description string) error {
	for _, r := range description {
		if unicode.IsControl(r) || r == '\n' || r == '\r' {
			return errors.New("skill description must be a single line without control characters")
		}
	}
	return nil
}

func validateSkillName(name string) error {
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("skill name contains a control character")
		}
		if r == '/' || r == '\\' {
			return errors.New("skill name cannot contain path separators")
		}
	}
	return nil
}
