// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package kcp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
)

func TestRetiredDelegationGuardRejectsOnlyAuthenticatedRetiredAccounts(t *testing.T) {
	tests := []struct {
		name     string
		username string
		wantCode int
	}{
		{name: "old delegated service account", username: "system:serviceaccount:default:railgrid-du-0123456789", wantCode: http.StatusForbidden},
		{name: "new platform delegated service account", username: "system:serviceaccount:default:railgrid-pdu-0123456789", wantCode: http.StatusNoContent},
		{name: "other namespace", username: "system:serviceaccount:other:railgrid-du-0123456789", wantCode: http.StatusNoContent},
		{name: "similar name", username: "system:serviceaccount:default:railgrid-du", wantCode: http.StatusNoContent},
		{name: "no authenticated identity", wantCode: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			handler := (retiredDelegationGuard{}).Wrap(next)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/default/configmaps", nil)
			if tt.username != "" {
				req = req.WithContext(request.WithUser(req.Context(), &user.DefaultInfo{Name: tt.username}))
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", response.Code, tt.wantCode, response.Body.String())
			}
			if tt.wantCode == http.StatusForbidden && response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("retirement denial content type = %q, want Kubernetes JSON Status", response.Header().Get("Content-Type"))
			}
		})
	}
}

func TestRetiredDelegationAuthenticatorRejectsOriginalIdentity(t *testing.T) {
	for _, tt := range []struct {
		name     string
		username string
		wantOK   bool
	}{
		{name: "legacy identity", username: "system:serviceaccount:default:railgrid-du-old", wantOK: false},
		{name: "platform identity", username: "system:serviceaccount:default:railgrid-pdu-new", wantOK: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			guard := retiredDelegationGuard{}
			config := &genericapiserver.Config{}
			config.Authentication.Authenticator = authenticator.RequestFunc(func(*http.Request) (*authenticator.Response, bool, error) {
				called = true
				return &authenticator.Response{User: &user.DefaultInfo{Name: tt.username}}, true, nil
			})
			if err := guard.installAuthenticator(config); err != nil {
				t.Fatal(err)
			}
			response, ok, err := config.Authentication.Authenticator.AuthenticateRequest(httptest.NewRequest(http.MethodGet, "/", nil))
			if !called {
				t.Fatal("native authenticator was not called")
			}
			if ok != tt.wantOK {
				t.Fatalf("authenticated = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantOK && (err != nil || response == nil) {
				t.Fatalf("allowed identity response = %#v, error = %v", response, err)
			}
			if !tt.wantOK && (err == nil || response != nil) {
				t.Fatalf("retired identity response = %#v, error = %v; want unauthenticated error", response, err)
			}
		})
	}
}

func TestRetiredDelegationHooksComposeWithExistingChains(t *testing.T) {
	t.Run("main", func(t *testing.T) {
		config := &genericapiserver.Config{BuildHandlerChainFunc: func(next http.Handler, _ *genericapiserver.Config) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := request.WithUser(r.Context(), &user.DefaultInfo{Name: "system:serviceaccount:default:railgrid-du-old"})
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		}}
		if err := (retiredDelegationGuard{}).installMain(config); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		config.BuildHandlerChainFunc(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("retired identity reached native handler") }), config).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/namespaces", nil))
		if response.Code != http.StatusForbidden {
			t.Fatalf("main chain status = %d, want 403", response.Code)
		}
	})

	t.Run("virtual workspace", func(t *testing.T) {
		next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("retired identity reached virtual handler") })
		workspaces := []rootapiserver.NamedVirtualWorkspace{{Name: "test", VirtualWorkspace: fakeRetiredWorkspace{target: fakeRetiredTarget{handler: next}}}}
		if err := (retiredDelegationGuard{}).installVirtual(workspaces); err != nil {
			t.Fatal(err)
		}
		target, err := workspaces[0].Register("test", genericapiserver.CompletedConfig{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/services/apiexport/root/example/clusters/*/api/v1/configmaps", nil)
		req = req.WithContext(request.WithUser(req.Context(), &user.DefaultInfo{Name: "system:serviceaccount:default:railgrid-du-old"}))
		response := httptest.NewRecorder()
		target.UnprotectedHandler().ServeHTTP(response, req)
		if response.Code != http.StatusForbidden {
			t.Fatalf("virtual handler status = %d, want 403", response.Code)
		}
	})
}

type fakeRetiredTarget struct {
	genericapiserver.DelegationTarget
	handler http.Handler
}

func (t fakeRetiredTarget) UnprotectedHandler() http.Handler { return t.handler }

type fakeRetiredWorkspace struct {
	framework.VirtualWorkspace
	target genericapiserver.DelegationTarget
}

func (v fakeRetiredWorkspace) Register(string, genericapiserver.CompletedConfig, genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
	return v.target, nil
}
