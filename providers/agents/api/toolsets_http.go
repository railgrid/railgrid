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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
)

// Toolsets are workspace-shared bundles of tool grants (families + connections
// + approval) that many agents link. CRUD here mirrors connections/schedules.

type toolsetRequest struct {
	Name            string   `json:"name"`
	DisplayName     string   `json:"displayName,omitempty"`
	Description     string   `json:"description,omitempty"`
	Families        []string `json:"families,omitempty"`
	Connections     []string `json:"connections,omitempty"`
	RequireApproval []string `json:"requireApproval,omitempty"`
}

// applyToolsetCreate validates the request and creates the toolset. Shared by
// the REST handler and the MCP create_toolset tool.
func applyToolsetCreate(ctx context.Context, c *agentsclient.Client, req *toolsetRequest) (*agentsv1alpha1.Toolset, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, errBadRequest("name is required")
	}
	ts := &agentsv1alpha1.Toolset{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec: agentsv1alpha1.ToolsetSpec{
			DisplayName:     req.DisplayName,
			Description:     req.Description,
			Families:        req.Families,
			Connections:     req.Connections,
			RequireApproval: req.RequireApproval,
		},
	}
	return c.Toolsets().Create(ctx, ts, metav1.CreateOptions{})
}

// updateToolsetRequest patches an existing toolset; pointer fields let the
// caller change only what they send.
type updateToolsetRequest struct {
	DisplayName     *string   `json:"displayName,omitempty"`
	Description     *string   `json:"description,omitempty"`
	Families        *[]string `json:"families,omitempty"`
	Connections     *[]string `json:"connections,omitempty"`
	RequireApproval *[]string `json:"requireApproval,omitempty"`
}

// applyToolsetUpdate reads the toolset, applies the patch fields that are
// present, and writes it back. Shared by the REST handler and the MCP
// update_toolset tool; list fields replace wholesale.
func applyToolsetUpdate(ctx context.Context, c *agentsclient.Client, name string, req *updateToolsetRequest) (*agentsv1alpha1.Toolset, error) {
	ts, err := c.Toolsets().Get(ctx, strings.TrimSpace(name), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if req.DisplayName != nil {
		ts.Spec.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.Description != nil {
		ts.Spec.Description = *req.Description
	}
	if req.Families != nil {
		ts.Spec.Families = *req.Families
	}
	if req.Connections != nil {
		ts.Spec.Connections = *req.Connections
	}
	if req.RequireApproval != nil {
		ts.Spec.RequireApproval = *req.RequireApproval
	}
	return c.Toolsets().Update(ctx, ts, metav1.UpdateOptions{})
}
