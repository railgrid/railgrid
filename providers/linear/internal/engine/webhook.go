// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

type Delivery struct {
	Action         string `json:"action"`
	Type           string `json:"type"`
	OrganizationID string `json:"organizationId"`
	WebhookID      string `json:"webhookId"`
	Timestamp      int64  `json:"webhookTimestamp"`
	Data           struct {
		ID      string `json:"id"`
		TeamID  string `json:"teamId"`
		IssueID string `json:"issueId"`
	} `json:"data"`
}

func Verify(raw []byte, signature, key string, now time.Time) (Delivery, error) {
	var d Delivery
	if len(raw) > 256*1024 || len(key) < 16 {
		return d, errors.New("invalid webhook")
	}
	sig, err := hex.DecodeString(signature)
	if err != nil {
		return d, errors.New("invalid signature")
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return d, errors.New("invalid signature")
	}
	if err = json.Unmarshal(raw, &d); err != nil {
		return d, errors.New("invalid payload")
	}
	delta := now.Sub(time.UnixMilli(d.Timestamp))
	if delta > time.Minute || delta < -time.Minute {
		return d, errors.New("expired webhook")
	}
	if d.Type != "Issue" && d.Type != "Comment" {
		return d, errors.New("unsupported event type")
	}
	if d.Action != "create" && d.Action != "update" && d.Action != "remove" {
		return d, errors.New("unsupported event action")
	}
	if d.Data.ID == "" {
		return d, errors.New("missing event identity")
	}
	return d, nil
}
func (e Engine) Webhook(ctx context.Context, name, signature, deliveryID string, raw []byte) error {
	if len(deliveryID) > 128 {
		return errors.New("delivery identifier too long")
	}
	conn, key, err := e.Connection(ctx, name)
	if err != nil {
		return err
	}
	sub := conn.Spec.Subscription
	if sub == nil {
		return errors.New("subscription not configured")
	}
	signing, err := e.Secret(ctx, sub.SigningSecretRef)
	if err != nil {
		return err
	}
	d, err := Verify(raw, signature, signing, e.now())
	if err != nil {
		return err
	}
	if d.WebhookID != sub.ID || d.OrganizationID != sub.OrganizationID {
		return errors.New("subscription identity mismatch")
	}
	team := d.Data.TeamID
	if d.Type == "Comment" {
		issue, err := e.API(key).Issue(ctx, d.Data.IssueID)
		if err != nil {
			return err
		}
		team = issue.Team.ID
	}
	if !Allowed(conn, team) {
		return errors.New("event team outside connection policy")
	}
	// Hash signed content, excluding delivery timestamp, so header tampering and
	// timestamp-only retries cannot bypass deduplication.
	var content map[string]any
	if err = json.Unmarshal(raw, &content); err != nil {
		return err
	}
	delete(content, "webhookTimestamp")
	canonical, err := json.Marshal(content)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(append([]byte(string(conn.UID)+"/"), canonical...))
	eventName := "e-" + hex.EncodeToString(sum[:])[:48]
	existing, err := e.Client.Resource(Events).Get(ctx, eventName, metav1.GetOptions{})
	if err == nil && existing != nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	// Fail closed at the retention cap. Linear retries on 503 and consumers can
	// explicitly reconcile issue pages if a delivery window was missed.
	list, err := e.Client.Resource(Events).List(ctx, metav1.ListOptions{Limit: 1001})
	if err != nil {
		return err
	}
	if len(list.Items) >= 1000 || list.GetContinue() != "" {
		return errors.New("event retention capacity reached")
	}
	event := api.Event{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupName + "/" + api.Version, Kind: "Event"}, ObjectMeta: metav1.ObjectMeta{Name: eventName}, Spec: api.EventSpec{Connection: name, ConnectionUID: string(conn.UID), DeliveryID: deliveryID, Type: d.Type, Action: d.Action, EntityID: d.Data.ID, IssueID: d.Data.IssueID, TeamID: team, ReceivedAt: metav1.NewTime(e.now()), ExpiresAt: metav1.NewTime(e.now().Add(7 * 24 * time.Hour))}}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&event)
	if err != nil {
		return err
	}
	_, err = e.Client.Resource(Events).Create(ctx, &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
func (e Engine) Prune(ctx context.Context, u *unstructured.Unstructured) error {
	var event api.Event
	if err := Decode(u, &event); err != nil {
		return err
	}
	if event.Spec.ExpiresAt.IsZero() || e.now().Before(event.Spec.ExpiresAt.Time) {
		return nil
	}
	uid := u.GetUID()
	rv := u.GetResourceVersion()
	return e.Client.Resource(Events).Delete(ctx, u.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
}
