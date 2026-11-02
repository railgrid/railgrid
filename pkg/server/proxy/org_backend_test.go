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

package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"k8s.io/klog/v2"
)

func TestOrgBackendTokenIsInterceptedBeforeNativeKCPAuth(t *testing.T) {
	called := false
	p := &KCPProxy{logger: klog.Background()}
	p.SetOrgBackendHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if got := r.Header.Get("Authorization"); got != "Bearer rgbyo1.invalid" {
			t.Errorf("broker Authorization = %q", got)
		}
		if got := r.Header.Get("Sec-WebSocket-Protocol"); got != "" {
			t.Errorf("broker request retained websocket protocol %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://hub/clusters/cluster-id/api/v1/secrets", nil)
	request.Header.Set("Authorization", "Bearer rgbyo1.invalid")
	request.Header.Set("Sec-WebSocket-Protocol", "base64url.bearer.authorization.k8s.io.dG9rZW4")
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || !called {
		t.Fatalf("broker status = %d called=%v; want 204 and handler call", recorder.Code, called)
	}
}

func TestInvalidOrgBackendTokenNeverFallsThrough(t *testing.T) {
	p := &KCPProxy{logger: klog.Background()}
	p.SetOrgBackendHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid broker token", http.StatusUnauthorized)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://hub/clusters/cluster-id/api/v1/secrets", nil)
	request.Header.Set("Authorization", "Bearer rgbyo1.invalid")
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want terminal broker 401", recorder.Code)
	}
}

func TestRetiredDelegatedServiceAccountClassIsDeniedAtHubProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	token := serviceAccountJWT(t, "system:serviceaccount:default:railgrid-du-old", "consumer-cluster")

	p := &KCPProxy{kcpTarget: target, passthroughTransport: upstream.Client().Transport, logger: klog.Background()}
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, serviceAccountRequest(token))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}

	virtualRequest := httptest.NewRequest(http.MethodGet, "http://hub/services/apiexport/root:railgrid:providers/foo/clusters/*/api", nil)
	virtualRequest.Header.Set("Authorization", "Bearer "+token)
	virtualRecorder := httptest.NewRecorder()
	p.ServeHTTP(virtualRecorder, virtualRequest)
	if virtualRecorder.Code != http.StatusForbidden {
		t.Fatalf("virtual workspace status = %d, want 403", virtualRecorder.Code)
	}
}

func TestNewPlatformDelegatedServiceAccountClassRemainsNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	token := serviceAccountJWT(t, "system:serviceaccount:default:railgrid-pdu-current", "consumer-cluster")
	p := &KCPProxy{kcpTarget: target, passthroughTransport: upstream.Client().Transport, logger: klog.Background()}
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, serviceAccountRequest(token))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", recorder.Code, recorder.Body.String())
	}
}

func serviceAccountRequest(token string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://hub/clusters/consumer-cluster/api/v1/namespaces", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

func serviceAccountJWT(t *testing.T, subject, cluster string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"sub": subject,
		"kubernetes.io/serviceaccount/clusterName": cluster,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("header.%s.signature", base64.RawURLEncoding.EncodeToString(payload))
}
