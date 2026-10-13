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

// Package huborigin canonicalizes the hub origins embedded in provider
// templates and consumed by the infrastructure access proxy.
package huborigin

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Normalize canonicalizes the scheme, hostname, and default port of a hub
// URL. Paths are retained for compatibility with existing hub URL consumers;
// callers that require an origin-only URL must validate that separately.
// A trailing slash is removed from the rendered URL, matching the access
// proxy's existing path-join behavior.
func Normalize(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("malformed origin")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("origin must use http or https")
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("origin has an empty port")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("origin has an invalid port")
		}
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("malformed origin")
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	u.Scheme = scheme
	u.Host = host
	return strings.TrimRight(u.String(), "/"), nil
}
