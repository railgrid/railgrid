// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Discord gateway bot: unlike Telegram/Slack (which POST inbound messages to a
// webhook), Discord delivers normal messages only over a persistent gateway
// WebSocket. This manager holds one discordgo session per discord Connection
// that carries a bot token, reads MESSAGE_CREATE events, and submits them as
// channel jobs — the same executor path Telegram/Slack chat uses. Replies go
// back to the exact channel the user typed in (Job.ReplyTarget). Requires the
// privileged MESSAGE CONTENT intent to be enabled on the Discord application.
//
// Runs inside the (single-replica) background executor, so there is exactly one
// gateway connection per bot — no duplicate handling.

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

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/executor"
	"github.com/faroshq/provider-agents/llm"
)

// discordManager owns the live gateway sessions, keyed by "<cluster>/<conn>".
type discordManager struct {
	bg       *background
	mu       sync.Mutex
	sessions map[string]*discordSession
}

type discordSession struct {
	sess *discordgo.Session
	fp   string // token fingerprint, to detect rotation
}

func newDiscordManager(bg *background) *discordManager {
	return &discordManager{bg: bg, sessions: map[string]*discordSession{}}
}

func tokenFingerprint(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])[:12]
}

// reconcile brings the live gateway sessions in line with the discord
// connections that currently carry a bot token. Called from the background
// tick, so bots connect within one poll interval of being created and
// disconnect when their connection (or token) is removed.
func (m *discordManager) reconcile(ctx context.Context) {
	list, err := m.bg.wildcard.Resource(agentsclient.ConnectionGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	desired := map[string]string{} // key -> bot token
	for i := range list.Items {
		conn, err := fromU[agentsv1alpha1.Connection](&list.Items[i])
		if err != nil || conn.Spec.Type != agentsv1alpha1.ConnectionTypeDiscord {
			continue
		}
		cluster := list.Items[i].GetAnnotations()["kcp.io/cluster"]
		if cluster == "" {
			continue
		}
		dyn, err := m.bg.scoped(cluster)
		if err != nil {
			continue
		}
		sec, err := (vwSecrets{dyn}).GetSecret(ctx, llm.SecretNamespace, connectionSecretName(conn.Name))
		if err != nil {
			continue
		}
		token := strings.TrimSpace(string(sec.Data["token"]))
		if token == "" {
			continue // webhook-only discord connection (outbound notify)
		}
		desired[cluster+"/"+conn.Name] = token
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Close sessions whose connection disappeared or whose token rotated.
	for key, s := range m.sessions {
		if fp := desired[key]; fp == "" || tokenFingerprint(fp) != s.fp {
			_ = s.sess.Close()
			delete(m.sessions, key)
		}
	}
	// Open sessions for newly-eligible connections.
	for key, token := range desired {
		if _, ok := m.sessions[key]; ok {
			continue
		}
		cluster, name, found := strings.Cut(key, "/")
		if !found {
			continue
		}
		dg, err := discordgo.New("Bot " + token)
		if err != nil {
			log.Printf("discord: session for %s: %v", key, err)
			continue
		}
		dg.Identify.Intents = discordgo.IntentGuilds | discordgo.IntentGuildMessages |
			discordgo.IntentDirectMessages | discordgo.IntentMessageContent
		dg.AddHandler(m.makeHandler(cluster, name))
		if err := dg.Open(); err != nil {
			// The most common failure is the MESSAGE CONTENT privileged intent
			// not being enabled on the application — surface it plainly.
			log.Printf("discord: gateway open for %s failed: %v (enable the MESSAGE CONTENT intent on the bot in the Discord developer portal)", key, err)
			continue
		}
		m.sessions[key] = &discordSession{sess: dg, fp: tokenFingerprint(token)}
		log.Printf("discord: gateway connected for %s", key)
	}
}

// makeHandler returns the MESSAGE_CREATE handler bound to one connection. It
// responds in DMs, when the bot is @-mentioned, or in the connection's
// configured channel — so the bot stays quiet in busy servers.
func (m *discordManager) makeHandler(cluster, connName string) func(*discordgo.Session, *discordgo.MessageCreate) {
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
		dyn, err := m.bg.scoped(cluster)
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

		agent, err := m.bg.server.routeChannelAgent(ctx, dyn, conn)
		if err != nil {
			_, _ = sess.ChannelMessageSend(mc.ChannelID, "No agent is bound to this Discord connection yet — set it as an agent's notify channel in the portal.")
			return
		}
		_ = sess.ChannelTyping(mc.ChannelID) // "thinking…" while the run executes

		if err := m.bg.exec.Submit(ctx, executor.Job{
			ID:          fmt.Sprintf("discord/%s/%s/%d", cluster, connName, time.Now().UnixNano()),
			Kind:        executor.KindChannel,
			ClusterID:   cluster,
			SourceName:  connName,
			AgentRef:    agent.Name,
			Task:        text,
			ReplyTarget: mc.ChannelID,
			Trigger:     agentsv1alpha1.RunTriggerChannel,
			SessionID:   "discord:" + connName + ":" + mc.ChannelID,
		}); err != nil {
			_, _ = sess.ChannelMessageSend(mc.ChannelID, "⚠️ couldn't queue that right now — try again in a moment.")
		}
	}
}

// closeAll disconnects every live gateway session (provider shutdown).
func (m *discordManager) closeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, s := range m.sessions {
		_ = s.sess.Close()
		delete(m.sessions, key)
	}
}
