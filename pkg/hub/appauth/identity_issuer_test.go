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

import "testing"

func TestNormalizeIdentityIssuer(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://Hub.Example.com", "https://hub.example.com/auth/apps/identity"},
		{"https://hub.example.com:443", "https://hub.example.com/auth/apps/identity"},
		{" https://Hub.Example.com:443/ ", "https://hub.example.com/auth/apps/identity"},
		{"https://hub.example.com/", "https://hub.example.com/auth/apps/identity"},
		{"https://localhost:9443/", "https://localhost:9443/auth/apps/identity"},
		{"https://[::1]:443/", "https://[::1]/auth/apps/identity"},
		{"http://localhost:9443", ""},
		{"", ""},
		{"https://hub.example.com/prefix", ""},
		{"https://user:pass@hub.example.com", ""},
		{"https://hub.example.com?mode=test", ""},
		{"https://hub.example.com?", ""},
		{"https://hub.example.com#fragment", ""},
		{"https://hub.example.com/%2f", ""},
		{"https://hub.example.com:", ""},
		{"https://hub.example.com:65536", ""},
		{"https://hub.example.com:0", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			issuer, err := NormalizeIdentityIssuer(tc.input)
			if tc.want == "" {
				if err == nil || issuer != "" {
					t.Fatalf("issuer/error=%q/%v, want rejection", issuer, err)
				}
				return
			}
			if err != nil || issuer != tc.want {
				t.Fatalf("issuer/error=%q/%v, want %q", issuer, err, tc.want)
			}
			if err := validateIdentityIssuer(issuer); err != nil {
				t.Fatalf("normalized issuer is invalid: %v", err)
			}
		})
	}
}
