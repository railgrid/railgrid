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

package tunnel

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServiceOnlyRouterDisablesSSHAndKubernetesProxy(t *testing.T) {
	router := setupRouter(nil, 0, SvcProxyOptions{})

	tests := []struct {
		name string
		path string
		want int
	}{
		{name: "ssh", path: "/ssh", want: http.StatusNotImplemented},
		{name: "kubernetes", path: "/k8s/api", want: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != tt.want {
				t.Fatalf("GET %s status = %d, want %d; body=%q", tt.path, recorder.Code, tt.want, recorder.Body.String())
			}
		})
	}
}
