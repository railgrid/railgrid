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

package haclient

import "testing"

func TestSvcTarget(t *testing.T) {
	for _, tt := range []struct{ name, host, want string }{
		{"default", "", "http://127.0.0.1:8123"},
		{"hostname", "example.test", "http://example.test:8123"},
		{"ipv4", "192.0.2.1", "http://192.0.2.1:8123"},
		{"ipv6", "::1", "http://[::1]:8123"},
		{"bracketed ipv6", "[::1]", "http://[::1]:8123"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := (Target{Scheme: "http", Host: tt.host, Port: 8123}).SvcTarget()
			if got != tt.want {
				t.Errorf("SvcTarget() = %q, want %q", got, tt.want)
			}
		})
	}
}
