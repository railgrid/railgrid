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
	// Type is the channel type: "telegram", "slack", or "smtp".
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
func Send(ctx context.Context, m Message) error {
	switch strings.TrimSpace(m.Type) {
	case "telegram":
		return sendTelegram(ctx, m)
	case "slack":
		return sendSlack(ctx, m)
	case "smtp":
		return sendSMTP(m)
	default:
		return fmt.Errorf("channel type %q is not a messaging type", m.Type)
	}
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
