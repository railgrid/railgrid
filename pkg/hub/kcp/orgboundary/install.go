// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package orgboundary

import (
	"fmt"
	"net/http"

	"github.com/kcp-dev/kcp/pkg/server"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
	genericapiserver "k8s.io/apiserver/pkg/server"
)

// Install attaches the organization boundary to a KCP server config before
// Complete. The same source resolver is installed on the main KCP authenticator
// and optional virtual-workspace server, so the check runs before either path
// processes impersonation headers.
func Install(config *server.Config) error {
	return InstallWithConfig(config, DefaultConfig())
}

// InstallWithConfig is Install with explicit process-local request limits.
func InstallWithConfig(config *server.Config, limits Config) error {
	if config == nil {
		return fmt.Errorf("KCP server config is required")
	}
	guard, err := New(limits)
	if err != nil {
		return err
	}
	if config.KcpSharedInformerFactory == nil {
		return fmt.Errorf("KCP LogicalCluster informer factory is required")
	}
	guard.SetInformers(config.KcpSharedInformerFactory)
	if !guard.hasLookup() {
		return fmt.Errorf("KCP LogicalCluster lister is required")
	}

	if config.GenericConfig == nil || config.GenericConfig.Authentication.Authenticator == nil {
		return fmt.Errorf("KCP request authenticator is required")
	}
	wrapped := boundaryAuthenticator{delegate: config.GenericConfig.Authentication.Authenticator, guard: guard}
	config.GenericConfig.Authentication.Authenticator = wrapped
	if config.MiniAggregator == nil {
		return fmt.Errorf("KCP mini-aggregator config is required")
	}
	config.MiniAggregator.GenericConfig.Authentication.Authenticator = wrapped
	if config.ApiExtensions != nil && config.ApiExtensions.GenericConfig != nil {
		config.ApiExtensions.GenericConfig.Authentication.Authenticator = wrapped
	}
	if config.Apis != nil && config.Apis.Generic != nil {
		config.Apis.Generic.Authentication.Authenticator = wrapped
	}

	if err := wrapHandlerChain(&config.MiniAggregator.GenericConfig, guard); err != nil {
		return fmt.Errorf("wrap KCP main API handler: %w", err)
	}
	if config.OptionalVirtual != nil {
		workspaces := config.OptionalVirtual.Extra.VirtualWorkspaces
		if len(workspaces) > 0 {
			last := len(workspaces) - 1
			vw := workspaces[last]
			if vw.VirtualWorkspace == nil {
				return fmt.Errorf("virtual workspace %q has no implementation", vw.Name)
			}
			// KCP builds the virtual-workspace delegate as a chain. Wrapping the
			// final registration puts one guard around the whole chain, preserves
			// each delegate's lifecycle hooks, and avoids charging requests once
			// for every registered virtual workspace.
			workspaces[last].VirtualWorkspace = guardedVirtualWorkspace{
				VirtualWorkspace: vw.VirtualWorkspace,
				guard:            guard,
			}
		}
	}
	return nil
}

// hasLookup ensures Install was given the local informer-backed source of truth.
func (g *Guard) hasLookup() bool { return g != nil && g.clusters != nil && g.synced != nil }

func wrapHandlerChain(config *genericapiserver.Config, guard *Guard) error {
	if config == nil || config.BuildHandlerChainFunc == nil {
		return fmt.Errorf("KCP handler-chain builder is required")
	}
	previous := config.BuildHandlerChainFunc
	config.BuildHandlerChainFunc = func(handler http.Handler, genericConfig *genericapiserver.Config) http.Handler {
		return previous(guard.Wrap(handler), genericConfig)
	}
	return nil
}

type guardedVirtualWorkspace struct {
	framework.VirtualWorkspace
	guard *Guard
}

func (v guardedVirtualWorkspace) Register(name string, config genericapiserver.CompletedConfig, delegate genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
	target, err := v.VirtualWorkspace.Register(name, config, delegate)
	if err != nil || target == nil {
		return target, err
	}
	handler := target.UnprotectedHandler()
	if handler == nil {
		return target, nil
	}
	return guardedDelegationTarget{
		DelegationTarget: target,
		handler:          v.guard.Wrap(handler),
	}, nil
}

type guardedDelegationTarget struct {
	genericapiserver.DelegationTarget
	handler http.Handler
}

func (t guardedDelegationTarget) UnprotectedHandler() http.Handler { return t.handler }

var _ framework.VirtualWorkspace = guardedVirtualWorkspace{}
