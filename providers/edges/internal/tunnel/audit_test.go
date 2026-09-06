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
	"strings"
	"testing"
)

// TestClientAddrIgnoresForgedForwardedForHops pins the property the SSH audit
// trail depends on: the trusted proxy in front of this handler APPENDS the peer
// it saw to X-Forwarded-For, so only the last hop is trustworthy. Anything to
// its left was chosen by the caller.
func TestClientAddrIgnoresForgedForwardedForHops(t *testing.T) {
	cases := []struct {
		name       string
		xff        string
		remoteAddr string
		want       string
	}{{
		name:       "forged leading hop is ignored, the appended hop wins",
		xff:        "10.0.0.1, 203.0.113.9",
		remoteAddr: "192.0.2.1:9999",
		want:       "203.0.113.9",
	}, {
		name:       "several forged leading hops are ignored",
		xff:        "1.2.3.4, 5.6.7.8,9.10.11.12,  203.0.113.9",
		remoteAddr: "192.0.2.1:9999",
		want:       "203.0.113.9",
	}, {
		name:       "junk in a leading hop does not poison the result",
		xff:        "not-an-ip\nfake audit line, 203.0.113.9",
		remoteAddr: "192.0.2.1:9999",
		want:       "203.0.113.9",
	}, {
		name:       "a single hop is the appended one",
		xff:        "203.0.113.9",
		remoteAddr: "192.0.2.1:9999",
		want:       "203.0.113.9",
	}, {
		name:       "bracketed IPv6 hop",
		xff:        "10.0.0.1, [2001:db8::2]",
		remoteAddr: "192.0.2.1:9999",
		want:       "2001:db8::2",
	}, {
		name:       "junk in the appended hop yields the placeholder, never the junk",
		xff:        "203.0.113.9, ../../etc/passwd",
		remoteAddr: "192.0.2.1:9999",
		want:       unknownAddr,
	}, {
		name:       "no header falls back to the peer, port stripped",
		remoteAddr: "192.0.2.7:53124",
		want:       "192.0.2.7",
	}, {
		name:       "no header, IPv6 peer, port stripped",
		remoteAddr: "[2001:db8::1]:443",
		want:       "2001:db8::1",
	}, {
		name:       "no header, peer without a port",
		remoteAddr: "192.0.2.7",
		want:       "192.0.2.7",
	}, {
		name:       "whitespace-only header falls back to the peer",
		xff:        "   ",
		remoteAddr: "192.0.2.7:53124",
		want:       "192.0.2.7",
	}, {
		name:       "unparseable peer yields the placeholder",
		remoteAddr: "not-an-address",
		want:       unknownAddr,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{Header: http.Header{}, RemoteAddr: tc.remoteAddr}
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := clientAddr(r); got != tc.want {
				t.Fatalf("clientAddr = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSanitizeAuditValueCannotForgeALogLine covers the exec command, which
// arrives as a raw query parameter and lands in a V(0) audit line.
func TestSanitizeAuditValueCannotForgeALogLine(t *testing.T) {
	forged := "uptime\n\"SSH session opened\" caller=\"root\" edge=\"other\""
	got := sanitizeAuditValue(forged)
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("sanitized value still carries a line break: %q", got)
	}
	if !strings.Contains(got, `\u000a`) {
		t.Fatalf("newline was dropped rather than escaped: %q", got)
	}
	if !strings.Contains(got, "uptime") || !strings.Contains(got, "SSH session opened") {
		t.Fatalf("sanitized value is not a faithful rendering: %q", got)
	}

	for _, ctrl := range []string{"\r", "\t", "\x00", "\x1b[2J", "\u2028"} {
		s := sanitizeAuditValue("a" + ctrl + "b")
		if strings.Contains(s, ctrl) {
			t.Fatalf("control sequence %q survived sanitization: %q", ctrl, s)
		}
	}

	if got := sanitizeAuditValue(`a\b`); got != `a\\b` {
		t.Fatalf("backslash not escaped: %q", got)
	}

	// Ordinary commands, including non-ASCII text, pass through untouched.
	for _, ok := range []string{"", "systemctl restart nginx", "echo 'héllo wörld'"} {
		if got := sanitizeAuditValue(ok); got != ok {
			t.Fatalf("sanitizeAuditValue(%q) = %q, want it unchanged", ok, got)
		}
	}
}

// TestAuditSSHUserCannotForgeALogLine: the SSH username in the "SSH session
// opened" audit line comes from the agent-reported sshCredentials, and klog
// renders a string carrying "\n" as an unquoted multi-line block.
func TestAuditSSHUserCannotForgeALogLine(t *testing.T) {
	forged := "root\n\"SSH session opened\" caller=\"eve\" edge=\"other\""
	got := auditSSHUser(&SSHClientCredentials{Username: forged})
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("audit username still carries a line break: %q", got)
	}
	if !strings.Contains(got, `\u000a`) || !strings.Contains(got, "root") {
		t.Fatalf("audit username is not a faithful escaped rendering: %q", got)
	}
	if got := auditSSHUser(&SSHClientCredentials{Username: "deploy"}); got != "deploy" {
		t.Fatalf("ordinary username changed: %q", got)
	}
	if got := auditSSHUser(nil); got != "root" {
		t.Fatalf("nil credentials: %q, want the default user", got)
	}
}

func TestSanitizeAuditValueTruncatesAndMarksIt(t *testing.T) {
	long := strings.Repeat("a", maxAuditValueLen*3)
	got := sanitizeAuditValue(long)
	if !strings.HasSuffix(got, auditTruncationMarker) {
		t.Fatalf("truncation is not marked: %q", got)
	}
	if want := maxAuditValueLen + len(auditTruncationMarker); len(got) != want {
		t.Fatalf("truncated length = %d, want %d", len(got), want)
	}
	if s := sanitizeAuditValue(strings.Repeat("a", maxAuditValueLen)); strings.Contains(s, auditTruncationMarker) {
		t.Fatalf("a value at the cap was marked truncated: %q", s)
	}
}
