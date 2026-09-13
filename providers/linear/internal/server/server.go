// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/faroshq/provider-linear/internal/authority"
	"github.com/faroshq/provider-linear/internal/linearapi"
	"github.com/faroshq/provider-sdk/tenantaccess"
	"k8s.io/client-go/dynamic"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Server struct {
	Authority       authority.Authority
	HubURL          string
	Insecure        bool
	InstanceID      string
	PrivateReceipts dynamic.Interface
}

func (s Server) Caller(r *http.Request) (dynamic.Interface, error) {
	cluster := r.Header.Get("X-Faros-Cluster")
	auth := r.Header.Get("Authorization")
	if !namePattern.MatchString(cluster) || !strings.HasPrefix(auth, "Bearer ") || len(auth) < 8 {
		return nil, errors.New("tenant bearer identity required")
	}
	return tenantaccess.NewDynamicClient(s.HubURL, cluster, strings.TrimPrefix(auth, "Bearer "), s.Insecure)
}
func (s Server) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/connections/{connection}/teams", s.teamDiscovery())
	mux.Handle("POST /api/onboarding/teams", newOnboardingHandler(s.Caller, func(ctx context.Context, key, after string) (linearapi.Page[linearapi.Team], error) {
		return linearapi.New(key).Teams(ctx, 50, after)
	}))
	mux.HandleFunc("POST /actions/clusters/{cluster}/teams/{team}/{action}/v1", s.actionHandler)
	mux.HandleFunc("GET /actions/clusters/{cluster}/teams/{team}/{action}/v1", s.actionHandler)
}
