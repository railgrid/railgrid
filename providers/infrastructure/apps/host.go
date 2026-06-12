/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package apps holds naming rules for Application instances — chiefly the
// deterministic public hostname an app is exposed on. The hostname is
// computed (not stored config) so the same value can be derived in three
// places that must agree without coordinating: the instance controller that
// stamps spec.expose.fqdn, the oauth2-proxy redirect URL baked into the
// running app, and the OAuth2 client redirect URI the tenant registers with
// their IdP up front.
package apps

import (
	"fmt"
	"regexp"

	"github.com/faroshq/provider-infrastructure/kro"
)

// OAuth2CallbackPath is the path oauth2-proxy serves its OIDC callback on.
// Appended to the app URL to form the redirect URI the IdP must allow.
const OAuth2CallbackPath = "/oauth2/callback"

// dnsLabel matches a single RFC-1123 DNS label (lowercase alphanumeric with
// internal hyphens). The full left-most label of the FQDN is
// "<prefix>-<tenantHash>", so we validate the prefix here and length-check
// the composed label in Host.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// tenantHashLen is the width of kro's tenant hash (12 hex chars). The
// composed label "<prefix>-<hash>" must stay within the 63-char DNS label
// limit, so the prefix is bounded at 63 - len(hash) - len("-").
const tenantHashLen = 12

// maxPrefixLen is the longest a hostname prefix may be so the composed
// left-most label "<prefix>-<tenantHash>" fits in 63 chars.
const maxPrefixLen = 63 - tenantHashLen - 1 // 50

// Host returns the deterministic public hostname for an app instance:
//
//	<prefix>-<tenantHash>.<baseDomain>
//
// prefix defaults to instanceName when empty (the common case — the
// Application template's expose.hostnamePrefix is optional). tenantPath is
// the kcp workspace path the instance lives in; its hash is the same one
// kro uses for the per-tenant runtime namespace, so the suffix that appears
// in the hostname also appears in `kubectl get ns` — a deliberate aid to
// tracing an app back to its tenant. baseDomain is the zone apps are served
// under (KEDGE_APP_BASE_DOMAIN, e.g. "apps.example.com"); it is NOT
// re-prefixed with "apps." here — callers pass the full suffix.
func Host(prefix, instanceName, tenantPath, baseDomain string) (string, error) {
	if prefix == "" {
		prefix = instanceName
	}
	if !dnsLabel.MatchString(prefix) {
		return "", fmt.Errorf("hostname prefix %q is not a valid DNS label (lowercase alphanumeric + internal hyphens)", prefix)
	}
	if len(prefix) > maxPrefixLen {
		return "", fmt.Errorf("hostname prefix %q is too long (%d > %d)", prefix, len(prefix), maxPrefixLen)
	}
	if baseDomain == "" {
		return "", fmt.Errorf("base domain is empty (set KEDGE_APP_BASE_DOMAIN)")
	}
	return fmt.Sprintf("%s-%s.%s", prefix, kro.LabelTenantValue(tenantPath), baseDomain), nil
}

// URL is the https URL for a host returned by Host.
func URL(host string) string { return "https://" + host }

// RedirectURL is the OAuth2 redirect URI for a host — the value baked into
// oauth2-proxy and registered with the IdP.
func RedirectURL(host string) string { return URL(host) + OAuth2CallbackPath }
