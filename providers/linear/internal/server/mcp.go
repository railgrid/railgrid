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
	"net/http"

	"github.com/faroshq/provider-linear/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (s Server) MCP() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{Name: "faros-linear", Version: "0.1.0"}, nil)
		mcp.AddTool(srv, &mcp.Tool{Name: "linear_submit_operation", Description: "Submit a typed Linear read or write command. Use one stable Kubernetes operation name per intent; inspect it after uncertainty instead of repeating writes."}, func(ctx context.Context, _ *mcp.CallToolRequest, input Submit) (*mcp.CallToolResult, any, error) {
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
	}, &mcp.StreamableHTTPOptions{Stateless: true})
}
