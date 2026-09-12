// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"net/http"
	"strings"

	"github.com/faroshq/faros/pkg/hub/tenant"
)

// providerCatalogMiddleware admits online-verified workload/delegated tokens
// only to the read-only catalog. It does not turn them into human sessions or
// assign a membership role. Human callers retain the existing optional-org flow.
func providerCatalogMiddleware(human func(http.Handler) http.Handler, verify func(*http.Request) (string, string, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fallback := human(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/providers" {
				user, path, err := verify(r)
				parts := strings.Split(path, ":")
				if err == nil && user != "" && len(parts) == 5 && strings.Join(parts[:3], ":") == workspacePathRoot && parts[3] != "" && parts[4] != "" {
					tc := tenant.TenantContext{User: user, OrgUUID: parts[3], WorkspaceUUID: parts[4]}
					next.ServeHTTP(w, r.WithContext(tenant.WithContext(r.Context(), tc)))
					return
				}
			}
			fallback.ServeHTTP(w, r)
		})
	}
}
