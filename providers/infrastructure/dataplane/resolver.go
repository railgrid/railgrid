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

// Package dataplane resolves a Template's declarative data-plane contract
// (Template.spec.dataPlane) against a concrete workload instance into the
// runtime target the provider proxies to. It is the server-side replacement for
// the runtimeTarget resolution App Studio does today against its own runtime
// kubeconfig — moved into the infrastructure provider so consumers reach a
// workload's data plane without holding a runtime-cluster credential.
//
// The single security invariant this package enforces is namespace
// confinement: every Service and Secret a verb resolves to MUST live in the
// instance's backend-owned runtime namespace (the value at
// RuntimeNamespacePath). A forged or mutated instance status therefore cannot
// redirect a proxy to an arbitrary Service or Secret elsewhere in the runtime
// cluster.
//
// See docs/app-studio-runtime-decoupling.md for the end-to-end design.
package dataplane

import (
	"fmt"
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// ResolvedTarget is the concrete runtime endpoint a data-plane verb resolves
// to. ServiceNamespace/ServiceName/ServicePort name the Service the provider
// reverse-proxies to via the runtime cluster's services/proxy subresource;
// UpstreamPath is prepended to the caller path; the Token* fields name the
// optional per-instance control-token Secret to inject. A FromStatus verb sets
// only FromStatus=true and leaves the proxy fields zero.
type ResolvedTarget struct {
	// FromStatus is true for a verb served straight from the instance status
	// with no runtime hop. The remaining fields are zero in that case.
	FromStatus bool

	ServiceNamespace string
	ServiceName      string
	ServicePort      string
	UpstreamPath     string

	// Stream and Upgrade mirror the endpoint declaration so the proxy layer can
	// disable buffering / allow connection upgrades.
	Stream  bool
	Upgrade bool

	// TokenSecretNamespace/TokenSecretName name the Secret whose "token" key is
	// injected as X-Sandbox-Control-Token. Empty when the contract declares no
	// token secret.
	TokenSecretNamespace string
	TokenSecretName      string
}

// objectRef is a {name, namespace} object read from the instance status.
type objectRef struct {
	Namespace string
	Name      string
}

// Resolve resolves a single verb of the contract against the instance. It
// returns an error when the verb is unknown, the contract is malformed, a
// required status ref is absent, or a resolved ref escapes the runtime
// namespace.
func Resolve(contract *infrav1alpha1.TemplateDataPlane, instance *unstructured.Unstructured, verb string) (ResolvedTarget, error) {
	if contract == nil {
		return ResolvedTarget{}, fmt.Errorf("template declares no data plane")
	}
	if instance == nil {
		return ResolvedTarget{}, fmt.Errorf("instance is nil")
	}
	endpoint, ok := contract.Endpoints[verb]
	if !ok {
		return ResolvedTarget{}, fmt.Errorf("data-plane verb %q is not declared by this template", verb)
	}

	if endpoint.FromStatus {
		return ResolvedTarget{FromStatus: true}, nil
	}

	// Every proxying verb is confined to the instance's backend-owned runtime
	// namespace, read authoritatively from the instance status.
	runtimeNamespace, err := nestedString(instance, contract.RuntimeNamespacePath)
	if err != nil {
		return ResolvedTarget{}, fmt.Errorf("verb %q: %w", verb, err)
	}
	if runtimeNamespace == "" {
		return ResolvedTarget{}, fmt.Errorf("verb %q: runtime namespace at %q is empty; instance is not ready", verb, contract.RuntimeNamespacePath)
	}

	if strings.TrimSpace(endpoint.ServicePath) == "" {
		return ResolvedTarget{}, fmt.Errorf("verb %q: servicePath is required for a proxy endpoint", verb)
	}
	if strings.TrimSpace(endpoint.Port) == "" {
		return ResolvedTarget{}, fmt.Errorf("verb %q: port is required for a proxy endpoint", verb)
	}
	service, err := refInNamespace(instance, endpoint.ServicePath, runtimeNamespace)
	if err != nil {
		return ResolvedTarget{}, fmt.Errorf("verb %q service: %w", verb, err)
	}

	target := ResolvedTarget{
		ServiceNamespace: service.Namespace,
		ServiceName:      service.Name,
		ServicePort:      strings.TrimSpace(endpoint.Port),
		UpstreamPath:     normalizeUpstreamPath(endpoint.UpstreamPath),
		Stream:           endpoint.Stream,
		Upgrade:          endpoint.Upgrade,
	}

	if path := strings.TrimSpace(contract.TokenSecretPath); path != "" {
		secret, err := refInNamespace(instance, path, runtimeNamespace)
		if err != nil {
			return ResolvedTarget{}, fmt.Errorf("verb %q token secret: %w", verb, err)
		}
		target.TokenSecretNamespace = secret.Namespace
		target.TokenSecretName = secret.Name
	}

	return target, nil
}

// MethodAllowed reports whether method is permitted for the named verb. An
// empty Methods list allows GET only (matching the API doc). Unknown verbs are
// not allowed.
func MethodAllowed(contract *infrav1alpha1.TemplateDataPlane, verb, method string) bool {
	if contract == nil {
		return false
	}
	endpoint, ok := contract.Endpoints[verb]
	if !ok {
		return false
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if len(endpoint.Methods) == 0 {
		return method == http.MethodGet
	}
	for _, m := range endpoint.Methods {
		if strings.ToUpper(strings.TrimSpace(m)) == method {
			return true
		}
	}
	return false
}

// refInNamespace reads a {name, namespace} object from the given status
// dot-path and enforces the namespace-confinement invariant: an empty namespace
// defaults to runtimeNamespace, and any other value is rejected.
func refInNamespace(instance *unstructured.Unstructured, path, runtimeNamespace string) (objectRef, error) {
	ref, err := nestedRef(instance, path)
	if err != nil {
		return objectRef{}, err
	}
	if ref.Name == "" {
		return objectRef{}, fmt.Errorf("ref at %q has no name; instance is not ready", path)
	}
	if ref.Namespace == "" {
		ref.Namespace = runtimeNamespace
	}
	if ref.Namespace != runtimeNamespace {
		return objectRef{}, fmt.Errorf("ref at %q points to namespace %q outside the instance runtime namespace %q", path, ref.Namespace, runtimeNamespace)
	}
	return ref, nil
}

// nestedRef reads a {name, namespace} object at the given dot-path.
func nestedRef(instance *unstructured.Unstructured, path string) (objectRef, error) {
	fields, err := splitPath(path)
	if err != nil {
		return objectRef{}, err
	}
	m, found, err := unstructured.NestedStringMap(instance.Object, fields...)
	if err != nil {
		return objectRef{}, fmt.Errorf("status path %q is not a {name, namespace} object: %w", path, err)
	}
	if !found {
		return objectRef{}, fmt.Errorf("status path %q is absent; instance is not ready", path)
	}
	return objectRef{
		Namespace: strings.TrimSpace(m["namespace"]),
		Name:      strings.TrimSpace(m["name"]),
	}, nil
}

// nestedString reads a string at the given dot-path. A missing path yields an
// empty string and no error (callers treat empty as "not ready").
func nestedString(instance *unstructured.Unstructured, path string) (string, error) {
	fields, err := splitPath(path)
	if err != nil {
		return "", err
	}
	v, _, err := unstructured.NestedString(instance.Object, fields...)
	if err != nil {
		return "", fmt.Errorf("status path %q is not a string: %w", path, err)
	}
	return strings.TrimSpace(v), nil
}

// splitPath turns a dot-path like "status.controlServiceRef" into its field
// components. A leading "spec."/"status." segment is kept verbatim; the path is
// resolved from the object root, so it can address spec, status, or metadata.
func splitPath(path string) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("empty status path")
	}
	parts := strings.Split(path, ".")
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return nil, fmt.Errorf("malformed status path %q", path)
		}
	}
	return parts, nil
}

// normalizeUpstreamPath ensures the configured upstream path begins with a
// slash; an empty path becomes "/".
func normalizeUpstreamPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}
