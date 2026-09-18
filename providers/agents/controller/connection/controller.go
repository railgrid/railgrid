// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package connection reconciles Connection CRs: everything the provider has
// to keep true about a connection on its own, without a user request.
//
//   - Inbound verification material for messaging connections. The inbound
//     handler rejects any delivery it cannot verify, so a connection enabled
//     for inbound before signing secrets existed would go silently dark. A
//     Slack signing secret can only come from the user: an inbound-enabled
//     Slack connection without one is parked in Status.Phase=Error until the
//     connection is updated, then set back to Ready. A Telegram secret_token
//     is ours to choose: a connection without one gets a fresh secret stored
//     and the webhook Telegram currently has for the bot re-registered with
//     it — no user action, no public-URL knowledge needed.
//   - OAuth token refresh. A connection whose refresh token is about to expire
//     is renewed and requeued for the next expiry; nothing polls.
//   - Discord gateway bots. A discord connection carrying a bot token gets a
//     live gateway session (opened by the Gateway, which keeps the socket map
//     in-process); a removed connection or token closes it.
//
// Status.Phase/Message for these concerns are written only here. The
// reconciler runs on the leader replica, so there is exactly one gateway
// session per bot and one writer per status.
package connection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/internal/connsecret"
	"github.com/railgrid/provider-agents/llm"
)

// Telegram is the slice of the Bot API the reconciler needs to re-register a
// webhook with a secret token.
type Telegram interface {
	// WebhookURL returns the URL currently registered for the bot ("" when
	// none).
	WebhookURL(ctx context.Context, botToken string) (string, error)
	// SetWebhook registers webhookURL for the bot with secretToken, which
	// Telegram then echoes on every delivery.
	SetWebhook(ctx context.Context, botToken, webhookURL, secretToken string) error
}

// OAuthRefresher exchanges a refresh token for a new access token. It knows
// the provider presets and the platform OAuth app credentials; the reconciler
// only knows when to ask.
type OAuthRefresher interface {
	// Refresh returns the Secret keys to write (token, refresh_token, expiry
	// as RFC3339) for the connection whose credential Secret data is given.
	Refresh(ctx context.Context, conn *agentsv1alpha1.Connection, secret map[string][]byte) (map[string]string, error)
}

// Gateway owns the live Discord gateway sessions. The reconciler states the
// desired set — one session per discord connection with a bot token — and the
// Gateway makes it so, keeping the sockets themselves out of the controller.
type Gateway interface {
	// Ensure opens (or keeps) the session for cluster/name with token. A
	// rotated token replaces the session. Idempotent.
	Ensure(ctx context.Context, cluster, name, token string) error
	// Remove closes the session for cluster/name, if any.
	Remove(cluster, name string)
}

const (
	// resyncInterval bounds how long a connection can drift from the truth on
	// a missed event: a credential Secret edited behind the reconciler's
	// back, a gateway session that died. Secrets are watched too, so this is
	// the safety net rather than the mechanism, and it is long on purpose —
	// the timed work (an OAuth expiry) sets its own, sooner requeue.
	resyncInterval = time.Hour
	// oauthRefreshSkew is how long before expiry a token is renewed, so a run
	// starting just before expiry still finishes on a valid token.
	oauthRefreshSkew = 15 * time.Minute
	// oauthRetryInterval spaces retries of a failed refresh: soon enough to
	// beat the expiry it is racing, not so often that a revoked grant spams
	// the token endpoint.
	oauthRetryInterval = 5 * time.Minute
	// gatewayRetryInterval re-attempts a gateway session that failed to open
	// (most often the MESSAGE CONTENT intent still disabled on the app).
	gatewayRetryInterval = time.Minute
)

// Reconciler keeps Connections verifiable, refreshed and connected.
type Reconciler struct {
	Manager  mcmanager.Manager
	Telegram Telegram
	OAuth    OAuthRefresher
	Gateway  Gateway
	// Now is the clock; nil means time.Now. Tests pin it.
	Now func() time.Time
}

// SetupWithManager wires the reconciler into the multicluster manager. The
// credential Secrets are watched as well as the Connections: an OAuth callback
// storing a token, a user pasting a signing secret, a rotated bot token — each
// lands in the Secret, not the Connection, and each must re-trigger.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-connection").
		For(&agentsv1alpha1.Connection{}).
		Watches(&corev1.Secret{}, mchandler.EnqueueRequestsFromMapFunc(secretToConnection)).
		Complete(r)
}

// secretToConnection maps a credential Secret event to its Connection.
func secretToConnection(_ context.Context, obj client.Object) []reconcile.Request {
	if obj.GetNamespace() != llm.SecretNamespace {
		return nil
	}
	name, ok := connsecret.ConnectionName(obj.GetName())
	if !ok {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name}}}
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Reconcile handles one Connection.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("connection", req.Name, "cluster", req.ClusterName)
	cluster := req.ClusterName.String()
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()

	var conn agentsv1alpha1.Connection
	if err := c.Get(ctx, req.NamespacedName, &conn); err != nil {
		if apierrors.IsNotFound(err) {
			r.removeGateway(cluster, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !conn.DeletionTimestamp.IsZero() {
		r.removeGateway(cluster, req.Name)
		return ctrl.Result{}, nil
	}

	result := ctrl.Result{RequeueAfter: resyncInterval}
	sooner := func(d time.Duration) {
		if d > 0 && d < result.RequeueAfter {
			result.RequeueAfter = d
		}
	}

	switch conn.Spec.Type {
	case agentsv1alpha1.ConnectionTypeSlack, agentsv1alpha1.ConnectionTypeTelegram:
		// Only connections that receive: an outbound-only Slack notify
		// connection has nothing to verify and must not show an error.
		if conn.Status.WebhookPath != "" {
			r.reconcileMessaging(ctx, logger, c, &conn)
		}
	case agentsv1alpha1.ConnectionTypeDiscord:
		sooner(r.reconcileGateway(ctx, logger, c, cluster, &conn))
	}

	if conn.Spec.Auth == "oauth" && conn.Spec.OAuth != nil {
		sooner(r.reconcileOAuth(ctx, logger, c, &conn))
	}
	return result, nil
}

func (r *Reconciler) removeGateway(cluster, name string) {
	if r.Gateway != nil {
		r.Gateway.Remove(cluster, name)
	}
}

// ---- messaging: inbound verification material -------------------------------

func (r *Reconciler) reconcileMessaging(ctx context.Context, logger klog.Logger, c client.Client, conn *agentsv1alpha1.Connection) {
	// A read we could not complete says nothing about whether the secret
	// exists, so leave the status alone rather than flagging a healthy
	// connection as broken; the resync re-reads it.
	secret, err := signingSecret(ctx, c, conn.Name)
	if err != nil {
		logger.Error(err, "reading the verification secret; leaving status unchanged")
		return
	}
	switch conn.Spec.Type {
	case agentsv1alpha1.ConnectionTypeSlack:
		switch {
		case secret == "" && conn.Status.Message != connsecret.SigningSecretMissingMessage:
			logger.Info("slack connection has inbound enabled but no signing secret; inbound events will be rejected until the connection is updated")
			if err := r.setStatus(ctx, c, conn, "Error", connsecret.SigningSecretMissingMessage); err != nil {
				logger.Error(err, "recording status")
			}
		case secret != "" && conn.Status.Message == connsecret.SigningSecretMissingMessage:
			if err := r.setStatus(ctx, c, conn, "Ready", ""); err != nil {
				logger.Error(err, "recording status")
			}
		}
	case agentsv1alpha1.ConnectionTypeTelegram:
		if secret == "" {
			r.adoptTelegramSecret(ctx, logger, c, conn)
		}
	}
}

var (
	errNoTelegramWebhook      = errors.New("no webhook is registered for the bot")
	errForeignTelegramWebhook = errors.New("the bot's registered webhook points elsewhere")
	errNoTelegramClient       = errors.New("telegram client not configured")
)

// adoptTelegramSecret gives a pre-existing Telegram connection a secret_token
// and re-registers its webhook with it.
func (r *Reconciler) adoptTelegramSecret(ctx context.Context, logger klog.Logger, c client.Client, conn *agentsv1alpha1.Connection) {
	var sec corev1.Secret
	if err := c.Get(ctx, credentialKey(conn.Name), &sec); err != nil {
		logger.Error(err, "reading the telegram credential Secret")
		return
	}
	botToken := strings.TrimSpace(string(sec.Data["token"]))
	if botToken == "" {
		return
	}
	generated, err := connsecret.NewSigningSecret()
	if err != nil {
		logger.Error(err, "generating a telegram secret")
		return
	}
	// Store first: from here on the handler demands the token, and Telegram
	// retries any delivery we refuse in the moment before setWebhook lands.
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Data[connsecret.SigningSecretKey] = []byte(generated)
	if err := c.Update(ctx, &sec); err != nil {
		logger.Error(err, "storing the telegram secret")
		return
	}
	err = errNoTelegramClient
	if r.Telegram != nil {
		var registered string
		registered, err = r.Telegram.WebhookURL(ctx, botToken)
		if err == nil {
			switch {
			case registered == "":
				err = errNoTelegramWebhook
			case !strings.HasSuffix(registered, conn.Status.WebhookPath):
				err = errForeignTelegramWebhook
			default:
				err = r.Telegram.SetWebhook(ctx, botToken, registered, generated)
			}
		}
	}
	if err != nil {
		logger.Error(err, "telegram webhook re-registration failed")
		_ = r.setStatus(ctx, c, conn, "Error", connsecret.TelegramRegisterFailedPrefix+err.Error()+" — click Enable inbound to register it again")
		return
	}
	logger.Info("telegram webhook re-registered with a secret token")
	if strings.HasPrefix(conn.Status.Message, connsecret.TelegramRegisterFailedPrefix) {
		_ = r.setStatus(ctx, c, conn, "Ready", "")
	}
}

// signingSecret reads the connection's verification secret. A missing Secret
// is "" with no error; a failed read is an error, because "" is what callers
// act on and collapsing a failed read into it would park a healthy connection
// in Error.
func signingSecret(ctx context.Context, c client.Client, connName string) (string, error) {
	var sec corev1.Secret
	err := c.Get(ctx, credentialKey(connName), &sec)
	switch {
	case apierrors.IsNotFound(err):
		return "", nil
	case err != nil:
		return "", err
	}
	return strings.TrimSpace(string(sec.Data[connsecret.SigningSecretKey])), nil
}

func credentialKey(connName string) types.NamespacedName {
	return types.NamespacedName{Namespace: llm.SecretNamespace, Name: connsecret.Name(connName)}
}

// setStatus writes phase/message on the Connection's status subresource
// against the resourceVersion we read, so a concurrent edit is not overwritten.
func (r *Reconciler) setStatus(ctx context.Context, c client.Client, conn *agentsv1alpha1.Connection, phase, message string) error {
	conn.Status.Phase = phase
	conn.Status.Message = message
	conn.Status.UpdatedAt = &metav1.Time{Time: r.now()}
	return c.Status().Update(ctx, conn)
}

// ---- oauth: token refresh ---------------------------------------------------

// reconcileOAuth renews the connection's access token once it is within
// oauthRefreshSkew of expiring, and returns when to come back: the moment the
// next token enters the window, a retry interval after a failure, or 0 when
// there is nothing to refresh (no refresh token, no expiry).
func (r *Reconciler) reconcileOAuth(ctx context.Context, logger klog.Logger, c client.Client, conn *agentsv1alpha1.Connection) time.Duration {
	if r.OAuth == nil {
		return 0
	}
	var sec corev1.Secret
	if err := c.Get(ctx, credentialKey(conn.Name), &sec); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "reading the oauth credential Secret")
			return oauthRetryInterval
		}
		return 0 // not connected yet; the Secret watch brings us back
	}
	refresh := string(sec.Data["refresh_token"])
	expiryRaw := string(sec.Data["expiry"])
	if refresh == "" || expiryRaw == "" {
		return 0 // nothing to refresh (e.g. Slack bot tokens don't expire)
	}
	expiry, err := time.Parse(time.RFC3339, expiryRaw)
	if err != nil {
		logger.Error(err, "unparseable token expiry; not refreshing", "expiry", expiryRaw)
		return 0
	}
	now := r.now()
	if until := expiry.Sub(now); until > oauthRefreshSkew {
		return until - oauthRefreshSkew
	}

	updates, err := r.OAuth.Refresh(ctx, conn, sec.Data)
	if err != nil {
		logger.Error(err, "oauth refresh failed")
		return oauthRetryInterval
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	for k, v := range updates {
		sec.Data[k] = []byte(v)
	}
	if err := c.Update(ctx, &sec); err != nil {
		logger.Error(err, "storing the refreshed token")
		return oauthRetryInterval
	}
	logger.Info("refreshed oauth token")
	if next, err := time.Parse(time.RFC3339, updates["expiry"]); err == nil {
		if d := next.Sub(now) - oauthRefreshSkew; d > 0 {
			return d
		}
		return oauthRetryInterval
	}
	return 0
}

// ---- discord: gateway desired state -----------------------------------------

// reconcileGateway makes the Discord session for this connection match its
// bot token: present → a live session, absent → none. Returns a sooner
// requeue when opening the session failed.
func (r *Reconciler) reconcileGateway(ctx context.Context, logger klog.Logger, c client.Client, cluster string, conn *agentsv1alpha1.Connection) time.Duration {
	if r.Gateway == nil {
		return 0
	}
	var sec corev1.Secret
	if err := c.Get(ctx, credentialKey(conn.Name), &sec); err != nil {
		if apierrors.IsNotFound(err) {
			r.Gateway.Remove(cluster, conn.Name)
			return 0
		}
		// Report rather than swallow: without this a connection whose Secret
		// is unreadable through the permission claim looks identical to a
		// healthy webhook-only one — the bot simply never appears.
		logger.Error(err, "reading the discord credential Secret")
		return gatewayRetryInterval
	}
	token := strings.TrimSpace(string(sec.Data["token"]))
	if token == "" {
		r.Gateway.Remove(cluster, conn.Name) // webhook-only discord connection (outbound notify)
		return 0
	}
	if err := r.Gateway.Ensure(ctx, cluster, conn.Name, token); err != nil {
		logger.Error(err, "discord gateway session")
		return gatewayRetryInterval
	}
	return 0
}

// String renders the reconciler for logs.
func (r *Reconciler) String() string {
	return fmt.Sprintf("connection.Reconciler{telegram=%t oauth=%t gateway=%t}", r.Telegram != nil, r.OAuth != nil, r.Gateway != nil)
}
