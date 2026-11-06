// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-sdk/attachments"
)

const (
	maxAgentChatContentParts     = 64
	maxAgentChatPartTextBytes    = 64 << 10
	maxAgentChatDerivedTextBytes = 256 << 10
)

// agentChatContentPart follows the shared composer part grammar. Receipts and
// coordinates are pointers only; uploaded bytes are kept in the attachment
// store and never copied into a request transcript.
type agentChatContentPart struct {
	Type          string                        `json:"type"`
	Text          string                        `json:"text,omitempty"`
	SkillID       string                        `json:"skillID,omitempty"`
	ResourceIndex int                           `json:"resourceIndex,omitempty"`
	Attachment    *store.AgentAttachmentReceipt `json:"attachment,omitempty"`
	decoded       bool
	textSet       bool
	skillIDSet    bool
	resourceSet   bool
	attachSet     bool
}

func (p *agentChatContentPart) UnmarshalJSON(raw []byte) error {
	if p == nil {
		return fmt.Errorf("content part is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return fmt.Errorf("content part must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("content part must contain one JSON object")
	}
	*p = agentChatContentPart{decoded: true}
	for key, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("content part %s cannot be null", key)
		}
		switch key {
		case "type":
			if err := json.Unmarshal(value, &p.Type); err != nil {
				return fmt.Errorf("content part type must be a string")
			}
		case "text":
			if err := json.Unmarshal(value, &p.Text); err != nil {
				return fmt.Errorf("content part text must be a string")
			}
			p.textSet = true
		case "skillID":
			if err := json.Unmarshal(value, &p.SkillID); err != nil {
				return fmt.Errorf("content part skillID must be a string")
			}
			p.skillIDSet = true
		case "resourceIndex":
			if err := json.Unmarshal(value, &p.ResourceIndex); err != nil {
				return fmt.Errorf("content part resourceIndex must be an integer")
			}
			p.resourceSet = true
		case "attachment":
			var receipt store.AgentAttachmentReceipt
			receiptDecoder := json.NewDecoder(bytes.NewReader(value))
			receiptDecoder.DisallowUnknownFields()
			if err := receiptDecoder.Decode(&receipt); err != nil {
				return fmt.Errorf("content part attachment receipt is invalid")
			}
			p.Attachment, p.attachSet = &receipt, true
		default:
			return fmt.Errorf("unknown content part field %q", key)
		}
	}
	return nil
}

func normalizeAgentChatContentParts(raw []agentChatContentPart, skills []agentSkillSelection, rawResources []agentContextResource) ([]agentChatContentPart, []agentContextResource, []store.AgentAttachmentReceipt, string, error) {
	if len(raw) > maxAgentChatContentParts {
		return nil, nil, nil, "", fmt.Errorf("contentParts must contain at most %d entries", maxAgentChatContentParts)
	}
	resources, err := normalizeAgentContextResources(rawResources)
	if err != nil {
		return nil, nil, nil, "", err
	}
	resourceIndexes := make(map[string]int, len(resources))
	for index, resource := range resources {
		resourceIndexes[agentContextResourceKey(resource)] = index
	}
	rawToCanonical := make([]int, len(rawResources))
	for i, resource := range rawResources {
		normalized, err := normalizeAgentContextResources([]agentContextResource{resource})
		if err != nil || len(normalized) != 1 {
			return nil, nil, nil, "", fmt.Errorf("invalid context resource reference")
		}
		index, ok := resourceIndexes[agentContextResourceKey(normalized[0])]
		if !ok {
			return nil, nil, nil, "", fmt.Errorf("invalid context resource reference")
		}
		rawToCanonical[i] = index
	}

	selectedSkills := make(map[string]struct{}, len(skills))
	for _, ref := range skills {
		id := strings.TrimSpace(ref.ID)
		if id == "" || len(id) > 256 || !utf8.ValidString(id) {
			return nil, nil, nil, "", fmt.Errorf("skillRefs contains an invalid skill ID")
		}
		if !validAgentSkillDigest(ref.SHA256) {
			return nil, nil, nil, "", fmt.Errorf("skillRefs contains an invalid digest for %q", id)
		}
		if _, duplicate := selectedSkills[id]; duplicate {
			return nil, nil, nil, "", fmt.Errorf("skillRefs must not repeat %q", id)
		}
		selectedSkills[id] = struct{}{}
	}
	if len(skills) > 8 {
		return nil, nil, nil, "", fmt.Errorf("at most eight skills may be selected for one turn")
	}

	parts := make([]agentChatContentPart, 0, len(raw))
	seenSkills := map[string]bool{}
	seenResources := map[int]bool{}
	seenAttachments := map[string]bool{}
	var receipts []store.AgentAttachmentReceipt
	var totalAttachmentBytes int64
	var derived strings.Builder
	for _, original := range raw {
		part := original
		part.Type = strings.ToLower(strings.TrimSpace(part.Type))
		if !part.decoded {
			part.textSet = part.textSet || part.Text != ""
			part.skillIDSet = part.skillIDSet || part.SkillID != ""
			part.resourceSet = part.resourceSet || part.Type == "resource" || part.ResourceIndex != 0
			part.attachSet = part.attachSet || part.Attachment != nil
		}
		switch part.Type {
		case "text":
			if part.skillIDSet || part.resourceSet || part.attachSet || part.SkillID != "" || part.ResourceIndex != 0 || part.Attachment != nil {
				return nil, nil, nil, "", fmt.Errorf("text content parts cannot include skillID, resourceIndex, or attachment")
			}
			if !utf8.ValidString(part.Text) || len([]byte(part.Text)) > maxAgentChatPartTextBytes {
				return nil, nil, nil, "", fmt.Errorf("text content parts must be valid UTF-8 and at most %d bytes", maxAgentChatPartTextBytes)
			}
			if part.Text == "" {
				continue
			}
			if len(parts) > 0 && parts[len(parts)-1].Type == "text" {
				parts[len(parts)-1].Text += part.Text
			} else {
				parts = append(parts, agentChatContentPart{Type: "text", Text: part.Text, textSet: true})
			}
			derived.WriteString(part.Text)
		case "skill":
			if part.textSet || part.resourceSet || part.attachSet || part.Text != "" || part.ResourceIndex != 0 || part.Attachment != nil {
				return nil, nil, nil, "", fmt.Errorf("skill content parts cannot include text, resourceIndex, or attachment")
			}
			skillID := strings.TrimSpace(part.SkillID)
			if skillID == "" || !part.skillIDSet && part.decoded {
				return nil, nil, nil, "", fmt.Errorf("skill content parts require skillID")
			}
			if _, ok := selectedSkills[skillID]; !ok {
				return nil, nil, nil, "", fmt.Errorf("content part references unselected assistant skill %q", skillID)
			}
			parts = append(parts, agentChatContentPart{Type: "skill", SkillID: skillID, skillIDSet: true})
			seenSkills[skillID] = true
			writeJSONMarker(&derived, map[string]string{"skillID": skillID, "type": "skill"})
		case "resource":
			if part.textSet || part.skillIDSet || part.attachSet || part.Text != "" || part.SkillID != "" || part.Attachment != nil || !part.resourceSet && part.decoded {
				return nil, nil, nil, "", fmt.Errorf("resource content parts require only resourceIndex")
			}
			if part.ResourceIndex < 0 || part.ResourceIndex >= len(rawToCanonical) {
				return nil, nil, nil, "", fmt.Errorf("resource content part index is out of range")
			}
			index := rawToCanonical[part.ResourceIndex]
			parts = append(parts, agentChatContentPart{Type: "resource", ResourceIndex: index, resourceSet: true})
			seenResources[index] = true
			writeJSONMarker(&derived, map[string]any{"resource": resources[index], "type": "resource"})
		case "attachment":
			if part.textSet || part.skillIDSet || part.resourceSet || part.Text != "" || part.SkillID != "" || part.ResourceIndex != 0 || !part.attachSet || part.Attachment == nil {
				return nil, nil, nil, "", fmt.Errorf("attachment content parts require only an attachment receipt")
			}
			receipt := *part.Attachment
			if err := validateAgentAttachmentReceiptInput(receipt); err != nil {
				return nil, nil, nil, "", err
			}
			if seenAttachments[receipt.ID] {
				return nil, nil, nil, "", fmt.Errorf("attachment content parts must not repeat an ID")
			}
			seenAttachments[receipt.ID] = true
			totalAttachmentBytes += receipt.SizeBytes
			if len(seenAttachments) > attachments.MaxAttachmentsPerTurn || totalAttachmentBytes > attachments.MaxBytesPerTurn {
				return nil, nil, nil, "", fmt.Errorf("selected attachments exceed the per-turn attachment limit")
			}
			receipts = append(receipts, receipt)
			parts = append(parts, agentChatContentPart{Type: "attachment", Attachment: &receipt, attachSet: true})
			writeJSONMarker(&derived, map[string]any{"attachment": receipt, "type": "attachment"})
		default:
			return nil, nil, nil, "", fmt.Errorf("unknown content part type %q", part.Type)
		}
		if derived.Len() > maxAgentChatDerivedTextBytes {
			return nil, nil, nil, "", fmt.Errorf("contentParts derived content exceeds %d bytes", maxAgentChatDerivedTextBytes)
		}
	}
	if len(raw) > 0 {
		for id := range selectedSkills {
			if !seenSkills[id] {
				return nil, nil, nil, "", fmt.Errorf("contentParts must represent selected assistant skill %q", id)
			}
		}
		for index := range resources {
			if !seenResources[index] {
				return nil, nil, nil, "", fmt.Errorf("contentParts must represent selected context resource %d", index)
			}
		}
	}
	return parts, resources, receipts, derived.String(), nil
}

func agentContextResourceKey(ref agentContextResource) string {
	return ref.Provider + "\x00" + ref.ResourceRef.APIVersion + "\x00" + ref.ResourceRef.Kind + "\x00" + ref.ResourceRef.Resource + "\x00" + ref.ResourceRef.Name
}

func agentChatContentPartsHaveText(parts []agentChatContentPart) bool {
	for _, part := range parts {
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			return true
		}
	}
	return false
}

func agentAttachmentInputsFromTranscript(message store.Message) []store.AgentAttachmentReceipt {
	if message.Metadata == nil {
		return nil
	}
	raw, ok := message.Metadata["contentParts"]
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var parts []agentChatContentPart
	if json.Unmarshal(encoded, &parts) != nil {
		return nil
	}
	var receipts []store.AgentAttachmentReceipt
	for _, part := range parts {
		if part.Type == "attachment" && part.Attachment != nil && validateAgentAttachmentReceiptInput(*part.Attachment) == nil {
			receipts = append(receipts, *part.Attachment)
		}
	}
	if len(receipts) > attachments.MaxAttachmentsPerTurn {
		return nil
	}
	return receipts
}

// publicAgentChatText projects the user's prose separately from the execution
// prompt's pointer markers. Structured inputs remain in display metadata;
// model history continues to use the original stored content.
func publicAgentChatText(message store.Message) (string, bool) {
	if message.Role != "user" || message.Metadata == nil {
		return "", false
	}
	raw, exists := message.Metadata["contentParts"]
	if !exists {
		return "", false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return "", false
	}
	var parts []agentChatContentPart
	if json.Unmarshal(encoded, &parts) != nil || len(parts) == 0 || len(parts) > maxAgentChatContentParts {
		return "", false
	}
	var text strings.Builder
	for _, part := range parts {
		switch part.Type {
		case "text":
			text.WriteString(part.Text)
		case "skill", "resource", "attachment":
		default:
			return "", false
		}
	}
	return strings.TrimSpace(text.String()), true
}

// sessionAttachmentInputOwners returns owner pointers for accepted attachment
// references already present in the replayable session transcript. It checks
// both append-only rows and the replacement history left by compaction.
func (s *Server) sessionAttachmentInputOwners(ctx context.Context, scope store.Scope, sessionID string) ([]string, error) {
	session, err := s.loadSessionContext(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	var owners []string
	appendTranscript := func(message store.Message) {
		if len(agentAttachmentInputsFromTranscript(message)) == 0 {
			return
		}
		owner, _ := message.Metadata["attachmentOwner"].(string)
		if strings.TrimSpace(owner) == "" {
			owner, _ = message.Metadata["actorID"].(string)
		}
		owners = append(owners, strings.TrimSpace(owner))
	}
	for _, message := range session.Messages {
		appendTranscript(message)
	}
	if session.Checkpoint != nil {
		for _, message := range session.Checkpoint.ReplacementHistory {
			if len(message.InputAttachments) > 0 {
				owners = append(owners, strings.TrimSpace(message.InputAttachmentOwner))
			}
		}
	}
	return owners, nil
}

func renderAgentChatContent(parts []agentChatContentPart, resources []agentContextResource) (string, bool) {
	var derived strings.Builder
	hasText := false
	for _, part := range parts {
		switch part.Type {
		case "text":
			hasText = hasText || strings.TrimSpace(part.Text) != ""
			derived.WriteString(part.Text)
		case "skill":
			derived.WriteByte('\n')
			writeJSONMarker(&derived, map[string]string{"skillID": part.SkillID, "type": "skill"})
			derived.WriteByte('\n')
		case "resource":
			if part.ResourceIndex < 0 || part.ResourceIndex >= len(resources) {
				continue
			}
			derived.WriteByte('\n')
			writeJSONMarker(&derived, map[string]any{"resource": resources[part.ResourceIndex], "type": "resource"})
			derived.WriteByte('\n')
		case "attachment":
			if part.Attachment == nil {
				continue
			}
			derived.WriteByte('\n')
			kind := attachments.AttachmentKindFor(part.Attachment.ContentType, part.Attachment.SizeBytes)
			if kind == attachments.AttachmentKindFile {
				derived.WriteString("Opaque attachment metadata only; file bytes are not exposed to the model: ")
			} else {
				derived.WriteString("Attached image or text input: ")
			}
			writeJSONMarker(&derived, part.Attachment)
			derived.WriteByte('\n')
		}
	}
	return strings.TrimSpace(derived.String()), hasText
}

func agentChatTranscriptMetadata(run taskRun) map[string]any {
	metadata := map[string]any{"actorID": strings.TrimSpace(run.CallerUser)}
	if len(run.AttachmentRefs) > 0 && strings.TrimSpace(run.AttachmentOwner) != "" {
		metadata["attachmentOwner"] = strings.TrimSpace(run.AttachmentOwner)
	}
	if len(run.ContentParts) > 0 {
		metadata["contentParts"] = append([]agentChatContentPart(nil), run.ContentParts...)
	}
	if len(run.ContextResources) > 0 {
		metadata["contextResources"] = append([]agentContextResource(nil), run.ContextResources...)
	}
	if len(run.SkillRefs) > 0 {
		metadata["skillRefs"] = append([]agentSkillSelection(nil), run.SkillRefs...)
	}
	return metadata
}

func validateAgentAttachmentReceiptInput(receipt store.AgentAttachmentReceipt) error {
	if err := attachments.ValidateAttachmentID(receipt.ID); err != nil {
		return fmt.Errorf("attachment receipt is invalid: %w", err)
	}
	contentType, err := attachments.NormalizeAttachmentContentType(receipt.ContentType)
	if err != nil || contentType != receipt.ContentType || strings.TrimSpace(receipt.Filename) == "" || receipt.SizeBytes <= 0 || receipt.CreatedAt.IsZero() {
		return fmt.Errorf("attachment receipt is incomplete or non-canonical")
	}
	if len(receipt.SHA256) != 64 || strings.ToLower(receipt.SHA256) != receipt.SHA256 {
		return fmt.Errorf("attachment receipt needs a lowercase SHA-256 digest")
	}
	if _, err := hex.DecodeString(receipt.SHA256); err != nil {
		return fmt.Errorf("attachment receipt needs a lowercase SHA-256 digest")
	}
	return nil
}

func writeJSONMarker(builder *strings.Builder, value any) {
	encoded, _ := json.Marshal(value)
	builder.Write(encoded)
}
