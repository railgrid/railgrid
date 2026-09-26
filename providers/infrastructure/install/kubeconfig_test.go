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

package install

import "testing"

// The minted runtime kubeconfig carries the same trust the bootstrap used. A
// dev bootstrap against the embedded kcp runs with insecure-skip-tls-verify
// and no CA; a runtime kubeconfig that dropped the flag would verify the
// self-signed cert against the system roots and never connect.
func TestRuntimeKubeconfigMirrorsTheBootstrapTrust(t *testing.T) {
	insecure := buildRuntimeKubeconfig(&RuntimeIdentity{Server: "https://localhost:6443/clusters/root:p", Insecure: true, ServiceAccount: "sa", Namespace: "default", Token: "t"})
	cluster := insecure.Clusters["provider-workspace"]
	if !cluster.InsecureSkipTLSVerify || len(cluster.CertificateAuthorityData) != 0 {
		t.Fatalf("insecure bootstrap → cluster = %+v, want insecure-skip-tls-verify and no CA", cluster)
	}
	verified := buildRuntimeKubeconfig(&RuntimeIdentity{Server: "https://kcp:6443/clusters/root:p", CAData: []byte("ca"), ServiceAccount: "sa", Namespace: "default", Token: "t"})
	cluster = verified.Clusters["provider-workspace"]
	if cluster.InsecureSkipTLSVerify || string(cluster.CertificateAuthorityData) != "ca" {
		t.Fatalf("verified bootstrap → cluster = %+v, want the CA and no insecure flag", cluster)
	}
}
