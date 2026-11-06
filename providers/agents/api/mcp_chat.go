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
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/railgrid/provider-sdk/attachments"
	"github.com/railgrid/provider-sdk/dataplane"
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
	Agent            string                         `json:"agent" jsonschema:"Name of the agent to talk to (see list_agents)"`
	Message          string                         `json:"message,omitempty" jsonschema:"The message for this turn of the conversation"`
	SessionID        string                         `json:"sessionId,omitempty" jsonschema:"Chat thread to continue; omit for the default thread"`
	ArtifactRefs     []store.AgentArtifactReference `json:"artifactRefs,omitempty" jsonschema:"Optional user-selected saved result references to attach to this attended turn; pointers only, with no result bytes"`
	ContentParts     []agentChatContentPart         `json:"contentParts,omitempty" jsonschema:"Optional structured text, skill, resource, and attachment parts"`
	SkillRefs        []agentSkillSelection          `json:"skillRefs,omitempty" jsonschema:"Optional enabled skill IDs and their immutable SHA-256 digests"`
	ContextResources []agentContextResource         `json:"contextResources,omitempty" jsonschema:"Optional provider resource coordinates selected as caller-authorized context"`
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
			"done, error, or approval_required when the run pauses. A pause with kind question includes an inboxID and a prompt or question group. " +
			"Use answer_chat to continue the same run with your current workspace tools. " +
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
		parts := append([]agentChatContentPart(nil), in.ContentParts...)
		if len(parts) == 0 && (len(in.SkillRefs) > 0 || len(in.ContextResources) > 0) {
			if message != "" {
				parts = append(parts, agentChatContentPart{Type: "text", Text: message})
			}
			for _, ref := range in.SkillRefs {
				parts = append(parts, agentChatContentPart{Type: "skill", SkillID: ref.ID})
			}
			for index := range in.ContextResources {
				parts = append(parts, agentChatContentPart{Type: "resource", ResourceIndex: index})
			}
		} else if len(parts) > 0 && message != "" && !agentChatContentPartsHaveText(parts) {
			parts = append([]agentChatContentPart{{Type: "text", Text: message}}, parts...)
		}
		parts, resources, attachmentRefs, _, err := normalizeAgentChatContentParts(parts, in.SkillRefs, in.ContextResources)
		if err != nil {
			return chatRefused(errBadRequest(err.Error()))
		}
		task, hasText := renderAgentChatContent(parts, resources)
		if len(parts) == 0 {
			task = message
		} else if !hasText && message != "" {
			task = strings.TrimSpace(message + "\n\n" + task)
		}
		if strings.TrimSpace(task) == "" {
			return chatRefused(errBadRequest("message or structured contentParts are required"))
		}
		id := s.mcpIdentity(ctx, r)
		if id.workspaceUUID == "" {
			return chatRefused(errBadRequest("this workspace is not mapped yet — open the agents UI once, then retry"))
		}
		agent, err := c.Agents().Get(ctx, strings.TrimSpace(in.Agent), metav1.GetOptions{})
		if err != nil {
			return chatRefused(err)
		}
		resolvedResources, resourcePrompt, err := s.resolveAgentContextResources(ctx, c, id, resources)
		if err != nil {
			return chatRefused(errBadRequest(err.Error()))
		}
		historicalAttachmentOwners, err := s.sessionAttachmentInputOwners(ctx, id.scope(agent.Name), sessionID)
		if err != nil {
			return chatRefused(errBadRequest("conversation attachment history could not be verified"))
		}
		attachmentOwner := ""
		if len(attachmentRefs) > 0 || len(historicalAttachmentOwners) > 0 {
			attachmentOwner, err = s.resolveAgentAttachmentOwner(ctx, id)
			if err != nil && len(attachmentRefs) > 0 {
				return chatRefused(errBadRequest("the current caller identity could not be verified for conversation attachments"))
			}
			// History from another uploader (or an unverified caller) is rendered
			// as metadata by the hydrator, never a reason to lock a shared session.

		}
		if len(attachmentRefs) > 0 {
			if err := s.verifyAgentAttachmentReceipts(ctx, id.scope(agent.Name), string(agent.UID), sessionID, attachmentOwner, attachmentRefs); err != nil {
				return chatRefused(errBadRequest("an attachment receipt is unavailable or does not match its upload"))
			}
		}
		if agent.Spec.HarnessBacked() {
			needsFiles := len(agent.Spec.Skills) > 0
			for _, receipt := range attachmentRefs {
				if attachments.AttachmentKindFor(receipt.ContentType, receipt.SizeBytes) != attachments.AttachmentKindFile {
					needsFiles = true
					break
				}
			}
			if needsFiles {
				capabilities := s.agentConversationCapabilities(ctx, c, agent, id)
				if !capabilities.Attachments || !capabilities.Skills {
					return chatRefused(errBadRequest("the selected harness runner cannot receive attached files or enabled skills; update or reconnect it before starting this turn"))
				}
			}
		}
		if resourcePrompt != "" {
			task = strings.TrimSpace(task + "\n\n" + resourcePrompt)
		}
		if len(in.ArtifactRefs) > 0 {
			encoded, err := json.Marshal(in.ArtifactRefs)
			if err != nil {
				return chatRefused(errBadRequest("saved result references could not be encoded"))
			}
			task += "\n\nAttached saved result references (pointers only; no result data is included). Use " + openSavedArtifactToolName + " with an exact artifactID and digest to read one: " + string(encoded)
		}
		if strings.TrimSpace(id.user) == "" {
			return chatRefused(errBadRequest("an authenticated caller is required to attach conversation inputs"))
		}

		run := taskRun{
			Scope: id.scope(agent.Name), Agent: agent,
			RunID: uuid.NewString(), SessionID: sessionID, Task: task, Trigger: agentsv1alpha1.RunTriggerChat,
			ArtifactOwner: id.user, ArtifactRefs: append([]store.AgentArtifactReference(nil), in.ArtifactRefs...),
			AttachmentOwner: attachmentOwner, AttachmentCaller: attachmentOwner,
			AttachmentRefs: append([]store.AgentAttachmentReceipt(nil), attachmentRefs...),
			ContentParts:   append([]agentChatContentPart(nil), parts...), SkillRefs: append([]agentSkillSelection(nil), in.SkillRefs...),
			ContextResourceOwner: id.user, ContextResources: append([]agentContextResource(nil), resolvedResources...),
		}
		s.callerAccess(ctx, c, id).applyTo(&run)
		if _, _, _, err := s.agentSkillRuntime(ctx, run, run.SkillRefs); err != nil {
			return chatRefused(errBadRequest(err.Error()))
		}

		emit := chatProgressEmitter(ctx, req)

		runID := run.RunID
		emit("start", map[string]any{"runID": runID, "sessionID": sessionID})

		// The run outlives the request: a long tool chain is not cancelled
		// because the browser tab that asked went away (see the verb's
		// detachedStreamContext). Values — including the SDK's request id
		// that routes the notifications — survive WithoutCancel.
		return nil, s.chatTurn(context.WithoutCancel(ctx), run, emit), nil
	})
	s.registerChatAnswerMCPTool(srv, r, &yes)
}

type chatAnswerInput struct {
	Agent    string             `json:"agent" jsonschema:"Name of the agent in the conversation"`
	InboxID  string             `json:"inboxID" jsonschema:"Inbox item from the paused chat run"`
	Response string             `json:"response,omitempty" jsonschema:"Reply to a single question"`
	Answers  map[string]*string `json:"answers,omitempty" jsonschema:"Reply or null skip for each question ID"`
}

func (s *Server) registerChatAnswerMCPTool(srv *mcp.Server, r *http.Request, yes *bool) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "answer_chat", Title: "Answer a paused chat question",
		Description: "Reply to a question that paused a chat run and stream its continuation with your current workspace tools.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: yes, OpenWorldHint: yes},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in chatAnswerInput) (*mcp.CallToolResult, chatFrame, error) {
		c, err := s.mcpClient(r)
		if err != nil {
			return chatRefused(err)
		}
		id := s.mcpIdentity(ctx, r)
		if id.orgUUID == "" || id.workspaceUUID == "" {
			return chatRefused(errBadRequest("workspace identity is unavailable"))
		}
		name := strings.TrimSpace(in.Agent)
		if name == "" || strings.TrimSpace(in.InboxID) == "" {
			return chatRefused(errBadRequest("agent and inboxID are required"))
		}
		// Match the inbox-resolve verb's parent read and subresource write
		// checks before touching the durable reply.
		if _, err := c.Agents().Get(ctx, name, metav1.GetOptions{}); err != nil {
			return chatRefused(err)
		}
		caller, err := s.tenant.For(id.clusterID, id.token)
		if err != nil {
			return chatRefused(err)
		}
		allowed, err := authorizeCallerResource(ctx, caller.Dynamic(), dataplane.ResourceAttributes{
			Group: agentsv1alpha1.GroupName, Version: agentsv1alpha1.Version,
			Resource: "agents", Subresource: "inbox-resolve", Name: name, Verb: "create",
		})
		if err != nil {
			return chatRefused(err)
		}
		if !allowed {
			return chatRefused(&requestError{http.StatusForbidden, "Forbidden", "caller cannot resolve this agent's inbox"})
		}
		wsScope := store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID}
		item, err := s.store.GetInboxItem(ctx, wsScope, in.InboxID)
		if err != nil || item.AgentName != name || item.Kind != store.InboxKindQuestion || item.RunID == "" {
			return chatRefused(&requestError{http.StatusNotFound, "NotFound", "no such chat question on this agent"})
		}
		run, err := s.store.GetRun(ctx, id.scope(name), item.RunID)
		if err != nil || run.Trigger != agentsv1alpha1.RunTriggerChat {
			return chatRefused(&requestError{http.StatusNotFound, "NotFound", "no chat run for this question"})
		}
		reply := resolveInboxRequest{Response: in.Response, Answers: in.Answers}
		payload, err := inboxAnswerPayload(item, store.InboxStateAnswered, &reply)
		if err != nil {
			return chatRefused(err)
		}
		access, err := s.prepareInboxAttachmentAccess(ctx, wsScope, item, store.InboxStateAnswered, s.callerAccess(ctx, c, id))
		if err != nil {
			return chatRefused(&requestError{http.StatusServiceUnavailable, "CallerIdentityUnavailable", "the current caller identity could not be verified for conversation attachments; retry this decision"})
		}
		item, err = s.resolveInboxDecision(ctx, wsScope, item.ID, store.InboxStateAnswered, reply.Response, time.Now().UTC(), payload...)
		if errors.Is(err, store.ErrInboxAlreadyResolved) {
			return chatRefused(errConflict(err.Error()))
		}
		if err != nil {
			return chatRefused(err)
		}
		s.events.publish(wsScope, "inbox", map[string]any{
			"id": item.ID, "state": string(item.State), "agent": name, "runID": item.RunID,
		})
		emit := chatProgressEmitter(ctx, req)
		started := false
		res, err := s.continueInboxRun(context.WithoutCancel(ctx), wsScope, item, access, true, item.Response, func(event string, data map[string]any) {
			if event == "start" {
				started = true
			}
			emit(event, data)
		})
		if !started && errors.Is(err, errResumeConflict) {
			return chatRefused(errConflict(err.Error()))
		}
		if !started && err != nil {
			return chatRefused(err)
		}
		return nil, s.chatTerminalFrame(item.RunID, res, err), nil
	})
}

// MCP progress is scoped to the live request. The run can keep executing after
// disconnect while its observer stops sending notifications.
func chatProgressEmitter(ctx context.Context, req *mcp.CallToolRequest) func(string, map[string]any) {
	token := req.Params.GetProgressToken()
	seq := 0
	return func(event string, data map[string]any) {
		if token == nil || ctx.Err() != nil {
			return
		}
		seq++
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token, Progress: float64(seq),
			Meta: mcp.Meta{chatEventMetaKey: event, chatDataMetaKey: data},
		})
	}
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
