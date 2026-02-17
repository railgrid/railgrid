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

package hub

// Options holds configuration for the hub server.
type Options struct {
	DataDir               string
	ListenAddr            string
	Kubeconfig            string
	ExternalKCPKubeconfig string
	DexIssuerURL          string
	DexClientID           string
	DexClientSecret       string
	ServingCertFile       string
	ServingKeyFile        string
	HubExternalURL        string
	DevMode               bool
}

// NewOptions returns default Options.
func NewOptions() *Options {
	return &Options{
		DataDir:        "/tmp/kedge-data",
		ListenAddr:     ":8443",
		HubExternalURL: "https://localhost:8443",
	}
}
