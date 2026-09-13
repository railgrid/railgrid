// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"fmt"
	"net/http"
	"reflect"

	"github.com/faroshq/provider-linear/internal/engine"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (s Server) MCP() (http.Handler, error) {
	// metav1.Time embeds time.Time but serializes as an RFC3339 string.
	// Register its wire shape explicitly rather than inferring the embedded type.
	inputSchema, err := jsonschema.For[Submit](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[metav1.Time](): {Type: "string", Format: "date-time"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("linear MCP input schema: %w", err)
	}
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{Name: "faros-linear", Version: "0.1.0"}, nil)
		mcp.AddTool(srv, &mcp.Tool{Name: "linear_submit_operation", InputSchema: inputSchema, Description: "Submit a typed Linear read or write command. Use one stable Kubernetes operation name per intent; inspect it after uncertainty instead of repeating writes."}, func(ctx context.Context, _ *mcp.CallToolRequest, input Submit) (*mcp.CallToolResult, any, error) {
			v, err := s.Submit(ctx, r, input)
			return nil, v, err
		})
		type Ref struct {
			Name string `json:"name"`
		}
		mcp.AddTool(srv, &mcp.Tool{Name: "linear_get_operation", Description: "Read the durable outcome of a submitted Linear command."}, func(ctx context.Context, _ *mcp.CallToolRequest, input Ref) (*mcp.CallToolResult, any, error) {
			cl, err := s.Caller(r)
			if err != nil {
				return nil, nil, err
			}
			v, err := cl.Resource(engine.Operations).Get(ctx, input.Name, metav1.GetOptions{})
			return nil, v, err
		})
		return srv
	}, &mcp.StreamableHTTPOptions{Stateless: true}), nil
}
