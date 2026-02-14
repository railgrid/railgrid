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
