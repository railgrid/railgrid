// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"net/http"
	"reflect"

	"github.com/faroshq/provider-linear/internal/actionapi"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type toolInput struct {
	Team      string          `json:"team"`
	RequestID string          `json:"requestId,omitempty"`
	Input     actionapi.Input `json:"input"`
}

func (s Server) MCP() (http.Handler, error) {
	shape, err := jsonschema.For[toolInput](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[metav1.Time](): {Type: "string", Format: "date-time"}}})
	if err != nil {
		return nil, err
	}
	for _, field := range []string{"connection", "teamID", "action"} {
		delete(shape.Properties["input"].Properties, field)
	}
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{Name: "faros-linear", Version: "0.1.0"}, nil)
		for action := range actionNames {
			mcp.AddTool(srv, &mcp.Tool{Name: "linear_" + action, InputSchema: shape, Description: "Invoke " + action + " on a registered Linear Team. Writes require a stable requestId; a UTC YYYYMMDDTHHMMSSZ. prefix allows confirmed receipts to expire after 30 days. Retain the key after uncertainty. Binding fields are derived from Team."}, func(ctx context.Context, _ *mcp.CallToolRequest, input toolInput) (*mcp.CallToolResult, any, error) {
				out, err := s.Action(ctx, r, input.Team, action, ActionRequest{RequestID: input.RequestID, Input: input.Input}, false)
				return nil, out, err
			})
		}
		type inspectInput struct {
			Team      string `json:"team"`
			Action    string `json:"action"`
			RequestID string `json:"requestId"`
		}
		mcp.AddTool(srv, &mcp.Tool{Name: "linear_inspect_write", Description: "Inspect a prior write without repeating it."}, func(ctx context.Context, _ *mcp.CallToolRequest, input inspectInput) (*mcp.CallToolResult, any, error) {
			out, err := s.Action(ctx, r, input.Team, input.Action, ActionRequest{RequestID: input.RequestID}, true)
			return nil, out, err
		})
		return srv
	}, &mcp.StreamableHTTPOptions{Stateless: true}), nil
}
