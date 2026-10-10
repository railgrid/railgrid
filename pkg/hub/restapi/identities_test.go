/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package restapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/gorilla/mux"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/identity"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

type stubAttestor struct {
	err      error
	provider string
}

func (s *stubAttestor) Attest(_ context.Context, _ *http.Request, provider string) (string, error) {
	s.provider = provider
	if s.err != nil {
		return "", s.err
	}
	return providers.ProviderSAUsername, nil
}

type stubIdentityService struct {
	ensured          int
	released         []string
	mode             tenancyv1alpha1.ScopedIdentityAttestationMode
	subject          string
	request          identity.Request
	ensureErr        error
	revisionVerified bool
}

func (s *stubIdentityService) Ensure(_ context.Context, req identity.Request, mode tenancyv1alpha1.ScopedIdentityAttestationMode, subject string) (*identity.Token, error) {
	s.ensured++
	s.mode, s.subject = mode, subject
	s.request = req
	if s.ensureErr != nil {
		return nil, s.ensureErr
	}
	return &identity.Token{Token: "minted", TokenType: "Bearer", ServiceAccount: "railgrid-si-x", Name: "si-x", OwnerRevisionVerified: s.revisionVerified}, nil
}

func (s *stubIdentityService) ReleaseByName(_ context.Context, provider, name string) error {
	s.released = append(s.released, provider+"/"+name)
	return nil
}

func (s *stubIdentityService) List(_ context.Context, _, _ string, _ *identity.Owner) ([]tenancyv1alpha1.ScopedIdentity, error) {
	return nil, nil
}

func identityRouter(service IdentityService, attestor IdentityAttestor) *mux.Router {
	router := mux.NewRouter()
	NewIdentityHandler(service, attestor, logr.Discard()).Register(router)
	return router
}

const identityBody = `{"owner":{"provider":"agents","kind":"Agent","group":"agents.railgrid.ai","version":"v1alpha1","resource":"agents","name":"scheduler","uid":"uid-1"},"clusterID":"cluster-1","rules":[]}`

func postIdentity(router *mux.Router, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, PathIdentities, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer provider-token")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestCreateAttestsBeforeMinting(t *testing.T) {
	service := &stubIdentityService{}
	attestor := &stubAttestor{}
	recorder := postIdentity(identityRouter(service, attestor), identityBody)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if attestor.provider != "agents" {
		t.Fatalf("attested provider = %q; the owner's provider is what must be proven", attestor.provider)
	}
	if service.mode != tenancyv1alpha1.ScopedIdentityAttestationProvider {
		t.Fatalf("attestation mode = %q", service.mode)
	}
	if service.subject != providers.ProviderSAUsername {
		t.Fatalf("subject = %q, want the reviewed provider service account", service.subject)
	}
	var response identityResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Token != "minted" || response.TokenType != "Bearer" || response.Name != "si-x" {
		t.Fatalf("response = %#v", response)
	}
}

func TestCreatePassesOwnerRevisionAndAcknowledgesEnforcement(t *testing.T) {
	service := &stubIdentityService{revisionVerified: true}
	body := strings.TrimSuffix(identityBody, "}") + `,"expectedOwnerGeneration":7,"expectedOwnerResourceVersion":"opaque-rv"}`
	recorder := postIdentity(identityRouter(service, &stubAttestor{}), body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if service.request.ExpectedOwnerGeneration != 7 || service.request.ExpectedOwnerResourceVersion != "opaque-rv" {
		t.Fatalf("service request owner revision = (%d, %q), want (7, opaque-rv)", service.request.ExpectedOwnerGeneration, service.request.ExpectedOwnerResourceVersion)
	}
	var response identityResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !response.OwnerRevisionVerified {
		t.Fatalf("response did not acknowledge owner revision enforcement: %s", recorder.Body.String())
	}
}

func TestCreateReturnsPermanentOwnerRevisionConflicts(t *testing.T) {
	for _, code := range []string{identity.CodeStaleOwner, identity.CodeVersionConflict} {
		t.Run(code, func(t *testing.T) {
			service := &stubIdentityService{ensureErr: identity.RevisionConflict{Code: code, Reason: "private detail"}}
			recorder := postIdentity(identityRouter(service, &stubAttestor{}), identityBody)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%s", recorder.Code, recorder.Body.String())
			}
			var body struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Code != code {
				t.Fatalf("code = %q, want %q", body.Code, code)
			}
			if strings.Contains(body.Message, "private detail") {
				t.Fatalf("response exposed internal revision detail: %q", body.Message)
			}
		})
	}
}

func TestCreateRejectsIncompleteOwnerRevisionAsBadRequest(t *testing.T) {
	service := &stubIdentityService{ensureErr: identity.Refusal{Code: identity.CodeInvalidRequest, Reason: "expected owner revision fields must be paired"}}
	body := strings.TrimSuffix(identityBody, "}") + `,"expectedOwnerGeneration":7}`
	recorder := postIdentity(identityRouter(service, &stubAttestor{}), body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	var bodyValue struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &bodyValue); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if bodyValue.Code != identity.CodeInvalidRequest {
		t.Fatalf("code = %q, want %q", bodyValue.Code, identity.CodeInvalidRequest)
	}
}

func TestCreateRefusesEachAttestationFailureWithoutLeakingState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"no bearer", providers.ErrHeartbeatNoBearer, http.StatusUnauthorized, "unauthenticated"},
		{"token rejected", providers.ErrHeartbeatTokenRejected, http.StatusUnauthorized, "unauthenticated"},
		{"wrong identity", fmt.Errorf("%w: authenticated as %q", providers.ErrHeartbeatWrongIdentity, "system:serviceaccount:default:other"), http.StatusForbidden, "wrong_identity"},
		{"verification unavailable", fmt.Errorf("%w: no cluster known", providers.ErrHeartbeatAuthUnavailable), http.StatusServiceUnavailable, "attestation_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &stubIdentityService{}
			recorder := postIdentity(identityRouter(service, &stubAttestor{err: tc.err}), identityBody)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			if service.ensured != 0 {
				t.Fatal("an unattested request reached the identity service")
			}
			var body struct{ Code, Message string }
			_ = json.Unmarshal(recorder.Body.Bytes(), &body)
			if body.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", body.Code, tc.wantCode)
			}
			// The authenticator's own errors name logical clusters and
			// expected usernames. This endpoint has no auth middleware in
			// front of it, so none of that may reach the wire.
			if strings.Contains(body.Message, "serviceaccount") || strings.Contains(body.Message, "cluster known") {
				t.Fatalf("refusal leaked deployment detail: %q", body.Message)
			}
		})
	}
}

func TestCreateReturnsAPolicyRefusalVerbatim(t *testing.T) {
	service := &stubIdentityService{ensureErr: identity.Refusal{
		Code: identity.CodeForeignWrite, Reason: "only get is minted on infrastructure's resources",
	}}
	recorder := postIdentity(identityRouter(service, &stubAttestor{}), identityBody)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	var body struct{ Code, Message string }
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	// A provider debugging its own request needs the offending rule's reason;
	// it describes the policy, never tenant state.
	if body.Code != identity.CodeForeignWrite || !strings.Contains(body.Message, "only get is minted") {
		t.Fatalf("refusal = %#v", body)
	}
}

func TestCreateRejectsUnknownFields(t *testing.T) {
	service := &stubIdentityService{}
	recorder := postIdentity(identityRouter(service, &stubAttestor{}),
		`{"owner":{"provider":"agents","kind":"Agent","version":"v1alpha1","resource":"agents","name":"a"},"audience":"smuggled"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if service.ensured != 0 {
		t.Fatal("a request with unknown fields reached the identity service")
	}
}

func TestDeleteIsAttestedAndScopedToTheCallingProvider(t *testing.T) {
	service := &stubIdentityService{}
	router := identityRouter(service, &stubAttestor{})

	request := httptest.NewRequest(http.MethodDelete, PathIdentities+"/si-x?provider=agents", nil)
	request.Header.Set("Authorization", "Bearer provider-token")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(service.released) != 1 || service.released[0] != "agents/si-x" {
		t.Fatalf("released = %v", service.released)
	}

	unattested := identityRouter(&stubIdentityService{}, &stubAttestor{err: providers.ErrHeartbeatTokenRejected})
	recorder = httptest.NewRecorder()
	unattested.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, PathIdentities+"/si-x?provider=agents", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unattested delete status = %d, want 401", recorder.Code)
	}
}
