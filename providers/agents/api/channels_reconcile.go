// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Reconcile of inbound-verification material for messaging connections that
// pre-date it. The inbound handler rejects any delivery it cannot verify, so a
// connection that was enabled for inbound before signing secrets existed
// would go silently dark. This loop (startup + every background tick) makes
// the state visible and, where the provider owns the secret, fixes it:
//
//   - Slack: the app signing secret can only come from the user. An
//     inbound-enabled Slack connection without one is parked in
//     Status.Phase=Error with connectionSigningSecretMissingMessage until the
//     connection is updated with a signingSecret; then it is set back to Ready.
//   - Telegram: the secret_token is ours to choose. A connection without one
//     gets a fresh secret stored and the webhook Telegram currently has for the
//     bot re-registered with it — no user action, no public-URL knowledge
//     needed (getWebhookInfo returns the registered URL).

import (
	"context"
	"log"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/llm"
)

const connectionTelegramRegisterFailedPrefix = "telegram webhook re-registration failed: "

func (b *background) reconcileChannelSecrets(ctx context.Context) {
	items, err := b.listAll(ctx, agentsclient.ConnectionGVR)
	if err != nil {
		log.Printf("channels: listing connections: %v", err)
		return
	}
	for i := range items {
		u := &items[i]
		conn, err := fromU[agentsv1alpha1.Connection](u)
		if err != nil {
			continue
		}
		if conn.Spec.Type != agentsv1alpha1.ConnectionTypeSlack && conn.Spec.Type != agentsv1alpha1.ConnectionTypeTelegram {
			continue
		}
		// Only connections that receive: an outbound-only Slack notify
		// connection has nothing to verify and must not show an error.
		if conn.Status.WebhookPath == "" {
			continue
		}
		cluster := u.GetAnnotations()["kcp.io/cluster"]
		if cluster == "" {
			continue
		}
		dyn, err := b.scoped(ctx, cluster)
		if err != nil {
			log.Printf("channels: connection %s/%s: addressing its workspace: %v", cluster, conn.Name, err)
			continue
		}
		// A read we could not complete says nothing about whether the secret
		// exists, so leave the status alone rather than flagging a healthy
		// connection as broken; the next sweep re-reads it.
		secret, serr := connectionSigningSecret(ctx, dyn, conn.Name)
		if serr != nil {
			log.Printf("channels: connection %s/%s: reading its verification secret: %v — leaving status unchanged", cluster, conn.Name, serr)
			continue
		}
		switch conn.Spec.Type {
		case agentsv1alpha1.ConnectionTypeSlack:
			b.reconcileSlackSecret(ctx, dyn, cluster, u, conn, secret)
		case agentsv1alpha1.ConnectionTypeTelegram:
			if secret == "" {
				b.adoptTelegramSecret(ctx, dyn, cluster, u, conn)
			}
		}
	}
}

func (b *background) reconcileSlackSecret(ctx context.Context, dyn dynamic.Interface, cluster string, u *unstructured.Unstructured, conn *agentsv1alpha1.Connection, secret string) {
	switch {
	case secret == "" && conn.Status.Message != connectionSigningSecretMissingMessage:
		log.Printf("channels: slack connection %s/%s has inbound enabled but no signing secret — inbound events will be rejected until the connection is updated", cluster, conn.Name)
		if err := setConnectionStatus(ctx, dyn, u, "Error", connectionSigningSecretMissingMessage); err != nil {
			log.Printf("channels: connection %s/%s: recording status: %v", cluster, conn.Name, err)
		}
	case secret != "" && conn.Status.Message == connectionSigningSecretMissingMessage:
		if err := setConnectionStatus(ctx, dyn, u, "Ready", ""); err != nil {
			log.Printf("channels: connection %s/%s: recording status: %v", cluster, conn.Name, err)
		}
	}
}

// adoptTelegramSecret gives a pre-existing Telegram connection a secret_token
// and re-registers its webhook with it.
func (b *background) adoptTelegramSecret(ctx context.Context, dyn dynamic.Interface, cluster string, u *unstructured.Unstructured, conn *agentsv1alpha1.Connection) {
	sec, err := vwSecrets{dyn}.GetSecret(ctx, llm.SecretNamespace, connectionSecretName(conn.Name))
	if err != nil {
		log.Printf("channels: telegram connection %s/%s: reading its credential Secret: %v", cluster, conn.Name, err)
		return
	}
	botToken := strings.TrimSpace(string(sec.Data["token"]))
	if botToken == "" {
		return
	}
	generated, err := newSigningSecret()
	if err != nil {
		log.Printf("channels: telegram connection %s/%s: generating secret: %v", cluster, conn.Name, err)
		return
	}
	// Store first: from here on the handler demands the token, and Telegram
	// retries any delivery we refuse in the moment before setWebhook lands.
	if err := updateSecretKeys(ctx, dyn, connectionSecretName(conn.Name), map[string]string{signingSecretKey: generated}); err != nil {
		log.Printf("channels: telegram connection %s/%s: storing secret: %v", cluster, conn.Name, err)
		return
	}
	registered, err := telegramWebhookURL(ctx, botToken)
	if err == nil {
		switch {
		case registered == "":
			err = errNoTelegramWebhook
		case !strings.HasSuffix(registered, conn.Status.WebhookPath):
			err = errForeignTelegramWebhook
		default:
			err = telegramSetWebhook(ctx, botToken, registered, generated)
		}
	}
	if err != nil {
		log.Printf("channels: telegram connection %s/%s: %s%v", cluster, conn.Name, connectionTelegramRegisterFailedPrefix, err)
		_ = setConnectionStatus(ctx, dyn, u, "Error", connectionTelegramRegisterFailedPrefix+err.Error()+" — click Enable inbound to register it again")
		return
	}
	log.Printf("channels: telegram connection %s/%s: webhook re-registered with a secret token", cluster, conn.Name)
	if strings.HasPrefix(conn.Status.Message, connectionTelegramRegisterFailedPrefix) {
		_ = setConnectionStatus(ctx, dyn, u, "Ready", "")
	}
}

type reconcileError string

func (e reconcileError) Error() string { return string(e) }

const (
	errNoTelegramWebhook      = reconcileError("no webhook is registered for the bot")
	errForeignTelegramWebhook = reconcileError("the bot's registered webhook points elsewhere")
)

// setConnectionStatus writes phase/message on the Connection's status
// subresource through the virtual workspace, using the listed object's
// resourceVersion so a concurrent edit is not overwritten.
func setConnectionStatus(ctx context.Context, dyn dynamic.Interface, u *unstructured.Unstructured, phase, message string) error {
	obj := u.DeepCopy()
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	status["phase"] = phase
	status["message"] = message
	status["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
	if err := unstructured.SetNestedMap(obj.Object, status, "status"); err != nil {
		return err
	}
	_, err := dyn.Resource(agentsclient.ConnectionGVR).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	return err
}
