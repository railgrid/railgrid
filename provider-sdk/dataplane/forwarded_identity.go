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

package dataplane

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func EncodeUpstreamIdentity(h http.Header) (string, error) {
	clean := http.Header{}
	for key, values := range h {
		if key == HeaderRemoteUser || key == HeaderRemoteGroup || strings.HasPrefix(key, HeaderRemoteExtraPrefix) {
			clean[key] = values
		}
	}
	if clean.Get(HeaderRemoteUser) == "" {
		return "", ErrNoCaller
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return "", err
	}
	if len(raw) > 16384 {
		return "", fmt.Errorf("forwarded identity is too large")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func DecodeUpstreamIdentity(value string) (http.Header, error) {
	if len(value) > 22000 {
		return nil, fmt.Errorf("forwarded identity is too large")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	// Re-encoding applies the same allowlist to untrusted envelopes.
	clean := http.Header{}
	for key, values := range h {
		if key != HeaderRemoteUser && key != HeaderRemoteGroup && !strings.HasPrefix(key, HeaderRemoteExtraPrefix) {
			return nil, fmt.Errorf("invalid forwarded identity header")
		}
		for _, v := range values {
			if strings.ContainsAny(v, "\r\n\x00") {
				return nil, fmt.Errorf("invalid forwarded identity value")
			}
		}
		clean[key] = values
	}
	if clean.Get(HeaderRemoteUser) == "" {
		return nil, ErrNoCaller
	}
	return clean, nil
}
