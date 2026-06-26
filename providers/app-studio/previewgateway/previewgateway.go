/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package previewgateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/faroshq/provider-app-studio/previewtoken"
)

const accessModePrivate = "private"

var strippedHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Cookie",
	"X-Forwarded-Authorization",
	"X-Forwarded-Access-Token",
	"X-Kedge-User",
	"X-Kedge-Tenant",
	"X-Kedge-Org",
	"X-Kedge-Workspace",
	"X-Sandbox-Control-Token",
}

type Handler struct {
	k8sClient kubernetes.Interface
	signer    *previewtoken.Signer
	transport http.RoundTripper
}

type Option func(*Handler)

func WithRoundTripper(transport http.RoundTripper) Option {
	return func(h *Handler) {
		h.transport = transport
	}
}

func New(client kubernetes.Interface, tokenSecret []byte, opts ...Option) (http.Handler, error) {
	if client == nil {
		return nil, fmt.Errorf("kubernetes client is required")
	}
	if len(tokenSecret) == 0 {
		return nil, fmt.Errorf("preview token secret is required")
	}
	h := &Handler{
		k8sClient: client,
		signer:    previewtoken.NewSigner(tokenSecret),
		transport: http.DefaultTransport,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

func NewFromConfig(cfg *rest.Config, tokenSecret []byte, opts ...Option) (http.Handler, error) {
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}
	return New(client, tokenSecret, opts...)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	token, useRedirect, err := h.authorize(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if useRedirect {
		h.redirectWithCookie(w, r, token)
		return
	}
	if err := h.proxy(w, r, token); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

func (h *Handler) authorize(r *http.Request) (previewtoken.Payload, bool, error) {
	queryToken := strings.TrimSpace(r.URL.Query().Get(previewtoken.QueryParam))
	if queryToken != "" {
		payload, err := h.parseToken(queryToken, r.Host)
		if err != nil {
			return previewtoken.Payload{}, false, err
		}
		return payload, true, nil
	}

	cookieName := previewtoken.CookieNameForHost(r.Host)
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		if errors.Is(err, http.ErrNoCookie) {
			return previewtoken.Payload{}, false, errors.New("missing preview token")
		}
		return previewtoken.Payload{}, false, fmt.Errorf("read preview token cookie: %w", err)
	}
	payload, err := h.parseToken(cookie.Value, r.Host)
	if err != nil {
		return previewtoken.Payload{}, false, err
	}
	return payload, false, nil
}

func (h *Handler) parseToken(token, requestHost string) (previewtoken.Payload, error) {
	payload, err := h.signer.Verify(token)
	if err != nil {
		return previewtoken.Payload{}, fmt.Errorf("invalid preview token: %w", err)
	}
	if previewtoken.NormalizeHost(payload.Host) != previewtoken.NormalizeHost(requestHost) {
		return previewtoken.Payload{}, errors.New("preview token host mismatch")
	}
	if strings.TrimSpace(strings.ToLower(payload.AccessMode)) != accessModePrivate {
		return previewtoken.Payload{}, errors.New("preview token access mode is not private")
	}
	return payload, nil
}

func (h *Handler) redirectWithCookie(w http.ResponseWriter, r *http.Request, payload previewtoken.Payload) {
	cookieName := previewtoken.CookieNameForHost(r.Host)
	token := r.URL.Query().Get(previewtoken.QueryParam)
	expiresAt := time.Unix(payload.ExpiresAt, 0).UTC()
	maxAge := int(time.Until(expiresAt).Seconds())
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		Expires:  expiresAt,
		MaxAge:   maxAge,
	})

	query := r.URL.Query()
	query.Del(previewtoken.QueryParam)
	cleanURL := *r.URL
	cleanURL.RawQuery = query.Encode()
	http.Redirect(w, r, cleanURL.String(), http.StatusFound)
}

func (h *Handler) proxy(w http.ResponseWriter, r *http.Request, payload previewtoken.Payload) error {
	svc, err := h.k8sClient.CoreV1().Services(payload.RuntimeNamespace).Get(r.Context(), payload.PreviewServiceName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("preview target service not found")
		}
		return fmt.Errorf("lookup preview target service: %w", err)
	}
	port := findServicePort(svc, payload.PreviewPortName)
	if port == 0 {
		return fmt.Errorf("preview target service port %q not found", payload.PreviewPortName)
	}

	target, _ := url.Parse(fmt.Sprintf("http://%s.%s.svc:%d", payload.PreviewServiceName, payload.RuntimeNamespace, port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = h.transport
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		q := req.URL.Query()
		q.Del(previewtoken.QueryParam)
		req.URL.RawQuery = q.Encode()
		for _, header := range strippedHeaders {
			req.Header.Del(header)
		}
	}
	proxy.ServeHTTP(w, r)
	return nil
}

func findServicePort(svc *corev1.Service, name string) int32 {
	if svc == nil || strings.TrimSpace(name) == "" {
		return 0
	}
	for _, port := range svc.Spec.Ports {
		if port.Name == strings.TrimSpace(name) {
			return port.Port
		}
	}
	return 0
}

func respondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
