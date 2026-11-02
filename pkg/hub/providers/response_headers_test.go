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
		"WWW-AUTHENTICATE":                    {"Bearer realm=\"provider\""},
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
		"Www-Authenticate":            "Bearer realm=\"provider\"",
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
		"Trailer", "X-Untrusted-Policy",
	} {
		if got := dst.Get(key); got != "" {
			t.Errorf("unsafe upstream header %s leaked as %q", key, got)
		}
	}
}
