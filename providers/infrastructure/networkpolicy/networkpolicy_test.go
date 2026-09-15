/*
Copyright 2026 The Railgrid Authors.

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

package networkpolicy

import (
	"reflect"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"

	"github.com/railgrid/provider-infrastructure/kro"
)

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvEnabled, " true ")
	t.Setenv(EnvAllowedNamespaces, "kube-system, monitoring\nkube-system")
	t.Setenv(EnvAllowedCIDRs, "10.0.0.0/16,,192.168.1.0/24")

	cfg, err := FromEnv(" cfgate-system ")
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	want := Config{
		Enabled:           true,
		GatewayNamespace:  "cfgate-system",
		AllowedNamespaces: []string{"kube-system", "monitoring"},
		AllowedCIDRs:      []string{"10.0.0.0/16", "192.168.1.0/24"},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("FromEnv() = %#v, want %#v", cfg, want)
	}
}

func TestFromEnvDefaultsToDisabled(t *testing.T) {
	t.Setenv(EnvEnabled, "")
	cfg, err := FromEnv("cfgate-system")
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.Enabled {
		t.Fatal("an unset enable switch must leave the policy disabled")
	}
}

func TestFromEnvRejectsUnparseableSwitch(t *testing.T) {
	t.Setenv(EnvEnabled, "yes please")
	if _, err := FromEnv("cfgate-system"); err == nil || !strings.Contains(err.Error(), EnvEnabled) {
		t.Fatalf("FromEnv() error = %v, want one naming %s", err, EnvEnabled)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "disabled ignores a broken allow list", cfg: Config{AllowedNamespaces: []string{"Not_A_Namespace"}, AllowedCIDRs: []string{"nope"}}},
		{name: "valid", cfg: Config{Enabled: true, GatewayNamespace: "cfgate-system", AllowedNamespaces: []string{"kube-system"}, AllowedCIDRs: []string{"10.0.0.0/8", "fd00::/64"}}},
		{name: "invalid namespace", cfg: Config{Enabled: true, AllowedNamespaces: []string{"Kube_System"}}, wantErr: `allowed namespace "Kube_System"`},
		{name: "invalid gateway namespace", cfg: Config{Enabled: true, GatewayNamespace: "bad.namespace"}, wantErr: `allowed namespace "bad.namespace"`},
		{name: "invalid CIDR", cfg: Config{Enabled: true, AllowedCIDRs: []string{"10.0.0.0"}}, wantErr: `allowed CIDR "10.0.0.0" is invalid`},
		{name: "host bits set", cfg: Config{Enabled: true, AllowedCIDRs: []string{"10.1.2.3/8"}}, wantErr: `use "10.0.0.0/8"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestBuild(t *testing.T) {
	const (
		namespace = "1abcdefg-default"
		tenant    = "1abcdefg"
	)
	np := Build(namespace, tenant, Config{
		Enabled:           true,
		GatewayNamespace:  "cfgate-system",
		AllowedNamespaces: []string{"kube-system", "cfgate-system"},
		AllowedCIDRs:      []string{"10.0.0.0/16"},
	})

	if np.Name != Name || np.Namespace != namespace {
		t.Fatalf("policy = %s/%s, want %s/%s", np.Namespace, np.Name, namespace, Name)
	}
	if !IsProviderOwned(np.Labels) {
		t.Fatalf("policy labels %v are not provider-owned", np.Labels)
	}
	if got := np.Labels[kro.LabelTenant]; got != kro.LabelTenantValue(tenant) {
		t.Fatalf("tenant label = %q, want %q", got, kro.LabelTenantValue(tenant))
	}
	if len(np.Spec.PodSelector.MatchLabels) != 0 || len(np.Spec.PodSelector.MatchExpressions) != 0 {
		t.Fatalf("podSelector = %v, want every pod", np.Spec.PodSelector)
	}
	if !reflect.DeepEqual(np.Spec.PolicyTypes, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}) {
		t.Fatalf("policyTypes = %v, want Ingress only", np.Spec.PolicyTypes)
	}
	if len(np.Spec.Egress) != 0 || len(np.Spec.Ingress) != 1 || len(np.Spec.Ingress[0].Ports) != 0 {
		t.Fatalf("rules = ingress %v egress %v, want one port-less ingress rule", np.Spec.Ingress, np.Spec.Egress)
	}

	peers := np.Spec.Ingress[0].From
	var got []string
	for _, peer := range peers {
		switch {
		case peer.IPBlock != nil:
			got = append(got, "cidr:"+peer.IPBlock.CIDR)
		case peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels[namespaceNameLabel] != "":
			got = append(got, "namespace:"+peer.NamespaceSelector.MatchLabels[namespaceNameLabel])
		case peer.NamespaceSelector != nil:
			want := map[string]string{kro.LabelTenant: kro.LabelTenantValue(tenant), kro.LabelManagedBy: kro.ManagedByValue}
			if !reflect.DeepEqual(peer.NamespaceSelector.MatchLabels, want) || peer.PodSelector != nil {
				t.Fatalf("tenant peer = %+v, want namespaces labelled %v", peer, want)
			}
			got = append(got, "tenant")
		case peer.PodSelector != nil:
			if len(peer.PodSelector.MatchLabels) != 0 {
				t.Fatalf("same-namespace peer = %v, want every pod", peer.PodSelector)
			}
			got = append(got, "same-namespace")
		default:
			t.Fatalf("unexpected empty peer %+v", peer)
		}
	}
	// The gateway namespace comes first and is not repeated when it is also
	// in the extra list.
	want := []string{"same-namespace", "tenant", "namespace:cfgate-system", "namespace:kube-system", "cidr:10.0.0.0/16"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("peers = %v, want %v", got, want)
	}
}

func TestBuildWithoutExtraSources(t *testing.T) {
	np := Build("1abcdefg-default", "1abcdefg", Config{Enabled: true})
	if n := len(np.Spec.Ingress[0].From); n != 2 {
		t.Fatalf("peers = %d, want only the same-namespace and same-workspace peers", n)
	}
}

func TestIsProviderOwned(t *testing.T) {
	tests := []struct {
		labels map[string]string
		want   bool
	}{
		{labels: Labels("1abcdefg"), want: true},
		{labels: map[string]string{LabelKey: LabelValue, kro.LabelManagedBy: kro.ManagedByValue}, want: true},
		{labels: map[string]string{LabelKey: LabelValue}},
		{labels: map[string]string{kro.LabelManagedBy: kro.ManagedByValue}},
		{labels: map[string]string{LabelKey: LabelValue, kro.LabelManagedBy: "someone-else"}},
		{labels: nil},
	}
	for _, tt := range tests {
		if got := IsProviderOwned(tt.labels); got != tt.want {
			t.Errorf("IsProviderOwned(%v) = %t, want %t", tt.labels, got, tt.want)
		}
	}
}
