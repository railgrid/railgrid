// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"

	agentsharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-sdk/attachments"
	sdk "github.com/railgrid/provider-sdk/skills"

	"github.com/railgrid/railgrid/pkg/runner"
)

// agentHarnessInputFiles builds a complete attempt manifest before any bytes
// are staged. Enabled skills are available to the harness as files because
// harness backends do not execute the model backend's lazy-load tools.
func (s *Server) agentHarnessInputFiles(ctx context.Context, run taskRun, sessionID string, snapshot sdk.Snapshot, fresh bool) ([]agentsharness.StagedInput, error) {
	files := make([]agentsharness.StagedInput, 0)
	for _, receipt := range run.AttachmentRefs {
		if attachments.AttachmentKindFor(receipt.ContentType, receipt.SizeBytes) == attachments.AttachmentKindFile {
			// Opaque uploads, including PDFs and over-bound images/text, remain
			// metadata-only inputs. They are never interpreted by the harness.
			continue
		}
		var item store.AgentAttachment
		var err error
		if fresh {
			attachmentStore, ok := s.store.(store.AgentAttachmentStore)
			if !ok {
				return nil, fmt.Errorf("agent attachment storage is not configured")
			}
			item, err = attachmentStore.GetAgentAttachment(ctx, run.Scope, string(run.Agent.UID), sessionID, run.AttachmentOwner, receipt.ID)
			if err != nil || !item.Draft || !attachments.ReceiptsMatch(receipt, item.Receipt()) {
				return nil, store.ErrAgentAttachmentReceipt
			}
		} else {
			item, err = s.loadBoundAgentAttachment(ctx, run.Scope, string(run.Agent.UID), sessionID, run.AttachmentOwner, run.RunID, receipt)
			if err != nil {
				return nil, err
			}
		}
		ref := runner.InputFile{
			ID:   harnessInputID("attachment\x00" + receipt.ID),
			Name: receipt.Filename, MediaType: receipt.ContentType,
			Digest: "sha256:" + receipt.SHA256, Size: receipt.SizeBytes,
		}
		files = append(files, agentsharness.StagedInput{Reference: ref, Data: item.Data})
	}

	for _, entry := range snapshot.Entries {
		if !entry.Enabled {
			continue
		}
		groupID := harnessInputID("skill-group\x00" + entry.QualifiedName)
		document := harnessSkillDocument(entry)
		files = append(files, newHarnessSkillInput(entry.QualifiedName, groupID, "SKILL.md", document))
		for _, resource := range entry.Resources {
			// A catalog warning can retain metadata for a resource the immutable
			// snapshot could not safely read. Such resources are unavailable to
			// both lazy model tools and the harness; do not fabricate their bytes.
			if resource.Digest == "" {
				continue
			}
			read, err := snapshot.ReadResource(ctx, entry.QualifiedName, resource.Path, sdk.ResourceReadOptions{Limit: sdk.HardMaxResourceRead})
			if err != nil || read.Truncated || int64(len(read.Content)) != resource.Size || digestString(read.Content) != resource.Digest {
				return nil, fmt.Errorf("enabled skill %q has an unavailable supporting resource %q", entry.QualifiedName, resource.Path)
			}
			files = append(files, newHarnessSkillInput(entry.QualifiedName, groupID, resource.Path, read.Content))
		}
	}
	manifest := make([]runner.InputFile, len(files))
	for i, file := range files {
		manifest[i] = file.Reference
		if file.Reference.Size != int64(len(file.Data)) || file.Reference.Digest != digestString(file.Data) {
			return nil, fmt.Errorf("staged input %q does not match its immutable manifest", file.Reference.ID)
		}
	}
	if err := runner.ValidateInputFiles(manifest); err != nil {
		return nil, fmt.Errorf("enabled Agent inputs exceed runner delivery limits: %w", err)
	}
	return files, nil
}

func newHarnessSkillInput(skillID, groupID, relativePath string, data []byte) agentsharness.StagedInput {
	name := path.Base(relativePath)
	return agentsharness.StagedInput{
		Reference: runner.InputFile{
			ID:   harnessInputID("skill-file\x00" + skillID + "\x00" + relativePath),
			Name: name, GroupID: groupID, RelativePath: relativePath,
			MediaType: "text/plain", Digest: digestString(data), Size: int64(len(data)),
		},
		Data: append([]byte(nil), data...),
	}
}

func harnessSkillDocument(entry sdk.Entry) []byte {
	// The parser's Content excludes frontmatter. Reconstruct only its normalized
	// identity and description so package-relative supporting links still resolve.
	return []byte(fmt.Sprintf("---\nname: %s\ndescription: %q\n---\n\n%s\n", entry.Name, entry.Description, entry.Content))
}

func harnessInputID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "input-" + hex.EncodeToString(sum[:12])
}

func digestString(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *Server) loadBoundAgentAttachment(ctx context.Context, scope store.Scope, agentUID, sessionID, owner, runID string, receipt store.AgentAttachmentReceipt) (store.AgentAttachment, error) {
	attachmentStore, ok := s.store.(store.AgentAttachmentStore)
	if !ok {
		return store.AgentAttachment{}, fmt.Errorf("agent attachment storage is not configured")
	}
	item, err := attachmentStore.GetAgentAttachment(ctx, scope, agentUID, sessionID, owner, receipt.ID)
	if err != nil || item.Draft || item.BindingID != runID || !attachments.ReceiptsMatch(receipt, item.Receipt()) {
		return store.AgentAttachment{}, store.ErrAgentAttachmentReceipt
	}
	return item, nil
}
