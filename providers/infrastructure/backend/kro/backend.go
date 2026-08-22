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

	"k8s.io/apimachinery/pkg/api/equality"
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

	// tokens are the platform-config values substituted for reserved ${faros.*}
	// placeholders in a Template's backendConfig before the RGD is authored —
	// platform-wide settings that belong on the backend, not in per-tenant data.
	// See substituteTokens in rgd.go. The tokens are the exposure-layer Gateway
	// parent (${faros.gatewayName}/${faros.gatewayNamespace}) — the ONE Gateway
	// every template's HTTPRoutes attach to (cfgate cloudflare-tunnel in prod,
	// envoy locally) — plus ${faros.appPublicPort} and the dev-overlay images
	// (${faros.devImage.<toolchain>}, ${faros.devAgentImage}); per-instance
	// inputs like container images are schema fields with defaults, not tokens
	// (see providers/infrastructure/docs/template-conventions.md).
	tokens map[string]string
}

var _ backend.Backend = (*Backend)(nil)

// DefaultGatewayName / DefaultGatewayNamespace are used when
// FAROS_GATEWAY_NAME / FAROS_GATEWAY_NAMESPACE are unset. They point at the
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
//   - FAROS_GATEWAY_NAME / FAROS_GATEWAY_NAMESPACE — the exposure-layer Gateway
//     parent every template's HTTPRoutes attach to (defaults
//     "cloudflare-tunnel" / "cfgate-system").
//   - FAROS_APP_PUBLIC_PORT — bare port number appended (as ":<port>") to
//     synthesized exposure URLs via ${faros.appPublicPort}. Unset in
//     production (443 implied); local kind sets 10443 (the envoy
//     port-forward).
//
// Per-instance inputs (container images, etc.) are NOT env tokens — templates
// declare them as schema fields (e.g. simple-webapp's spec.image, database's
// spec.version), the same convention every other template follows. See
// providers/infrastructure/docs/template-conventions.md.
func New(runtime dynamic.Interface) *Backend {
	gatewayName := os.Getenv("FAROS_GATEWAY_NAME")
	if gatewayName == "" {
		gatewayName = DefaultGatewayName
	}
	gatewayNamespace := os.Getenv("FAROS_GATEWAY_NAMESPACE")
	if gatewayNamespace == "" {
		gatewayNamespace = DefaultGatewayNamespace
	}
	tokens := map[string]string{
		gatewayNameToken:      gatewayName,
		gatewayNamespaceToken: gatewayNamespace,
		appPublicPortToken:    appPublicPortSuffix(os.Getenv("FAROS_APP_PUBLIC_PORT")),
		previewConsoleVerificationJWKSConfigKey: strings.TrimSpace(
			os.Getenv("FAROS_PREVIEW_CONSOLE_VERIFICATION_JWKS"),
		),
	}
	maps.Copy(tokens, accessGateTokens())
	maps.Copy(tokens, devImageTokens())
	return &Backend{runtime: runtime, tokens: tokens}
}

// DefaultAccessProxyImage backs ${faros.accessProxyImage} when
// FAROS_ACCESS_PROXY_IMAGE is unset. Production should pin a digest — the
// gate fronts every published app.
const DefaultAccessProxyImage = "ghcr.io/faroshq/faros-access-proxy:latest"

// accessGateTokens resolves the access-gate token family (see rgd.go). The
// hub URLs may legitimately be empty on hubs that never publish privately;
// the gate only requires them in private mode, so empty substitution renders
// a public-only gate rather than failing template setup.
func accessGateTokens() map[string]string {
	image := strings.TrimSpace(os.Getenv("FAROS_ACCESS_PROXY_IMAGE"))
	if image == "" {
		image = DefaultAccessProxyImage
	}
	hubURL := strings.TrimSpace(os.Getenv("FAROS_ACCESS_HUB_URL"))
	if hubURL == "" {
		hubURL = strings.TrimSpace(os.Getenv("FAROS_HUB_URL"))
	}
	hubPublicURL := strings.TrimSpace(os.Getenv("FAROS_ACCESS_HUB_PUBLIC_URL"))
	if hubPublicURL == "" {
		hubPublicURL = hubURL
	}
	hubInsecure := "false"
	if strings.EqualFold(strings.TrimSpace(os.Getenv("FAROS_ACCESS_HUB_INSECURE")), "true") {
		hubInsecure = "true"
	}
	return map[string]string{
		accessProxyImageToken: image,
		hubURLToken:           hubURL,
		hubPublicURLToken:     hubPublicURL,
		hubInsecureToken:      hubInsecure,
	}
}

// appPublicPortSuffix turns FAROS_APP_PUBLIC_PORT into the ":<port>" suffix
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
	klog.Background().Info("ignoring invalid FAROS_APP_PUBLIC_PORT (want a bare port number 1-65535)", "value", raw)
	return ""
}

// DefaultNodeDevImage / DefaultUniversalDevImage / DefaultDevAgentImage back
// the dev-overlay images when the env knobs are unset, so a stock deployment
// (and local dev) can run node or universal coding sandboxes out of the box.
// Production should pin digests via FAROS_DEV_IMAGE_NODE,
// FAROS_DEV_IMAGE_UNIVERSAL, and FAROS_DEV_AGENT_IMAGE (they run tenant code —
// see docs/app-studio-template-sandboxes.md §9).
const (
	// DefaultNodeDevImage is a plain node toolchain image — the dev agent is
	// injected by init container, so nothing faros-specific is baked in
	// (bookworm, not slim: dev flows need git and the usual build tools).
	DefaultNodeDevImage      = "docker.io/library/node:22-bookworm"
	DefaultUniversalDevImage = "ghcr.io/faroshq/faros-universal-dev:latest"
	DefaultDevAgentImage     = "ghcr.io/faroshq/faros-dev-agent:latest"
)

// devImageTokens collects the platform-managed dev-mode images: every
// FAROS_DEV_IMAGE_<TOOLCHAIN> env var becomes ${faros.devImage.<toolchain>}
// (underscores → dashes, lowercased), plus the agent injector image. The node
// toolchain and the agent get in-binary defaults; any other toolchain a
// template references without configuration fails that template's setup with
// a pointer to the missing env var (see applyDevOverlay).
func devImageTokens() map[string]string {
	out := map[string]string{
		devImageTokenPrefix + "node}":      DefaultNodeDevImage,
		devImageTokenPrefix + "universal}": DefaultUniversalDevImage,
		devAgentImageToken:                 DefaultDevAgentImage,
	}
	const envPrefix = "FAROS_DEV_IMAGE_"
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
	if v := os.Getenv("FAROS_DEV_AGENT_IMAGE"); v != "" {
		out[devAgentImageToken] = v
	}
	return out
}

// ResolveDevelopmentImageToken resolves the platform-owned image token from a
// Template development component using the same environment/default mapping
// used when synthesizing the development overlay. Callers must never accept a
// tenant-provided image string; only the reserved token family is valid.
func ResolveDevelopmentImageToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, devImageTokenPrefix) || !strings.HasSuffix(token, "}") {
		return "", fmt.Errorf("development image %q is not a reserved platform token", token)
	}
	image := strings.TrimSpace(devImageTokens()[token])
	if image == "" {
		return "", fmt.Errorf("development image token %q is not configured", token)
	}
	return image, nil
}

// Name returns "kro".
func (b *Backend) Name() string { return Name }

// SetupTemplate derives the RGD from the Template and applies it to the
// runtime cluster. Idempotent: re-applies on every reconcile pass. A build
// error (malformed schema/backendConfig) is returned so the Template
// controller surfaces BackendError; a successful apply reports Ready=true.
func (b *Backend) SetupTemplate(ctx context.Context, tmpl *infrav1alpha1.Template) (backend.TemplateStatus, error) {
	// The Template controller gates this platform-owned entry by feature flag,
	// but the backend also fails closed regardless of that flag. This protects
	// direct SetupTemplate callers and prevents a mutable tenant-code image from
	// reaching the runtime cluster during a gate/configuration race.
	if tmpl.Name == infrav1alpha1.UniversalCodingSandboxTemplateName {
		if err := validateUniversalDevImages(b.tokens); err != nil {
			return backend.TemplateStatus{Ready: false, Message: err.Error()}, fmt.Errorf("template %q: %w", tmpl.Name, err)
		}
	}
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

func validateUniversalDevImages(tokens map[string]string) error {
	for _, image := range []struct {
		name  string
		token string
	}{
		{name: "universal", token: devImageTokenPrefix + "universal}"},
		{name: "dev agent", token: devAgentImageToken},
	} {
		if err := infrav1alpha1.ValidateImmutableImageRef(strings.TrimSpace(tokens[image.token])); err != nil {
			return fmt.Errorf("universal coding sandbox %s image: %w", image.name, err)
		}
	}
	return nil
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
// server-assigned resourceVersion on update so it's a compare-and-set. An
// update is only written when the desired spec or labels actually differ —
// an unconditional update bumps the RGD's generation, which makes kro
// re-reconcile every instance of the template (recreating includeWhen-gated
// Jobs and the like) on every Template reconcile pass.
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

	existingSpec, _, _ := unstructured.NestedMap(existing.Object, "spec")
	desiredSpec, _, _ := unstructured.NestedMap(rgd.Object, "spec")
	labelsCurrent := true
	for k, v := range rgd.GetLabels() {
		if existing.GetLabels()[k] != v {
			labelsCurrent = false
			break
		}
	}
	if labelsCurrent && equality.Semantic.DeepEqual(existingSpec, desiredSpec) {
		return nil
	}

	rgd.SetResourceVersion(existing.GetResourceVersion())
	if _, err := b.runtime.Resource(rgdGVR).Update(ctx, rgd, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return nil
}
