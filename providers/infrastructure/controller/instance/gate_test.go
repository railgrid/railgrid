// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package instance

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// The gate matrix decides whether a workload ends up behind an IdP, behind
// nothing, or not published at all. Every row here is a posture, not a
// branch. Ported from the retired application controller's gate test — the
// per-kind flags are now traits derived from the Template.
func TestDecideGate(t *testing.T) {
	// An always-published app that may legitimately run ungated (demo/dev):
	// the application template's shape.
	alwaysPublic := templateTraits{
		publishable: true, hasFQDN: true, hasOIDC: true, hasCredentials: true, hasFarosCluster: true,
	}
	// A workload with no auth of its own: internal by default, and never
	// publishable without a gate — the searxng/browser shape.
	internalFirst := templateTraits{
		publishable: true, optionalExposure: true, hasFQDN: true, hasOIDC: true,
		gateRequired: true, hasCredentials: true,
	}
	// An exposure-only workload with no oidc block — the simple-webapp shape.
	exposureOnly := templateTraits{publishable: true, hasFQDN: true, hasFarosCluster: true}
	// An internal workload — the worker/database shape.
	internal := templateTraits{}

	for _, tc := range []struct {
		name          string
		traits        templateTraits
		exposeEnabled bool
		mode          string
		wantReason    string // "" = no OIDCConfigured condition expected
		wantStatus    metav1.ConditionStatus
		wantStamp     bool
		wantBridge    bool
	}{{
		name:   "internal-first instance stays internal by default",
		traits: internalFirst, exposeEnabled: false, mode: "",
		// True, not False: this is the recommended state, not a misconfiguration.
		wantReason: "NotExposed", wantStatus: metav1.ConditionTrue,
	}, {
		// The mode is irrelevant while it is not published — no route exists
		// to gate, so there is nothing to complain about.
		name:   "internal-first instance ignores oidc mode while unexposed",
		traits: internalFirst, exposeEnabled: false, mode: modeNone,
		wantReason: "NotExposed", wantStatus: metav1.ConditionTrue,
	}, {
		name:   "internal-first instance exposed with a gate proceeds",
		traits: internalFirst, exposeEnabled: true, mode: modeBYO,
		wantReason: "Configured", wantStatus: metav1.ConditionTrue, wantStamp: true, wantBridge: true,
	}, {
		// The template's schema offers only "byo", so this is a hand-edited
		// instance. Refusing is the whole point of gateRequired.
		name:   "internal-first instance exposed WITHOUT a gate is refused",
		traits: internalFirst, exposeEnabled: true, mode: modeNone,
		wantReason: "GateRequired", wantStatus: metav1.ConditionFalse,
	}, {
		// An omitted oidc block defaults to none — which for a gate-required
		// kind must be refused just as loudly as an explicit one.
		name:   "internal-first instance exposed with no oidc block is refused",
		traits: internalFirst, exposeEnabled: true, mode: "",
		wantReason: "GateRequired", wantStatus: metav1.ConditionFalse,
	}, {
		// application is always published; expose.enabled is not its switch,
		// so it must not be short-circuited into NotExposed.
		name:   "always-public kind is unaffected by expose.enabled",
		traits: alwaysPublic, exposeEnabled: false, mode: modeBYO,
		wantReason: "Configured", wantStatus: metav1.ConditionTrue, wantStamp: true, wantBridge: true,
	}, {
		name:   "always-public kind may run ungated",
		traits: alwaysPublic, exposeEnabled: false, mode: "",
		wantReason: "GateDisabled", wantStatus: metav1.ConditionTrue, wantStamp: true,
	}, {
		// The retired platform mode fails closed but still stamps: the
		// instance keeps its hostname while the condition explains the fix.
		name:   "platform mode is refused but not stripped",
		traits: alwaysPublic, exposeEnabled: false, mode: modePlatform,
		wantReason: "PlatformSSOUnsupported", wantStatus: metav1.ConditionFalse, wantStamp: true,
	}, {
		name:   "exposure-only kind stamps and reports nothing",
		traits: exposureOnly, exposeEnabled: false, mode: "",
		wantStamp: true,
	}, {
		name:   "internal kind neither stamps nor reports",
		traits: internal, exposeEnabled: false, mode: "",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := decideGate(tc.traits, tc.exposeEnabled, tc.mode)
			if err != nil {
				t.Fatalf("decideGate: %v", err)
			}
			if tc.wantReason == "" {
				if d.cond != nil {
					t.Fatalf("expected no OIDC condition; got %+v", d.cond)
				}
			} else {
				if d.cond == nil || d.cond.reason != tc.wantReason || d.cond.status != tc.wantStatus {
					t.Fatalf("condition = %+v, want reason=%s status=%s", d.cond, tc.wantReason, tc.wantStatus)
				}
			}
			if d.stamp != tc.wantStamp {
				t.Errorf("stamp = %v, want %v", d.stamp, tc.wantStamp)
			}
			if d.bridgeOIDC != tc.wantBridge {
				t.Errorf("bridgeOIDC = %v, want %v", d.bridgeOIDC, tc.wantBridge)
			}
		})
	}

	if _, err := decideGate(alwaysPublic, true, "bogus"); err == nil {
		t.Error("expected an error for an unknown oidc.mode")
	}
}

// TestTraitsFor pins the schema→traits derivation against the shapes the
// seed templates use.
func TestTraitsFor(t *testing.T) {
	mk := func(t *testing.T, exposure infrav1alpha1.TemplateExposure, schema map[string]any) *infrav1alpha1.Template {
		t.Helper()
		raw, err := json.Marshal(schema)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return &infrav1alpha1.Template{
			Spec: infrav1alpha1.TemplateSpec{
				Exposure: exposure,
				Schema:   &runtime.RawExtension{Raw: raw},
			},
		}
	}

	// searxng/browser shape: optional exposure, byo-only oidc.
	searxng := mk(t, infrav1alpha1.ExposureOptional, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"expose": map[string]any{"type": "object", "properties": map[string]any{
				"enabled": map[string]any{"type": "boolean"},
				"fqdn":    map[string]any{"type": "string"},
			}},
			"oidc": map[string]any{"type": "object", "properties": map[string]any{
				"mode": map[string]any{"type": "string", "enum": []any{"byo"}},
			}},
			"credentialsSecretName": map[string]any{"type": "string"},
		},
	})
	tr, err := traitsFor(searxng)
	if err != nil {
		t.Fatalf("traitsFor(searxng): %v", err)
	}
	want := templateTraits{publishable: true, optionalExposure: true, hasFQDN: true, hasOIDC: true, gateRequired: true, hasCredentials: true}
	if tr != want {
		t.Errorf("searxng traits = %+v, want %+v", tr, want)
	}

	// application shape: public, oidc mode allows none.
	application := mk(t, infrav1alpha1.ExposurePublic, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"expose": map[string]any{"type": "object", "properties": map[string]any{
				"fqdn": map[string]any{"type": "string"},
			}},
			"oidc": map[string]any{"type": "object", "properties": map[string]any{
				"mode": map[string]any{"type": "string", "enum": []any{"none", "byo"}},
			}},
			"credentialsSecretName": map[string]any{"type": "string"},
			"farosCluster":          map[string]any{"type": "string"},
		},
	})
	tr, err = traitsFor(application)
	if err != nil {
		t.Fatalf("traitsFor(application): %v", err)
	}
	want = templateTraits{publishable: true, hasFQDN: true, hasOIDC: true, hasCredentials: true, hasFarosCluster: true}
	if tr != want {
		t.Errorf("application traits = %+v, want %+v", tr, want)
	}

	// worker shape: internal, nothing declared.
	worker := mk(t, infrav1alpha1.ExposureInternal, map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "string"}},
	})
	tr, err = traitsFor(worker)
	if err != nil {
		t.Fatalf("traitsFor(worker): %v", err)
	}
	if tr != (templateTraits{}) {
		t.Errorf("worker traits = %+v, want zero", tr)
	}
}
