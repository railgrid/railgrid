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

package auth

// DexConfig holds Dex-specific configuration.
type DexConfig struct {
	// Connectors defines the identity provider connectors.
	Connectors []ConnectorConfig `json:"connectors,omitempty"`
}

// ConnectorConfig defines an identity provider connector.
type ConnectorConfig struct {
	Type   string `json:"type"`   // github, google, ldap, etc.
	ID     string `json:"id"`
	Name   string `json:"name"`
	Config map[string]interface{} `json:"config,omitempty"`
}
