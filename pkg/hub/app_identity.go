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

package hub

import "github.com/railgrid/railgrid/pkg/hub/appauth"

// configureAppIdentityIssuer keeps optional identity configuration from
// preventing unrelated published-app authentication or the hub from starting.
func configureAppIdentityIssuer(cfg *appauth.Config, hubURL string) error {
	issuer, err := appauth.NormalizeIdentityIssuer(hubURL)
	if err == nil {
		cfg.IdentityIssuer = issuer
		return nil
	}
	cfg.IdentityIssuer = ""
	cfg.IdentityApp = nil
	cfg.IdentityAccountLookup = nil
	cfg.IdentityWorkspaceMember = nil
	cfg.IdentityKeys = nil
	cfg.IdentitySigningKey = nil
	cfg.IdentitySubjectKey = nil
	cfg.IdentityPublicKeys = nil
	return err
}
