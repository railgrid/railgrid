/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package kro implements the backend.Backend that turns a Template into a
// kro ResourceGraphDefinition on the runtime cluster.
//
// The Template is the source of truth (it lives in the provider's kcp
// workspace; the Template controller publishes its CRD/APIResourceSchema so
// tenants can create instances). This backend derives the matching RGD from
// the same Template and writes it to the kro runtime cluster — the cluster
// kro's controller-runtime manager watches RGDs on (a kind cluster in dev),
// NOT kcp. Once the RGD exists, kro registers the dynamic watch and
// reconciles instances; instance workloads land on the runtime cluster while
// the instance object + status stay in the tenant's kcp workspace (see the
// kro fork's --deploy-to-local-runtime split).
//
// This backend does NOT reconcile instances itself — the kro controller does
// that. Run() therefore just blocks: the RGD authoring happens in
// SetupTemplate/TeardownTemplate, driven by the Template controller.
package kro

import (
	"context"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/backend"
)

// Name is the backend identifier operators put in Template.spec.backend.
const Name = "kro"

// Backend authors kro ResourceGraphDefinitions on the runtime cluster from
// Templates. It implements backend.Backend.
type Backend struct {
	// runtime is a dynamic client scoped to the kro runtime cluster (where
	// the kro controller watches RGDs). In dev this is the kind cluster
	// pointed at by KRO_KUBECONFIG.
	runtime dynamic.Interface

	// tokens are the platform-config values substituted for reserved ${kedge.*}
	// placeholders in a Template's backendConfig before the RGD is authored —
	// platform-wide settings that belong on the backend, not in per-tenant data.
	// See substituteTokens in rgd.go. The tokens are the exposure-layer Gateway
	// parent (${kedge.gatewayName}/${kedge.gatewayNamespace}) — the ONE Gateway
	// every template's HTTPRoutes attach to (cfgate cloudflare-tunnel in prod,
	// envoy locally) — plus ${kedge.appPublicPort} and the dev-overlay images
	// (${kedge.devImage.<toolchain>}, ${kedge.devAgentImage}); per-instance
	// inputs like container images are schema fields with defaults, not tokens
	// (see providers/infrastructure/docs/template-conventions.md).
	tokens map[string]string
}

var _ backend.Backend = (*Backend)(nil)

// DefaultGatewayName / DefaultGatewayNamespace are used when
// KEDGE_GATEWAY_NAME / KEDGE_GATEWAY_NAMESPACE are unset. They point at the
// cfgate Cloudflare Tunnel Gateway we ship with (the Gateway API exposure
// layer: reverse tunnels, edge TLS).
const (
	DefaultGatewayName      = "cloudflare-tunnel"
	DefaultGatewayNamespace = "cfgate-system"
)

// New constructs the kro backend against the runtime cluster's dynamic
// client. The caller (controller_manager) builds it from KRO_KUBECONFIG.
//
// Platform config is read from the environment once and substituted into
// backendConfig at RGD build time (so changing it is a config change, not a
// template edit):
//
//   - KEDGE_GATEWAY_NAME / KEDGE_GATEWAY_NAMESPACE — the exposure-layer Gateway
//     parent every template's HTTPRoutes attach to (defaults
//     "cloudflare-tunnel" / "cfgate-system").
//   - KEDGE_APP_PUBLIC_PORT — bare port number appended (as ":<port>") to
//     synthesized exposure URLs via ${kedge.appPublicPort}. Unset in
//     production (443 implied); local kind sets 10443 (the envoy
//     port-forward).
//
// Per-instance inputs (container images, etc.) are NOT env tokens — templates
// declare them as schema fields (e.g. simple-webapp's spec.image, database's
// spec.version), the same convention every other template follows. See
// providers/infrastructure/docs/template-conventions.md.
func New(runtime dynamic.Interface) *Backend {
	gatewayName := os.Getenv("KEDGE_GATEWAY_NAME")
	if gatewayName == "" {
		gatewayName = DefaultGatewayName
	}
	gatewayNamespace := os.Getenv("KEDGE_GATEWAY_NAMESPACE")
	if gatewayNamespace == "" {
		gatewayNamespace = DefaultGatewayNamespace
	}
	tokens := map[string]string{
		gatewayNameToken:      gatewayName,
		gatewayNamespaceToken: gatewayNamespace,
		appPublicPortToken:    appPublicPortSuffix(os.Getenv("KEDGE_APP_PUBLIC_PORT")),
	}
	maps.Copy(tokens, devImageTokens())
	return &Backend{runtime: runtime, tokens: tokens}
}

// appPublicPortSuffix turns KEDGE_APP_PUBLIC_PORT into the ":<port>" suffix
// spliced into backendConfig JSON/CEL by plain byte substitution. The value is
// operator-provided and substituted unescaped, so accept only a bare port in
// the valid range (a stray quote, ":", or path would corrupt every synthesized
// RGD / produce invalid URLs). Anything else is logged and treated as unset.
func appPublicPortSuffix(raw string) string {
	port := strings.TrimPrefix(strings.TrimSpace(raw), ":")
	if port == "" {
		return ""
	}
	if n, err := strconv.Atoi(port); err == nil && n >= 1 && n <= 65535 {
		return ":" + strconv.Itoa(n)
	}
	klog.Background().Info("ignoring invalid KEDGE_APP_PUBLIC_PORT (want a bare port number 1-65535)", "value", raw)
	return ""
}

// DefaultNodeDevImage / DefaultDevAgentImage back the dev-overlay images when
// the env knobs are unset, so a stock deployment (and local dev) can run
// node-toolchain sandboxes out of the box. Production should pin digests via
// KEDGE_DEV_IMAGE_NODE / KEDGE_DEV_AGENT_IMAGE (they run tenant code — see
// docs/app-studio-template-sandboxes.md §9).
const (
	// DefaultNodeDevImage is a plain node toolchain image — the dev agent is
	// injected by init container, so nothing kedge-specific is baked in
	// (bookworm, not slim: dev flows need git and the usual build tools).
	DefaultNodeDevImage  = "docker.io/library/node:22-bookworm"
	DefaultDevAgentImage = "ghcr.io/faroshq/kedge-dev-agent:latest"
)

// devImageTokens collects the platform-managed dev-mode images: every
// KEDGE_DEV_IMAGE_<TOOLCHAIN> env var becomes ${kedge.devImage.<toolchain>}
// (underscores → dashes, lowercased), plus the agent injector image. The node
// toolchain and the agent get in-binary defaults; any other toolchain a
// template references without configuration fails that template's setup with
// a pointer to the missing env var (see applyDevOverlay).
func devImageTokens() map[string]string {
	out := map[string]string{
		devImageTokenPrefix + "node}": DefaultNodeDevImage,
		devAgentImageToken:            DefaultDevAgentImage,
	}
	const envPrefix = "KEDGE_DEV_IMAGE_"
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" || !strings.HasPrefix(k, envPrefix) {
			continue
		}
		toolchain := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(k, envPrefix), "_", "-"))
		if toolchain == "" {
			continue
		}
		out[devImageTokenPrefix+toolchain+"}"] = v
	}
	if v := os.Getenv("KEDGE_DEV_AGENT_IMAGE"); v != "" {
		out[devAgentImageToken] = v
	}
	return out
}

// Name returns "kro".
func (b *Backend) Name() string { return Name }

// SetupTemplate derives the RGD from the Template and applies it to the
// runtime cluster. Idempotent: re-applies on every reconcile pass. A build
// error (malformed schema/backendConfig) is returned so the Template
// controller surfaces BackendError; a successful apply reports Ready=true.
func (b *Backend) SetupTemplate(ctx context.Context, tmpl *infrav1alpha1.Template) (backend.TemplateStatus, error) {
	rgd, err := buildRGD(tmpl, b.tokens)
	if err != nil {
		return backend.TemplateStatus{Ready: false, Message: err.Error()}, err
	}
	if err := b.applyRGD(ctx, rgd); err != nil {
		return backend.TemplateStatus{Ready: false, Message: "applying RGD: " + err.Error()}, err
	}
	klog.FromContext(ctx).WithName("backend.kro").Info("applied ResourceGraphDefinition to runtime cluster",
		"template", tmpl.Name, "rgd", tmpl.Name)
	return backend.TemplateStatus{Ready: true, Message: "RGD applied to runtime cluster"}, nil
}

// TeardownTemplate removes the Template's RGD from the runtime cluster. kro
// then garbage-collects the generated CRD + stops the dynamic watch. 404 is
// success (already gone). Idempotent.
func (b *Backend) TeardownTemplate(ctx context.Context, tmpl *infrav1alpha1.Template) error {
	err := b.runtime.Resource(rgdGVR).Delete(ctx, tmpl.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting RGD %q: %w", tmpl.Name, err)
	}
	return nil
}

// Run blocks until ctx is cancelled. Instance reconciliation is owned by the
// kro controller (which watches the RGDs this backend writes), so there is
// no per-process loop to run here. vwConfig is unused for the same reason.
func (b *Backend) Run(ctx context.Context, _ *rest.Config) error {
	klog.FromContext(ctx).WithName("backend.kro").Info("kro backend ready (RGDs authored on the runtime cluster; reconciliation handled by the kro controller)")
	<-ctx.Done()
	return nil
}

// applyRGD creates or updates the RGD on the runtime cluster, preserving the
// server-assigned resourceVersion on update so it's a compare-and-set.
func (b *Backend) applyRGD(ctx context.Context, rgd *unstructured.Unstructured) error {
	existing, err := b.runtime.Resource(rgdGVR).Get(ctx, rgd.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := b.runtime.Resource(rgdGVR).Create(ctx, rgd, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	rgd.SetResourceVersion(existing.GetResourceVersion())
	if _, err := b.runtime.Resource(rgdGVR).Update(ctx, rgd, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return nil
}
