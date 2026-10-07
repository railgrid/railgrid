/*
Copyright 2026 The Railgrid Authors.

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

package api

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"k8s.io/client-go/rest"
)

type projectIdentityTransportCallers struct {
	*testCallers
	providerConfig *rest.Config
}

func (c *projectIdentityTransportCallers) ProviderRESTConfig(target string) (*rest.Config, error) {
	config := rest.CopyConfig(c.providerConfig)
	config.Host = target
	return config, nil
}

func newProjectIdentityTransportCallers(target string, caBundle []byte) *projectIdentityTransportCallers {
	return &projectIdentityTransportCallers{
		testCallers: newTestCallers(nil, target),
		providerConfig: &rest.Config{
			BearerToken:     "provider-token",
			BearerTokenFile: "/not-read/provider-token",
			Username:        "provider-user",
			Password:        "provider-password",
			Impersonate:     rest.ImpersonationConfig{UserName: "provider-impersonation"},
			TLSClientConfig: rest.TLSClientConfig{
				Insecure: true,
				CAData:   caBundle,
				CertData: []byte("provider client certificate must be discarded"),
				KeyData:  []byte("provider client key must be discarded"),
			},
		},
	}
}

func TestProjectActionUsesVerifiedInternalHubTrustOnly(t *testing.T) {
	var calls atomic.Int32
	ref := &aiv1alpha1.ProjectProviderResourceReference{Name: "item", APIVersion: "example/v1", Kind: "Item", Resource: "items"}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer project-workload-token" {
			t.Errorf("Authorization = %q, want only the Project identity", got)
		}
		for _, header := range []string{"Impersonate-User", "X-Remote-User", "X-Railgrid-User", "X-Railgrid-Tenant"} {
			if got := r.Header.Get(header); got != "" {
				t.Errorf("%s = %q, want no provider or human identity", header, got)
			}
		}
		if r.TLS != nil && len(r.TLS.PeerCertificates) != 0 {
			t.Errorf("provider client certificate was presented: %d peer certificates", len(r.TLS.PeerCertificates))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(projectProviderActionEnvelope{
			Provider: "other", Action: "lookup", ActionVersion: "v1", ResourceRef: ref, Result: json.RawMessage(`{"ok":true}`),
		})
	}))
	defer upstream.Close()

	wrongPublicOrigin := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer wrongPublicOrigin.Close()
	wrongPublicCA := testServerCertPEM(t, wrongPublicOrigin)
	internalCA := testServerCertPEM(t, upstream)

	// The public action-runtime CA must not authorize a different internal
	// hub certificate. The provider kubeconfig's insecure flag is ignored.
	callers := newProjectIdentityTransportCallers(upstream.URL, nil)
	s := &Server{
		callers: callers, hubBase: upstream.URL,
		actionsExternalURL:      "https://public.example",
		actionsCABundle:         string(wrongPublicCA),
		projectIdentityTokenFor: testProjectIdentityToken,
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Authorization", "Bearer human-caller-token")
	status, _, err := s.forwardProjectProviderAction(request, identity{clusterID: "cluster-a"}, projectIntegrationTestOwner(), "other", "lookup", "v1", testProjectActionSchemaDigest, ref, json.RawMessage(`{}`))
	if err == nil || status != http.StatusBadGateway {
		t.Fatalf("forward with only the public-origin CA = status %d, err %v; want verified TLS failure", status, err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("untrusted internal hub received %d requests", got)
	}

	// Existing provider CA trust is added to system roots, and the same
	// provider kubeconfig still cannot contribute its bearer, client cert, or
	// impersonation settings.
	s.callers = newProjectIdentityTransportCallers(upstream.URL, internalCA)
	status, envelope, err := s.forwardProjectProviderAction(request, identity{clusterID: "cluster-a"}, projectIntegrationTestOwner(), "other", "lookup", "v1", testProjectActionSchemaDigest, ref, json.RawMessage(`{}`))
	if err != nil || status != http.StatusOK || envelope.Error != nil {
		t.Fatalf("forward with the internal hub CA = status %d envelope %#v err %v", status, envelope, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("trusted internal hub received %d requests, want one", got)
	}
}

func TestProjectActionUsesProviderCAAndRejectsUntrustedHub(t *testing.T) {
	ref := &aiv1alpha1.ProjectProviderResourceReference{Name: "item", APIVersion: "example/v1", Kind: "Item", Resource: "items"}
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer project-workload-token" {
			t.Errorf("Authorization = %q, want Project identity", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(projectProviderActionEnvelope{
			Provider: "other", Action: "lookup", ActionVersion: "v1", ResourceRef: ref, Result: json.RawMessage(`{"ok":true}`),
		})
	}))
	defer upstream.Close()

	serverCA := testServerCertPEM(t, upstream)
	callers := newProjectIdentityTransportCallers(upstream.URL, serverCA)
	s := &Server{callers: callers, hubBase: upstream.URL, projectIdentityTokenFor: testProjectIdentityToken}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	status, _, err := s.forwardProjectProviderAction(request, identity{clusterID: "cluster-a"}, projectIntegrationTestOwner(), "other", "lookup", "v1", testProjectActionSchemaDigest, ref, json.RawMessage(`{}`))
	if err != nil || status != http.StatusOK {
		t.Fatalf("forward with provider kubeconfig CA = status %d, err %v", status, err)
	}

	// The same kubeconfig has insecure=true but no CA. Do not honor that flag,
	// and do not let an unrelated public-origin bundle create trust.
	callers = newProjectIdentityTransportCallers(upstream.URL, nil)
	s.callers = callers
	wrongPublicOrigin := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	s.actionsCABundle = string(testServerCertPEM(t, wrongPublicOrigin))
	wrongPublicOrigin.Close()
	status, _, err = s.forwardProjectProviderAction(request, identity{clusterID: "cluster-a"}, projectIntegrationTestOwner(), "other", "lookup", "v1", testProjectActionSchemaDigest, ref, json.RawMessage(`{}`))
	if err == nil || status != http.StatusBadGateway {
		t.Fatalf("forward with an untrusted hub and insecure provider config = status %d err %v, want verified TLS failure", status, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream received %d requests, want only the trusted request", got)
	}
}

func TestProjectNamedReadUsesVerifiedInternalHubTrustAndProjectBearer(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer project-workload-token" {
			t.Errorf("Authorization = %q, want Project identity", got)
		}
		for _, header := range []string{"Impersonate-User", "X-Remote-User", "X-Railgrid-User"} {
			if got := r.Header.Get(header); got != "" {
				t.Errorf("%s = %q, want no provider or human identity", header, got)
			}
		}
		if r.TLS != nil && len(r.TLS.PeerCertificates) != 0 {
			t.Errorf("provider client certificate was presented: %d peer certificates", len(r.TLS.PeerCertificates))
		}
		if got, want := r.URL.Path, "/clusters/cluster-a/apis/example/v1/items/item"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"example/v1","kind":"Item","metadata":{"name":"item"}}`))
	}))
	defer upstream.Close()

	callers := newProjectIdentityTransportCallers(upstream.URL, testServerCertPEM(t, upstream))
	s := &Server{
		callers: callers, hubBase: upstream.URL,
		projectIdentityTokenFor: testProjectIdentityToken,
	}
	ref := &aiv1alpha1.ProjectProviderResourceReference{Name: "item", APIVersion: "example/v1", Kind: "Item", Resource: "items"}
	object, err := s.readProjectProviderReference(context.Background(), identity{clusterID: "cluster-a"}, projectIntegrationTestOwner(), ref)
	if err != nil {
		t.Fatalf("read Project provider reference over the verified internal hub: %v", err)
	}
	if got := object.GetName(); got != "item" {
		t.Fatalf("read object name = %q, want item", got)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream received %d requests, want one", got)
	}
}

func TestProjectNamedReadRejectsRedirect(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("redirect target received Authorization %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	callers := newProjectIdentityTransportCallers(redirect.URL, nil)
	s := &Server{
		callers: callers, hubBase: redirect.URL,
		projectIdentityTokenFor: testProjectIdentityToken,
	}
	ref := &aiv1alpha1.ProjectProviderResourceReference{Name: "item", APIVersion: "example/v1", Kind: "Item", Resource: "items"}
	if _, err := s.readProjectProviderReference(context.Background(), identity{clusterID: "cluster-a"}, projectIntegrationTestOwner(), ref); err == nil {
		t.Fatal("Project named read followed an HTTP redirect")
	}
	if got := targetCalls.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests", got)
	}
}

func testServerCertPEM(t *testing.T, server *httptest.Server) []byte {
	t.Helper()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("TLS test server has no certificate")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
}

func TestAnonymousProjectRESTConfigDropsProviderIdentity(t *testing.T) {
	callers := newProjectIdentityTransportCallers("https://hub.example", nil)
	server := &Server{callers: callers}
	config, err := server.anonymousProjectRESTConfig("https://hub.example/clusters/cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	if config.Insecure {
		t.Fatal("provider kubeconfig insecure-skip-tls-verify was retained")
	}
	if config.BearerToken != "" || config.BearerTokenFile != "" || config.Username != "" || config.Password != "" {
		t.Fatal("provider authentication settings were retained")
	}
	if config.Impersonate.UserName != "" || len(config.CertData) != 0 || len(config.KeyData) != 0 {
		t.Fatal("provider impersonation or client certificate settings were retained")
	}
	if strings.Contains(config.Host, "provider-token") {
		t.Fatal("provider bearer was copied into the REST endpoint")
	}
}

func TestProjectProviderActionTransportRequiresVerifiedTLSSettings(t *testing.T) {
	if _, err := projectProviderActionTransport(nil, false); err == nil {
		t.Fatal("nil provider config produced an action transport")
	}
	_, err := projectProviderActionTransport(&rest.Config{TLSClientConfig: rest.TLSClientConfig{Insecure: true, CAData: []byte("not a PEM bundle")}}, false)
	if err == nil {
		t.Fatal("invalid provider CA bundle was accepted")
	}
}

func TestProjectHubRequestsHonorExplicitDevelopmentTLSOption(t *testing.T) {
	for _, route := range []string{"catalog", "action", "read"} {
		for _, insecure := range []bool{false, true} {
			name := route + "/verified-default"
			if insecure {
				name = route + "/development-opt-in"
			}
			t.Run(name, func(t *testing.T) {
				var calls atomic.Int32
				ref := &aiv1alpha1.ProjectProviderResourceReference{Name: "item", APIVersion: "example/v1", Kind: "Item", Resource: "items"}
				upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Header.Get("Authorization") != "Bearer project-workload-token" {
						t.Error("request did not use the Project token")
					}
					for _, header := range []string{"Impersonate-User", "X-Remote-User", "X-Railgrid-User"} {
						if r.Header.Get(header) != "" {
							t.Errorf("request leaked identity header %s", header)
						}
					}
					if len(r.TLS.PeerCertificates) != 0 {
						t.Error("request presented provider client credentials")
					}
					w.Header().Set("Content-Type", "application/json")
					switch route {
					case "catalog":
						_, _ = w.Write([]byte(`{"items":[]}`))
					case "action":
						_ = json.NewEncoder(w).Encode(projectProviderActionEnvelope{Provider: "other", Action: "lookup", ActionVersion: "v1", ResourceRef: ref, Result: json.RawMessage(`{}`)})
					case "read":
						_, _ = w.Write([]byte(`{"apiVersion":"example/v1","kind":"Item","metadata":{"name":"item"}}`))
					}
				}))
				defer upstream.Close()
				s := &Server{hubBase: upstream.URL, callers: newProjectIdentityTransportCallers(upstream.URL, nil), mcpInsecureSkipTLSVerify: insecure, projectIdentityTokenFor: testProjectIdentityToken}
				id := identity{clusterID: "cluster-a", tenant: "cluster-a", orgUUID: "org-a", workspaceUUID: "workspace-a"}
				var err error
				switch route {
				case "catalog":
					_, err = s.fetchProviderCatalogForProject(context.Background(), id, projectIntegrationTestOwner())
				case "action":
					_, _, err = s.forwardProjectProviderAction(httptest.NewRequest(http.MethodPost, "/", nil), id, projectIntegrationTestOwner(), "other", "lookup", "v1", testProjectActionSchemaDigest, ref, json.RawMessage(`{}`))
				case "read":
					_, err = s.readProjectProviderReference(context.Background(), id, projectIntegrationTestOwner(), ref)
				}
				if insecure && (err != nil || calls.Load() != 1) {
					t.Fatalf("explicit development TLS option failed: calls=%d err=%v", calls.Load(), err)
				}
				if !insecure && (err == nil || calls.Load() != 0) {
					t.Fatalf("default trusted an untrusted certificate: calls=%d err=%v", calls.Load(), err)
				}
			})
		}
	}
}
