// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Channel inbound: chatting with an agent FROM Telegram/Slack. Each messaging
// connection gets an HMAC-tokenized webhook URL; the platform POSTs message
// events there. Routing: the message goes to the agent whose
// defaultNotifyConnection is this connection (symmetric with outbound — that
// agent "lives" on this channel), overridable via connection config "agent".
// Replies are delivered back through the same connection by the executor's
// channel-job handling. Security: the URL token authenticates the caller, and
// only messages from the connection's configured chat/channel are accepted.

import (
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/executor"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
)

// channelWebhookName namespaces channel webhook tokens away from trigger ones.
func channelWebhookName(conn string) string { return "channel/" + conn }

// webhookChannel receives a messaging platform's event POST, validates it,
// and submits an interactive channel run. Fast path (Slack demands a response
// within 3s): parse + validate, submit to the executor, 200.
func (s *Server) webhookChannel(w http.ResponseWriter, r *http.Request) {
	cluster, name, token := r.PathValue("cluster"), r.PathValue("name"), r.PathValue("token")
	expected := s.webhookToken(cluster, channelWebhookName(name))
	if expected == "" || s.bg == nil || s.bg.wildcard == nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "background executor is not running on this provider")
		return
	}
	if !hmac.Equal([]byte(expected), []byte(token)) {
		writeStatus(w, http.StatusForbidden, "Forbidden", "invalid webhook token")
		return
	}
	dyn, err := s.bg.scoped(cluster)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	cu, err := dyn.Resource(agentsclient.ConnectionGVR).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", "connection not found")
		return
	}
	conn, err := fromU[agentsv1alpha1.Connection](cu)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	var text, source string
	switch conn.Spec.Type {
	case agentsv1alpha1.ConnectionTypeTelegram:
		text, source = parseTelegramUpdate(body)
	case agentsv1alpha1.ConnectionTypeSlack:
		// Slack URL verification handshake: echo the challenge.
		var probe struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
		}
		_ = json.Unmarshal(body, &probe)
		if probe.Type == "url_verification" {
			writeJSON(w, http.StatusOK, map[string]string{"challenge": probe.Challenge})
			return
		}
		text, source = parseSlackEvent(body)
	default:
		writeStatus(w, http.StatusBadRequest, "BadRequest", "connection type does not support inbound")
		return
	}
	if strings.TrimSpace(text) == "" {
		w.WriteHeader(http.StatusOK) // non-message event (edits, joins, bots) — ack silently
		return
	}
	// Only the configured chat/channel may talk to the agent. Unknown senders
	// are acked (200) without action so we neither leak info nor cause the
	// platform to retry.
	if conn.Spec.Channel == "" || source != conn.Spec.Channel {
		log.Printf("channel inbound %s/%s: message from unconfigured chat %q ignored", cluster, name, source)
		w.WriteHeader(http.StatusOK)
		return
	}

	agent, err := s.routeChannelAgent(r, dyn, conn)
	if err != nil {
		s.bg.replyToChannel(r.Context(), dyn, name, "No agent is bound to this channel yet — open the agents portal and set this connection as an agent's notify channel.")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Session + inbox commands are handled synchronously (no LLM round-trip).
	scope := s.bg.scopeFor(r.Context(), cluster, agent.Name)
	session := "channel:" + name
	if reply, handled := s.channelCommand(r, scope, dyn, name, agent, session, text); handled {
		if reply != "" {
			s.bg.replyToChannel(r.Context(), dyn, name, reply)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := s.bg.exec.Submit(r.Context(), executor.Job{
		ID:         fmt.Sprintf("%s/%s/%d", cluster, name, time.Now().UnixNano()),
		Kind:       executor.KindChannel,
		ClusterID:  cluster,
		SourceName: name,
		AgentRef:   agent.Name,
		Task:       text,
		Trigger:    agentsv1alpha1.RunTriggerChannel,
		SessionID:  session,
	}); err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

// channelCommand handles slash commands from a channel. Returns
// (reply, handled): handled=false means the text is a normal chat message.
// Inbox commands act on PENDING items, numbered newest-first as shown by
// /inbox — so "/approve 1" approves the most recent request.
func (s *Server) channelCommand(r *http.Request, scope store.Scope, dyn dynamic.Interface, connName string, agent *agentsv1alpha1.Agent, session, text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", false
	}
	ctx := r.Context()
	wsScope := store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID}
	pending := func() []store.InboxItem {
		items, _ := s.store.ListInbox(ctx, wsScope, store.InboxStatePending)
		return items
	}
	pickItem := func(arg string, items []store.InboxItem) (store.InboxItem, string) {
		if len(items) == 0 {
			return store.InboxItem{}, "Nothing is pending."
		}
		if arg == "" {
			if len(items) == 1 {
				return items[0], ""
			}
			return store.InboxItem{}, fmt.Sprintf("%d items pending — reply /inbox to list, then e.g. /approve 1.", len(items))
		}
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > len(items) {
			return store.InboxItem{}, "Pick an item number from /inbox."
		}
		return items[n-1], ""
	}

	switch fields[0] {
	case "/new":
		_ = s.store.DeleteSession(ctx, scope, session)
		return "🆕 Started a fresh session.", true
	case "/status":
		cred := agent.Spec.Models["chat"]
		return fmt.Sprintf("🤖 %s — model credential: %s, pending approvals/questions: %d", agent.Name, orDash(cred), len(pending())), true
	case "/inbox":
		items := pending()
		if len(items) == 0 {
			return "📭 Nothing needs your attention.", true
		}
		var b strings.Builder
		b.WriteString("📥 Pending:\n")
		for i, it := range items {
			fmt.Fprintf(&b, "%d. [%s/%s] %s\n", i+1, it.AgentName, it.Kind, it.Prompt)
		}
		b.WriteString("Reply /approve N, /deny N, or /answer N <text>.")
		return b.String(), true
	case "/approve", "/deny":
		arg := ""
		if len(fields) > 1 {
			arg = fields[1]
		}
		item, msg := pickItem(arg, pending())
		if msg != "" {
			return msg, true
		}
		state := store.InboxStateApproved
		verb := "✅ Approved"
		if fields[0] == "/deny" {
			state = store.InboxStateDenied
			verb = "🚫 Denied"
		}
		if _, err := s.store.ResolveInboxItem(ctx, wsScope, item.ID, state, "via channel", time.Now().UTC()); err != nil {
			return "Failed: " + err.Error(), true
		}
		extra := ""
		if item.Kind == store.InboxKindApproval && state == store.InboxStateApproved {
			extra = " Tell the agent to retry (the approval authorizes one call)."
		}
		return verb + ": " + item.Prompt + extra, true
	case "/answer":
		if len(fields) < 3 {
			return "Usage: /answer N your answer text", true
		}
		item, msg := pickItem(fields[1], pending())
		if msg != "" {
			return msg, true
		}
		answer := strings.Join(fields[2:], " ")
		if _, err := s.store.ResolveInboxItem(ctx, wsScope, item.ID, store.InboxStateAnswered, answer, time.Now().UTC()); err != nil {
			return "Failed: " + err.Error(), true
		}
		return "💬 Answered: " + item.Prompt, true
	}
	_ = dyn
	_ = connName
	return "", false
}

// routeChannelAgent picks the agent for a channel message: explicit
// config["agent"] override first, else the agent whose defaultNotifyConnection
// is this connection.
func (s *Server) routeChannelAgent(r *http.Request, dyn dynamic.Interface, conn *agentsv1alpha1.Connection) (*agentsv1alpha1.Agent, error) {
	if override := strings.TrimSpace(conn.Spec.Config["agent"]); override != "" {
		au, err := dyn.Resource(agentsclient.AgentGVR).Get(r.Context(), override, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("configured agent %q: %w", override, err)
		}
		return fromU[agentsv1alpha1.Agent](au)
	}
	list, err := dyn.Resource(agentsclient.AgentGVR).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		agent, err := fromU[agentsv1alpha1.Agent](&list.Items[i])
		if err != nil {
			continue
		}
		if agent.Spec.DefaultNotifyConnection == conn.Name {
			return agent, nil
		}
	}
	return nil, fmt.Errorf("no agent bound to connection %q", conn.Name)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// parseTelegramUpdate extracts (text, chatID) from a Telegram update, ignoring
// bot-authored and non-text messages.
func parseTelegramUpdate(body []byte) (text, chatID string) {
	var upd struct {
		Message struct {
			Text string `json:"text"`
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
			From struct {
				IsBot bool `json:"is_bot"`
			} `json:"from"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &upd); err != nil {
		return "", ""
	}
	if upd.Message.From.IsBot || upd.Message.Text == "" {
		return "", ""
	}
	return upd.Message.Text, strconv.FormatInt(upd.Message.Chat.ID, 10)
}

// parseSlackEvent extracts (text, channelID) from a Slack Events API callback,
// ignoring bot messages (including our own replies) and non-message events.
func parseSlackEvent(body []byte) (text, channelID string) {
	var evt struct {
		Type  string `json:"type"`
		Event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			BotID   string `json:"bot_id"`
			Text    string `json:"text"`
			Channel string `json:"channel"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &evt); err != nil {
		return "", ""
	}
	e := evt.Event
	if evt.Type != "event_callback" || e.Type != "message" || e.BotID != "" || e.Subtype != "" || e.Text == "" {
		return "", ""
	}
	return e.Text, e.Channel
}

// enableInboundRequest carries the public origin the portal runs on, so the
// webhook URL is externally reachable.
type enableInboundRequest struct {
	PublicBaseURL string `json:"publicBaseURL"`
}

// enableInbound mints the connection's inbound webhook URL, registers it with
// the platform where possible (Telegram setWebhook), and records it in the
// Connection status. Slack cannot be registered programmatically — the URL is
// returned for pasting into the Slack app's Event Subscriptions.
func (s *Server) enableInbound(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	conn, err := c.Connections().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	if conn.Spec.Type != agentsv1alpha1.ConnectionTypeTelegram && conn.Spec.Type != agentsv1alpha1.ConnectionTypeSlack {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "inbound is only supported for telegram and slack connections")
		return
	}
	token := s.webhookToken(id.clusterID, channelWebhookName(name))
	if token == "" {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "webhook signing unavailable — the provider needs KEDGE_PROVIDER_KUBECONFIG (or AGENTS_WEBHOOK_KEY)")
		return
	}
	var req enableInboundRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	path := "/services/providers/agents/webhooks/channels/" + id.clusterID + "/" + name + "/" + token
	full := strings.TrimRight(strings.TrimSpace(req.PublicBaseURL), "/") + path

	registered := false
	note := ""
	switch conn.Spec.Type {
	case agentsv1alpha1.ConnectionTypeTelegram:
		botToken := s.connectionToken(r, c, name)
		if botToken == "" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "connection has no bot token stored")
			return
		}
		if err := telegramSetWebhook(r, botToken, full); err != nil {
			note = "Telegram setWebhook failed: " + err.Error() + " — the URL must be publicly reachable (HTTPS)."
		} else {
			registered = true
			note = "Telegram webhook registered. Message your bot to chat with the agent."
		}
	case agentsv1alpha1.ConnectionTypeSlack:
		note = "Paste this URL into your Slack app → Event Subscriptions → Request URL, and subscribe to message.channels / message.im bot events."
	}

	conn.Status.WebhookPath = path
	if updated, uerr := c.Connections().UpdateStatus(r.Context(), conn, metav1.UpdateOptions{}); uerr == nil {
		conn = updated
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"webhookPath": path,
		"webhookURL":  full,
		"registered":  registered,
		"note":        note,
	})
}

// connectionToken reads a connection's stored secret token as the caller.
func (s *Server) connectionToken(r *http.Request, c *agentsclient.Client, name string) string {
	sec, err := c.GetSecret(r.Context(), llm.SecretNamespace, connectionSecretName(name))
	if err != nil {
		return ""
	}
	if v, ok := sec.Data["token"]; ok {
		return string(v)
	}
	return ""
}

func telegramSetWebhook(r *http.Request, botToken, webhookURL string) error {
	api := "https://api.telegram.org/bot" + botToken + "/setWebhook"
	resp, err := http.PostForm(api, url.Values{"url": {webhookURL}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out.OK {
		if out.Description == "" {
			out.Description = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("%s", out.Description)
	}
	_ = r
	return nil
}
