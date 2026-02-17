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

// Package http provides HTTP utility functions.
package http

import (
	"net/http"
	"net/url"
	"unicode/utf8"
)

// CheckSameOrAllowedOrigin validates the Origin header against allowed origins.
// From https://github.com/gorilla/websocket
func CheckSameOrAllowedOrigin(r *http.Request, validOrigins []url.URL) bool {
	originHeader := r.Header["Origin"]
	if len(originHeader) == 0 {
		return true
	}
	origin, err := url.Parse(originHeader[0])
	if err != nil {
		return false
	}

	if equalASCIIFold(origin.Host, r.Host) {
		return true
	}
	for _, validOrigin := range validOrigins {
		if equalASCIIFold(origin.Host, validOrigin.Host) {
			return true
		}
	}
	return false
}

// equalASCIIFold returns true if s is equal to t with ASCII case folding as
// defined in RFC 4790.
// From https://github.com/gorilla/websocket
func equalASCIIFold(s, t string) bool {
	for s != "" && t != "" {
		sr, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		tr, size := utf8.DecodeRuneInString(t)
		t = t[size:]
		if sr == tr {
			continue
		}
		if 'A' <= sr && sr <= 'Z' {
			sr = sr + 'a' - 'A'
		}
		if 'A' <= tr && tr <= 'Z' {
			tr = tr + 'a' - 'A'
		}
		if sr != tr {
			return false
		}
	}
	return s == t
}
