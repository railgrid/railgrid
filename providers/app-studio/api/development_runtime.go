/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

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
	// Validate the runner exists in the workspace before reaching the data
	// plane, so a missing runner surfaces as 404 rather than a proxy error.
	if _, _, err := s.runtimeTargetForProject(r.Context(), c, target.ResourceName); err != nil {
		writeRuntimeTargetError(w, err)
		return
	}
	respBody, status, err := s.sandboxDataPlanePost(r.Context(), id, target.ResourceName, dataPlaneVerbRestart, []byte(`{}`))
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
	// Validate the runner exists in the workspace first (404 vs proxy error).
	if _, _, err := s.runtimeTargetForProject(r.Context(), c, target.ResourceName); err != nil {
		writeRuntimeTargetError(w, err)
		return
	}
	// Stream logs from the infrastructure provider's data-plane subresource;
	// it owns the runtime credential and the control-token injection.
	if err := s.sandboxDataPlaneStream(r.Context(), id, target.ResourceName, dataPlaneVerbLog, w); err != nil {
		// Headers may already be sent on a mid-stream failure; only safe to
		// write a status when nothing has been flushed yet.
		writeStatus(w, http.StatusBadGateway, "BadGateway", err.Error())
		return
	}
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

// previewReadiness probes the sandbox preview through the infrastructure
// provider's data-plane proxy subresource (App Studio no longer reaches the
// runtime cluster directly). The proxy returns the runtime service-proxy
// response, so a 503 carrying a Kubernetes "Status" body means the runtime is
// still starting / has no ready endpoints; any normal response means ready.
func (s *Server) previewReadiness(ctx context.Context, id identity, runnerName string) projectSandboxPreviewURLResponse {
	notReady := func(reason, message string) projectSandboxPreviewURLResponse {
		return projectSandboxPreviewURLResponse{Ready: false, Reason: reason, Message: message}
	}
	status, body, err := s.sandboxDataPlaneProbe(ctx, id, runnerName, "/")
	if err != nil {
		return notReady("service_unavailable", "Preview is getting ready. The sandbox runtime is not reachable yet.")
	}
	switch {
	case status == http.StatusServiceUnavailable && isPreviewRuntimeStartingStatus(body):
		return notReady("runtime_starting", "Preview is getting ready. The sandbox runtime is not serving traffic yet.")
	case status == http.StatusNotFound:
		return notReady("service_not_found", "Preview is getting ready. The preview service has not been created yet.")
	case status == http.StatusBadGateway, status == http.StatusGatewayTimeout, status == http.StatusServiceUnavailable:
		return notReady("service_unavailable", "Preview is getting ready. The sandbox runtime is not reachable yet.")
	default:
		return projectSandboxPreviewURLResponse{Ready: true}
	}
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
