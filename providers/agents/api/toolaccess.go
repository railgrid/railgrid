// Copyright 2026 The Faros Authors.
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
	"k8s.io/client-go/dynamic"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/tools"
)

// clientCR implements tools.CRAccess over the per-request tenant client
// (acting as the calling user).
type clientCR struct{ c *agentsclient.Client }

var _ tools.CRAccess = clientCR{}

func (a clientCR) GetAgent(ctx context.Context, name string) (*agentsv1alpha1.Agent, error) {
	return a.c.Agents().Get(ctx, name, metav1.GetOptions{})
}

func (a clientCR) CreateSchedule(ctx context.Context, s *agentsv1alpha1.Schedule) error {
	_, err := a.c.Schedules().Create(ctx, s, metav1.CreateOptions{})
	return err
}

func (a clientCR) ListSchedules(ctx context.Context) ([]agentsv1alpha1.Schedule, error) {
	list, err := a.c.Schedules().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (a clientCR) ListConnections(ctx context.Context) ([]agentsv1alpha1.Connection, error) {
	list, err := a.c.Connections().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (a clientCR) GetConnection(ctx context.Context, name string) (*agentsv1alpha1.Connection, error) {
	return a.c.Connections().Get(ctx, name, metav1.GetOptions{})
}

func (a clientCR) GetToolset(ctx context.Context, name string) (*agentsv1alpha1.Toolset, error) {
	return a.c.Toolsets().Get(ctx, name, metav1.GetOptions{})
}

// vwCR implements tools.CRAccess over a virtual-workspace dynamic client
// scoped to one tenant cluster (background execution path).
type vwCR struct{ dyn dynamic.Interface }

var _ tools.CRAccess = vwCR{}

func (a vwCR) GetAgent(ctx context.Context, name string) (*agentsv1alpha1.Agent, error) {
	u, err := a.dyn.Resource(agentsclient.AgentGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return fromU[agentsv1alpha1.Agent](u)
}

func (a vwCR) CreateSchedule(ctx context.Context, s *agentsv1alpha1.Schedule) error {
	s.TypeMeta = metav1.TypeMeta{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "Schedule"}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(s)
	if err != nil {
		return err
	}
	_, err = a.dyn.Resource(agentsclient.ScheduleGVR).Create(ctx, &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{})
	return err
}

func (a vwCR) ListSchedules(ctx context.Context) ([]agentsv1alpha1.Schedule, error) {
	list, err := a.dyn.Resource(agentsclient.ScheduleGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]agentsv1alpha1.Schedule, 0, len(list.Items))
	for i := range list.Items {
		s, err := fromU[agentsv1alpha1.Schedule](&list.Items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, nil
}

func (a vwCR) ListConnections(ctx context.Context) ([]agentsv1alpha1.Connection, error) {
	list, err := a.dyn.Resource(agentsclient.ConnectionGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]agentsv1alpha1.Connection, 0, len(list.Items))
	for i := range list.Items {
		c, err := fromU[agentsv1alpha1.Connection](&list.Items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

func (a vwCR) GetConnection(ctx context.Context, name string) (*agentsv1alpha1.Connection, error) {
	u, err := a.dyn.Resource(agentsclient.ConnectionGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return fromU[agentsv1alpha1.Connection](u)
}

func (a vwCR) GetToolset(ctx context.Context, name string) (*agentsv1alpha1.Toolset, error) {
	u, err := a.dyn.Resource(agentsclient.ToolsetGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return fromU[agentsv1alpha1.Toolset](u)
}
