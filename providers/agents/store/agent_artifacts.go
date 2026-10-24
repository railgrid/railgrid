// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/railgrid/provider-agents/resulttypes"
)

const (
	MaxAgentArtifactBytes             = 1 << 20
	MaxAgentArtifactsPerOwner         = 100
	MaxAgentArtifactBytesPerOwner     = 16 << 20
	MaxAgentArtifactSources           = resulttypes.MaxAgentArtifactSources
	MaxAgentArtifactSourceActions     = resulttypes.MaxAgentArtifactSourceActions
	MaxAgentArtifactInputBytes        = 16 << 10
	MaxAgentArtifactReferencesPerChat = 8
)

var (
	ErrAgentArtifactLimit          = errors.New("agent artifact retention limit reached")
	ErrAgentArtifactResultTooLarge = errors.New("tool result exceeds the agent artifact size limit")
	ErrAgentArtifactNotFound       = errors.New("agent artifact not found")
)

// AgentArtifactReference is a user-selected pointer attached to one
// attended chat turn. It contains no result bytes; the reader resolves it
// under the current caller identity and revalidates any source references.
type AgentArtifactReference struct {
	ArtifactID string `json:"artifactID"`
	Digest     string `json:"digest"`
}

func (r AgentArtifactReference) Validate() error {
	if strings.TrimSpace(r.ArtifactID) == "" || len(r.ArtifactID) > 128 || r.ArtifactID != strings.TrimSpace(r.ArtifactID) {
		return fmt.Errorf("artifact reference needs a bounded artifact ID")
	}
	if len(r.Digest) != sha256.Size*2 || strings.ToLower(r.Digest) != r.Digest {
		return fmt.Errorf("artifact reference needs a lowercase SHA-256 digest")
	}
	if _, err := hex.DecodeString(r.Digest); err != nil {
		return fmt.Errorf("artifact reference needs a lowercase SHA-256 digest")
	}
	return nil
}

// SourceObject keeps store-facing APIs source-compatible while the DTOs live
// in a dependency-light package usable by the backend seam.
type SourceObject = resulttypes.SourceObject
type SourceAccess = resulttypes.SourceAccess
type SourceReference = resulttypes.SourceReference

// AgentArtifact is an immutable caller-owned capture of one successful tool
// result. Its retention is independent of the Run trace that produced it.
type AgentArtifact struct {
	ID           string                `json:"id"`
	AgentName    string                `json:"agentName"`
	AgentUID     string                `json:"agentUID"`
	Owner        string                `json:"owner"`
	RunID        string                `json:"runID"`
	SessionID    string                `json:"sessionID"`
	ToolCallID   string                `json:"toolCallID"`
	ToolName     string                `json:"toolName"`
	Title        string                `json:"title"`
	MediaType    string                `json:"mediaType"`
	Input        []byte                `json:"input,omitempty"`
	InputDigest  string                `json:"inputDigest,omitempty"`
	InputSize    int64                 `json:"inputSize,omitempty"`
	Digest       string                `json:"digest"`
	Size         int64                 `json:"size"`
	Data         []byte                `json:"data"`
	Sources      []SourceReference     `json:"sources,omitempty"`
	Presentation *ProviderPresentation `json:"presentation,omitempty"`
	CreatedAt    time.Time             `json:"createdAt"`
}

func (a AgentArtifact) validate() error {
	switch {
	case strings.TrimSpace(a.ID) == "":
		return fmt.Errorf("artifact needs an ID")
	case strings.TrimSpace(a.AgentName) == "" || strings.TrimSpace(a.AgentUID) == "":
		return fmt.Errorf("artifact needs an Agent name and UID")
	case strings.TrimSpace(a.Owner) == "":
		return fmt.Errorf("artifact needs an owner")
	case strings.TrimSpace(a.RunID) == "" || strings.TrimSpace(a.SessionID) == "" || strings.TrimSpace(a.ToolCallID) == "":
		return fmt.Errorf("artifact needs its exact run, session and tool-call identity")
	case strings.TrimSpace(a.ToolName) == "":
		return fmt.Errorf("artifact needs a tool name")
	case len(a.Title) > 160:
		return fmt.Errorf("artifact title exceeds 160 bytes")
	case len(a.Data) == 0 || len(a.Data)+len(a.Input) > MaxAgentArtifactBytes:
		return fmt.Errorf("artifact data and input must total between 1 and %d bytes", MaxAgentArtifactBytes)
	case len(a.Input) > MaxAgentArtifactInputBytes:
		return fmt.Errorf("artifact input must not exceed %d bytes", MaxAgentArtifactInputBytes)
	case a.Size != int64(len(a.Data)):
		return fmt.Errorf("artifact size does not match its data")
	case a.InputSize != int64(len(a.Input)):
		return fmt.Errorf("artifact input size does not match its input")
	case a.Digest != artifactDigest(a.Data):
		return fmt.Errorf("artifact digest does not match its data")
	case (len(a.Input) == 0) != (a.InputDigest == ""):
		return fmt.Errorf("artifact input digest must be present exactly when input is present")
	case len(a.Input) > 0 && a.InputDigest != artifactDigest(a.Input):
		return fmt.Errorf("artifact input digest does not match its input")
	case strings.TrimSpace(a.MediaType) == "" || a.CreatedAt.IsZero():
		return fmt.Errorf("artifact needs a media type and creation time")
	}
	if len(a.Sources) > MaxAgentArtifactSources {
		return fmt.Errorf("artifact has more than %d sources", MaxAgentArtifactSources)
	}
	if a.Presentation != nil && a.Presentation.MCPAppReference != nil {
		if len(a.Sources) == 0 {
			return fmt.Errorf("MCP App references require source references")
		}
	}
	for i, source := range a.Sources {
		if err := source.Validate(); err != nil {
			return fmt.Errorf("artifact source %d: %w", i, err)
		}
	}
	if a.Presentation != nil {
		if err := a.Presentation.Validate(); err != nil {
			return fmt.Errorf("artifact presentation: %w", err)
		}
	}
	return nil
}

func (a AgentArtifact) retainedBytes() int64 {
	return a.Size + a.InputSize
}

func artifactDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cloneAgentArtifact(in AgentArtifact) AgentArtifact {
	out := in
	out.Data = append([]byte(nil), in.Data...)
	out.Input = append([]byte(nil), in.Input...)
	out.Presentation = in.Presentation.Clone()
	if in.Sources != nil {
		out.Sources = make([]SourceReference, len(in.Sources))
		for i, source := range in.Sources {
			out.Sources[i] = source
			out.Sources[i].Access = append([]SourceAccess(nil), source.Access...)
		}
	}
	return out
}
