// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/schema"
	"github.com/railgrid/provider-agents/backend"
)

// hydrateUserInputs creates invocation-only copies. The engine's authoritative
// history remains pointer-only, so checkpoints and compaction never acquire
// uploaded bytes or data URLs. The provider reauthorizes every read.
func hydrateUserInputs(ctx context.Context, in []*schema.Message, identities map[*schema.Message]historyIdentity, hydrate backend.UserInputHydrator) ([]*schema.Message, error) {
	out := append([]*schema.Message(nil), in...)
	for i, message := range in {
		if message == nil || message.Role != schema.User {
			continue
		}
		identity := identities[message]
		if len(identity.inputAttachments) == 0 {
			continue
		}
		if hydrate == nil {
			return nil, errors.New("uploaded attachments require a current authorized input reader")
		}
		request := Message{Role: RoleUser, Content: message.Content, ID: identity.id, Sequence: identity.sequence, InputAttachments: append([]backend.AttachmentReceipt(nil), identity.inputAttachments...), InputAttachmentOwner: identity.inputAttachmentOwner, InputAttachmentBindingID: identity.inputAttachmentBindingID}
		content, err := hydrate(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("read uploaded user input: %w", err)
		}
		clone := *message
		if content.Text != "" {
			clone.Content += "\n\n" + content.Text
		}
		if len(content.Images) > 0 {
			parts := make([]schema.MessageInputPart, 0, len(content.Images)+1)
			if clone.Content != "" {
				parts = append(parts, schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: clone.Content})
			}
			parts = append(parts, clone.UserInputMultiContent...)
			for _, image := range content.Images {
				if len(image.Data) == 0 {
					return nil, errors.New("uploaded image input is empty")
				}
				encoded := base64.StdEncoding.EncodeToString(image.Data)
				parts = append(parts, schema.MessageInputPart{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &encoded, MIMEType: image.MIMEType}, Detail: schema.ImageURLDetailAuto}})
			}
			clone.Content = ""
			clone.UserInputMultiContent = parts
		}
		out[i] = &clone
	}
	return out, nil
}
