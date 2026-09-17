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

import "testing"

func TestIsKCPAPIPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/api":                       true, // core discovery root
		"/apis":                      true, // group discovery root
		"/api/v1":                    true,
		"/apis/apis.kcp.io/v1alpha1": true,
		"/clusters/abc/apis":         true,
		"/services/apiexport/abc/x":  true,
		"/apiserver":                 false,
		"/apisx":                     false,
		"/ui/":                       false,
		"/services/providers/code":   false,
		"/":                          false,
	} {
		if got := isKCPAPIPath(path); got != want {
			t.Errorf("isKCPAPIPath(%q) = %v, want %v", path, got, want)
		}
	}
}
