// Copyright 2026 The Faros Authors.
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
	"encoding/binary"
	"encoding/hex"
	"strings"

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"

	"github.com/faroshq/provider-agents/store"
)

const (
	agentsAgentCreatedAction = "agents_agent_created"
	agentsRunTerminalAction  = "agents_run_terminal"
	agentsResourceIDDomain   = "faros/agents/resource/v1\x00"
)

// telemetryTracker returns the configured product telemetry dependency. A
// zero-value Server is common in focused unit tests, so keep the safe no-op
// behavior at the call boundary as well as in New.
func (s *Server) telemetryTracker() producttelemetry.Tracker {
	if s == nil || s.telemetry == nil {
		return producttelemetry.NoopTracker{}
	}
	return s.telemetry
}

// trackProductEvent deliberately discards SDK errors. Telemetry is best effort:
// a full queue, invalid optional configuration, or a failed network request
// must never change the product operation's result. Event data and credentials
// are intentionally not logged here.
func (s *Server) trackProductEvent(ctx context.Context, event producttelemetry.Event) {
	_ = s.telemetryTracker().Track(ctx, event)
}

// agentResourceID is the provider-local opaque identity for one agent. It is
// deliberately derived from immutable tenant scope IDs plus the canonical CR
// name, rather than from a CR UID or a run identifier, so create and terminal
// funnel events join across API surfaces and provider restarts without sending
// the human-chosen name. Length framing avoids ambiguous concatenations while
// the fixed domain separates this digest from any other provider identity.
func agentResourceID(orgUUID, workspaceUUID, agentName string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(agentsResourceIDDomain))
	for _, value := range []string{
		strings.TrimSpace(orgUUID),
		strings.TrimSpace(workspaceUUID),
		strings.TrimSpace(agentName),
	} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(value))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) trackAgentCreated(ctx context.Context, id identity, agentName string) {
	orgUUID := strings.TrimSpace(id.orgUUID)
	workspaceUUID := strings.TrimSpace(id.workspaceUUID)
	name := strings.TrimSpace(agentName)
	actor := strings.TrimSpace(id.user)
	if orgUUID == "" || workspaceUUID == "" || name == "" || actor == "" {
		return
	}
	s.trackProductEvent(ctx, producttelemetry.Event{
		Action:      agentsAgentCreatedAction,
		OrgID:       orgUUID,
		WorkspaceID: workspaceUUID,
		ResourceID:  agentResourceID(orgUUID, workspaceUUID, name),
		Actor:       actor,
		Properties:  map[string]any{"outcome": "success"},
	})
}

func terminalRunOutcome(phase store.RunPhase) (string, bool) {
	switch phase {
	case store.RunPhaseSucceeded:
		return "succeeded", true
	case store.RunPhaseFailed:
		return "failed", true
	case store.RunPhaseAborted:
		return "aborted", true
	default:
		return "", false
	}
}

func (s *Server) trackRunTerminal(ctx context.Context, scope store.Scope, agentName, runID, outcome string) {
	orgUUID := strings.TrimSpace(scope.OrgUUID)
	workspaceUUID := strings.TrimSpace(scope.WorkspaceUUID)
	name := strings.TrimSpace(agentName)
	runID = strings.TrimSpace(runID)
	if orgUUID == "" || workspaceUUID == "" || name == "" || runID == "" || outcome == "" {
		return
	}
	s.trackProductEvent(ctx, producttelemetry.Event{
		Action:        agentsRunTerminalAction,
		OrgID:         orgUUID,
		WorkspaceID:   workspaceUUID,
		ResourceID:    agentResourceID(orgUUID, workspaceUUID, name),
		CorrelationID: runID,
		Properties:    map[string]any{"outcome": outcome},
	})
}
