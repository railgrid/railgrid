// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tunnel

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/providerrelay"
)

type providerRelayContext struct{}

type ProviderRelayVerifier interface {
	Verify(context.Context, string, string, string, string, string, string) (providerrelay.Claims, error)
}

func (p *Server) SetProviderRelayVerifier(v ProviderRelayVerifier) { p.providerRelayVerifier = v }

func (p *Server) verifyProviderRelay(ctx context.Context, r *http.Request, cluster, service, path string) (context.Context, error) {
	values := r.Header.Values(providerrelay.Header)
	// Consume it even on denial; no tenant, appliance or agent receives proof.
	r.Header.Del(providerrelay.Header)
	if len(values) == 0 {
		return ctx, nil
	}
	if len(values) != 1 || p.providerRelayVerifier == nil {
		return ctx, providerrelay.ErrInvalid
	}
	claims, err := p.providerRelayVerifier.Verify(ctx, values[0], cluster, service, r.Method, path, r.URL.RawQuery)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, providerRelayContext{}, claims), nil
}

func stampProviderRelay(h http.Header, ctx context.Context) bool {
	claims, ok := ctx.Value(providerRelayContext{}).(providerrelay.Claims)
	if !ok {
		return false
	}
	h.Del(dataplane.HeaderUser)
	h.Del(dataplane.HeaderTenant)
	h.Del(dataplane.HeaderCluster)
	h.Set(dataplane.HeaderRemoteUser, claims.Identity.User)
	for _, group := range claims.Identity.Groups {
		h.Add(dataplane.HeaderRemoteGroup, group)
	}
	for key, values := range claims.Identity.Extra {
		for _, value := range values {
			h.Add(dataplane.HeaderRemoteExtraPrefix+url.PathEscape(strings.ToLower(key)), value)
		}
	}
	h.Set(dataplane.HeaderHops, strconv.Itoa(claims.Hops))
	return true
}
