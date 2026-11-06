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
	"slices"
	"time"

	agents "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/internal/edgeref"
	"github.com/railgrid/provider-agents/llm"

	"github.com/railgrid/railgrid/pkg/runner"
	runnerclient "github.com/railgrid/railgrid/pkg/runner/client"
)

type agentInputCapabilities struct {
	Attachments bool   `json:"attachments"`
	Skills      bool   `json:"skills"`
	Resources   bool   `json:"resources"`
	Reason      string `json:"reason,omitempty"`
}

// Capability discovery never launches a harness. A failed or old runner leaves
// text and coordinate-only resource references usable, with explicit feedback
// for inputs that require actual file delivery.
func (s *Server) agentConversationCapabilities(ctx context.Context, c *agentsclient.Client, agent *agents.Agent, id identity) agentInputCapabilities {
	if !agent.Spec.HarnessBacked() {
		return agentInputCapabilities{Attachments: true, Skills: true, Resources: true}
	}
	unavailable := agentInputCapabilities{Resources: true, Reason: "This runner cannot receive files or skill packages yet. Update or reconnect the runner, then retry."}
	config := agent.Spec.Harness()
	if config == nil || s.runners == nil || id.clusterID == "" {
		return unavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	credential, err := c.GetModelCredential(ctx, config.CredentialRef)
	if err != nil {
		return unavailable
	}
	selector := llm.HarnessSelector(credential.Spec.Provider)
	if selector == "" {
		return unavailable
	}
	token, err := s.harnessIdentity(ctx, id.clusterID, agent)
	if err != nil {
		return unavailable
	}
	dispatcher, err := s.runners(ctx, runnerclient.ServiceRef{Cluster: id.clusterID, Service: edgeref.RunnerServiceName(config.EdgeRef.Kind, config.EdgeRef.Name, selector), EdgeKind: config.EdgeRef.Kind, EdgeName: config.EdgeRef.Name, RunnerID: edgeref.RunnerID(config.EdgeRef.Name, selector)}, token)
	if err != nil {
		return unavailable
	}
	reader, ok := dispatcher.(capabilityReader)
	if !ok {
		return unavailable
	}
	capabilities, err := reader.Capabilities(ctx)
	if err != nil || !capabilities.Ready || !slices.Contains(capabilities.Verification, runner.InputFilesCapability) {
		return unavailable
	}
	return agentInputCapabilities{Attachments: true, Skills: true, Resources: true}
}
