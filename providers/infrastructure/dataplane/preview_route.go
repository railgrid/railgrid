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

package dataplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	defaultPreviewBackendNamespace = "kedge-preview"
	defaultPreviewBackendService   = "sandbox-preview-gateway"
	defaultPreviewBackendPort      = 8080
)

var (
	httpRouteGVR = schema.GroupVersionResource{
		Group:    "gateway.networking.k8s.io",
		Version:  "v1",
		Resource: "httproutes",
	}
	referenceGrantGVR = schema.GroupVersionResource{
		Group:    "gateway.networking.k8s.io",
		Version:  "v1beta1",
		Resource: "referencegrants",
	}
)

// PreviewRouteEnsurer verifies and reconciles the Gateway API grant required by
// a SandboxRunner preview route. The caller has already been authorized against
// the SandboxRunner instance before this runs.
type PreviewRouteEnsurer interface {
	Ensure(ctx context.Context, instance *unstructured.Unstructured) error
}

// GatewayParentRef is the platform Gateway parent a sandbox preview route is
// allowed to attach to. Empty fields mean "do not enforce this field".
type GatewayParentRef struct {
	Name        string
	Namespace   string
	SectionName string
}

// PreviewRouteConfig is the platform allowlist for sandbox preview routing.
type PreviewRouteConfig struct {
	BaseDomain       string
	ParentGateway    GatewayParentRef
	BackendNamespace string
	BackendService   string
	BackendPort      int64
}

// PreviewRouteConfigFromEnv reads infrastructure-owned preview routing config.
// APP_STUDIO_* fallbacks keep local/dev deployments working while ownership is
// moving out of App Studio.
func PreviewRouteConfigFromEnv() PreviewRouteConfig {
	return normalizePreviewRouteConfig(PreviewRouteConfig{
		BaseDomain: envAny("KEDGE_SANDBOX_PREVIEW_BASE_DOMAIN", "APP_STUDIO_PREVIEW_BASE_DOMAIN"),
		ParentGateway: GatewayParentRef{
			Name:        envAny("KEDGE_SANDBOX_PREVIEW_HTTPROUTE_PARENT_GATEWAY_NAME", "APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_NAME"),
			Namespace:   envAny("KEDGE_SANDBOX_PREVIEW_HTTPROUTE_PARENT_GATEWAY_NAMESPACE", "APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_NAMESPACE"),
			SectionName: envAny("KEDGE_SANDBOX_PREVIEW_HTTPROUTE_PARENT_GATEWAY_SECTION_NAME", "APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_SECTION_NAME"),
		},
		BackendNamespace: envAny("KEDGE_SANDBOX_PREVIEW_BACKEND_NAMESPACE", "APP_STUDIO_PREVIEW_BACKEND_NAMESPACE"),
		BackendService:   envAny("KEDGE_SANDBOX_PREVIEW_BACKEND_SERVICE_NAME", "APP_STUDIO_PREVIEW_BACKEND_SERVICE_NAME"),
		BackendPort:      envAnyInt64(defaultPreviewBackendPort, "KEDGE_SANDBOX_PREVIEW_BACKEND_SERVICE_PORT", "APP_STUDIO_PREVIEW_BACKEND_SERVICE_PORT"),
	})
}

type PreviewRouteManager struct {
	client dynamic.Interface
	config PreviewRouteConfig
}

func NewPreviewRouteManager(client dynamic.Interface, config PreviewRouteConfig) *PreviewRouteManager {
	if client == nil {
		return nil
	}
	return &PreviewRouteManager{
		client: client,
		config: normalizePreviewRouteConfig(config),
	}
}

func (m *PreviewRouteManager) Ensure(ctx context.Context, instance *unstructured.Unstructured) error {
	if m == nil || m.client == nil {
		return nil
	}
	info, err := previewRouteInfoFromInstance(instance, m.config)
	if err != nil {
		return err
	}

	route, err := m.client.Resource(httpRouteGVR).Namespace(info.RouteNamespace).Get(ctx, info.RouteName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return previewRouteNotReady(fmt.Sprintf("preview HTTPRoute %s/%s has not been created yet", info.RouteNamespace, info.RouteName))
	}
	if err != nil {
		return fmt.Errorf("get preview HTTPRoute %s/%s: %w", info.RouteNamespace, info.RouteName, err)
	}
	if err := validatePreviewHTTPRoute(route, info, m.config); err != nil {
		return err
	}
	if err := m.ensureReferenceGrant(ctx, info); err != nil {
		return err
	}
	if !httpRouteReady(route) {
		return previewRouteNotReady(fmt.Sprintf("preview HTTPRoute %s/%s has not been accepted with resolved backend references yet", info.RouteNamespace, info.RouteName))
	}
	return nil
}

type previewRouteInfo struct {
	RunnerName     string
	Host           string
	RouteName      string
	RouteNamespace string
	Gateway        GatewayParentRef
}

func previewRouteInfoFromInstance(instance *unstructured.Unstructured, config PreviewRouteConfig) (previewRouteInfo, error) {
	if instance == nil {
		return previewRouteInfo{}, fmt.Errorf("sandbox runner is nil")
	}
	name := strings.TrimSpace(instance.GetName())
	if name == "" {
		return previewRouteInfo{}, fmt.Errorf("sandbox runner has no name")
	}
	host, _, _ := unstructured.NestedString(instance.Object, "status", "previewRoute", "host")
	routeName, _, _ := unstructured.NestedString(instance.Object, "status", "previewRoute", "httpRouteRef", "name")
	routeNamespace, _, _ := unstructured.NestedString(instance.Object, "status", "previewRoute", "httpRouteRef", "namespace")
	gatewayName, _, _ := unstructured.NestedString(instance.Object, "status", "previewRoute", "gatewayRef", "name")
	gatewayNamespace, _, _ := unstructured.NestedString(instance.Object, "status", "previewRoute", "gatewayRef", "namespace")
	info := previewRouteInfo{
		RunnerName:     name,
		Host:           strings.TrimSpace(host),
		RouteName:      strings.TrimSpace(routeName),
		RouteNamespace: strings.TrimSpace(routeNamespace),
		Gateway: GatewayParentRef{
			Name:      strings.TrimSpace(gatewayName),
			Namespace: strings.TrimSpace(gatewayNamespace),
		},
	}
	if info.RouteName == "" || info.RouteNamespace == "" {
		return previewRouteInfo{}, previewRouteNotReady("sandbox preview HTTPRoute status is not populated yet")
	}
	if info.RouteName != info.RunnerName {
		return previewRouteInfo{}, fmt.Errorf("sandbox preview HTTPRoute name %q does not match runner %q", info.RouteName, info.RunnerName)
	}
	if config.BaseDomain != "" {
		wantHost := info.RunnerName + "." + strings.Trim(strings.TrimSpace(config.BaseDomain), ".")
		if info.Host != wantHost {
			return previewRouteInfo{}, fmt.Errorf("sandbox preview host %q does not match expected host %q", info.Host, wantHost)
		}
	}
	return info, nil
}

func validatePreviewHTTPRoute(route *unstructured.Unstructured, info previewRouteInfo, config PreviewRouteConfig) error {
	if route.GetName() != info.RouteName || route.GetNamespace() != info.RouteNamespace {
		return fmt.Errorf("preview HTTPRoute identity changed from %s/%s to %s/%s", info.RouteNamespace, info.RouteName, route.GetNamespace(), route.GetName())
	}
	if err := validateRouteHost(route, info, config); err != nil {
		return err
	}
	if err := validateRouteParent(route, info, config); err != nil {
		return err
	}
	return validateRouteBackend(route, config)
}

func validateRouteHost(route *unstructured.Unstructured, info previewRouteInfo, config PreviewRouteConfig) error {
	hosts, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	want := info.Host
	if config.BaseDomain != "" {
		want = info.RunnerName + "." + strings.Trim(strings.TrimSpace(config.BaseDomain), ".")
	}
	if want == "" {
		return nil
	}
	if len(hosts) != 1 || hosts[0] != want {
		return fmt.Errorf("preview HTTPRoute hostnames %v do not match expected host %q", hosts, want)
	}
	return nil
}

func validateRouteParent(route *unstructured.Unstructured, info previewRouteInfo, config PreviewRouteConfig) error {
	parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if len(parents) != 1 {
		return fmt.Errorf("preview HTTPRoute must have exactly one parentRef, got %d", len(parents))
	}
	parent, ok := parents[0].(map[string]any)
	if !ok {
		return fmt.Errorf("preview HTTPRoute parentRef is malformed")
	}
	got := GatewayParentRef{
		Name:        stringField(parent, "name"),
		Namespace:   stringField(parent, "namespace"),
		SectionName: stringField(parent, "sectionName"),
	}
	want := config.ParentGateway
	if want.Name == "" {
		want.Name = info.Gateway.Name
	}
	if want.Namespace == "" {
		want.Namespace = info.Gateway.Namespace
	}
	if want.SectionName != "" && got.SectionName != want.SectionName {
		return fmt.Errorf("preview HTTPRoute parent section %q does not match expected section %q", got.SectionName, want.SectionName)
	}
	if want.Name != "" && got.Name != want.Name {
		return fmt.Errorf("preview HTTPRoute parent name %q does not match expected name %q", got.Name, want.Name)
	}
	if want.Namespace != "" && got.Namespace != want.Namespace {
		return fmt.Errorf("preview HTTPRoute parent namespace %q does not match expected namespace %q", got.Namespace, want.Namespace)
	}
	return nil
}

func validateRouteBackend(route *unstructured.Unstructured, config PreviewRouteConfig) error {
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	if len(rules) != 1 {
		return fmt.Errorf("preview HTTPRoute must have exactly one rule, got %d", len(rules))
	}
	rule, ok := rules[0].(map[string]any)
	if !ok {
		return fmt.Errorf("preview HTTPRoute rule is malformed")
	}
	refs, ok := rule["backendRefs"].([]any)
	if !ok || len(refs) != 1 {
		return fmt.Errorf("preview HTTPRoute must have exactly one backendRef")
	}
	ref, ok := refs[0].(map[string]any)
	if !ok {
		return fmt.Errorf("preview HTTPRoute backendRef is malformed")
	}
	group := stringField(ref, "group")
	kind := stringField(ref, "kind")
	if group != "" || (kind != "" && kind != "Service") {
		return fmt.Errorf("preview HTTPRoute backend must be a core Service, got group=%q kind=%q", group, kind)
	}
	gotNamespace := stringField(ref, "namespace")
	gotName := stringField(ref, "name")
	gotPort, ok := int64Field(ref, "port")
	if gotNamespace != config.BackendNamespace || gotName != config.BackendService || !ok || gotPort != config.BackendPort {
		return fmt.Errorf("preview HTTPRoute has unexpected backend %s/%s:%d", gotNamespace, gotName, gotPort)
	}
	return nil
}

func (m *PreviewRouteManager) ensureReferenceGrant(ctx context.Context, info previewRouteInfo) error {
	grant := referenceGrantFor(info, m.config)
	client := m.client.Resource(referenceGrantGVR).Namespace(m.config.BackendNamespace)
	existing, err := client.Get(ctx, grant.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := client.Create(ctx, grant, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create preview ReferenceGrant %s/%s: %w", m.config.BackendNamespace, grant.GetName(), err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get preview ReferenceGrant %s/%s: %w", m.config.BackendNamespace, grant.GetName(), err)
	}
	if referenceGrantMatches(existing, grant) {
		return nil
	}
	grant.SetResourceVersion(existing.GetResourceVersion())
	if _, err := client.Update(ctx, grant, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update preview ReferenceGrant %s/%s: %w", m.config.BackendNamespace, grant.GetName(), err)
	}
	return nil
}

func referenceGrantFor(info previewRouteInfo, config PreviewRouteConfig) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1beta1",
		"kind":       "ReferenceGrant",
		"metadata": map[string]any{
			"name":      info.RunnerName,
			"namespace": config.BackendNamespace,
			"labels": map[string]any{
				"app.kubernetes.io/name":         "sandbox-runner",
				"app.kubernetes.io/instance":     info.RunnerName,
				"app.kubernetes.io/managed-by":   "kedge-infrastructure",
				"kedge.faros.sh/route-name":      info.RouteName,
				"kedge.faros.sh/route-namespace": info.RouteNamespace,
			},
		},
		"spec": map[string]any{
			"from": []any{
				map[string]any{
					"group":     "gateway.networking.k8s.io",
					"kind":      "HTTPRoute",
					"namespace": info.RouteNamespace,
				},
			},
			"to": []any{
				map[string]any{
					"group": "",
					"kind":  "Service",
					"name":  config.BackendService,
				},
			},
		},
	}}
}

func referenceGrantMatches(existing, desired *unstructured.Unstructured) bool {
	existingSpec, _, _ := unstructured.NestedMap(existing.Object, "spec")
	desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	return reflect.DeepEqual(existingSpec, desiredSpec) &&
		reflect.DeepEqual(existing.GetLabels(), desired.GetLabels())
}

func httpRouteReady(route *unstructured.Unstructured) bool {
	accepted := false
	resolvedRefs := false
	parents, _, _ := unstructured.NestedSlice(route.Object, "status", "parents")
	for _, rawParent := range parents {
		parent, ok := rawParent.(map[string]any)
		if !ok {
			continue
		}
		rawConditions, ok := parent["conditions"].([]any)
		if !ok {
			continue
		}
		for _, rawCondition := range rawConditions {
			condition, ok := rawCondition.(map[string]any)
			if !ok {
				continue
			}
			if stringField(condition, "status") != "True" {
				continue
			}
			switch stringField(condition, "type") {
			case "Accepted":
				accepted = true
			case "ResolvedRefs":
				resolvedRefs = true
			}
		}
	}
	return accepted && resolvedRefs
}

type previewRouteNotReadyError struct {
	message string
}

func previewRouteNotReady(message string) error {
	return previewRouteNotReadyError{message: message}
}

func (e previewRouteNotReadyError) Error() string {
	return "preview route not ready: " + e.message
}

func IsPreviewRouteNotReady(err error) bool {
	var target previewRouteNotReadyError
	return errors.As(err, &target)
}

func normalizePreviewRouteConfig(config PreviewRouteConfig) PreviewRouteConfig {
	config.BaseDomain = strings.Trim(strings.TrimSpace(config.BaseDomain), ".")
	config.ParentGateway.Name = strings.TrimSpace(config.ParentGateway.Name)
	config.ParentGateway.Namespace = strings.TrimSpace(config.ParentGateway.Namespace)
	config.ParentGateway.SectionName = strings.TrimSpace(config.ParentGateway.SectionName)
	config.BackendNamespace = strings.TrimSpace(config.BackendNamespace)
	if config.BackendNamespace == "" {
		config.BackendNamespace = defaultPreviewBackendNamespace
	}
	config.BackendService = strings.TrimSpace(config.BackendService)
	if config.BackendService == "" {
		config.BackendService = defaultPreviewBackendService
	}
	if config.BackendPort == 0 {
		config.BackendPort = defaultPreviewBackendPort
	}
	return config
}

func envAny(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func envAnyInt64(fallback int64, names ...string) int64 {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 32)
			if err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return fallback
}

func stringField(m map[string]any, field string) string {
	value, _ := m[field].(string)
	return strings.TrimSpace(value)
}

func int64Field(m map[string]any, field string) (int64, bool) {
	switch value := m[field].(type) {
	case int:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		return int64(value), value == float64(int64(value))
	default:
		return 0, false
	}
}
