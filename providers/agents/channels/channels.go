// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package channels delivers agent messages to external messaging channels
// (Telegram, Slack, email). It is the outbound half of the channel surface —
// the `notify` tool and schedule/heartbeat/budget alerts call Send. Inbound
// (webhook → run) is wired in the api package. Provider-agnostic and
// SDK-portable.
package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

// Message is one outbound notification.
type Message struct {
	// Type is the channel type: "telegram", "slack", "smtp", or "discord".
	Type string
	// Token is the channel credential (bot token, OAuth token, or SMTP
	// password). Read from the connection Secret by the caller.
	Token string
	// Target is the destination: a Telegram chat id, Slack channel id, or an
	// email address.
	Target string
	// Config carries type-specific non-secret settings (e.g. smtp host/port/from).
	Config map[string]string
	// Text is the message body.
	Text string
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Send delivers m to its channel. Returns an error describing any delivery
// failure (surfaced to the user on a "test" send).
// messageLimit is the largest message one backend accepts. 0 means no practical
// limit (email), so the text is sent whole.
func messageLimit(typ string) int {
	switch typ {
	case "discord":
		return 2000
	case "telegram":
		return 4096
	case "slack":
		return 3000
	default:
		return 0
	}
}

// Send delivers text to a chat backend, splitting it across several messages
// when it exceeds what that backend accepts.
//
// Splitting rather than truncating: an agent's answer can run far past any chat
// limit — a research report is tens of thousands of characters — and cutting it
// at the limit silently discards almost all of the work, with nothing to tell the
// reader more existed. Parts go in order and stop at the first failure, so a
// partial delivery is still a prefix of the real answer rather than a mix.
func Send(ctx context.Context, m Message) error {
	typ := strings.TrimSpace(m.Type)
	parts := chunkMessage(m.Text, messageLimit(typ))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		one := m
		one.Text = part
		if err := sendOne(ctx, typ, one); err != nil {
			return err
		}
	}
	return nil
}

func sendOne(ctx context.Context, typ string, m Message) error {
	switch typ {
	case "telegram":
		return sendTelegram(ctx, m)
	case "slack":
		return sendSlack(ctx, m)
	case "smtp":
		return sendSMTP(m)
	case "discord":
		return sendDiscord(ctx, m)
	default:
		return fmt.Errorf("channel type %q is not a messaging type", m.Type)
	}
}

// sendDiscord delivers to Discord in one of two modes:
//   - Bot: Token is a bot token and Target is a channel id → REST message on
//     that channel (the mode gateway-bot chat replies use).
//   - Webhook: Target is an incoming-webhook URL → post directly (outbound
//     notify only, no token).
//
// Discord caps message content at 2000 chars; Send has already split the text
// into pieces that fit, so this posts exactly what it is given.
func sendDiscord(ctx context.Context, m Message) error {
	if m.Target == "" {
		return fmt.Errorf("discord needs a bot token + channel id, or an incoming-webhook URL")
	}
	body, _ := json.Marshal(map[string]string{"content": m.Text})

	var req *http.Request
	var err error
	if strings.HasPrefix(m.Target, "https://") {
		// Webhook URL mode.
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, m.Target, bytes.NewReader(body))
	} else {
		// Bot mode: POST a message to the channel id as the bot.
		if m.Token == "" {
			return fmt.Errorf("discord channel id needs a bot token")
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, "https://discord.com/api/v10/channels/"+m.Target+"/messages", bytes.NewReader(body))
		if req != nil {
			req.Header.Set("Authorization", "Bot "+m.Token)
		}
	}
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("discord send: HTTP %d", resp.StatusCode)
	}
	return nil
}

func sendTelegram(ctx context.Context, m Message) error {
	if m.Token == "" || m.Target == "" {
		return fmt.Errorf("telegram needs a bot token (secret) and a chat id (channel)")
	}
	api := "https://api.telegram.org/bot" + m.Token + "/sendMessage"
	body, _ := json.Marshal(map[string]any{"chat_id": m.Target, "text": m.Text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("telegram sendMessage: HTTP %d", resp.StatusCode)
	}
	return nil
}

func sendSlack(ctx context.Context, m Message) error {
	// Two modes: an incoming-webhook URL (target is the URL) or the Web API
	// with a bot token + channel id.
	if strings.HasPrefix(m.Target, "https://hooks.slack.com/") {
		body, _ := json.Marshal(map[string]string{"text": m.Text})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, m.Target, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("slack webhook: HTTP %d", resp.StatusCode)
		}
		return nil
	}
	if m.Token == "" || m.Target == "" {
		return fmt.Errorf("slack needs a bot token (secret) + channel id, or an incoming-webhook URL as the channel")
	}
	form := url.Values{"channel": {m.Target}, "text": {m.Text}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/chat.postMessage", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+m.Token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out.OK {
		return fmt.Errorf("slack chat.postMessage: %s", out.Error)
	}
	return nil
}

func sendSMTP(m Message) error {
	host := m.Config["host"]
	port := m.Config["port"]
	from := m.Config["from"]
	user := m.Config["username"]
	if host == "" || from == "" || m.Target == "" {
		return fmt.Errorf("smtp needs config host, from, and a recipient (channel)")
	}
	if port == "" {
		port = "587"
	}
	if user == "" {
		user = from
	}
	addr := host + ":" + port
	msg := "From: " + from + "\r\nTo: " + m.Target + "\r\nSubject: " + firstNonEmpty(m.Config["subject"], "Message from your agent") + "\r\n\r\n" + m.Text
	auth := smtp.PlainAuth("", user, m.Token, host)
	return smtp.SendMail(addr, auth, from, []string{m.Target}, []byte(msg))
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
