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

// Package networkpolicy builds the ingress NetworkPolicy that isolates one
// tenant runtime namespace ("<clusterID>-<namespace>") from every other
// workspace's namespaces on the shared runtime cluster.
//
// Without it the runtime cluster is flat: a pod in workspace A can dial
// http://<app>-api.<clusterB>-default.svc.cluster.local and reach workspace
// B's private app directly (bypassing B's access gate), B's Postgres/Redis
// Services, or B's Playwright browser (which accepts any Host). Cluster IDs are
// not secret, so the namespace name is no protection.
//
// The policy selects every pod in the namespace, is Ingress-only (so it never
// interacts with the egress policies the universal coding sandbox ships), and
// admits exactly the legitimate sources:
//
//   - pods in the same namespace — intra-workspace traffic (gate → app,
//     cron-job → api, connections.database/cache) stays exactly as open as
//     before;
//   - pods in the same workspace's other runtime namespaces (a workspace that
//     uses several kcp namespaces), matched by the provider-owned
//     faros.sh/tenant + faros.sh/managed-by namespace labels;
//   - the exposure Gateway's namespace (FAROS_GATEWAY_NAMESPACE): the HTTPRoute
//     → <name>-gate / <name>-oauth hop is dialled by the Gateway's data-plane
//     pods (cfgate's cloudflared, Envoy Gateway's envoy);
//   - operator-configured extra namespaces and CIDRs — in particular whatever
//     source the runtime kube-apiserver's services/proxy hop presents, which the
//     infrastructure data plane (sync/exec/logs/restart/env/proxy) rides on and
//     which is CNI- and topology-specific (node address, overlay tunnel address,
//     konnectivity-agent pod).
//
// Kubelet probes are not affected: NetworkPolicy implementations admit
// traffic from the pod's own node. Whether that also covers an apiserver
// running on that node, or reaching pods through a tunnel, depends on the CNI,
// which is why the services/proxy source is left to AllowedNamespaces and
// AllowedCIDRs.
package networkpolicy

import (
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/faroshq/provider-infrastructure/kro"
)

const (
	// Name is the NetworkPolicy's name in every tenant runtime namespace.
	Name = "faros-tenant-isolation"

	// LabelKey / LabelValue mark the policy as the provider-owned tenant
	// isolation policy, alongside kro.LabelManagedBy. Enabled, the provider
	// owns the name and converges any object called Name onto the desired
	// policy; disabled, it deletes only an object carrying both labels.
	LabelKey   = "faros.sh/network-policy"
	LabelValue = "tenant-isolation"

	// namespaceNameLabel is the immutable label the API server stamps on every
	// Namespace (Kubernetes 1.22+); selecting on it names one namespace.
	namespaceNameLabel = "kubernetes.io/metadata.name"
)

// Environment variables on the provider serve container. The operator copies
// them verbatim from its own environment onto the serve Deployment it owns
// (see operator/serve.go), so both deploy modes read the same names.
const (
	// EnvEnabled turns the policy on ("true") or off ("false", the default).
	// Off also removes a previously created provider-owned policy, so the
	// switch is a reversible kill switch.
	EnvEnabled = "FAROS_TENANT_NETWORK_POLICY_ENABLED"
	// EnvAllowedNamespaces is a comma-separated list of extra namespaces whose
	// pods may reach tenant pods (e.g. a Gateway implementation that runs its
	// proxies outside the Gateway's namespace, kube-system for
	// konnectivity-agent, a monitoring namespace).
	EnvAllowedNamespaces = "FAROS_TENANT_NETWORK_POLICY_ALLOWED_NAMESPACES"
	// EnvAllowedCIDRs is a comma-separated list of CIDRs admitted as ipBlock
	// peers — typically the addresses the runtime kube-apiserver's
	// services/proxy traffic arrives from when it runs on another node.
	EnvAllowedCIDRs = "FAROS_TENANT_NETWORK_POLICY_ALLOWED_CIDRS"
)

// EnvNames lists every variable above, for the operator's passthrough.
var EnvNames = []string{EnvEnabled, EnvAllowedNamespaces, EnvAllowedCIDRs}

// Config is the platform-wide tenant isolation configuration.
type Config struct {
	// Enabled creates/maintains the policy in every tenant runtime namespace.
	// When false the provider removes the policy it previously created.
	Enabled bool
	// GatewayNamespace is the exposure Gateway's namespace
	// (FAROS_GATEWAY_NAMESPACE); always admitted when non-empty.
	GatewayNamespace string
	// AllowedNamespaces are extra namespaces whose pods are admitted.
	AllowedNamespaces []string
	// AllowedCIDRs are extra ipBlock peers.
	AllowedCIDRs []string
}

// FromEnv reads the configuration from the environment. gatewayNamespace is
// the resolved exposure Gateway namespace (FAROS_GATEWAY_NAMESPACE with its
// in-binary default applied).
func FromEnv(gatewayNamespace string) (Config, error) {
	cfg := Config{
		GatewayNamespace:  strings.TrimSpace(gatewayNamespace),
		AllowedNamespaces: splitList(os.Getenv(EnvAllowedNamespaces)),
		AllowedCIDRs:      splitList(os.Getenv(EnvAllowedCIDRs)),
	}
	if raw := strings.TrimSpace(os.Getenv(EnvEnabled)); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q: want true or false", EnvEnabled, raw)
		}
		cfg.Enabled = enabled
	}
	return cfg, nil
}

// Validate rejects an allow list that would render an invalid policy. A
// disabled configuration is always valid: turning the policy off must work
// even while the allow list is being fixed.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	for _, ns := range c.namespaces() {
		if errs := validation.IsDNS1123Label(ns); len(errs) != 0 {
			return fmt.Errorf("tenant network policy: allowed namespace %q is invalid: %s", ns, strings.Join(errs, "; "))
		}
	}
	for _, cidr := range c.AllowedCIDRs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("tenant network policy: allowed CIDR %q is invalid: %w", cidr, err)
		}
		// The API server stores ipBlock.cidr as written and newer releases
		// reject host bits, so require the canonical form: it is also what the
		// converge loop compares against.
		if network.String() != cidr {
			return fmt.Errorf("tenant network policy: allowed CIDR %q is not in canonical form, use %q", cidr, network.String())
		}
	}
	return nil
}

// namespaces returns the gateway namespace followed by the extra namespaces,
// de-duplicated, in a stable order.
func (c Config) namespaces() []string {
	var out []string
	for _, ns := range append([]string{c.GatewayNamespace}, c.AllowedNamespaces...) {
		if ns != "" && !slices.Contains(out, ns) {
			out = append(out, ns)
		}
	}
	return out
}

// Labels are the labels the provider stamps on the policy it owns.
func Labels(tenant string) map[string]string {
	return map[string]string{
		LabelKey:           LabelValue,
		kro.LabelManagedBy: kro.ManagedByValue,
		kro.LabelTenant:    kro.LabelTenantValue(tenant),
	}
}

// IsProviderOwned reports whether labels mark an object as the provider-owned
// isolation policy.
func IsProviderOwned(labels map[string]string) bool {
	return labels[LabelKey] == LabelValue && labels[kro.LabelManagedBy] == kro.ManagedByValue
}

// Build returns the isolation policy for one tenant runtime namespace. tenant
// is the workspace's kcp logical-cluster ID (the runtime namespace prefix).
func Build(namespace, tenant string, cfg Config) *networkingv1.NetworkPolicy {
	peers := []networkingv1.NetworkPolicyPeer{
		// A podSelector without a namespaceSelector selects pods in the
		// policy's own namespace: all intra-namespace traffic stays open.
		{PodSelector: &metav1.LabelSelector{}},
		// The same workspace's other runtime namespaces. Both labels are
		// written by the provider when it creates the namespace; tenants have
		// no way to label runtime-cluster namespaces.
		{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
			kro.LabelTenant:    kro.LabelTenantValue(tenant),
			kro.LabelManagedBy: kro.ManagedByValue,
		}}},
	}
	for _, ns := range cfg.namespaces() {
		peers = append(peers, networkingv1.NetworkPolicyPeer{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{namespaceNameLabel: ns}},
		})
	}
	for _, cidr := range cfg.AllowedCIDRs {
		peers = append(peers, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr}})
	}

	return &networkingv1.NetworkPolicy{
		TypeMeta: metav1.TypeMeta{APIVersion: networkingv1.SchemeGroupVersion.String(), Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      Name,
			Namespace: namespace,
			Labels:    Labels(tenant),
		},
		Spec: networkingv1.NetworkPolicySpec{
			// Every pod in the namespace, including ones created later.
			PodSelector: metav1.LabelSelector{},
			// Ingress only: egress stays governed by the templates' own
			// policies (the coding sandbox's default-deny egress).
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: peers}},
		},
	}
}

// splitList splits a comma- or whitespace-separated list, dropping empties and
// duplicates while keeping the first-seen order.
func splitList(raw string) []string {
	var out []string
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}
