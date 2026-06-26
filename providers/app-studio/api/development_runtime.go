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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	asclient "github.com/faroshq/provider-app-studio/client"
)

const (
	previewReadinessProbeTimeout = 2 * time.Second
	kcpClusterAnnotation         = "kcp.io/cluster"
)

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

func (s *Server) runtimeTargetForProject(ctx context.Context, c *asclient.Client, name string) (runtimeTarget, *unstructured.Unstructured, error) {
	if c == nil {
		return runtimeTarget{}, nil, fmt.Errorf("project client is not configured")
	}
	if strings.TrimSpace(name) == "" {
		return runtimeTarget{}, nil, fmt.Errorf("sandbox runner name is empty")
	}
	obj, err := c.Resource(sandboxRunnerResource, "").Get(ctx, name, metav1.GetOptions{})
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
		Preview:       runtimeServiceRef{Namespace: runtimeNamespace, Name: name + "-preview", PortName: "preview"},
		Control:       runtimeServiceRef{Namespace: runtimeNamespace, Name: name + "-control", PortName: "control"},
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
	return expectedKROPrefixedNamespace(obj, name)
}

func expectedKROPrefixedNamespace(obj *unstructured.Unstructured, namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return ""
	}
	clusterID := strings.TrimSpace(obj.GetAnnotations()[kcpClusterAnnotation])
	if clusterID == "" {
		return ""
	}
	return clusterID + "-" + namespace
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

func patchLastSync(ctx context.Context, c *asclient.Client, name string, t metav1.Time) error {
	if c == nil {
		return nil
	}
	obj, err := c.Resource(sandboxRunnerResource, "").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := unstructured.SetNestedField(obj.Object, t.Format("2006-01-02T15:04:05Z07:00"), "status", "lastSyncTime"); err != nil {
		return err
	}
	_, err = c.Resource(sandboxRunnerResource, "").UpdateStatus(ctx, obj, metav1.UpdateOptions{})
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
	if ok, err := s.previewServiceResponding(ctx, target.Preview); err != nil {
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  "service_unavailable",
			Message: "Preview is getting ready. The sandbox runtime is not reachable yet.",
		}
	} else if !ok {
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  "runtime_starting",
			Message: "Preview is getting ready. The sandbox runtime is not serving traffic yet.",
		}
	}
	return projectSandboxPreviewURLResponse{Ready: true}
}

func (s *Server) previewServiceResponding(ctx context.Context, ref runtimeServiceRef) (bool, error) {
	if s.runtimeConfig == nil {
		return true, nil
	}
	target, err := url.Parse(s.runtimeConfig.Host)
	if err != nil {
		return false, err
	}
	transport, err := restTransport(s.runtimeConfig)
	if err != nil {
		return false, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, previewReadinessProbeTimeout)
	defer cancel()
	reqURL := *target
	reqURL.Path = runtimeServicePath(ref, "")
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return false, err
	}
	req.Host = target.Host
	stripPreviewForwardedCredentials(req.Header)
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusServiceUnavailable && previewStatusContentType(resp.Header.Get("Content-Type")) {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return false, err
		}
		if isPreviewRuntimeStartingStatus(raw) {
			return false, nil
		}
	}
	return true, nil
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
