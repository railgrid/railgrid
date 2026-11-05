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

package providers

import (
	"net/http"
	"testing"
)

func TestCopyOrgProviderResponseHeadersAllowlistAndForcedPolicy(t *testing.T) {
	dst := http.Header{
		"Access-Control-Allow-Origin": {"https://console.example"},
		"X-Outer-Middleware":          {"kept"},
		"Content-Security-Policy":     {"unsafe outer value"},
		"Cache-Control":               {"public"},
	}
	src := http.Header{
		"cOnTeNt-TyPe":                        {"application/json"},
		"MCP-SESSION-ID":                      {"session-1"},
		"mcp-protocol-version":                {"2025-06-18"},
		"WWW-AUTHENTICATE":                    {"Basic realm=\"provider\""},
		"Location":                            {"https://provider.example/login"},
		"Set-Cookie":                          {"sid=evil; Path=/"},
		"Set-Cookie2":                         {"sid2=evil"},
		"Clear-Site-Data":                     {"\"cookies\", \"storage\""},
		"Access-Control-Allow-Origin":         {"*"},
		"Strict-Transport-Security":           {"max-age=31536000"},
		"Alt-Svc":                             {"h3=\":443\""},
		"NEL":                                 {"{\"report_to\":\"evil\"}"},
		"Report-To":                           {"{\"group\":\"evil\"}"},
		"Reporting-Endpoints":                 {"default=\"https://evil.example\""},
		"Content-Security-Policy-Report-Only": {"default-src https:"},
		"Trailer":                             {"Set-Cookie"},
		"X-Untrusted-Policy":                  {"anything"},
	}

	CopyOrgProviderResponseHeaders(dst, src)

	for key, want := range map[string]string{
		"Content-Type":                "application/json",
		"Mcp-Session-Id":              "session-1",
		"Mcp-Protocol-Version":        "2025-06-18",
		"Cache-Control":               "private, no-store",
		"X-Content-Type-Options":      "nosniff",
		"Content-Security-Policy":     orgProviderContentSecurityPolicy,
		"X-Outer-Middleware":          "kept",
		"Access-Control-Allow-Origin": "https://console.example",
	} {
		if got := dst.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{
		"Set-Cookie", "Set-Cookie2", "Clear-Site-Data", "Strict-Transport-Security",
		"Alt-Svc", "NEL", "Report-To", "Reporting-Endpoints", "Content-Security-Policy-Report-Only",
		"Trailer", "X-Untrusted-Policy", "Www-Authenticate", "Location",
	} {
		if got := dst.Get(key); got != "" {
			t.Errorf("unsafe upstream header %s leaked as %q", key, got)
		}
	}
}

func TestCopyOrgProviderRedirectSafely(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		location string
		wantLoc  string
	}{
		{name: "relative proxy path", status: http.StatusMovedPermanently, location: "proxy/?tab=1", wantLoc: "proxy/?tab=1"},
		{name: "same-origin absolute path", status: http.StatusFound, location: "/clusters/consumer/login", wantLoc: "/clusters/consumer/login"},
		{name: "external absolute URL", status: http.StatusFound, location: "https://other.example/login"},
		{name: "script scheme URL", status: http.StatusFound, location: "javascript:alert(1)"},
		{name: "network-path reference", status: http.StatusFound, location: "//other.example/login"},
		{name: "backslash path", status: http.StatusFound, location: `\\other.example/login`},
		{name: "encoded backslash path", status: http.StatusFound, location: "/%5C%5Cother.example/login"},
		{name: "non-redirect status", status: http.StatusOK, location: "proxy/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dst := make(http.Header)
			src := http.Header{"Location": {test.location}}
			copyOrgProviderLocation(dst, src, test.status)
			if got := dst.Get("Location"); got != test.wantLoc {
				t.Fatalf("Location = %q, want %q", got, test.wantLoc)
			}
		})
	}
}
