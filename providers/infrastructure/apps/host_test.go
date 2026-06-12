/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package apps

import (
	"strings"
	"testing"

	"github.com/faroshq/provider-infrastructure/kro"
)

func TestHost(t *testing.T) {
	const tenant = "root:kedge:orgs:acme:prod"
	const base = "apps.example.com"
	hash := kro.LabelTenantValue(tenant)

	tests := []struct {
		name     string
		prefix   string
		instance string
		base     string
		want     string
		wantErr  bool
	}{
		{
			name:     "explicit prefix",
			prefix:   "shop",
			instance: "my-app",
			base:     base,
			want:     "shop-" + hash + "." + base,
		},
		{
			name:     "empty prefix defaults to instance name",
			prefix:   "",
			instance: "my-app",
			base:     base,
			want:     "my-app-" + hash + "." + base,
		},
		{
			name:     "uppercase prefix rejected",
			prefix:   "Shop",
			instance: "my-app",
			base:     base,
			wantErr:  true,
		},
		{
			name:     "leading hyphen rejected",
			prefix:   "-shop",
			instance: "my-app",
			base:     base,
			wantErr:  true,
		},
		{
			name:     "underscore rejected",
			prefix:   "my_app",
			instance: "my-app",
			base:     base,
			wantErr:  true,
		},
		{
			name:     "over-long prefix rejected",
			prefix:   strings.Repeat("a", maxPrefixLen+1),
			instance: "my-app",
			base:     base,
			wantErr:  true,
		},
		{
			name:     "max-length prefix accepted",
			prefix:   strings.Repeat("a", maxPrefixLen),
			instance: "my-app",
			base:     base,
			want:     strings.Repeat("a", maxPrefixLen) + "-" + hash + "." + base,
		},
		{
			name:     "empty base domain rejected",
			prefix:   "shop",
			instance: "my-app",
			base:     "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Host(tt.prefix, tt.instance, tenant, tt.base)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Host(%q, %q, _, %q) = %q, want error", tt.prefix, tt.instance, tt.base, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Host(%q, %q, _, %q) unexpected error: %v", tt.prefix, tt.instance, tt.base, err)
			}
			if got != tt.want {
				t.Fatalf("Host = %q, want %q", got, tt.want)
			}
			// The left-most label must be a valid DNS label within 63 chars.
			label := got[:strings.IndexByte(got, '.')]
			if len(label) > 63 {
				t.Fatalf("left-most label %q exceeds 63 chars (%d)", label, len(label))
			}
			if !dnsLabel.MatchString(label) {
				t.Fatalf("left-most label %q is not a valid DNS label", label)
			}
		})
	}
}

func TestHostIsDeterministic(t *testing.T) {
	a, err := Host("shop", "my-app", "root:kedge:orgs:acme", "apps.example.com")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Host("shop", "my-app", "root:kedge:orgs:acme", "apps.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("Host not deterministic: %q != %q", a, b)
	}
}

func TestDifferentTenantsDifferentHost(t *testing.T) {
	a, _ := Host("shop", "my-app", "root:kedge:orgs:acme", "apps.example.com")
	b, _ := Host("shop", "my-app", "root:kedge:orgs:globex", "apps.example.com")
	if a == b {
		t.Fatalf("expected different hosts for different tenants, both = %q", a)
	}
}

func TestURLAndRedirectURL(t *testing.T) {
	const host = "shop-abc123.apps.example.com"
	if got, want := URL(host), "https://shop-abc123.apps.example.com"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
	if got, want := RedirectURL(host), "https://shop-abc123.apps.example.com/oauth2/callback"; got != want {
		t.Fatalf("RedirectURL = %q, want %q", got, want)
	}
}
