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
)

const (
	MaxAgentArtifactBytes             = 1 << 20
	MaxAgentArtifactsPerAgent         = 100
	MaxAgentArtifactBytesPerAgent     = 16 << 20
	MaxAgentArtifactSources           = 32
	MaxAgentArtifactSourceActions     = 8
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

// SourceObject is the exact tenant object a tool says it read. UID makes a
// delete-and-recreate at the same name a different source.
type SourceObject struct {
	Group     string `json:"group,omitempty"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// SourceAccess is one published API action needed to use the source.
type SourceAccess struct {
	Group       string `json:"group,omitempty"`
	Version     string `json:"version"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Name        string `json:"name,omitempty"`
	Verb        string `json:"verb"`
}

// SourceReference is tool-authored provenance. It is never authority itself:
// a source-backed artifact reader rechecks every action and exact object UID
// against the current caller before returning captured bytes.
type SourceReference struct {
	Label  string         `json:"label,omitempty"`
	Object SourceObject   `json:"object"`
	Access []SourceAccess `json:"access"`
}

func (s SourceReference) Validate() error { return s.validate() }

func (s SourceReference) validate() error {
	if len(s.Label) > 512 {
		return fmt.Errorf("source label exceeds 512 bytes")
	}
	if len(s.Access) == 0 || len(s.Access) > MaxAgentArtifactSourceActions {
		return fmt.Errorf("source needs between one and %d required actions", MaxAgentArtifactSourceActions)
	}
	if strings.TrimSpace(s.Object.Version) == "" || strings.TrimSpace(s.Object.Resource) == "" ||
		strings.TrimSpace(s.Object.Name) == "" || strings.TrimSpace(s.Object.UID) == "" {
		return fmt.Errorf("source object needs version, resource, name and UID")
	}
	hasObjectGet := false
	for i, access := range s.Access {
		if strings.TrimSpace(access.Version) == "" || strings.TrimSpace(access.Resource) == "" || strings.TrimSpace(access.Name) == "" || strings.TrimSpace(access.Verb) == "" {
			return fmt.Errorf("source action %d needs version, resource, name and verb", i)
		}
		if len(access.Group) > 253 || len(access.Version) > 63 || len(access.Resource) > 253 || len(access.Subresource) > 253 ||
			len(access.Namespace) > 253 || len(access.Name) > 253 || len(access.Verb) > 32 {
			return fmt.Errorf("source action %d has an oversized permission coordinate", i)
		}
		if access.Group == s.Object.Group && access.Version == s.Object.Version && access.Resource == s.Object.Resource &&
			access.Namespace == s.Object.Namespace && access.Name == s.Object.Name && access.Verb == "get" && access.Subresource == "" {
			hasObjectGet = true
		}
	}
	if len(s.Object.Group) > 253 || len(s.Object.Version) > 63 || len(s.Object.Resource) > 253 || len(s.Object.Name) > 253 ||
		len(s.Object.Namespace) > 253 || len(s.Object.UID) > 128 {
		return fmt.Errorf("source object has an oversized identity coordinate")
	}
	if !hasObjectGet {
		return fmt.Errorf("source reference must require GET on its exact object")
	}
	return nil
}

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
	if a.Presentation != nil && a.Presentation.MCPApp != nil && len(a.Sources) == 0 {
		return fmt.Errorf("MCP App resources require source references")
	}
	for i, source := range a.Sources {
		if err := source.validate(); err != nil {
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
	retained := a.Size + a.InputSize
	if a.Presentation != nil && a.Presentation.MCPApp != nil {
		retained += int64(len(a.Presentation.MCPApp.HTML))
	}
	return retained
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
