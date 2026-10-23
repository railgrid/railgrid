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
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

// The chat tool: one attended conversation turn on the MCP class.
//
// The portal's chat used to be a REST route and then a data-plane verb. A verb
// carries no caller credential — kcp stamps the caller's identity and the gate
// acts as the provider — so a chat run through it cannot dial the hub's
// aggregate MCP endpoint, which the edges family does AS THE CALLING USER. The
// MCP class is the one route that still carries the caller's bearer, so the
// portal sends a chat turn here: the same grammar as the verb (chatTurn), with
// each event delivered as an MCP progress notification and the terminal frame
// as the call's result.
//
// It is interactive by trigger, so it gets the agent's interactive tool grant
// and the edges family, under the same approval gating as a verb-driven chat.
// That grant is for a person: the portal, or a person's own MCP client. It is
// therefore NOT on the federated surface. The hub's aggregate dials this
// endpoint through its own federation client, which carries the bearer and the
// cluster but never a resolved user; the hub's service proxy — the route the
// portal takes — strips any inbound X-Railgrid-User and stamps the identity it
// resolved. The tool is registered only when that stamp is present
// (fromServiceProxy), so an agent run that reaches the aggregate (as the user,
// with the edges family) finds no agents__chat there: it cannot obtain another
// agent's interactive grant, and a chat cannot recurse into a chat. Delegation
// between agents is run_agent, unattended by design.

type chatInput struct {
	Agent        string                         `json:"agent" jsonschema:"Name of the agent to talk to (see list_agents)"`
	Message      string                         `json:"message" jsonschema:"The message for this turn of the conversation"`
	SessionID    string                         `json:"sessionId,omitempty" jsonschema:"Chat thread to continue; omit for the default thread"`
	ArtifactRefs []store.AgentArtifactReference `json:"artifactRefs,omitempty" jsonschema:"Optional user-selected saved result references to attach to this attended turn; pointers only, with no result bytes"`
}

// chatEventMetaKey is where a progress notification carries the chat event
// (and chatDataMetaKey its payload) in the notification's _meta, so a client
// needs no convention beyond "read the frame out of _meta".
const (
	chatEventMetaKey = "event"
	chatDataMetaKey  = "data"
)

// headerRailgridUser is the caller identity the hub's service proxy resolves
// and stamps on a request it forwards to this provider's backend. The
// aggregate's federation client does not set it.
const headerRailgridUser = "X-Railgrid-User"

// fromServiceProxy reports whether the request reached /mcp through the hub's
// service proxy (a direct caller: the portal, a person's MCP client) rather
// than the aggregate's federation. The proxy always strips an inbound
// X-Railgrid-User and sets its own, so the header's presence is the proxy's
// stamp, not the caller's claim.
func fromServiceProxy(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get(headerRailgridUser)) != ""
}

// registerChatMCPTool adds the chat tool for direct callers; a federated
// request gets no such tool (see the package comment above).
func (s *Server) registerChatMCPTool(srv *mcp.Server, r *http.Request) {
	if !fromServiceProxy(r) {
		return
	}
	yes := true
	mcp.AddTool(srv, &mcp.Tool{
		Name:  "chat",
		Title: "Chat with an agent",
		Description: "Send one message to an agent in a conversation and get its reply. This is the attended turn the portal's chat uses: " +
			"the agent runs with its interactive tool grant and, as you, with the workspace's connected clusters and servers. " +
			"Pass a progress token to receive the turn's events (deltas, tool calls) as progress notifications; the result is the terminal event — " +
			"done, error, or approval_required when the agent paused for your approval or to ask a question (resolve those in the portal). " +
			"To hand an agent a task unattended, use run_agent instead.",
		Annotations: &mcp.ToolAnnotations{
			// The turn may run write-capable tools as the caller (gated by the
			// agent's approval rules, not by this tool), so say so rather than
			// leave it to the default.
			ReadOnlyHint:    false,
			DestructiveHint: &yes,
			IdempotentHint:  false,
			OpenWorldHint:   &yes,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in chatInput) (*mcp.CallToolResult, chatFrame, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return chatRefused(err)
		}
		message := strings.TrimSpace(in.Message)
		if message == "" {
			return chatRefused(errBadRequest("message is required"))
		}
		sessionID := strings.TrimSpace(in.SessionID)
		if sessionID == "" {
			sessionID = "default"
		}
		if len(in.ArtifactRefs) > store.MaxAgentArtifactReferencesPerChat {
			return chatRefused(errBadRequest(fmt.Sprintf("at most %d saved result references may be attached", store.MaxAgentArtifactReferencesPerChat)))
		}
		seenArtifacts := make(map[string]struct{}, len(in.ArtifactRefs))
		for _, ref := range in.ArtifactRefs {
			if err := ref.Validate(); err != nil {
				return chatRefused(errBadRequest(err.Error()))
			}
			if _, duplicate := seenArtifacts[ref.ArtifactID]; duplicate {
				return chatRefused(errBadRequest("saved result references must not repeat an artifact ID"))
			}
			seenArtifacts[ref.ArtifactID] = struct{}{}
		}
		if len(in.ArtifactRefs) > 0 {
			encoded, err := json.Marshal(in.ArtifactRefs)
			if err != nil {
				return chatRefused(errBadRequest("saved result references could not be encoded"))
			}
			message += "\n\nAttached saved result references (pointers only; no result data is included). Use open_analysis_artifact with an exact artifactID and digest to read one: " + string(encoded)
		}
		id := s.mcpIdentity(ctx, r)
		if id.workspaceUUID == "" {
			return chatRefused(errBadRequest("this workspace is not mapped yet — open the agents UI once, then retry"))
		}
		agent, err := c.Agents().Get(ctx, strings.TrimSpace(in.Agent), metav1.GetOptions{})
		if err != nil {
			return chatRefused(err)
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
			ArtifactOwner: id.user, ArtifactRefs: append([]store.AgentArtifactReference(nil), in.ArtifactRefs...),
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

// chatRefused answers a failure before the turn started: a tool error for the
// model's sake, and an "error" frame carrying the HTTP class the verb would
// have answered with (errorClass) as the structured result, so the portal
// presents a missing agent as 404 and a bad request as 400 on either route
// instead of flattening every refusal into an upstream failure.
func chatRefused(err error) (*mcp.CallToolResult, chatFrame, error) {
	code, reason := errorClass(err)
	return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, chatFrame{Event: "error", Data: map[string]any{
			"message": err.Error(), "code": code, "reason": reason,
		}}, nil
}
