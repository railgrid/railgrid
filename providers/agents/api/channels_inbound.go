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
// channel-job handling. Security: the URL token gates the route, every
// delivery must carry the platform's own proof (Slack request signature,
// Telegram secret token — see channels_verify.go), duplicate deliveries are
// acknowledged without a second run, and only messages from the connection's
// configured chat/channel are accepted.

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	if expected == "" || s.bg == nil || !s.bg.ready() {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "background executor is not running on this provider")
		return
	}
	if !hmac.Equal([]byte(expected), []byte(token)) {
		writeStatus(w, http.StatusForbidden, "Forbidden", "invalid webhook token")
		return
	}
	dyn, err := s.bg.scoped(r.Context(), cluster)
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

	if conn.Spec.Type != agentsv1alpha1.ConnectionTypeTelegram && conn.Spec.Type != agentsv1alpha1.ConnectionTypeSlack {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "connection type does not support inbound")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 256*1024))

	// Platform proof BEFORE parsing anything: the URL token alone is not
	// authentication (the URL is displayed in the portal). A connection with no
	// signing secret stored is rejected outright — the reconcile loop flags it
	// in status; there is no unverified grace mode.
	secret, serr := connectionSigningSecret(r.Context(), dyn, name)
	if serr != nil {
		// Not a 401: the connection may well have a secret we simply could not
		// read, and saying "no verification secret" would send the user after a
		// setting that is already correct. 503 is also what makes the platforms
		// redeliver — Slack retries a non-2xx, Telegram resends until it sees a
		// 2xx — so the message survives the blip instead of being dropped.
		log.Printf("channel inbound %s/%s: reading the verification secret: %v", cluster, name, serr)
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "could not read the connection's verification secret — retry")
		return
	}
	if err := verifyInbound(conn.Spec.Type, secret, r, body, time.Now()); err != nil {
		log.Printf("channel inbound %s/%s: rejected %s delivery: %v", cluster, name, conn.Spec.Type, err)
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return
	}

	var ev inboundEvent
	switch conn.Spec.Type {
	case agentsv1alpha1.ConnectionTypeTelegram:
		ev = parseTelegramUpdate(body)
	case agentsv1alpha1.ConnectionTypeSlack:
		// Slack URL verification handshake: echo the challenge. Slack signs this
		// request like any other, so it is answered only after verification.
		var probe struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
		}
		_ = json.Unmarshal(body, &probe)
		if probe.Type == "url_verification" {
			writeJSON(w, http.StatusOK, map[string]string{"challenge": probe.Challenge})
			return
		}
		ev = parseSlackEvent(body)
	}
	if strings.TrimSpace(ev.Text) == "" {
		w.WriteHeader(http.StatusOK) // non-message event (edits, joins, bots) — ack silently
		return
	}
	// Only the configured chat/channel may talk to the agent. Unknown senders
	// are acked (200) without action so we neither leak info nor cause the
	// platform to retry.
	if conn.Spec.Channel == "" || ev.Source != conn.Spec.Channel {
		log.Printf("channel inbound %s/%s: message from unconfigured chat %q ignored", cluster, name, ev.Source)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Redeliveries (Slack retries after a slow/non-2xx answer and says so in
	// X-Slack-Retry-Num; Telegram resends until acked) must not run twice.
	key := inboundEventKey(cluster, name, ev.ID)
	if !s.bg.seen.claim(key) {
		if retry := r.Header.Get("X-Slack-Retry-Num"); retry != "" {
			log.Printf("channel inbound %s/%s: acknowledged Slack retry %s of already-handled event %s", cluster, name, retry, ev.ID)
		} else {
			log.Printf("channel inbound %s/%s: duplicate delivery of event %s acknowledged", cluster, name, ev.ID)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	agent, err := s.routeChannelAgent(r.Context(), dyn, conn)
	if err != nil {
		s.bg.replyToChannel(r.Context(), dyn, name, "No agent is bound to this channel yet — open the agents portal and set this connection as an agent's notify channel.")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Session + inbox commands are handled synchronously (no LLM round-trip).
	scope := s.bg.scopeFor(r.Context(), cluster, agent.Name)
	session := "channel:" + name
	if reply, handled := s.channelCommand(r, scope, dyn, name, agent, session, ev.Text); handled {
		if reply != "" {
			s.bg.replyToChannel(r.Context(), dyn, name, reply)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// Slack redelivers after 3s without a 2xx; wait for a queue slot at most
	// this long, then tell the platform to try again rather than losing the
	// message. The same budget is fine for Telegram.
	sctx, cancel := context.WithTimeout(r.Context(), channelSubmitWait)
	defer cancel()
	if err := s.bg.exec.Submit(sctx, executor.Job{
		ID:         fmt.Sprintf("%s/%s/%s", cluster, name, orNano(ev.ID)),
		Kind:       executor.KindChannel,
		ClusterID:  cluster,
		SourceName: name,
		AgentRef:   agent.Name,
		Task:       ev.Text,
		Trigger:    agentsv1alpha1.RunTriggerChannel,
		SessionID:  session,
	}); err != nil {
		s.bg.seen.release(key) // let the platform's retry through
		// The caller is Slack or Telegram, not an operator. The submit error
		// names queue depth and capacity, the job kind and source, and the
		// context error — useful in the log, none of the platform's business,
		// and nothing it needs in order to redeliver. Log it; answer with a
		// fixed body.
		log.Printf("channel inbound %s/%s: submit failed: %v", cluster, name, err)
		if errors.Is(err, executor.ErrQueueFull) {
			w.Header().Set("Retry-After", "5")
		}
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", inboundSubmitUnavailableMessage)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// channelSubmitWait bounds how long the inbound handler waits for executor
// queue space — under Slack's 3s redelivery timer.
const channelSubmitWait = 2500 * time.Millisecond

// inboundSubmitUnavailableMessage is the whole of what an external platform
// learns when a verified delivery could not be queued. It is deliberately
// fixed: the underlying error is logged for the operator, and the platform
// only needs the 503 (plus Retry-After when the queue was full) to redeliver.
const inboundSubmitUnavailableMessage = "temporarily unable to accept the delivery; retry later"

// verifyInbound applies the platform's per-delivery proof for a connection
// type. Both platforms are checked over the raw body, before any parsing.
func verifyInbound(connType, secret string, r *http.Request, body []byte, now time.Time) error {
	switch connType {
	case agentsv1alpha1.ConnectionTypeSlack:
		return verifySlackSignature(secret, r.Header.Get("X-Slack-Request-Timestamp"), r.Header.Get("X-Slack-Signature"), body, now)
	case agentsv1alpha1.ConnectionTypeTelegram:
		return verifyTelegramSecret(secret, r.Header.Get("X-Telegram-Bot-Api-Secret-Token"))
	}
	return fmt.Errorf("connection type %q does not support inbound", connType)
}

func orNano(id string) string {
	if id != "" {
		return id
	}
	return strconv.FormatInt(time.Now().UnixNano(), 10)
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
		resolved, err := s.resolveInboxDecision(ctx, wsScope, item.ID, state, "via channel", time.Now().UTC())
		if err != nil {
			return "Failed: " + err.Error(), true
		}
		s.events.publish(wsScope, "inbox", map[string]any{
			"id": resolved.ID, "state": string(resolved.State), "agent": resolved.AgentName, "runID": resolved.RunID,
		})
		extra := ""
		if item.Kind == store.InboxKindApproval && resolved.RunID != "" {
			// Resume the paused run through the virtual workspace — the reply
			// (or the denial's fallout) arrives on this channel when it finishes.
			rd := resumeDeps{Creds: vwSecrets{dyn}, CR: vwCR{dyn}}
			go s.resumeApprovedRun(wsScope, resolved, rd, state == store.InboxStateApproved, "via channel")
			extra = " Resuming the run…"
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
// config["agent"] override first, else the agent that lists this connection as
// one of its channels (spec.channels[].connectionRef). An agent may own
// several channels, so it can
// receive on more than one connection; inbound uniqueness (at most one agent
// per connection) is enforced when an agent's channels are saved.
func (s *Server) routeChannelAgent(ctx context.Context, dyn dynamic.Interface, conn *agentsv1alpha1.Connection) (*agentsv1alpha1.Agent, error) {
	if override := strings.TrimSpace(conn.Spec.Config["agent"]); override != "" {
		au, err := dyn.Resource(agentsclient.AgentGVR).Get(ctx, override, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("configured agent %q: %w", override, err)
		}
		return fromU[agentsv1alpha1.Agent](au)
	}
	list, err := dyn.Resource(agentsclient.AgentGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		agent, err := fromU[agentsv1alpha1.Agent](&list.Items[i])
		if err != nil {
			continue
		}
		if agent.Spec.AgentClaimsConnection(conn.Name) {
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

// inboundEvent is the one message a platform delivery carries, reduced to what
// the handler needs: the text, the chat/channel it came from, and the
// platform's id for the delivery (the de-duplication key). Empty Text means
// "nothing to do" (bot message, edit, join, non-text update).
type inboundEvent struct {
	Text   string
	Source string
	ID     string
}

// parseTelegramUpdate extracts the message from a Telegram update, ignoring
// bot-authored and non-text messages. update_id is unique per bot and
// monotonically increasing, so it is the dedup key.
func parseTelegramUpdate(body []byte) inboundEvent {
	var upd struct {
		UpdateID int64 `json:"update_id"`
		Message  struct {
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
		return inboundEvent{}
	}
	if upd.Message.From.IsBot || upd.Message.Text == "" {
		return inboundEvent{}
	}
	id := ""
	if upd.UpdateID != 0 {
		id = strconv.FormatInt(upd.UpdateID, 10)
	}
	return inboundEvent{Text: upd.Message.Text, Source: strconv.FormatInt(upd.Message.Chat.ID, 10), ID: id}
}

// parseSlackEvent extracts the message from a Slack Events API callback,
// ignoring bot messages (including our own replies) and non-message events.
// The envelope's event_id is the dedup key (it is stable across Slack's
// retries); older payloads without one fall back to the message ts + channel.
func parseSlackEvent(body []byte) inboundEvent {
	var evt struct {
		Type    string `json:"type"`
		EventID string `json:"event_id"`
		Event   struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			BotID   string `json:"bot_id"`
			Text    string `json:"text"`
			Channel string `json:"channel"`
			TS      string `json:"ts"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &evt); err != nil {
		return inboundEvent{}
	}
	e := evt.Event
	if evt.Type != "event_callback" || e.Type != "message" || e.BotID != "" || e.Subtype != "" || e.Text == "" {
		return inboundEvent{}
	}
	id := evt.EventID
	if id == "" && e.TS != "" {
		id = e.TS + "@" + e.Channel
	}
	return inboundEvent{Text: e.Text, Source: e.Channel, ID: id}
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
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "webhook signing unavailable — the provider needs FAROS_PROVIDER_KUBECONFIG (or AGENTS_WEBHOOK_KEY)")
		return
	}
	var req enableInboundRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	path := "/services/providers/agents/webhooks/channels/" + id.clusterID + "/" + name + "/" + token
	full := strings.TrimRight(strings.TrimSpace(req.PublicBaseURL), "/") + path

	registered := false
	note := ""
	// A failed read must not read as "no secret stored". Both branches below
	// act on that emptiness destructively: Slack would reject a correctly
	// configured connection as missing its signing secret, and Telegram would
	// mint and store a fresh secret token over whatever is already there —
	// which breaks inbound until the webhook is re-registered, because the
	// deliveries in flight still carry the old one. NotFound alone means empty.
	sec, serr := c.GetSecret(r.Context(), llm.SecretNamespace, connectionSecretName(name))
	if serr != nil && !apierrors.IsNotFound(serr) {
		writeUpdateError(w, fmt.Errorf("read connection secret for %q: %w", name, serr))
		return
	}
	signing := ""
	if sec != nil {
		signing = strings.TrimSpace(string(sec.Data[signingSecretKey]))
	}
	switch conn.Spec.Type {
	case agentsv1alpha1.ConnectionTypeTelegram:
		botToken := s.connectionToken(r, c, name)
		if botToken == "" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "connection has no bot token stored")
			return
		}
		// Connections created before secret tokens existed have none yet: mint
		// one here so (re-)enabling inbound always registers a verified webhook.
		if signing == "" {
			generated, err := newSigningSecret()
			if err != nil {
				writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
				return
			}
			if err := mergeConnectionSecret(r.Context(), c, name, map[string]string{signingSecretKey: generated}); err != nil {
				writeUpdateError(w, err)
				return
			}
			signing = generated
		}
		if err := telegramSetWebhook(r.Context(), botToken, full, signing); err != nil {
			note = "Telegram setWebhook failed: " + err.Error() + " — the URL must be publicly reachable (HTTPS)."
		} else {
			registered = true
			note = "Telegram webhook registered (with a secret token). Message your bot to chat with the agent."
		}
	case agentsv1alpha1.ConnectionTypeSlack:
		// Without the app signing secret every event would be rejected (401);
		// do not hand out a URL that cannot work.
		if signing == "" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "add the Slack app signing secret to this connection first (Slack app → Basic Information → Signing Secret), then enable inbound")
			return
		}
		note = "Paste this URL into your Slack app → Event Subscriptions → Request URL, and subscribe to message.channels / message.im bot events. Requests are verified with the app signing secret."
	}

	conn.Status.WebhookPath = path
	if conn.Status.Message == connectionSigningSecretMissingMessage {
		conn.Status.Phase, conn.Status.Message = "Ready", ""
	}
	if _, uerr := c.Connections().UpdateStatus(r.Context(), conn, metav1.UpdateOptions{}); uerr != nil {
		log.Printf("enable inbound %s: recording webhook path: %v", name, uerr)
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
	return s.connectionTokenCtx(r.Context(), c, name)
}

func (s *Server) connectionTokenCtx(ctx context.Context, c *agentsclient.Client, name string) string {
	sec, err := c.GetSecret(ctx, llm.SecretNamespace, connectionSecretName(name))
	if err != nil {
		return ""
	}
	if v, ok := sec.Data["token"]; ok {
		return string(v)
	}
	return ""
}

// telegramAPIBase is the Bot API origin; tests point it at a local server.
var telegramAPIBase = "https://api.telegram.org"

// telegramSetWebhook registers webhookURL for the bot. secretToken is sent as
// secret_token, which Telegram then echoes in X-Telegram-Bot-Api-Secret-Token
// on every delivery — the proof the inbound handler requires.
func telegramSetWebhook(ctx context.Context, botToken, webhookURL, secretToken string) error {
	form := url.Values{"url": {webhookURL}}
	if secretToken != "" {
		form.Set("secret_token", secretToken)
	}
	return telegramCall(ctx, botToken, "setWebhook", form, nil)
}

// telegramWebhookURL returns the URL currently registered for the bot ("" when
// none), via getWebhookInfo.
func telegramWebhookURL(ctx context.Context, botToken string) (string, error) {
	var info struct {
		URL string `json:"url"`
	}
	if err := telegramCall(ctx, botToken, "getWebhookInfo", nil, &info); err != nil {
		return "", err
	}
	return info.URL, nil
}

func telegramCall(ctx context.Context, botToken, method string, form url.Values, result any) error {
	api := telegramAPIBase + "/bot" + botToken + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	// A body that is not the Bot API's JSON envelope — an HTML error page from
	// an intercepting proxy, a truncated response — would otherwise leave out.OK
	// false and surface as a bare "HTTP 200", which describes neither what went
	// wrong nor where. Report the decode failure and the status together.
	if derr := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&out); derr != nil {
		return fmt.Errorf("telegram %s: decode response (HTTP %d): %w", method, resp.StatusCode, derr)
	}
	if !out.OK {
		if out.Description == "" {
			out.Description = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("%s", out.Description)
	}
	if result != nil && len(out.Result) > 0 {
		return json.Unmarshal(out.Result, result)
	}
	return nil
}
