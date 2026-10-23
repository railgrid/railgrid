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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
)

type openAgentArtifactInput struct {
	ArtifactID string `json:"artifactID"`
	Digest     string `json:"digest"`
}

type savedAgentArtifactReference struct {
	ArtifactID string `json:"artifactID"`
	Digest     string `json:"digest"`
}

// savedCaptureForToolEvent adds a citation pointer only for a successful open
// of an exact artifact that was attached to this attended turn. It carries no
// artifact bytes and has no selection claim.
func savedCaptureForToolEvent(run taskRun, ev backend.ToolEvent) *savedAgentArtifactReference {
	if ev.Err || ev.Name != "open_analysis_artifact" {
		return nil
	}
	var input openAgentArtifactInput
	decoder := json.NewDecoder(strings.NewReader(ev.Args))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		return nil
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return nil
	}
	input.ArtifactID, input.Digest = strings.TrimSpace(input.ArtifactID), strings.TrimSpace(input.Digest)
	for _, ref := range run.ArtifactRefs {
		if ref.ArtifactID == input.ArtifactID && ref.Digest == input.Digest && ref.Validate() == nil {
			return &savedAgentArtifactReference{ArtifactID: ref.ArtifactID, Digest: ref.Digest}
		}
	}
	return nil
}

func (s *Server) openAgentArtifactTool(run taskRun) backend.Tool {
	read := func(ctx context.Context, argsJSON string, expected *backend.ToolResultReference) (backend.Observation, error) {
		var input openAgentArtifactInput
		decoder := json.NewDecoder(strings.NewReader(argsJSON))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return backend.Observation{}, fmt.Errorf("invalid saved result reference: %w", err)
		}
		var trailing any
		if !errors.Is(decoder.Decode(&trailing), io.EOF) {
			return backend.Observation{}, fmt.Errorf("saved result reference must contain one JSON object")
		}
		input.ArtifactID, input.Digest = strings.TrimSpace(input.ArtifactID), strings.TrimSpace(input.Digest)
		selected := false
		for _, ref := range run.ArtifactRefs {
			if ref.ArtifactID == input.ArtifactID && ref.Digest == input.Digest && ref.Validate() == nil {
				selected = true
				break
			}
		}
		if !selected {
			return backend.Observation{}, fmt.Errorf("saved result was not attached to this attended turn")
		}
		if run.Agent == nil || run.Agent.UID == "" || run.ArtifactOwner == "" {
			return backend.Observation{}, fmt.Errorf("saved result caller identity is unavailable")
		}
		artifact, found, err := s.store.GetAgentArtifact(ctx, run.Scope, string(run.Agent.UID), run.ArtifactOwner, input.ArtifactID)
		if err != nil || !found || artifact.Digest != input.Digest {
			return backend.Observation{}, fmt.Errorf("saved result is unavailable")
		}
		if len(artifact.Sources) > 0 {
			if s.tenant == nil || run.ClusterID == "" || run.HubToken == "" {
				return backend.Observation{}, backend.ErrToolResultReferenceCallerAuthorizationUnavailable
			}
			caller, err := s.tenant.For(run.ClusterID, run.HubToken)
			if err != nil || caller == nil {
				return backend.Observation{}, backend.ErrToolResultReferenceCallerAuthorizationUnavailable
			}
			if revalidateAgentArtifactSources(ctx, caller.Dynamic(), artifact.Sources) != nil {
				return backend.Observation{}, fmt.Errorf("saved result source access or identity is unavailable")
			}
		}
		payload, err := json.Marshal(savedAgentArtifactReference{ArtifactID: artifact.ID, Digest: artifact.Digest})
		if err != nil {
			return backend.Observation{}, fmt.Errorf("encode saved result reference: %w", err)
		}
		if expected != nil {
			var saved savedAgentArtifactReference
			if len(expected.Receipt) > 0 || json.Unmarshal(expected.Payload, &saved) != nil ||
				saved.ArtifactID != artifact.ID || saved.Digest != artifact.Digest || string(payload) != string(expected.Payload) {
				return backend.Observation{}, fmt.Errorf("saved result reference no longer matches the attached artifact")
			}
			payload = append(payload[:0], expected.Payload...)
		}
		var presentation *store.ProviderPresentation
		if artifact.Presentation != nil {
			presentation = artifact.Presentation.Clone()
			if presentation.MCPAppReference != nil {
				caller := identity{clusterID: run.ClusterID, token: run.HubToken}
				app, ok := s.currentMCPAppResource(ctx, caller, *presentation.MCPAppReference)
				if ok && app.ResourceURI == presentation.MCPAppReference.ResourceURI && app.SHA256 == presentation.MCPAppReference.SHA256 {
					presentation.MCPApp = nil
				} else {
					presentation.MCPAppReference, presentation.MCPApp = nil, nil
				}
			}
			if presentation.MCPAppReference == nil && presentation.Title == "" {
				presentation = nil
			}
		}
		return backend.Observation{Text: string(artifact.Data), ResultReference: payload, Presentation: presentation}, nil
	}

	return backend.Tool{
		Name: "open_analysis_artifact",
		Desc: "Open one exact user-attached saved result by artifactID and digest. The provider rereads the immutable capture and rechecks every source permission and exact source UID before returning its text. This is a reference to an existing result, not a new query.",
		Params: map[string]backend.Param{
			"artifactID": {Type: "string", Desc: "artifactID from the saved result references attached to this turn", Required: true},
			"digest":     {Type: "string", Desc: "the attached artifact's immutable SHA-256 digest", Required: true},
		},
		ResultPolicy: backend.ToolResultReferenceOnly,
		ExecRich: func(ctx context.Context, argsJSON string) (backend.Observation, error) {
			return read(ctx, argsJSON, nil)
		},
		RehydrateResult: func(ctx context.Context, argsJSON string, ref backend.ToolResultReference) (backend.Observation, error) {
			return read(ctx, argsJSON, &ref)
		},
	}
}
