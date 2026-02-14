package http

import (
	"net/http"
	"net/url"
	"testing"
)

func TestCheckSameOrAllowedOrigin(t *testing.T) {
	tests := []struct {
		name         string
		origin       string
		host         string
		validOrigins []url.URL
		want         bool
	}{
		{
			name: "no origin header",
			host: "example.com",
			want: true,
		},
		{
			name:   "same origin",
			origin: "http://example.com",
			host:   "example.com",
			want:   true,
		},
		{
			name:   "different origin, not in allowed list",
			origin: "http://evil.com",
			host:   "example.com",
			want:   false,
		},
		{
			name:         "different origin, in allowed list",
			origin:       "http://trusted.com",
			host:         "example.com",
			validOrigins: []url.URL{{Host: "trusted.com"}},
			want:         true,
		},
		{
			name:   "case insensitive match",
			origin: "http://EXAMPLE.COM",
			host:   "example.com",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &http.Request{
				Host:   tt.host,
				Header: http.Header{},
			}
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}

			got := CheckSameOrAllowedOrigin(r, tt.validOrigins)
			if got != tt.want {
				t.Errorf("CheckSameOrAllowedOrigin() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEqualASCIIFold(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"abc", "abc", true},
		{"ABC", "abc", true},
		{"abc", "ABC", true},
		{"abc", "def", false},
		{"", "", true},
		{"a", "", false},
	}

	for _, tt := range tests {
		got := equalASCIIFold(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("equalASCIIFold(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
