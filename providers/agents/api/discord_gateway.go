// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Discord gateway bot: unlike Telegram/Slack (which POST inbound messages to a
// webhook), Discord delivers normal messages only over a persistent gateway
// WebSocket. This gateway holds one discordgo session per discord Connection
// that carries a bot token, reads MESSAGE_CREATE events, and submits them as
// channel jobs — the same executor path Telegram/Slack chat uses. Replies go
// back to the exact channel the user typed in (Job.ReplyTarget). Requires the
// privileged MESSAGE CONTENT intent to be enabled on the Discord application.
//
// Which sessions should exist is decided by the Connection reconciler
// (controller/connection), which calls Ensure/Remove as connections and their
// bot tokens come and go; only the socket map lives here. The reconciler runs
// on the leader replica, so there is exactly one gateway connection per bot —
// no duplicate handling — and the manager closes every session when the
// leadership term ends.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/executor"
)

// DiscordGateway owns the live gateway sessions, keyed by "<cluster>/<conn>".
// It satisfies controller/connection.Gateway.
type DiscordGateway struct {
	bg       *background
	mu       sync.Mutex
	sessions map[string]*discordSession
}

type discordSession struct {
	sess *discordgo.Session
	fp   string // token fingerprint, to detect rotation
}

func newDiscordGateway(bg *background) *DiscordGateway {
	return &DiscordGateway{bg: bg, sessions: map[string]*discordSession{}}
}

func tokenFingerprint(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])[:12]
}

// Ensure brings the session for one connection in line with its bot token:
// none → opened, same token → kept, rotated token → replaced. Idempotent, so
// the reconciler can call it on every pass.
func (m *DiscordGateway) Ensure(_ context.Context, cluster, name, token string) error {
	key := cluster + "/" + name
	fp := tokenFingerprint(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[key]; ok {
		if s.fp == fp {
			return nil
		}
		_ = s.sess.Close()
		delete(m.sessions, key)
	}
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return fmt.Errorf("session for %s: %w", key, err)
	}
	dg.Identify.Intents = discordgo.IntentGuilds | discordgo.IntentGuildMessages |
		discordgo.IntentDirectMessages | discordgo.IntentMessageContent
	dg.AddHandler(m.makeHandler(cluster, name))
	if err := dg.Open(); err != nil {
		// The most common failure is the MESSAGE CONTENT privileged intent
		// not being enabled on the application — surface it plainly.
		return fmt.Errorf("gateway open for %s failed: %w (enable the MESSAGE CONTENT intent on the bot in the Discord developer portal)", key, err)
	}
	m.sessions[key] = &discordSession{sess: dg, fp: fp}
	log.Printf("discord: gateway connected for %s", key)
	return nil
}

// Remove closes the session for one connection, if any.
func (m *DiscordGateway) Remove(cluster, name string) {
	key := cluster + "/" + name
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[key]; ok {
		_ = s.sess.Close()
		delete(m.sessions, key)
		log.Printf("discord: gateway closed for %s", key)
	}
}

// Sessions reports the connections with a live session, for tests and health.
func (m *DiscordGateway) Sessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.sessions))
	for k := range m.sessions {
		out = append(out, k)
	}
	return out
}

// makeHandler returns the MESSAGE_CREATE handler bound to one connection. It
// responds in DMs, when the bot is @-mentioned, or in the connection's
// configured channel — so the bot stays quiet in busy servers.
func (m *DiscordGateway) makeHandler(cluster, connName string) func(*discordgo.Session, *discordgo.MessageCreate) {
	return func(sess *discordgo.Session, mc *discordgo.MessageCreate) {
		if mc.Author == nil || mc.Author.Bot {
			return
		}
		botID := ""
		if sess.State != nil && sess.State.User != nil {
			botID = sess.State.User.ID
		}
		if mc.Author.ID == botID {
			return
		}
		text := strings.TrimSpace(mc.Content)
		if text == "" {
			return
		}
		isDM := mc.GuildID == ""
		mentioned := false
		for _, u := range mc.Mentions {
			if u.ID == botID {
				mentioned = true
				break
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		dyn, err := m.bg.scoped(ctx, cluster)
		if err != nil {
			return
		}
		cu, err := dyn.Resource(agentsclient.ConnectionGVR).Get(ctx, connName, metav1.GetOptions{})
		if err != nil {
			return
		}
		conn, err := fromU[agentsv1alpha1.Connection](cu)
		if err != nil {
			return
		}
		inConfigured := conn.Spec.Channel != "" && conn.Spec.Channel == mc.ChannelID
		if !isDM && !mentioned && !inConfigured {
			return // not addressed to the bot — stay quiet
		}
		if mentioned && botID != "" {
			text = strings.TrimSpace(strings.NewReplacer("<@"+botID+">", "", "<@!"+botID+">", "").Replace(text))
		}
		if text == "" {
			return
		}

		// The gateway session is authenticated by the bot token, so there is no
		// signature to verify here (Discord only signs HTTP interaction
		// callbacks, which this provider does not expose). A resumed session
		// can replay MESSAGE_CREATE, so the message id is still de-duplicated.
		key := inboundEventKey(cluster, connName, "discord:"+mc.ID)
		if !m.bg.seen.claim(key) {
			return
		}

		agent, err := m.bg.server.routeChannelAgent(ctx, dyn, conn)
		if err != nil {
			_, _ = sess.ChannelMessageSend(mc.ChannelID, "No agent is bound to this Discord connection yet — set it as an agent's notify channel in the portal.")
			return
		}
		_ = sess.ChannelTyping(mc.ChannelID) // "thinking…" while the run executes

		if err := m.bg.exec.Submit(ctx, executor.Job{
			ID:          fmt.Sprintf("discord/%s/%s/%s", cluster, connName, orNano(mc.ID)),
			Kind:        executor.KindChannel,
			ClusterID:   cluster,
			SourceName:  connName,
			AgentRef:    agent.Name,
			Task:        text,
			ReplyTarget: mc.ChannelID,
			Trigger:     agentsv1alpha1.RunTriggerChannel,
			SessionID:   "discord:" + connName + ":" + mc.ChannelID,
		}); err != nil {
			m.bg.seen.release(key)
			log.Printf("discord: %s/%s: %v", cluster, connName, err)
			_, _ = sess.ChannelMessageSend(mc.ChannelID, "⚠️ couldn't queue that right now — try again in a moment.")
		}
	}
}

// CloseAll disconnects every live gateway session (provider shutdown, or the
// end of a leadership term — the next leader opens its own).
func (m *DiscordGateway) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, s := range m.sessions {
		_ = s.sess.Close()
		delete(m.sessions, key)
	}
}
