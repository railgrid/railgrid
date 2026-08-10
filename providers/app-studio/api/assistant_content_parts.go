// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/faroshq/provider-app-studio/store"
)

const (
	projectAssistantMaxContentParts         = 64
	projectAssistantMaxContentPartBytes     = 64 << 10
	projectAssistantContentPartTextType     = "text"
	projectAssistantContentPartSkillType    = "skill"
	projectAssistantContentPartResourceType = "resource"
)

// projectAssistantContentPart is the bounded structured representation carried
// by a turn. The private presence bits let strict JSON decoding distinguish a
// missing resourceIndex from the valid value zero while keeping the internal
// representation convenient for callers and canonical projections.
type projectAssistantContentPart struct {
	Type          string `json:"type"`
	Text          string `json:"text,omitempty"`
	SkillID       string `json:"skillID,omitempty"`
	ResourceIndex int    `json:"resourceIndex,omitempty"`

	decoded          bool
	textSet          bool
	skillIDSet       bool
	resourceIndexSet bool
}

func (p *projectAssistantContentPart) UnmarshalJSON(raw []byte) error {
	if p == nil {
		return fmt.Errorf("content part is required")
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&fields); err != nil {
		return fmt.Errorf("content part must be an object: %w", err)
	}
	if fields == nil {
		return fmt.Errorf("content part must be an object")
	}
	*p = projectAssistantContentPart{decoded: true}
	for key, value := range fields {
		switch key {
		case "type":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("content part type must be a string")
			}
			if err := json.Unmarshal(value, &p.Type); err != nil {
				return fmt.Errorf("content part type must be a string")
			}
		case "text":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("content part text must be a string")
			}
			if err := json.Unmarshal(value, &p.Text); err != nil {
				return fmt.Errorf("content part text must be a string")
			}
			p.textSet = true
		case "skillID":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("content part skillID must be a string")
			}
			if err := json.Unmarshal(value, &p.SkillID); err != nil {
				return fmt.Errorf("content part skillID must be a string")
			}
			p.skillIDSet = true
		case "resourceIndex":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("content part resourceIndex must be an integer")
			}
			if err := json.Unmarshal(value, &p.ResourceIndex); err != nil {
				return fmt.Errorf("content part resourceIndex must be an integer")
			}
			p.resourceIndexSet = true
		default:
			return fmt.Errorf("unknown content part field %q", key)
		}
	}
	return nil
}

func (p projectAssistantContentPart) MarshalJSON() ([]byte, error) {
	fields := map[string]any{"type": p.Type}
	switch p.Type {
	case projectAssistantContentPartTextType:
		fields["text"] = p.Text
	case projectAssistantContentPartSkillType:
		fields["skillID"] = p.SkillID
	case projectAssistantContentPartResourceType:
		fields["resourceIndex"] = p.ResourceIndex
	default:
		// Preserve the type in diagnostics/projections. Validation rejects this
		// value before a durable write, but marshaling remains deterministic.
		if p.Text != "" {
			fields["text"] = p.Text
		}
		if p.SkillID != "" {
			fields["skillID"] = p.SkillID
		}
		if p.resourceIndexSet || p.ResourceIndex != 0 {
			fields["resourceIndex"] = p.ResourceIndex
		}
	}
	return json.Marshal(fields)
}

func cloneProjectAssistantContentParts(in []projectAssistantContentPart) []projectAssistantContentPart {
	return append([]projectAssistantContentPart(nil), in...)
}

func projectAssistantContentPartsSupplied(parts []projectAssistantContentPart) bool {
	return len(parts) > 0
}

// canonicalProjectAssistantContextResources returns the sorted resource
// references together with a raw-index -> canonical-index map. Parts refer to
// the request's original array; durable state refers only to the sorted,
// deduplicated representation.
func canonicalProjectAssistantContextResources(raw []projectAssistantContextResourceInput) ([]projectAssistantContextResourceInput, map[int]int, error) {
	canonical, err := normalizeProjectAssistantContextResources(raw)
	if err != nil {
		return nil, nil, err
	}
	byKey := make(map[string]int, len(canonical))
	for index := range canonical {
		byKey[automaticProviderReferenceKey(canonical[index].Provider, &canonical[index].ResourceRef)] = index
	}
	rawToCanonical := make(map[int]int, len(raw))
	for index, item := range raw {
		normalized, normalizeErr := normalizeProjectAssistantContextResources([]projectAssistantContextResourceInput{item})
		if normalizeErr != nil {
			return nil, nil, normalizeErr
		}
		if len(normalized) != 1 {
			return nil, nil, newValidationError("invalid context resource")
		}
		key := automaticProviderReferenceKey(normalized[0].Provider, &normalized[0].ResourceRef)
		canonicalIndex, ok := byKey[key]
		if !ok {
			return nil, nil, newValidationError("invalid context resource")
		}
		rawToCanonical[index] = canonicalIndex
	}
	return canonical, rawToCanonical, nil
}

func projectAssistantContentPartText(text string) projectAssistantContentPart {
	return projectAssistantContentPart{Type: projectAssistantContentPartTextType, Text: text, textSet: true}
}

func projectAssistantContentPartSkill(skillID string) projectAssistantContentPart {
	return projectAssistantContentPart{Type: projectAssistantContentPartSkillType, SkillID: skillID, skillIDSet: true}
}

func projectAssistantContentPartResource(index int) projectAssistantContentPart {
	return projectAssistantContentPart{Type: projectAssistantContentPartResourceType, ResourceIndex: index, resourceIndexSet: true}
}

// normalizeProjectAssistantContentParts validates and canonicalizes the
// structured turn payload. It is deliberately independent from discovery:
// selected receipts still come from the normal skill/provider authorities,
// while this function only binds user-provided references to those selections.
func normalizeProjectAssistantContentParts(
	raw []projectAssistantContentPart,
	selectedSkillIDs []string,
	resources []projectAssistantContextResourceInput,
) ([]projectAssistantContentPart, []projectAssistantContextResourceInput, string, error) {
	canonicalResources, rawToCanonical, err := canonicalProjectAssistantContextResources(resources)
	if err != nil {
		return nil, nil, "", err
	}
	selectedSkills := make(map[string]struct{}, len(selectedSkillIDs))
	for _, id := range projectAssistantCanonicalSkillIDs(selectedSkillIDs) {
		selectedSkills[id] = struct{}{}
	}
	selectedResources := make(map[int]struct{}, len(canonicalResources))
	for index := range canonicalResources {
		selectedResources[index] = struct{}{}
	}

	parts := make([]projectAssistantContentPart, 0, min(len(raw), projectAssistantMaxContentParts))
	seenSkills := make(map[string]struct{}, len(selectedSkills))
	seenResources := make(map[int]struct{}, len(selectedResources))
	var derived strings.Builder
	for _, original := range raw {
		part := original
		part.Type = strings.ToLower(strings.TrimSpace(part.Type))
		if !part.decoded {
			// Internal tests and callers may construct parts directly. Infer
			// presence for non-zero fields while keeping JSON's missing index
			// distinction in the decoded path above.
			part.textSet = part.textSet || part.Text != ""
			part.skillIDSet = part.skillIDSet || part.SkillID != ""
			part.resourceIndexSet = part.resourceIndexSet || part.ResourceIndex != 0 || part.Type == projectAssistantContentPartResourceType
		}
		var canonical projectAssistantContentPart
		switch part.Type {
		case projectAssistantContentPartTextType:
			if part.skillIDSet || part.resourceIndexSet || part.SkillID != "" || part.ResourceIndex != 0 {
				return nil, nil, "", newValidationError("text content parts cannot include skillID or resourceIndex")
			}
			if !utf8.ValidString(part.Text) {
				return nil, nil, "", newValidationError("text content parts must be valid UTF-8")
			}
			if part.Text == "" {
				continue
			}
			canonical = projectAssistantContentPartText(part.Text)
			if len(parts) > 0 && parts[len(parts)-1].Type == projectAssistantContentPartTextType {
				parts[len(parts)-1].Text += canonical.Text
			} else {
				parts = append(parts, canonical)
			}
			derived.WriteString(part.Text)
		case projectAssistantContentPartSkillType:
			if part.textSet || part.resourceIndexSet || part.Text != "" || part.ResourceIndex != 0 {
				return nil, nil, "", newValidationError("skill content parts cannot include text or resourceIndex")
			}
			id := strings.TrimSpace(part.SkillID)
			if id == "" {
				return nil, nil, "", newValidationError("skill content parts require skillID")
			}
			if !utf8.ValidString(id) {
				return nil, nil, "", newValidationError("skill content part skillID must be valid UTF-8")
			}
			if _, ok := selectedSkills[id]; !ok {
				return nil, nil, "", newValidationError(fmt.Sprintf("content part references unselected assistant skill %q", id))
			}
			canonical = projectAssistantContentPartSkill(id)
			parts = append(parts, canonical)
			seenSkills[id] = struct{}{}
			derived.WriteString("[@skill:")
			derived.WriteString(id)
			derived.WriteByte(']')
		case projectAssistantContentPartResourceType:
			if part.textSet || part.skillIDSet || part.Text != "" || part.SkillID != "" {
				return nil, nil, "", newValidationError("resource content parts cannot include text or skillID")
			}
			if !part.resourceIndexSet {
				return nil, nil, "", newValidationError("resource content parts require resourceIndex")
			}
			if part.ResourceIndex < 0 || part.ResourceIndex >= len(resources) {
				return nil, nil, "", newValidationError("resource content part index is out of range")
			}
			canonicalIndex, ok := rawToCanonical[part.ResourceIndex]
			if !ok {
				return nil, nil, "", newValidationError("resource content part index is invalid")
			}
			canonical = projectAssistantContentPartResource(canonicalIndex)
			parts = append(parts, canonical)
			seenResources[canonicalIndex] = struct{}{}
			ref := canonicalResources[canonicalIndex]
			derived.WriteString("[@resource:")
			derived.WriteString(strings.TrimSpace(ref.Provider))
			derived.WriteByte('/')
			derived.WriteString(strings.TrimSpace(ref.ResourceRef.APIVersion))
			derived.WriteByte('/')
			derived.WriteString(strings.TrimSpace(ref.ResourceRef.Kind))
			derived.WriteByte('/')
			derived.WriteString(strings.TrimSpace(ref.ResourceRef.Resource))
			derived.WriteByte('/')
			derived.WriteString(strings.TrimSpace(ref.ResourceRef.Name))
			derived.WriteByte(']')
		default:
			return nil, nil, "", newValidationError(fmt.Sprintf("unknown content part type %q", part.Type))
		}
		if len(parts) > projectAssistantMaxContentParts {
			return nil, nil, "", newValidationError(fmt.Sprintf("contentParts must contain at most %d normalized entries", projectAssistantMaxContentParts))
		}
		if derived.Len() > projectAssistantMaxContentPartBytes {
			return nil, nil, "", newValidationError(fmt.Sprintf("contentParts derived content must be at most %d bytes", projectAssistantMaxContentPartBytes))
		}
	}
	if len(parts) == 0 {
		return nil, nil, "", newValidationError("contentParts must contain at least one non-empty part")
	}
	for skillID := range selectedSkills {
		if _, ok := seenSkills[skillID]; !ok {
			return nil, nil, "", newValidationError(fmt.Sprintf("contentParts must represent selected assistant skill %q", skillID))
		}
	}
	for resourceIndex := range selectedResources {
		if _, ok := seenResources[resourceIndex]; !ok {
			return nil, nil, "", newValidationError(fmt.Sprintf("contentParts must represent selected context resource %d", resourceIndex))
		}
	}
	if !utf8.ValidString(derived.String()) {
		return nil, nil, "", newValidationError("contentParts derived content must be valid UTF-8")
	}
	return parts, canonicalResources, derived.String(), nil
}

func projectAssistantContentPartsCanonicalJSON(parts []projectAssistantContentPart) json.RawMessage {
	if len(parts) == 0 {
		return nil
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return nil
	}
	return raw
}

func projectAssistantContentPartsFromRunAudit(run store.AssistantRun) []projectAssistantContentPart {
	var audit projectAssistantRunAudit
	if len(run.Audit) == 0 || json.Unmarshal(run.Audit, &audit) != nil {
		return nil
	}
	return cloneProjectAssistantContentParts(audit.ContentParts)
}

func projectAssistantThreadSelectionDataWithParts(
	skills []projectAssistantSkillReceipt,
	resources []projectAssistantContextResourceReceipt,
	parts []projectAssistantContentPart,
) json.RawMessage {
	data := map[string]any{}
	if views := projectAssistantSkillViewsFromReceipts(skills); len(views) > 0 {
		data["skills"] = views
	}
	if views := projectAssistantContextResourceViews(resources); len(views) > 0 {
		data["contextResources"] = views
	}
	if len(parts) > 0 {
		data["contentParts"] = parts
	}
	if len(data) == 0 {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	return raw
}

// projectAssistantCanonicalContentPartsForAudit returns a detached copy. Part
// order remains the model-facing order and therefore is intentionally preserved.
func projectAssistantCanonicalContentPartsForAudit(parts []projectAssistantContentPart) []projectAssistantContentPart {
	return cloneProjectAssistantContentParts(parts)
}

func projectAssistantCanonicalContentPartsForIdentity(parts []projectAssistantContentPart, skills []string, resources []projectAssistantContextResourceInput) []projectAssistantContentPart {
	if len(parts) == 0 {
		return nil
	}
	canonical, _, _, err := normalizeProjectAssistantContentParts(parts, skills, resources)
	if err == nil {
		return canonical
	}
	return cloneProjectAssistantContentParts(parts)
}
