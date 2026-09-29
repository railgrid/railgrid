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

package client

import (
	"strings"
	"testing"

	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func insecureRef() ServiceRef {
	return ServiceRef{
		Cluster:  "lyl8vgmozoweym52",
		Service:  "dev-edge-server-1-claude",
		EdgeKind: EdgeKindLinuxServer,
		EdgeName: "dev-edge-server-1",
		RunnerID: "dev-edge-server-1-claude",
	}
}

func insecureConfig() *rest.Config {
	return &rest.Config{
		Host:            "https://console.127.0.0.1.sslip.io:9443/clusters/lyl8vgmozoweym52",
		BearerToken:     "provider-token",
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	}
}

// TestUnverifiedTLSIsRefusedUnlessTheCallerSaysItAlreadyUsesIt.
//
// Reported from a development stack: a harness-backed run failed with "verified
// TLS and a dedicated tenant credential are required" because the provider was
// pointed at a self-signed hub. Refusing only this hop did not protect the
// harness credential — every other call that provider makes to the same hub,
// including the ones carrying its own identity, already went the same way — it
// only made the feature unusable there. So the refusal stands by default and the
// caller may say, explicitly, that this is the connection it already has.
func TestUnverifiedTLSIsRefusedUnlessTheCallerSaysItAlreadyUsesIt(t *testing.T) {
	if _, err := New(insecureConfig(), insecureRef()); err == nil {
		t.Fatal("unverified TLS was accepted by default")
	} else if !strings.Contains(err.Error(), "unverified TLS is refused") {
		t.Errorf("the refusal should name unverified TLS: %v", err)
	}

	if _, err := New(insecureConfig(), insecureRef(), AllowInsecureTLS()); err != nil {
		t.Fatalf("an explicit opt-in was still refused: %v", err)
	}
}

// TestTheOptInRelaxesNothingElse: it is about the channel, not about who the
// call is made as. A caller that wanted those relaxed wants a different call.
func TestTheOptInRelaxesNothingElse(t *testing.T) {
	for name, break_ := range map[string]func(*rest.Config){
		"no credential": func(c *rest.Config) { c.BearerToken = "" },
		"impersonation": func(c *rest.Config) { c.Impersonate.UserName = "someone" },
		"basic auth":    func(c *rest.Config) { c.Username, c.Password = "u", "p" },
		"client cert":   func(c *rest.Config) { c.CertData = []byte("cert"); c.KeyData = []byte("key") },
		"exec provider": func(c *rest.Config) { c.ExecProvider = &clientcmdapi.ExecConfig{Command: "helper"} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := insecureConfig()
			break_(cfg)
			if _, err := New(cfg, insecureRef(), AllowInsecureTLS()); err == nil {
				t.Fatalf("AllowInsecureTLS also accepted %s", name)
			}
		})
	}
}

// TestEachRefusalNamesItsOwnFault: one sentence for six unrelated faults told a
// reader that something was wrong without saying what, which is how the reported
// failure above read.
func TestEachRefusalNamesItsOwnFault(t *testing.T) {
	for want, break_ := range map[string]func(*rest.Config){
		"carries no credential":    func(c *rest.Config) { c.BearerToken = "" },
		"impersonation is refused": func(c *rest.Config) { c.Impersonate.UserName = "someone" },
		"basic auth is refused":    func(c *rest.Config) { c.Username, c.Password = "u", "p" },
		"client certificate":       func(c *rest.Config) { c.CertData = []byte("cert") },
		"exec or auth provider":    func(c *rest.Config) { c.AuthProvider = &clientcmdapi.AuthProviderConfig{Name: "oidc"} },
	} {
		t.Run(want, func(t *testing.T) {
			cfg := insecureConfig()
			break_(cfg)
			_, err := New(cfg, insecureRef(), AllowInsecureTLS())
			if err == nil {
				t.Fatal("accepted a config it should refuse")
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the message does not name the fault (%q): %v", want, err)
			}
		})
	}
}
