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

	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-agents/executor"
)

// ControllerDeps is what the multicluster reconcilers (controller/...) need
// from the HTTP/background half of the provider. main wires them together;
// neither side imports the other. Every value is safe for concurrent use.
type ControllerDeps struct {
	// Config targets the provider's own kcp workspace (the APIExport and its
	// endpoint slice live there); the manager's apiexport provider and the
	// leader-election lease both use it.
	Config *rest.Config
	// Submit queues background work with a Pending run row recorded first
	// (see background.Submit); the Schedule reconciler fires through it.
	Submit executor.Submitter
	// Telegram re-registers a bot's webhook with a secret token.
	Telegram TelegramBotAPI
	// OAuth renews expiring connection tokens.
	OAuth *OAuthRefresher
	// Gateway holds the Discord sessions the Connection reconciler asks for.
	// Its CloseAll belongs at the end of a leadership term.
	Gateway *DiscordGateway
}

// ControllerDeps returns the reconciler dependencies, or ok=false when the
// background executor is not running (no provider kubeconfig): without tenant
// access there is nothing for the controllers to act through.
func (s *Server) ControllerDeps() (ControllerDeps, bool) {
	if s.bg == nil {
		return ControllerDeps{}, false
	}
	return ControllerDeps{
		Config:   s.bg.base,
		Submit:   s.bg,
		Telegram: TelegramBotAPI{},
		OAuth:    s.OAuthRefresher(),
		Gateway:  s.bg.discord,
	}, true
}

// TelegramBotAPI adapts the Bot API calls the inbound channel code already
// makes to controller/connection.Telegram.
type TelegramBotAPI struct{}

// WebhookURL returns the URL currently registered for the bot ("" when none).
func (TelegramBotAPI) WebhookURL(ctx context.Context, botToken string) (string, error) {
	return telegramWebhookURL(ctx, botToken)
}

// SetWebhook registers webhookURL for the bot with secretToken.
func (TelegramBotAPI) SetWebhook(ctx context.Context, botToken, webhookURL, secretToken string) error {
	return telegramSetWebhook(ctx, botToken, webhookURL, secretToken)
}
