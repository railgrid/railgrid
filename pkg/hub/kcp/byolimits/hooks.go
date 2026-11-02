// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package byolimits

import (
	"fmt"
	"net/http"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"
	genericapiserver "k8s.io/apiserver/pkg/server"
)

// InstallMain decorates the outer MiniAggregator config after kcp NewConfig
// has copied its configs. Passing only kcp's GenericConfig misses this copy.
func (g *Guard) InstallMain(config *genericapiserver.Config) error {
	previous := config.BuildHandlerChainFunc
	if previous == nil {
		return fmt.Errorf("embedded kcp has no main handler chain")
	}
	config.BuildHandlerChainFunc = func(api http.Handler, c *genericapiserver.Config) http.Handler {
		wrapped := previous(g.Wrap(api), c)
		g.mainInstalled.Store(true)
		return wrapped
	}
	return nil
}

// InstallVirtual decorates the final aggregate delegation target. The VW root
// installs its own BuildHandlerChainFunc later, so replacing that field would
// silently lose the guard. Its default chain authenticates and authorizes
// before calling the resulting target's UnprotectedHandler.
func (g *Guard) InstallVirtual(workspaces []rootapiserver.NamedVirtualWorkspace) error {
	if len(workspaces) == 0 {
		return fmt.Errorf("embedded virtual workspaces are required for BYO limits")
	}
	last := &workspaces[len(workspaces)-1]
	last.VirtualWorkspace = &limitedWorkspace{VirtualWorkspace: last.VirtualWorkspace, guard: g}
	return nil
}

type limitedWorkspace struct {
	framework.VirtualWorkspace
	guard *Guard
}

func (v *limitedWorkspace) Register(name string, config genericapiserver.CompletedConfig, delegate genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
	target, err := v.VirtualWorkspace.Register(name, config, delegate)
	if err != nil {
		return nil, err
	}
	v.guard.virtualInstalled.Store(true)
	return &limitedTarget{DelegationTarget: target, handler: v.guard.Wrap(target.UnprotectedHandler())}, nil
}

type limitedTarget struct {
	genericapiserver.DelegationTarget
	handler http.Handler
}

func (t *limitedTarget) UnprotectedHandler() http.Handler { return t.handler }
