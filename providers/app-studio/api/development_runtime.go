/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

const (
	previewTokenQuery        = "kedgePreviewToken"
	previewTokenTTL          = time.Hour
	previewRewriteBodyLimit  = 16 << 20
	previewScopePrefix       = "__kedge_preview"
	previewBasePathPlacehold = "__kedge_preview_base__/"
	kcpClusterAnnotation     = "kcp.io/cluster"
)

var errPreviewRewriteBodyTooLarge = errors.New("preview response body is too large to rewrite")

var sandboxRunnerGVR = schema.GroupVersionResource{
	Group:    "infrastructure.kedge.faros.sh",
	Version:  "v1alpha1",
	Resource: "sandboxrunners",
}

type runtimeServiceRef struct {
	Namespace string
	Name      string
	PortName  string
}

type runtimeSecretRef struct {
	Namespace string
	Name      string
}

type runtimeTarget struct {
	Preview       runtimeServiceRef
	Control       runtimeServiceRef
	ControlSecret runtimeSecretRef
}

type previewTokenPayload struct {
	TenantPath         string `json:"tenantPath"`
	Project            string `json:"project"`
	RuntimeNamespace   string `json:"runtimeNamespace"`
	PreviewServiceName string `json:"previewServiceName"`
	PreviewPortName    string `json:"previewPortName"`
	SandboxRunner      string `json:"sandboxRunner"`
	ExpiresAt          int64  `json:"expiresAt"`
}

type previewSigner struct {
	secret []byte
	now    func() time.Time
}

func newPreviewSigner(secret []byte) *previewSigner {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			sum := sha256.Sum256([]byte(time.Now().String()))
			secret = sum[:]
		}
	}
	return &previewSigner{secret: append([]byte(nil), secret...), now: time.Now}
}

func (s *previewSigner) sign(payload previewTokenPayload) (string, time.Time, error) {
	expiresAt := s.now().Add(previewTokenTTL).UTC()
	payload.ExpiresAt = expiresAt.Unix()
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + sig, time.Unix(payload.ExpiresAt, 0).UTC(), nil
}

func (s *previewSigner) verify(token, projectName string) (previewTokenPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return previewTokenPayload{}, fmt.Errorf("invalid preview token")
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(parts[0]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, want) {
		return previewTokenPayload{}, fmt.Errorf("invalid preview token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return previewTokenPayload{}, fmt.Errorf("invalid preview token")
	}
	var payload previewTokenPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return previewTokenPayload{}, fmt.Errorf("invalid preview token")
	}
	if payload.Project != projectName {
		return previewTokenPayload{}, fmt.Errorf("preview token is for a different project")
	}
	if payload.PreviewPortName == "" {
		payload.PreviewPortName = "preview"
	}
	if payload.TenantPath == "" || payload.RuntimeNamespace == "" || payload.PreviewServiceName == "" || payload.SandboxRunner == "" {
		return previewTokenPayload{}, fmt.Errorf("preview token is incomplete")
	}
	if s.now().Unix() > payload.ExpiresAt {
		return previewTokenPayload{}, fmt.Errorf("preview token expired")
	}
	return payload, nil
}

func (s *Server) runtimeTargetForProject(ctx context.Context, c *asclient.Client, name string) (runtimeTarget, *unstructured.Unstructured, error) {
	if c == nil {
		return runtimeTarget{}, nil, fmt.Errorf("project client is not configured")
	}
	if strings.TrimSpace(name) == "" {
		return runtimeTarget{}, nil, fmt.Errorf("sandbox runner name is empty")
	}
	obj, err := c.Dynamic().Resource(sandboxRunnerGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return runtimeTarget{}, nil, err
	}
	target, err := runtimeTargetFromInstance(obj)
	if err != nil {
		return runtimeTarget{}, nil, err
	}
	return target, obj, nil
}

func runtimeTargetFromInstance(obj *unstructured.Unstructured) (runtimeTarget, error) {
	if obj == nil {
		return runtimeTarget{}, fmt.Errorf("sandbox runner is nil")
	}
	name, err := sandboxRunnerInstanceName(obj)
	if err != nil {
		return runtimeTarget{}, err
	}
	runtimeNamespace := name
	if statusNamespace, ok, err := sandboxRunnerStatusRuntimeNamespace(obj, name); err != nil {
		return runtimeTarget{}, err
	} else if ok {
		runtimeNamespace = statusNamespace
	}
	expected := runtimeTarget{
		Preview:       runtimeServiceRef{Namespace: runtimeNamespace, Name: name, PortName: "preview"},
		Control:       runtimeServiceRef{Namespace: runtimeNamespace, Name: name, PortName: "control"},
		ControlSecret: runtimeSecretRef{Namespace: runtimeNamespace, Name: name + "-control"},
	}
	if ref, ok := runtimeServiceRefFromStatus(obj, runtimeNamespace, "preview", "status", "previewServiceRef"); ok && ref != expected.Preview {
		return runtimeTarget{}, fmt.Errorf("sandbox runner previewServiceRef points outside expected runtime service")
	}
	if ref, ok := runtimeServiceRefFromStatus(obj, runtimeNamespace, "control", "status", "controlServiceRef"); ok && ref != expected.Control {
		return runtimeTarget{}, fmt.Errorf("sandbox runner controlServiceRef points outside expected runtime service")
	}
	if ref, ok := runtimeSecretRefFromStatus(obj, runtimeNamespace, "status", "controlSecretRef"); ok && ref != expected.ControlSecret {
		return runtimeTarget{}, fmt.Errorf("sandbox runner controlSecretRef points outside expected runtime secret")
	}
	return expected, nil
}

func sandboxRunnerStatusRuntimeNamespace(obj *unstructured.Unstructured, name string) (string, bool, error) {
	statusNamespace, _, _ := unstructured.NestedString(obj.Object, "status", "runtimeNamespace")
	statusNamespace = strings.TrimSpace(statusNamespace)
	if statusNamespace == "" {
		return "", false, nil
	}
	if statusNamespace != name && statusNamespace != expectedKROPrefixedRuntimeNamespace(obj, name) {
		return "", false, fmt.Errorf("sandbox runner runtime namespace %q does not match expected namespace %q", statusNamespace, name)
	}
	return statusNamespace, true, nil
}

func expectedKROPrefixedRuntimeNamespace(obj *unstructured.Unstructured, name string) string {
	clusterID := strings.TrimSpace(obj.GetAnnotations()[kcpClusterAnnotation])
	if clusterID == "" {
		return ""
	}
	return clusterID + "-" + name
}

func sandboxRunnerInstanceName(obj *unstructured.Unstructured) (string, error) {
	metadataName := strings.TrimSpace(obj.GetName())
	specName, _, _ := unstructured.NestedString(obj.Object, "spec", "name")
	specName = strings.TrimSpace(specName)
	switch {
	case metadataName == "" && specName == "":
		return "", fmt.Errorf("sandbox runner name is empty")
	case metadataName == "":
		return specName, nil
	case specName == "":
		return metadataName, nil
	case metadataName != specName:
		return "", fmt.Errorf("sandbox runner metadata.name %q does not match spec.name %q", metadataName, specName)
	default:
		return metadataName, nil
	}
}

func runtimeServiceRefFromStatus(obj *unstructured.Unstructured, fallbackNamespace, defaultPortName string, fields ...string) (runtimeServiceRef, bool) {
	values, ok := nestedStringMap(obj.Object, fields...)
	if !ok {
		return runtimeServiceRef{}, false
	}
	ref := runtimeServiceRef{
		Namespace: strings.TrimSpace(values["namespace"]),
		Name:      strings.TrimSpace(values["name"]),
		PortName:  strings.TrimSpace(values["portName"]),
	}
	if ref.Namespace == "" {
		ref.Namespace = strings.TrimSpace(fallbackNamespace)
	}
	if ref.PortName == "" {
		ref.PortName = defaultPortName
	}
	return ref, true
}

func runtimeSecretRefFromStatus(obj *unstructured.Unstructured, fallbackNamespace string, fields ...string) (runtimeSecretRef, bool) {
	values, ok := nestedStringMap(obj.Object, fields...)
	if !ok {
		return runtimeSecretRef{}, false
	}
	ref := runtimeSecretRef{
		Namespace: strings.TrimSpace(values["namespace"]),
		Name:      strings.TrimSpace(values["name"]),
	}
	if ref.Namespace == "" {
		ref.Namespace = strings.TrimSpace(fallbackNamespace)
	}
	return ref, true
}

func (s *Server) restartProjectDevelopment(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	target, ok := projectDevelopmentSyncTarget(p, id)
	if !ok {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "project has no sandbox runner binding")
		return
	}
	runtimeTarget, _, err := s.runtimeTargetForProject(r.Context(), c, target.ResourceName)
	if err != nil {
		writeRuntimeTargetError(w, err)
		return
	}
	respBody, status, err := s.postRuntimeService(r.Context(), runtimeTarget, "restart", []byte(`{}`))
	if err != nil {
		writeStatus(w, http.StatusBadGateway, "BadGateway", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}

func (s *Server) logsProjectDevelopment(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	target, ok := projectDevelopmentSyncTarget(p, id)
	if !ok {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "project has no sandbox runner binding")
		return
	}
	runtimeTarget, _, err := s.runtimeTargetForProject(r.Context(), c, target.ResourceName)
	if err != nil {
		writeRuntimeTargetError(w, err)
		return
	}
	if s.runtimeConfig == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "runtime kubeconfig not configured")
		return
	}
	upstream := s.runtimeConfig.Host + runtimeServicePath(runtimeTarget.Control, "logs")
	transport, err := restTransport(s.runtimeConfig)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	token, err := s.runtimeControlToken(r.Context(), runtimeTarget.ControlSecret)
	if err != nil {
		writeStatus(w, http.StatusBadGateway, "BadGateway", err.Error())
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	req.Header.Set("X-Sandbox-Control-Token", token)
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		writeStatus(w, http.StatusBadGateway, "BadGateway", err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) statusProjectDevelopment(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	target, ok := projectDevelopmentSyncTarget(p, id)
	if !ok {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "project has no sandbox runner binding")
		return
	}
	_, obj, err := s.runtimeTargetForProject(r.Context(), c, target.ResourceName)
	if err != nil {
		writeRuntimeTargetError(w, err)
		return
	}
	status, _ := obj.Object["status"].(map[string]any)
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) postRuntimeService(ctx context.Context, target runtimeTarget, op string, body []byte) ([]byte, int, error) {
	if s.runtimeConfig == nil {
		return nil, 0, fmt.Errorf("runtime kubeconfig not configured")
	}
	upstream := s.runtimeConfig.Host + runtimeServicePath(target.Control, op)
	transport, err := restTransport(s.runtimeConfig)
	if err != nil {
		return nil, 0, err
	}
	token, err := s.runtimeControlToken(ctx, target.ControlSecret)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Sandbox-Control-Token", token)
	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("POST runtime service %s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, 0, err
	}
	return raw, resp.StatusCode, nil
}

func (s *Server) runtimeControlToken(ctx context.Context, ref runtimeSecretRef) (string, error) {
	if s.runtimeClient == nil {
		return "", fmt.Errorf("runtime client is not configured")
	}
	secret, err := s.runtimeClient.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	token := string(secret.Data["token"])
	if token == "" {
		return "", fmt.Errorf("runtime control token is empty")
	}
	return token, nil
}

func (s *Server) syncResponseWithPreviewURL(raw []byte, id identity, p *aiv1alpha1.Project, name string, target runtimeTarget) []byte {
	body := map[string]any{}
	if err := json.Unmarshal(raw, &body); err != nil {
		return raw
	}
	if _, ok := body["previewURL"]; !ok {
		previewURL, expiresAt := s.signedProjectPreviewURLAndExpiry(p.Name, id.tenantPath, name, target)
		body["previewURL"] = previewURL
		if expiresAt != "" {
			body["previewTokenExpiresAt"] = expiresAt
		}
	}
	next, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return next
}

func (s *Server) signedProjectPreviewURL(projectName, tenantPath, name string, target runtimeTarget) string {
	previewURL, _ := s.signedProjectPreviewURLAndExpiry(projectName, tenantPath, name, target)
	return previewURL
}

func (s *Server) signedProjectPreviewURLAndExpiry(projectName, tenantPath, name string, target runtimeTarget) (string, string) {
	previewURL := externalProjectPreviewPath(projectName)
	token, expiresAt, err := s.previewSigner.sign(previewTokenPayload{
		TenantPath:         tenantPath,
		Project:            projectName,
		RuntimeNamespace:   target.Preview.Namespace,
		PreviewServiceName: target.Preview.Name,
		PreviewPortName:    target.Preview.PortName,
		SandboxRunner:      name,
	})
	if err != nil {
		return previewURL, ""
	}
	return previewURL + "?" + previewTokenQuery + "=" + url.QueryEscape(token), expiresAt.Format(time.RFC3339)
}

func patchLastSync(ctx context.Context, c *asclient.Client, name string, t metav1.Time) error {
	if c == nil {
		return nil
	}
	obj, err := c.Dynamic().Resource(sandboxRunnerGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := unstructured.SetNestedField(obj.Object, t.Format("2006-01-02T15:04:05Z07:00"), "status", "lastSyncTime"); err != nil {
		return err
	}
	_, err = c.Dynamic().Resource(sandboxRunnerGVR).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
		return nil
	}
	return err
}

func (s *Server) previewReadiness(ctx context.Context, target runtimeTarget) projectSandboxPreviewURLResponse {
	if s.runtimeClient == nil {
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  "runtime_not_configured",
			Message: "Preview is getting ready. The sandbox runtime is still being configured.",
		}
	}
	endpoints, err := s.runtimeClient.CoreV1().Endpoints(target.Preview.Namespace).Get(ctx, target.Preview.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  "service_not_found",
			Message: "Preview is getting ready. The preview service has not been created yet.",
		}
	}
	if err != nil {
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  "service_unavailable",
			Message: "Preview is getting ready. The sandbox runtime is not reachable yet.",
		}
	}
	if !hasReadyEndpoint(endpoints) {
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  "no_ready_endpoints",
			Message: "Preview is getting ready. The sandbox runtime is not serving traffic yet.",
		}
	}
	return projectSandboxPreviewURLResponse{Ready: true}
}

func hasReadyEndpoint(endpoints *corev1.Endpoints) bool {
	if endpoints == nil {
		return false
	}
	for _, subset := range endpoints.Subsets {
		if len(subset.Addresses) > 0 {
			return true
		}
	}
	return false
}

func (s *Server) previewProjectDevelopment(w http.ResponseWriter, r *http.Request) {
	projectName := mux.Vars(r)["project"]
	targetRef, suffix, ok := s.previewProjectTarget(w, r, projectName)
	if !ok {
		return
	}
	if s.runtimeConfig == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "runtime kubeconfig not configured")
		return
	}
	target, err := url.Parse(s.runtimeConfig.Host)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	transport, err := restTransport(s.runtimeConfig)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	scopedBasePath := scopedProjectPreviewBasePath(projectName, r.URL.Path)
	upstreamBasePath := runtimeServicePath(targetRef, "")
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = runtimeServicePath(targetRef, suffix)
		req.URL.RawQuery = previewRuntimeRawQuery(r.URL.Query())
		req.Host = target.Host
		req.Header.Del("Accept-Encoding")
		stripPreviewForwardedCredentials(req.Header)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode == http.StatusServiceUnavailable && previewStatusContentType(resp.Header.Get("Content-Type")) {
			raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			if isPreviewRuntimeStartingStatus(raw) {
				replacePreviewResponse(resp, http.StatusServiceUnavailable, previewRuntimeStartingHTML())
				return nil
			}
			resp.Body = io.NopCloser(bytes.NewReader(raw))
		}
		if scopedBasePath == "" || !previewRewritableContentType(resp.Header.Get("Content-Type")) {
			return nil
		}
		raw, err := readPreviewRewriteBody(resp.Body)
		if err != nil {
			if errors.Is(err, errPreviewRewriteBodyTooLarge) {
				_ = resp.Body.Close()
				replacePreviewResponse(resp, http.StatusBadGateway, previewResponseTooLargeHTML())
				return nil
			}
			return err
		}
		_ = resp.Body.Close()
		next := rewritePreviewResponseBody(resp.Header.Get("Content-Type"), scopedBasePath, raw, upstreamBasePath)
		resp.Body = io.NopCloser(bytes.NewReader(next))
		resp.ContentLength = int64(len(next))
		resp.Header.Set("Content-Length", strconv.Itoa(len(next)))
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Etag")
		return nil
	}
	proxy.ServeHTTP(w, r)
}

func previewStatusContentType(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return mediaType == "application/json" || mediaType == "application/status+json"
}

func isPreviewRuntimeStartingStatus(raw []byte) bool {
	var status struct {
		Kind    string `json:"kind"`
		Status  string `json:"status"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
		Code    int    `json:"code"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return false
	}
	if status.Kind != "Status" || status.Status != "Failure" {
		return false
	}
	if status.Code != http.StatusServiceUnavailable && status.Reason != "ServiceUnavailable" {
		return false
	}
	message := strings.ToLower(status.Message)
	return strings.Contains(message, "error trying to reach service") ||
		strings.Contains(message, "no endpoints available") ||
		strings.Contains(message, "connection refused")
}

func replacePreviewResponse(resp *http.Response, statusCode int, body []byte) {
	resp.StatusCode = statusCode
	resp.Status = fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Type", "text/html; charset=utf-8")
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Set("Cache-Control", "no-store")
	resp.Header.Set("Retry-After", "2")
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Etag")
}

func previewRuntimeStartingHTML() []byte {
	return []byte(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta http-equiv="refresh" content="2">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Preview is starting</title>
  <style>
    :root { color-scheme: light dark; }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      display: grid;
      place-items: center;
      background: #0f1117;
      color: #f5f7fb;
      font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    main {
      width: min(420px, calc(100vw - 48px));
      text-align: center;
      line-height: 1.5;
    }
    .mark {
      width: 36px;
      height: 36px;
      margin: 0 auto 16px;
      border: 2px solid rgba(245, 247, 251, 0.2);
      border-top-color: #8b7cf6;
      border-radius: 999px;
      animation: spin 0.9s linear infinite;
    }
    h1 {
      margin: 0;
      font-size: 15px;
      font-weight: 650;
      letter-spacing: 0;
    }
    p {
      margin: 8px 0 0;
      color: rgba(245, 247, 251, 0.68);
      font-size: 13px;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
  </style>
</head>
<body>
  <main>
    <div class="mark" aria-hidden="true"></div>
    <h1>Preview is starting</h1>
    <p>The app process is still accepting its first connections. This pane will retry automatically.</p>
  </main>
</body>
</html>`)
}

func previewResponseTooLargeHTML() []byte {
	return []byte(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Preview response is too large</title>
</head>
<body>
  <main>
    <h1>Preview response is too large</h1>
    <p>The sandbox app returned a response that is too large for App Studio to rewrite safely.</p>
  </main>
</body>
</html>`)
}

func (s *Server) previewProjectTarget(w http.ResponseWriter, r *http.Request, projectName string) (runtimeServiceRef, string, bool) {
	if r.URL.Query().Get(previewTokenQuery) != "" || previewProjectRequestScope(projectName, r.URL.Path) != "" {
		payload, suffix, ok := s.previewTokenFromRequest(w, r, projectName)
		if !ok {
			return runtimeServiceRef{}, "", false
		}
		return runtimeServiceRef{Namespace: payload.RuntimeNamespace, Name: payload.PreviewServiceName, PortName: payload.PreviewPortName}, suffix, true
	}
	if strings.TrimSpace(r.Header.Get("X-Kedge-Tenant")) == "" && bearerToken(r) == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "tenant context missing")
		return runtimeServiceRef{}, "", false
	}
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return runtimeServiceRef{}, "", false
	}
	target, ok := projectDevelopmentSyncTarget(p, id)
	if !ok {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "project has no sandbox runner binding")
		return runtimeServiceRef{}, "", false
	}
	runtimeTarget, _, err := s.runtimeTargetForProject(r.Context(), c, target.ResourceName)
	if err != nil {
		writeRuntimeTargetError(w, err)
		return runtimeServiceRef{}, "", false
	}
	return runtimeTarget.Preview, previewProjectRuntimeSuffix(projectName, r.URL.Path), true
}

func (s *Server) previewTokenFromRequest(w http.ResponseWriter, r *http.Request, projectName string) (previewTokenPayload, string, bool) {
	token := strings.TrimSpace(r.URL.Query().Get(previewTokenQuery))
	if token != "" {
		payload, err := s.previewSigner.verify(token, projectName)
		if err != nil {
			writeStatus(w, http.StatusUnauthorized, "Unauthorized", err.Error())
			return previewTokenPayload{}, "", false
		}
		scope := previewTokenScope(token)
		setPreviewTokenCookie(w, r, projectName, scope, token, time.Unix(payload.ExpiresAt, 0))
		http.Redirect(w, r, scopedProjectPreviewRedirectURL(projectName, scope, r.URL.Query()), http.StatusFound)
		return previewTokenPayload{}, "", false
	}
	scope, suffix, ok := previewProjectRequestScopeAndSuffix(projectName, r.URL.Path)
	if !ok {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "tenant context missing")
		return previewTokenPayload{}, "", false
	}
	cookie, err := r.Cookie(previewCookieName(projectName, scope))
	if err != nil {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "tenant context missing")
		return previewTokenPayload{}, "", false
	}
	if previewTokenScope(cookie.Value) != scope {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "invalid preview token")
		return previewTokenPayload{}, "", false
	}
	payload, err := s.previewSigner.verify(cookie.Value, projectName)
	if err != nil {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return previewTokenPayload{}, "", false
	}
	return payload, suffix, true
}

func setPreviewTokenCookie(w http.ResponseWriter, r *http.Request, projectName, scope, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     previewCookieName(projectName, scope),
		Value:    token,
		Path:     externalScopedProjectPreviewPath(projectName, scope),
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   previewCookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func previewCookieSecure(r *http.Request) bool {
	if r == nil || r.TLS != nil {
		return true
	}
	if proto := forwardedPreviewProto(r); proto != "" {
		return proto == "https"
	}
	return true
}

func forwardedPreviewProto(r *http.Request) string {
	for _, header := range []string{"X-Forwarded-Proto", "X-Forwarded-Scheme"} {
		value := strings.TrimSpace(r.Header.Get(header))
		if value == "" {
			continue
		}
		proto, _, _ := strings.Cut(value, ",")
		return strings.ToLower(strings.TrimSpace(proto))
	}
	forwarded := r.Header.Get("Forwarded")
	for _, part := range strings.Split(forwarded, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "proto") {
			continue
		}
		return strings.ToLower(strings.Trim(strings.TrimSpace(value), `"`))
	}
	return ""
}

func readPreviewRewriteBody(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, previewRewriteBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > previewRewriteBodyLimit {
		return nil, errPreviewRewriteBodyTooLarge
	}
	return raw, nil
}

func previewCookieName(projectName, scope string) string {
	sum := sha256.Sum256([]byte(projectName + "\x00" + scope))
	return "kedge_app_studio_preview_" + hex.EncodeToString(sum[:])[:16]
}

func externalProjectPreviewPath(projectName string) string {
	return "/services/providers/app-studio/api/projects/" + projectName + "/preview/"
}

func externalScopedProjectPreviewPath(projectName, scope string) string {
	return externalProjectPreviewPath(projectName) + previewScopePrefix + "/" + scope + "/"
}

func scopedProjectPreviewBasePath(projectName, requestPath string) string {
	scope := previewProjectRequestScope(projectName, requestPath)
	if scope == "" {
		return ""
	}
	return externalScopedProjectPreviewPath(projectName, scope)
}

func scopedProjectPreviewRedirectURL(projectName, scope string, query url.Values) string {
	target := externalScopedProjectPreviewPath(projectName, scope)
	if raw := previewRuntimeRawQuery(query); raw != "" {
		target += "?" + raw
	}
	return target
}

func previewTokenScope(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:16]
}

func previewProjectRequestScope(projectName, requestPath string) string {
	scope, _, ok := previewProjectRequestScopeAndSuffix(projectName, requestPath)
	if !ok {
		return ""
	}
	return scope
}

func previewProjectRequestScopeAndSuffix(projectName, requestPath string) (string, string, bool) {
	suffix := previewProjectRuntimeSuffix(projectName, requestPath)
	segment := previewScopePrefix + "/"
	if !strings.HasPrefix(suffix, segment) {
		return "", suffix, false
	}
	rest := strings.TrimPrefix(suffix, segment)
	scope, next, found := strings.Cut(rest, "/")
	if !found || !validPreviewScope(scope) {
		return "", suffix, false
	}
	return scope, next, true
}

func previewProjectRuntimeSuffix(projectName, requestPath string) string {
	prefix := "/api/projects/" + projectName + "/preview/"
	return strings.TrimPrefix(requestPath, prefix)
}

func validPreviewScope(scope string) bool {
	if len(scope) != 16 {
		return false
	}
	for _, ch := range scope {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}

func previewRuntimeRawQuery(query url.Values) string {
	if _, ok := query[previewTokenQuery]; !ok {
		return query.Encode()
	}
	next := make(url.Values, len(query))
	for key, values := range query {
		if key == previewTokenQuery {
			continue
		}
		next[key] = append([]string(nil), values...)
	}
	return next.Encode()
}

func previewRewritableContentType(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return mediaType == "text/html" ||
		mediaType == "text/css" ||
		mediaType == "text/javascript" ||
		mediaType == "application/javascript" ||
		mediaType == "application/ecmascript"
}

func rewritePreviewResponseBody(contentType, basePath string, raw []byte, upstreamBasePaths ...string) []byte {
	if basePath == "" {
		return raw
	}
	text := string(raw)
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch mediaType {
	case "text/html":
		text = rewritePreviewHTMLUpstreamURLs(text, basePath, upstreamBasePaths)
		text = rewritePreviewHTMLRootURLs(text, basePath)
		text = injectPreviewBaseTag(text, basePath)
	case "text/css":
		text = rewritePreviewCSSUpstreamURLs(text, basePath, upstreamBasePaths)
		text = rewritePreviewCSSRootURLs(text, basePath)
	case "text/javascript", "application/javascript", "application/ecmascript":
		text = rewritePreviewJavaScriptUpstreamURLs(text, basePath, upstreamBasePaths)
		text = rewritePreviewJavaScriptRootURLs(text, basePath)
	}
	return []byte(text)
}

func injectPreviewBaseTag(text, basePath string) string {
	if strings.Contains(strings.ToLower(text), "<base ") {
		return text
	}
	const head = "<head>"
	idx := strings.Index(strings.ToLower(text), head)
	tag := `<base href="` + basePath + `">`
	if idx < 0 {
		return tag + text
	}
	insert := idx + len(head)
	return text[:insert] + "\n  " + tag + text[insert:]
}

func rewritePreviewHTMLRootURLs(text, basePath string) string {
	text = strings.NewReplacer(
		`src="`+basePath, `src="`+previewBasePathPlacehold,
		`src='`+basePath, `src='`+previewBasePathPlacehold,
		`href="`+basePath, `href="`+previewBasePathPlacehold,
		`href='`+basePath, `href='`+previewBasePathPlacehold,
		`action="`+basePath, `action="`+previewBasePathPlacehold,
		`action='`+basePath, `action='`+previewBasePathPlacehold,
	).Replace(text)
	text = strings.NewReplacer(
		`src="/`, `src="`+basePath,
		`src='/`, `src='`+basePath,
		`href="/`, `href="`+basePath,
		`href='/`, `href='`+basePath,
		`action="/`, `action="`+basePath,
		`action='/`, `action='`+basePath,
	).Replace(text)
	return strings.NewReplacer(
		`src="`+previewBasePathPlacehold, `src="`+basePath,
		`src='`+previewBasePathPlacehold, `src='`+basePath,
		`href="`+previewBasePathPlacehold, `href="`+basePath,
		`href='`+previewBasePathPlacehold, `href='`+basePath,
		`action="`+previewBasePathPlacehold, `action="`+basePath,
		`action='`+previewBasePathPlacehold, `action='`+basePath,
	).Replace(text)
}

func rewritePreviewHTMLUpstreamURLs(text, basePath string, upstreamBasePaths []string) string {
	for _, upstreamBasePath := range normalizedPreviewUpstreamBasePaths(upstreamBasePaths) {
		text = strings.NewReplacer(
			`src="`+upstreamBasePath, `src="`+basePath,
			`src='`+upstreamBasePath, `src='`+basePath,
			`href="`+upstreamBasePath, `href="`+basePath,
			`href='`+upstreamBasePath, `href='`+basePath,
			`action="`+upstreamBasePath, `action="`+basePath,
			`action='`+upstreamBasePath, `action='`+basePath,
		).Replace(text)
	}
	return text
}

func rewritePreviewJavaScriptRootURLs(text, basePath string) string {
	text = strings.NewReplacer(
		`fetch('`+basePath, `fetch('`+previewBasePathPlacehold,
		`fetch("`+basePath, `fetch("`+previewBasePathPlacehold,
		"fetch(`"+basePath, "fetch(`"+previewBasePathPlacehold,
		`= '`+basePath, `= '`+previewBasePathPlacehold,
		`= "`+basePath, `= "`+previewBasePathPlacehold,
		"= `"+basePath, "= `"+previewBasePathPlacehold,
		`='`+basePath, `='`+previewBasePathPlacehold,
		`="`+basePath, `="`+previewBasePathPlacehold,
		"=`"+basePath, "=`"+previewBasePathPlacehold,
	).Replace(text)
	text = strings.NewReplacer(
		`fetch('/`, `fetch('`+basePath,
		`fetch("/`, `fetch("`+basePath,
		"fetch(`/", "fetch(`"+basePath,
		`= '/`, `= '`+basePath,
		`= "/`, `= "`+basePath,
		"= `/", "= `"+basePath,
		`='/`, `='`+basePath,
		`="/`, `="`+basePath,
		"=`/", "=`"+basePath,
	).Replace(text)
	return strings.NewReplacer(
		`fetch('`+previewBasePathPlacehold, `fetch('`+basePath,
		`fetch("`+previewBasePathPlacehold, `fetch("`+basePath,
		"fetch(`"+previewBasePathPlacehold, "fetch(`"+basePath,
		`= '`+previewBasePathPlacehold, `= '`+basePath,
		`= "`+previewBasePathPlacehold, `= "`+basePath,
		"= `"+previewBasePathPlacehold, "= `"+basePath,
		`='`+previewBasePathPlacehold, `='`+basePath,
		`="`+previewBasePathPlacehold, `="`+basePath,
		"=`"+previewBasePathPlacehold, "=`"+basePath,
	).Replace(text)
}

func rewritePreviewJavaScriptUpstreamURLs(text, basePath string, upstreamBasePaths []string) string {
	for _, upstreamBasePath := range normalizedPreviewUpstreamBasePaths(upstreamBasePaths) {
		text = strings.NewReplacer(
			`fetch('`+upstreamBasePath, `fetch('`+basePath,
			`fetch("`+upstreamBasePath, `fetch("`+basePath,
			"fetch(`"+upstreamBasePath, "fetch(`"+basePath,
		).Replace(text)
	}
	return text
}

func rewritePreviewCSSRootURLs(text, basePath string) string {
	text = strings.NewReplacer(
		`url(`+basePath, `url(`+previewBasePathPlacehold,
		`url("`+basePath, `url("`+previewBasePathPlacehold,
		`url('`+basePath, `url('`+previewBasePathPlacehold,
	).Replace(text)
	text = strings.NewReplacer(
		`url(/`, `url(`+basePath,
		`url("/`, `url("`+basePath,
		`url('/`, `url('`+basePath,
	).Replace(text)
	return strings.NewReplacer(
		`url(`+previewBasePathPlacehold, `url(`+basePath,
		`url("`+previewBasePathPlacehold, `url("`+basePath,
		`url('`+previewBasePathPlacehold, `url('`+basePath,
	).Replace(text)
}

func rewritePreviewCSSUpstreamURLs(text, basePath string, upstreamBasePaths []string) string {
	for _, upstreamBasePath := range normalizedPreviewUpstreamBasePaths(upstreamBasePaths) {
		text = strings.NewReplacer(
			`url(`+upstreamBasePath, `url(`+basePath,
			`url("`+upstreamBasePath, `url("`+basePath,
			`url('`+upstreamBasePath, `url('`+basePath,
		).Replace(text)
	}
	return text
}

func normalizedPreviewUpstreamBasePaths(upstreamBasePaths []string) []string {
	normalized := make([]string, 0, len(upstreamBasePaths))
	for _, upstreamBasePath := range upstreamBasePaths {
		upstreamBasePath = strings.TrimSpace(upstreamBasePath)
		if upstreamBasePath == "" {
			continue
		}
		if !strings.HasPrefix(upstreamBasePath, "/") {
			upstreamBasePath = "/" + upstreamBasePath
		}
		if !strings.HasSuffix(upstreamBasePath, "/") {
			upstreamBasePath += "/"
		}
		normalized = append(normalized, upstreamBasePath)
	}
	return normalized
}

func stripPreviewForwardedCredentials(header http.Header) {
	header.Del("Authorization")
	header.Del("Cookie")
	header.Del("X-Kedge-Tenant")
	header.Del("X-Kedge-User")
	header.Del("X-Sandbox-Control-Token")
	for key := range header {
		if strings.HasPrefix(strings.ToLower(key), "x-kedge-") {
			header.Del(key)
		}
	}
}

func runtimeServicePath(ref runtimeServiceRef, suffix string) string {
	cleanSuffix := strings.TrimPrefix(path.Clean("/"+suffix), "/")
	base := "/api/v1/namespaces/" + ref.Namespace + "/services/" + ref.Name + ":" + ref.PortName + "/proxy"
	if cleanSuffix == "" || cleanSuffix == "." {
		return base + "/"
	}
	return base + "/" + cleanSuffix
}

func restTransport(cfg *rest.Config) (http.RoundTripper, error) {
	return rest.TransportFor(cfg)
}

func writeRuntimeTargetError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
	case apierrors.IsForbidden(err):
		writeStatus(w, http.StatusForbidden, "Forbidden", err.Error())
	default:
		writeStatus(w, http.StatusConflict, "RuntimeNotReady", err.Error())
	}
}
