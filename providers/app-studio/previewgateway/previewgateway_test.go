/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package previewgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/faroshq/provider-app-studio/previewtoken"
)

func TestPreviewGatewayHealthzIsUnauthenticated(t *testing.T) {
	t.Parallel()

	h := newGatewayHandler(t, nil, "test-secret")

	req := httptest.NewRequest("GET", "http://preview.example.com/healthz", nil)
	req.Host = "preview.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	defer closeResponseBody(t, res.Body)
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Fatalf("GET /healthz status = %d, want %d", got, want)
	}
	var payload map[string]string
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode /healthz response: %v", err)
	}
	if got, want := payload["status"], "ok"; got != want {
		t.Fatalf("/healthz status value = %q, want %q", got, want)
	}
}

func TestPreviewGatewayRequiresAuthForNonHealthz(t *testing.T) {
	t.Parallel()

	h := newGatewayHandler(t, nil, "test-secret")
	req := httptest.NewRequest("GET", "http://preview.example.com/", nil)
	req.Host = "preview.example.com"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if got, want := rec.Result().StatusCode, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestPreviewGatewayQueryTokenWritesCookieAndRedirects(t *testing.T) {
	t.Parallel()

	const secret = "test-secret"
	payload := tokenPayload("preview.example.com", "runtime-ns", "runtime-svc", "preview", "private")
	token := signToken(t, secret, payload)

	h := newGatewayHandler(t, nil, secret)
	req := httptest.NewRequest("GET", "http://preview.example.com/path/to/app?foo=bar&kedgePreviewToken="+url.QueryEscape(token)+"&baz=qux", nil)
	req.Host = "preview.example.com"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer closeResponseBody(t, res.Body)
	if got, want := res.StatusCode, http.StatusFound; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	loc := res.Header.Get("Location")
	if loc == "" {
		t.Fatal("Location header missing on redirect")
	}
	redirectURL, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location %q: %v", loc, err)
	}
	if got, want := redirectURL.Path, "/path/to/app"; got != want {
		t.Fatalf("redirect path = %q, want %q", got, want)
	}
	if _, ok := redirectURL.Query()[previewtoken.QueryParam]; ok {
		t.Fatalf("redirect URL still contains %q query param", previewtoken.QueryParam)
	}

	cookies := res.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("set cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if got, want := cookie.Name, previewtoken.CookieNameForHost("preview.example.com"); got != want {
		t.Fatalf("cookie name = %q, want %q", got, want)
	}
	if got, want := cookie.Value, token; got != want {
		t.Fatalf("cookie value = %q, want %q", got, want)
	}
	if !cookie.HttpOnly || !cookie.Secure {
		t.Fatalf("cookie must be HttpOnly and Secure, got %+v", cookie)
	}
	if got, want := cookie.SameSite, http.SameSiteNoneMode; got != want {
		t.Fatalf("cookie SameSite = %v, want %v", got, want)
	}
	if got, want := int64(cookie.Expires.Unix()), payload.ExpiresAt; got != want {
		t.Fatalf("cookie expiry = %d, want %d", got, want)
	}
}

func TestPreviewGatewayRejectsWrongHostToken(t *testing.T) {
	t.Parallel()

	const secret = "test-secret"
	payload := tokenPayload("preview.example.com", "runtime-ns", "runtime-svc", "preview", "private")
	token := signToken(t, secret, payload)

	h := newGatewayHandler(t, nil, secret)
	req := httptest.NewRequest("GET", "http://wrong.example.com/?"+previewtoken.QueryParam+"="+url.QueryEscape(token), nil)
	req.Host = "wrong.example.com"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if got, want := rec.Result().StatusCode, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestPreviewGatewayRejectsMalformedToken(t *testing.T) {
	t.Parallel()

	h := newGatewayHandler(t, nil, "test-secret")
	req := httptest.NewRequest("GET", "http://preview.example.com/?"+previewtoken.QueryParam+"=not-a-token", nil)
	req.Host = "preview.example.com"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if got, want := rec.Result().StatusCode, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestPreviewGatewayRejectsExpiredToken(t *testing.T) {
	t.Parallel()

	const secret = "test-secret"
	payload := tokenPayload("preview.example.com", "runtime-ns", "runtime-svc", "preview", "private")
	payload.ExpiresAt = time.Now().Add(-time.Minute).UTC().Unix()
	token := signToken(t, secret, payload)

	h := newGatewayHandler(t, nil, secret)
	req := httptest.NewRequest("GET", "http://preview.example.com/?"+previewtoken.QueryParam+"="+url.QueryEscape(token), nil)
	req.Host = "preview.example.com"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if got, want := rec.Result().StatusCode, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestPreviewGatewayRejectsMissingServiceOrNamedPort(t *testing.T) {
	t.Parallel()

	const secret = "test-secret"
	signer := previewtoken.NewSigner([]byte(secret))
	missingPortPayload := tokenPayload("preview.example.com", "runtime-ns", "runtime-svc", "missing", "private")
	missingServicePayload := tokenPayload("preview.example.com", "runtime-ns", "missing-svc", "preview", "private")

	tokenMissingPort := signPayload(t, signer, missingPortPayload)
	tokenMissingService := signPayload(t, signer, missingServicePayload)

	h := newGatewayHandler(t, []runtime.Object{
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "runtime-svc",
				Namespace: "runtime-ns",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{Name: "preview", Port: 8080}},
			},
		},
	}, secret)

	for name, token := range map[string]string{
		"missing-service": tokenMissingService,
		"missing-port":    tokenMissingPort,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://preview.example.com/", nil)
			req.Host = "preview.example.com"
			req.AddCookie(&http.Cookie{
				Name:  previewtoken.CookieNameForHost("preview.example.com"),
				Value: token,
			})
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)
			if got, want := rec.Result().StatusCode, http.StatusBadGateway; got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
		})
	}
}

func TestPreviewGatewayStripsForwardedCredentialsAndPreviewTokenQuery(t *testing.T) {
	t.Parallel()

	const secret = "test-secret"
	payload := tokenPayload("preview.example.com", "runtime-ns", "runtime-svc", "preview", "private")
	token := signToken(t, secret, payload)
	transport := &captureRoundTripper{}

	h := newGatewayHandler(t, []runtime.Object{
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "runtime-svc",
				Namespace: "runtime-ns",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{Name: "preview", Port: 8080}},
			},
		},
	}, secret, WithRoundTripper(transport))

	req := httptest.NewRequest("GET", "http://preview.example.com/path?hello=world&kedgePreviewToken=", nil)
	req.Host = "preview.example.com"
	req.AddCookie(&http.Cookie{
		Name:  previewtoken.CookieNameForHost("preview.example.com"),
		Value: token,
	})
	for _, header := range strippedHeaders {
		if header == "Cookie" {
			continue
		}
		req.Header.Set(header, "value")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer closeResponseBody(t, res.Body)
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got := len(transport.requests); got != 1 {
		t.Fatalf("upstream request count = %d, want 1", got)
	}
	upstreamReq := transport.requests[0]
	if got, want := upstreamReq.URL.Scheme, "http"; got != want {
		t.Fatalf("upstream scheme = %q, want %q", got, want)
	}
	if got, want := upstreamReq.URL.Host, "runtime-svc.runtime-ns.svc:8080"; got != want {
		t.Fatalf("upstream host = %q, want %q", got, want)
	}
	if got, want := upstreamReq.URL.Path, "/path"; got != want {
		t.Fatalf("upstream path = %q, want %q", got, want)
	}
	if got := upstreamReq.URL.Query().Get("hello"); got != "world" {
		t.Fatalf("upstream query hello = %q, want %q", got, "world")
	}
	if got := upstreamReq.URL.Query().Get(previewtoken.QueryParam); got != "" {
		t.Fatalf("upstream query %s = %q, want empty", previewtoken.QueryParam, got)
	}
	for _, header := range strippedHeaders {
		if _, ok := upstreamReq.Header[header]; ok {
			t.Fatalf("upstream contains stripped header %q", header)
		}
	}
}

func TestPreviewGatewayRejectsNonPrivateAccessMode(t *testing.T) {
	t.Parallel()

	const secret = "test-secret"
	payload := tokenPayload("preview.example.com", "runtime-ns", "runtime-svc", "preview", "public")
	token := signToken(t, secret, payload)

	h := newGatewayHandler(t, []runtime.Object{
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "runtime-svc",
				Namespace: "runtime-ns",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{Name: "preview", Port: 8080}},
			},
		},
	}, secret)

	req := httptest.NewRequest("GET", "http://preview.example.com/", nil)
	req.Host = "preview.example.com"
	req.AddCookie(&http.Cookie{
		Name:  previewtoken.CookieNameForHost("preview.example.com"),
		Value: token,
	})
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if got, want := rec.Result().StatusCode, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func newGatewayHandler(t *testing.T, objects []runtime.Object, secret string, opts ...Option) http.Handler {
	t.Helper()
	client := kubernetesfake.NewSimpleClientset(objects...)
	h, err := New(client, []byte(secret), opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func tokenPayload(host, namespace, svc, portName, accessMode string) previewtoken.Payload {
	return previewtoken.Payload{
		ProjectName:        "todo",
		TenantPath:         "root:kedge:ws",
		ResourceName:       "runtime-svc",
		Host:               host,
		RuntimeNamespace:   namespace,
		PreviewServiceName: svc,
		PreviewPortName:    portName,
		AccessMode:         accessMode,
		ExpiresAt:          time.Now().Add(time.Hour).UTC().Unix(),
	}
}

func signToken(t *testing.T, secret string, payload previewtoken.Payload) string {
	t.Helper()
	token, _, err := previewtoken.NewSigner([]byte(secret)).Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return token
}

func signPayload(t *testing.T, signer *previewtoken.Signer, payload previewtoken.Payload) string {
	t.Helper()
	token, _, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return token
}

type captureRoundTripper struct {
	requests []*http.Request
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req.Clone(context.Background()))
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("ok")),
	}, nil
}

func closeResponseBody(t *testing.T, body io.ReadCloser) {
	if err := body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}
