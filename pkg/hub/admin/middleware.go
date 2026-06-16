/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package admin

import (
	"context"
	"net/http"
)

// UserResolver maps an inbound request to the caller's User CR name, or returns
// an error if no identity can be resolved. Mirrors tenant.UserResolver so the
// hub can reuse the same OIDC IdentifyUser plumbing.
type UserResolver interface {
	ResolveUser(r *http.Request) (string, error)
}

// UserResolverFunc adapts a function to UserResolver.
type UserResolverFunc func(r *http.Request) (string, error)

// ResolveUser implements UserResolver.
func (f UserResolverFunc) ResolveUser(r *http.Request) (string, error) { return f(r) }

// AdminChecker decides whether a resolved caller is a platform admin. The hub
// supplies an implementation that matches the User CR's name / email /
// rbacIdentity against the --admin-users allowlist.
type AdminChecker interface {
	IsAdmin(ctx context.Context, userName string) bool
}

// AdminCheckerFunc adapts a function to AdminChecker.
type AdminCheckerFunc func(ctx context.Context, userName string) bool

// IsAdmin implements AdminChecker.
func (f AdminCheckerFunc) IsAdmin(ctx context.Context, userName string) bool { return f(ctx, userName) }

type adminCtxKey struct{}

// Middleware gates a subrouter so only platform admins reach it: 401 when no
// identity resolves, 403 when the caller is authenticated but not an admin.
func Middleware(resolver UserResolver, checker AdminChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name, err := resolver.ResolveUser(r)
			if err != nil || name == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if !checker.IsAdmin(r.Context(), name) {
				http.Error(w, "forbidden: admin access required", http.StatusForbidden)
				return
			}
			ctx := context.WithValue(r.Context(), adminCtxKey{}, name)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
