/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package hub

import (
	"context"

	"github.com/faroshq/faros/pkg/hub/kcp"
	"github.com/faroshq/faros/pkg/hub/providers"
)

// edgeRouteResolver adapts *kcp.Bootstrapper to providers.EdgeRouteResolver.
//
// The two EdgeRoute types are mirrored rather than shared because pkg/hub/kcp
// deliberately carries no dependency on pkg/hub/providers — the same reason
// ProviderClaim is mirrored in the other direction. This package is the one
// place that imports both, so the conversion belongs here and nowhere else.
type edgeRouteResolver struct {
	bootstrapper *kcp.Bootstrapper
}

// ResolveProviderEdgeRoute returns the provider-package view of an org-owned
// provider's edge route, or nil when it has none.
func (e edgeRouteResolver) ResolveProviderEdgeRoute(ctx context.Context, orgUUID, providerName, backendURL string) (*providers.EdgeRoute, error) {
	if e.bootstrapper == nil {
		return nil, nil
	}
	route, err := e.bootstrapper.ResolveProviderEdgeRoute(ctx, orgUUID, providerName, backendURL)
	if err != nil || route == nil {
		return nil, err
	}
	return &providers.EdgeRoute{
		WorkspaceUUID: route.WorkspaceUUID,
		Cluster:       route.Cluster,
		EdgeName:      route.EdgeName,
		ServiceName:   route.ServiceName,
	}, nil
}
