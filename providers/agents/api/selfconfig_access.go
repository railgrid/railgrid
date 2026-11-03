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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/tools"
)

// Both run paths can write, so both carry the self-configuration family.
var (
	_ tools.SelfConfigAccess = clientCR{}
	_ tools.SelfConfigAccess = vwCR{}
)

func (a clientCR) UpdateAgent(ctx context.Context, ag *agentsv1alpha1.Agent) error {
	_, err := a.c.Agents().Update(ctx, ag, metav1.UpdateOptions{})
	return err
}

func (a clientCR) CreateTrigger(ctx context.Context, t *agentsv1alpha1.Trigger) error {
	_, err := a.c.Triggers().Create(ctx, t, metav1.CreateOptions{})
	return err
}

func (a clientCR) GetTrigger(ctx context.Context, name string) (*agentsv1alpha1.Trigger, error) {
	return a.c.Triggers().Get(ctx, name, metav1.GetOptions{})
}

func (a clientCR) UpdateTrigger(ctx context.Context, t *agentsv1alpha1.Trigger) error {
	_, err := a.c.Triggers().Update(ctx, t, metav1.UpdateOptions{})
	return err
}

func (a clientCR) DeleteTrigger(ctx context.Context, name string) error {
	return a.c.Triggers().Delete(ctx, name, metav1.DeleteOptions{})
}

func (a clientCR) ListTriggers(ctx context.Context) ([]agentsv1alpha1.Trigger, error) {
	list, err := a.c.Triggers().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (a clientCR) ListWebhooks(ctx context.Context) ([]agentsv1alpha1.Webhook, error) {
	list, err := a.c.Webhooks().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// toU converts a typed object for the dynamic client, stamping the TypeMeta
// the converter does not carry over from the Go type.
func toU(obj runtime.Object, kind string) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetAPIVersion(agentsv1alpha1.SchemeGroupVersion.String())
	u.SetKind(kind)
	return u, nil
}

func (a vwCR) UpdateAgent(ctx context.Context, ag *agentsv1alpha1.Agent) error {
	u, err := toU(ag, "Agent")
	if err != nil {
		return err
	}
	_, err = a.dyn.Resource(agentsclient.AgentGVR).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

func (a vwCR) CreateTrigger(ctx context.Context, t *agentsv1alpha1.Trigger) error {
	u, err := toU(t, "Trigger")
	if err != nil {
		return err
	}
	_, err = a.dyn.Resource(agentsclient.TriggerGVR).Create(ctx, u, metav1.CreateOptions{})
	return err
}

func (a vwCR) GetTrigger(ctx context.Context, name string) (*agentsv1alpha1.Trigger, error) {
	u, err := a.dyn.Resource(agentsclient.TriggerGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return fromU[agentsv1alpha1.Trigger](u)
}

func (a vwCR) UpdateTrigger(ctx context.Context, t *agentsv1alpha1.Trigger) error {
	u, err := toU(t, "Trigger")
	if err != nil {
		return err
	}
	_, err = a.dyn.Resource(agentsclient.TriggerGVR).Update(ctx, u, metav1.UpdateOptions{})
	return err
}

func (a vwCR) DeleteTrigger(ctx context.Context, name string) error {
	return a.dyn.Resource(agentsclient.TriggerGVR).Delete(ctx, name, metav1.DeleteOptions{})
}

func (a vwCR) ListTriggers(ctx context.Context) ([]agentsv1alpha1.Trigger, error) {
	return listVW[agentsv1alpha1.Trigger](ctx, a, agentsclient.TriggerGVR)
}

func (a vwCR) ListWebhooks(ctx context.Context) ([]agentsv1alpha1.Webhook, error) {
	return listVW[agentsv1alpha1.Webhook](ctx, a, agentsclient.WebhookGVR)
}

func listVW[T any](ctx context.Context, a vwCR, gvr schema.GroupVersionResource) ([]T, error) {
	list, err := a.dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(list.Items))
	for i := range list.Items {
		obj, err := fromU[T](&list.Items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *obj)
	}
	return out, nil
}
