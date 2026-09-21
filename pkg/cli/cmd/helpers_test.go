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

package cmd

import "testing"

func TestNormalizeHubURL(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"", ""},
		{"https://console-dev.railgrid.ai", "https://console-dev.railgrid.ai"},
		{"https://console-dev.railgrid.ai/", "https://console-dev.railgrid.ai"},
		{"https://console-dev.railgrid.ai///", "https://console-dev.railgrid.ai"},
		{"console-dev.railgrid.ai/", "https://console-dev.railgrid.ai"},
		{"http://localhost:8080/", "http://localhost:8080"},
		{"https://hub.example.com/prefix///", "https://hub.example.com/prefix"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := normalizeHubURL(tc.input); got != tc.want {
				t.Errorf("normalizeHubURL(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
