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

package appauth

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// NormalizeIdentityIssuer converts a public hub origin into the canonical
// HTTPS issuer used by application assertions. It does not alter other hub URL
// consumers or allow identity assertions over HTTP.
func NormalizeIdentityIssuer(hubURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(hubURL))
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.Trim(u.Path, "/") != "" {
		return "", fmt.Errorf("hub external URL must be an HTTPS origin")
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("hub external URL has an empty port")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("hub external URL has an invalid port")
		}
	}
	origin, err := canonicalHTTPSOrigin(u)
	if err != nil {
		return "", err
	}
	return origin + IdentityIssuerPath, nil
}
