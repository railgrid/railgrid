// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package kcp

import (
	"fmt"
	"net/http"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

// retiredDelegationGuard rejects only credentials that native kcp has already
// authenticated as one of Railgrid's retired tenant delegation accounts.
// It deliberately has no client, informer, or tenant inventory dependency.
type retiredDelegationGuard struct{}

// installAuthenticator rejects the credentials' identity before the native
// impersonation filter can replace it with an effective identity.
func (g retiredDelegationGuard) installAuthenticator(config *genericapiserver.Config) error {
	if config == nil || config.Authentication.Authenticator == nil {
		return fmt.Errorf("embedded kcp has no native authenticator for retired delegation guard")
	}
	config.Authentication.Authenticator = retiredDelegationAuthenticator{
		delegate: config.Authentication.Authenticator,
		guard:    g,
	}
	return nil
}

type retiredDelegationAuthenticator struct {
	delegate authenticator.Request
	guard    retiredDelegationGuard
}

func (a retiredDelegationAuthenticator) AuthenticateRequest(req *http.Request) (*authenticator.Response, bool, error) {
	response, ok, err := a.delegate.AuthenticateRequest(req)
	if err != nil || !ok || response == nil || response.User == nil || !a.guard.retired(response.User.GetName()) {
		return response, ok, err
	}
	return nil, false, fmt.Errorf("retired delegated service-account credentials are no longer accepted")
}

func (retiredDelegationGuard) retired(username string) bool {
	// The shared helper performs only a bounded local prefix check; no tenant
	// inventory or native Kubernetes request is needed for this identity class.
	return serviceaccounts.IsRetiredDelegationUsername(username)
}

func (g retiredDelegationGuard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticated, ok := request.UserFrom(r.Context()); ok && authenticated != nil && g.retired(authenticated.GetName()) {
			status := apierrors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("retired delegated service-account credentials are no longer accepted"))
			responsewriters.WriteRawJSON(http.StatusForbidden, responsewriters.ErrorToAPIStatus(status), w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// installMain decorates the copied MiniAggregator chain after native
// authentication middleware has been composed. It wraps the existing chain
// instead of replacing it, so other Railgrid guards remain intact.
func (retiredDelegationGuard) installMain(config *genericapiserver.Config) error {
	if config == nil || config.BuildHandlerChainFunc == nil {
		return fmt.Errorf("embedded kcp has no main handler chain for retired delegation guard")
	}
	previous := config.BuildHandlerChainFunc
	config.BuildHandlerChainFunc = func(api http.Handler, c *genericapiserver.Config) http.Handler {
		return previous(retiredDelegationGuard{}.Wrap(api), c)
	}
	return nil
}

// installVirtual decorates the final embedded virtual-workspace delegation
// target. The VW root owns its handler chain, so the request guard belongs on
// the resulting target's UnprotectedHandler, after native authentication.
func (g retiredDelegationGuard) installVirtual(workspaces []rootapiserver.NamedVirtualWorkspace) error {
	if len(workspaces) == 0 {
		return fmt.Errorf("embedded virtual workspaces are required for retired delegation guard")
	}
	last := &workspaces[len(workspaces)-1]
	if last.VirtualWorkspace == nil {
		return fmt.Errorf("embedded virtual workspace chain is unavailable for retired delegation guard")
	}
	last.VirtualWorkspace = &retiredDelegationWorkspace{VirtualWorkspace: last.VirtualWorkspace, guard: g}
	return nil
}

type retiredDelegationWorkspace struct {
	framework.VirtualWorkspace
	guard retiredDelegationGuard
}

func (v *retiredDelegationWorkspace) Register(name string, config genericapiserver.CompletedConfig, delegate genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
	target, err := v.VirtualWorkspace.Register(name, config, delegate)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("virtual workspace registration returned no delegation target")
	}
	return &retiredDelegationTarget{DelegationTarget: target, handler: v.guard.Wrap(target.UnprotectedHandler())}, nil
}

type retiredDelegationTarget struct {
	genericapiserver.DelegationTarget
	handler http.Handler
}

func (t *retiredDelegationTarget) UnprotectedHandler() http.Handler { return t.handler }
