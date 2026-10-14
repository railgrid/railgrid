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
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// The chat tool: one attended conversation turn on the MCP class.
//
// The portal's chat used to be a REST route and then a data-plane verb. A verb
// carries no caller credential — kcp stamps the caller's identity and the gate
// acts as the provider — so a chat run through it cannot dial the hub's
// aggregate MCP endpoint, which the edges family does AS THE CALLING USER. The
// MCP class is the one route that still carries the caller's bearer (the hub's
// service proxy and its aggregate both forward it), so the portal sends a chat
// turn here: the same grammar as the verb (chatTurn), with each event
// delivered as an MCP progress notification and the terminal frame as the
// call's result.
//
// It is interactive by trigger, so it gets the agent's interactive tool grant
// and the edges family, under the same approval gating as a verb-driven chat.
// A human is who it is for: the portal, or a person's own MCP client. An agent
// that wants to delegate work uses run_agent, which is unattended by design.

type chatInput struct {
	Agent     string `json:"agent" jsonschema:"Name of the agent to talk to (see list_agents)"`
	Message   string `json:"message" jsonschema:"The message for this turn of the conversation"`
	SessionID string `json:"sessionId,omitempty" jsonschema:"Chat thread to continue; omit for the default thread"`
}

// chatEventMetaKey is where a progress notification carries the chat event
// (and chatEventDataMetaKey its payload) in the notification's _meta, so a
// client needs no convention beyond "read the frame out of _meta".
const (
	chatEventMetaKey = "event"
	chatDataMetaKey  = "data"
)

// registerChatMCPTool adds the chat tool.
func (s *Server) registerChatMCPTool(srv *mcp.Server, r *http.Request) {
	yes := true
	mcp.AddTool(srv, &mcp.Tool{
		Name:  "chat",
		Title: "Chat with an agent",
		Description: "Send one message to an agent in a conversation and get its reply. This is the attended turn the portal's chat uses: " +
			"the agent runs with its interactive tool grant and, as you, with the workspace's connected clusters and servers. " +
			"Pass a progress token to receive the turn's events (deltas, tool calls) as progress notifications; the result is the terminal event — " +
			"done, error, or approval_required when the agent paused for your approval or to ask a question (resolve those in the portal). " +
			"To hand an agent a task unattended, use run_agent instead.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: false, OpenWorldHint: &yes},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in chatInput) (*mcp.CallToolResult, chatFrame, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return nil, chatFrame{}, err
		}
		message := strings.TrimSpace(in.Message)
		if message == "" {
			return nil, chatFrame{}, errBadRequest("message is required")
		}
		sessionID := strings.TrimSpace(in.SessionID)
		if sessionID == "" {
			sessionID = "default"
		}
		id := s.mcpIdentity(ctx, r)
		if id.workspaceUUID == "" {
			return nil, chatFrame{}, errBadRequest("this workspace is not mapped yet — open the agents UI once, then retry")
		}
		agent, err := c.Agents().Get(ctx, strings.TrimSpace(in.Agent), metav1.GetOptions{})
		if err != nil {
			return nil, chatFrame{}, err
		}

		// Events ride progress notifications on the call's own stream (the SDK
		// routes a notification sent on the request context to it). Without a
		// progress token the caller asked for the answer only, and gets the
		// terminal frame. A failed send means the client went away — the run
		// is not interrupted for that, same as a dropped SSE stream on the verb.
		token := req.Params.GetProgressToken()
		seq := 0
		emit := func(event string, data map[string]any) {
			if token == nil || ctx.Err() != nil {
				return
			}
			seq++
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      float64(seq),
				Meta:          mcp.Meta{chatEventMetaKey: event, chatDataMetaKey: data},
			})
		}

		runID := uuid.NewString()
		emit("start", map[string]any{"runID": runID, "sessionID": sessionID})

		run := taskRun{
			Scope: id.scope(agent.Name), Agent: agent,
			RunID:     runID,
			SessionID: sessionID, Task: message, Trigger: agentsv1alpha1.RunTriggerChat,
		}
		// The caller's access: their tenant client, and — this being the route
		// that carries their bearer — the hub's aggregate MCP endpoint for the
		// edges family.
		s.callerAccess(ctx, c, id).applyTo(&run)

		// The run outlives the request: a long tool chain is not cancelled
		// because the browser tab that asked went away (see the verb's
		// detachedStreamContext). Values — including the SDK's request id
		// that routes the notifications — survive WithoutCancel.
		return nil, s.chatTurn(context.WithoutCancel(ctx), run, emit), nil
	})
}
