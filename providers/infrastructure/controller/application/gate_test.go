// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package application

import "testing"

// The gate matrix decides whether a workload ends up behind an IdP, behind
// nothing, or not published at all. Every row here is a posture, not a branch.
func TestGateFor(t *testing.T) {
	// An always-published app that may legitimately run ungated (demo/dev).
	alwaysPublic := instanceKind{name: "infra-application", oidc: true}
	// A workload with no auth of its own: internal by default, and never
	// publishable without a gate.
	internalFirst := instanceKind{name: "infra-searxng", oidc: true, optionalExposure: true, gateRequired: true}

	for _, tc := range []struct {
		name          string
		kind          instanceKind
		exposeEnabled bool
		mode          string
		wantReason    string // "" = proceed
		wantStatus    string
		wantMode      string
	}{{
		name: "internal-first instance stays internal by default",
		kind: internalFirst, exposeEnabled: false, mode: "",
		// True, not False: this is the recommended state, not a misconfiguration.
		wantReason: "NotExposed", wantStatus: "True",
	}, {
		// The mode is irrelevant while it is not published — no route exists to
		// gate, so there is nothing to complain about.
		name: "internal-first instance ignores oidc mode while unexposed",
		kind: internalFirst, exposeEnabled: false, mode: modeNone,
		wantReason: "NotExposed", wantStatus: "True",
	}, {
		name: "internal-first instance exposed with a gate proceeds",
		kind: internalFirst, exposeEnabled: true, mode: modeBYO,
		wantMode: modeBYO,
	}, {
		// The template's schema offers only "byo", so this is a hand-edited
		// instance. Refusing is the whole point of gateRequired.
		name: "internal-first instance exposed WITHOUT a gate is refused",
		kind: internalFirst, exposeEnabled: true, mode: modeNone,
		wantReason: "GateRequired", wantStatus: "False",
	}, {
		// An omitted oidc block defaults to none — which for a gate-required
		// kind must be refused just as loudly as an explicit one.
		name: "internal-first instance exposed with no oidc block is refused",
		kind: internalFirst, exposeEnabled: true, mode: "",
		wantReason: "GateRequired", wantStatus: "False",
	}, {
		// application is always published; expose.enabled is not its switch, so
		// it must not be short-circuited into NotExposed.
		name: "always-public kind is unaffected by expose.enabled",
		kind: alwaysPublic, exposeEnabled: false, mode: modeBYO,
		wantMode: modeBYO,
	}, {
		name: "always-public kind may still run ungated",
		kind: alwaysPublic, exposeEnabled: false, mode: modeNone,
		wantMode: modeNone,
	}, {
		name: "always-public kind with no oidc block defaults to ungated",
		kind: alwaysPublic, exposeEnabled: false, mode: "",
		wantMode: modeNone,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := gateFor(tc.kind, tc.exposeEnabled, tc.mode)
			if tc.wantReason == "" {
				if got.condition != nil {
					t.Fatalf("condition = %+v, want none (proceed with mode %q)", got.condition, tc.wantMode)
				}
				if got.mode != tc.wantMode {
					t.Fatalf("mode = %q, want %q", got.mode, tc.wantMode)
				}
				return
			}
			if got.condition == nil {
				t.Fatalf("proceeded with mode %q, want a terminal %q condition", got.mode, tc.wantReason)
			}
			if got.condition.reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.condition.reason, tc.wantReason)
			}
			if got.condition.status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got.condition.status, tc.wantStatus)
			}
			if got.condition.message == "" {
				t.Fatal("condition carries no message — the user is told nothing")
			}
		})
	}
}

// The two workloads that motivated all of this must actually carry the flags;
// a registration that silently lost gateRequired would publish an open
// metasearch proxy the moment someone set expose.enabled.
func TestInternalFirstKindsAreRegisteredWithAGate(t *testing.T) {
	want := map[string]bool{"infra-searxng": true, "infra-browser": true}
	seen := map[string]bool{}
	for _, k := range instanceKinds {
		if !want[k.name] {
			continue
		}
		seen[k.name] = true
		if !k.optionalExposure {
			t.Errorf("%s: optionalExposure is false — every instance would be treated as published", k.name)
		}
		if !k.gateRequired {
			t.Errorf("%s: gateRequired is false — it could be published with no authentication at all", k.name)
		}
		if !k.oidc {
			t.Errorf("%s: oidc is false — its client secret would never be bridged, so the gate could not start", k.name)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("%s is not registered in instanceKinds — its fqdn is never stamped, so exposing it yields a route to nowhere", name)
		}
	}
}
