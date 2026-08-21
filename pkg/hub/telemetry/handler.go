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

package telemetry

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

func NewProviderHandler(runtime *Runtime, auth ProviderAuthenticator, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if runtime == nil || !runtime.Enabled() {
			http.NotFound(w, r)
			return
		}
		provider, ok := parseProviderPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if auth == nil || auth.Authenticate(r.Context(), r, provider) != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var event Event
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			status := http.StatusBadRequest
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, "invalid telemetry event", status)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			status := http.StatusBadRequest
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, "invalid telemetry event", status)
			return
		}
		if err := runtime.Track(r.Context(), provider, event); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrQueueFull) {
				status = http.StatusTooManyRequests
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
}

func parseProviderPath(path string) (string, bool) {
	if !strings.HasPrefix(path, ProviderPathPrefix) || !strings.HasSuffix(path, "/telemetry") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(path, ProviderPathPrefix), "/telemetry")
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}
