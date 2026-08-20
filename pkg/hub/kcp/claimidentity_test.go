/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kcp

import (
	"errors"
	"strings"
	"testing"
)

const (
	platformInfraIdentity = "63be8c1a76ad6131708a5df7f0642887a8ead7a46c8558e9017053693681928b"
	byoInfraIdentity      = "91fbfd3b7f016e07cf3e4a577687c38246e17bf53e4fc77416797259e2f606ff"
	byoInfraPath          = "root:faros:tenants:86b7f9e7:providers:infrastructure"
)

// compareClaims calls the real decision function. Re-implementing it here would
// make every assertion below vacuous — it would pass with the production check
// deleted.
func compareClaims(claims []ProviderClaim, declared map[string]string, serving map[string]servingExport) []ClaimIdentityMismatch {
	return compareClaimIdentities(claims, declared, serving)
}

func infraClaim() ProviderClaim {
	return ProviderClaim{Group: "infrastructure.faros.sh", Resource: "instances", Accepted: true}
}

// The case that motivated this: app-studio pins the PLATFORM infrastructure
// identity, the workspace binds a SELF-HOSTED infrastructure. kcp reports the
// binding healthy and serves none of the claimed resources, so without this
// check the only symptom is a 404 the dependent retries forever.
func TestClaimIdentityMismatchIsDetected(t *testing.T) {
	got := compareClaims(
		[]ProviderClaim{infraClaim()},
		map[string]string{"infrastructure.faros.sh/instances": platformInfraIdentity},
		map[string]servingExport{"infrastructure.faros.sh": {path: byoInfraPath, identity: byoInfraIdentity}},
	)

	if len(got) != 1 {
		t.Fatalf("mismatches = %d, want 1: %+v", len(got), got)
	}
	if got[0].Actual != byoInfraIdentity || got[0].Declared != platformInfraIdentity {
		t.Errorf("mismatch reported the wrong way round: %+v", got[0])
	}
	if got[0].ServingExportPath != byoInfraPath {
		t.Errorf("ServingExportPath = %q, want the export the workspace binds", got[0].ServingExportPath)
	}
}

// The error text has to be actionable on its own — it is what the portal shows
// and what someone pastes into a helm --set.
func TestClaimIdentityMismatchErrorNamesBothSides(t *testing.T) {
	err := &ClaimIdentityMismatchError{
		Provider: "app-studio",
		Mismatches: []ClaimIdentityMismatch{{
			Group: "infrastructure.faros.sh", Resource: "instances",
			Declared: platformInfraIdentity, Actual: byoInfraIdentity, ServingExportPath: byoInfraPath,
		}},
	}

	msg := err.Error()
	for _, want := range []string{"app-studio", "infrastructure.faros.sh/instances", byoInfraPath} {
		if !strings.Contains(msg, want) {
			t.Errorf("message omits %q: %s", want, msg)
		}
	}
	// Both hashes must appear, or the reader cannot tell which end is stale.
	if !strings.Contains(msg, platformInfraIdentity[:12]) || !strings.Contains(msg, byoInfraIdentity[:12]) {
		t.Errorf("message does not show both identities: %s", msg)
	}
	if !errors.Is(err, ErrClaimIdentityMismatch) {
		t.Error("error does not match its sentinel, so the handler will return 500 instead of 409")
	}
}

// Whether a stale pin can be repaired is a function of who owns the export.
// A self-hosted copy serves one org, so repointing it affects only that org.
// A platform copy's single identity per claimed resource belongs to every org
// at once, so repointing it for one silently redirects the rest.
func TestOnlyOrgScopedExportsMayBeRepointed(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{name: "self-hosted org copy", path: byoInfraPath, want: true},
		{name: "org copy of app-studio", path: "root:faros:tenants:86b7f9e7:providers:app-studio", want: true},
		{name: "platform provider", path: "root:faros:providers:app-studio", want: false},
		{name: "platform system workspace", path: "root:faros:system:providers", want: false},
		{name: "empty", path: "", want: false},
		// Must not be fooled by a path that merely starts with the same letters.
		{name: "lookalike sibling of the tenants parent", path: "root:faros:tenantsandthings:providers:x", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := orgScopedExport(tc.path); got != tc.want {
				t.Errorf("orgScopedExport(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// Cases that must NOT be reported: a false positive here blocks Enable on a
// working configuration, which is worse than the bug being fixed.
func TestClaimIdentityMismatchQuietCases(t *testing.T) {
	for _, tc := range []struct {
		name     string
		claims   []ProviderClaim
		declared map[string]string
		serving  map[string]servingExport
	}{{
		name:     "identities agree",
		claims:   []ProviderClaim{infraClaim()},
		declared: map[string]string{"infrastructure.faros.sh/instances": byoInfraIdentity},
		serving:  map[string]servingExport{"infrastructure.faros.sh": {path: byoInfraPath, identity: byoInfraIdentity}},
	}, {
		name:     "claim was rejected by the user, so it grants nothing",
		claims:   []ProviderClaim{{Group: "infrastructure.faros.sh", Resource: "instances", Accepted: false}},
		declared: map[string]string{"infrastructure.faros.sh/instances": platformInfraIdentity},
		serving:  map[string]servingExport{"infrastructure.faros.sh": {path: byoInfraPath, identity: byoInfraIdentity}},
	}, {
		name:     "core types carry no identity by construction",
		claims:   []ProviderClaim{{Group: "", Resource: "secrets", Accepted: true}},
		declared: map[string]string{"/secrets": ""},
		serving:  map[string]servingExport{},
	}, {
		// The dependency gate covers this; treating it as a mismatch would turn
		// a clear "enable X first" into a confusing identity error.
		name:     "group not served in this workspace yet",
		claims:   []ProviderClaim{infraClaim()},
		declared: map[string]string{"infrastructure.faros.sh/instances": platformInfraIdentity},
		serving:  map[string]servingExport{},
	}, {
		// kcp stamps identityHash asynchronously. Mid-provisioning is not a
		// misconfiguration, and failing here would make Enable flaky.
		name:     "serving export exists but its identity is not stamped yet",
		claims:   []ProviderClaim{infraClaim()},
		declared: map[string]string{"infrastructure.faros.sh/instances": platformInfraIdentity},
		serving:  map[string]servingExport{"infrastructure.faros.sh": {path: byoInfraPath, identity: ""}},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareClaims(tc.claims, tc.declared, tc.serving); len(got) != 0 {
				t.Errorf("reported a mismatch on a valid configuration: %+v", got)
			}
		})
	}
}
