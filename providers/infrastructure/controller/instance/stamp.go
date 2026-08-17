// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package instance

// Exposure gate + platform stamps. The retired application controller keyed
// this behavior off a hardcoded per-kind table ({oidc, optionalExposure,
// gateRequired} per GVK); with the flattened Instance kind the same
// treatment is derived from the Template itself:
//
//   - oidc            ⇔ the values schema declares an "oidc" object
//   - optionalExposure ⇔ Template.spec.exposure == optional
//   - gateRequired    ⇔ the schema's oidc.mode enum offers no "none" —
//     a workload with no auth of its own must never publish ungated, so an
//     instance hand-edited past its schema is refused here, defence in depth
//   - fqdn / farosCluster / credentialsSecretName stamps ⇔ the schema
//     declares those fields (stamping undeclared fields would desync the
//     stored values from what the runtime CRD prunes)

import (
	"context"
	"encoding/json"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/apps"
	"github.com/faroshq/provider-infrastructure/instancespec"
	"github.com/faroshq/provider-infrastructure/kro"
)

const (
	modeNone     = "none"
	modeBYO      = "byo"
	modePlatform = "platform"
)

// templateTraits is the exposure treatment derived from one Template.
type templateTraits struct {
	publishable      bool // exposure public or optional
	optionalExposure bool // exposure optional: expose.enabled gates publish
	hasFQDN          bool // schema declares expose.fqdn (stampable)
	hasOIDC          bool // schema declares an oidc block
	gateRequired     bool // oidc.mode enum offers no "none"
	hasCredentials   bool // schema declares credentialsSecretName (stampable)
	hasFarosCluster  bool // schema declares farosCluster (stampable)
}

// traitsFor derives the exposure treatment from the Template's effective
// values schema + its declared exposure class.
func traitsFor(tmpl *infrav1alpha1.Template) (templateTraits, error) {
	spec, err := instancespec.EffectiveSchema(tmpl)
	if err != nil {
		return templateTraits{}, err
	}
	t := templateTraits{
		publishable:      tmpl.Spec.Publishable(),
		optionalExposure: tmpl.Spec.ExposureClass() == infrav1alpha1.ExposureOptional,
	}
	if expose, ok := spec.Properties["expose"]; ok {
		_, t.hasFQDN = expose.Properties["fqdn"]
	}
	if oidc, ok := spec.Properties["oidc"]; ok {
		t.hasOIDC = true
		t.gateRequired = !oidcModeAllowsNone(oidc)
	}
	_, t.hasCredentials = spec.Properties["credentialsSecretName"]
	_, t.hasFarosCluster = spec.Properties["farosCluster"]
	return t, nil
}

// oidcModeAllowsNone reports whether the schema's oidc.mode enum admits
// "none". A schema without a mode property (or without an enum) is treated
// as allowing it — gateRequired is a restriction the schema must opt into
// by enumerating modes.
func oidcModeAllowsNone(oidc apiextensionsv1.JSONSchemaProps) bool {
	mode, ok := oidc.Properties["mode"]
	if !ok || len(mode.Enum) == 0 {
		return true
	}
	for _, e := range mode.Enum {
		var s string
		if json.Unmarshal(e.Raw, &s) == nil && s == modeNone {
			return true
		}
	}
	return false
}

// conditionSpec is one provider-owned condition to place on the Instance.
type conditionSpec struct {
	condType string
	status   metav1.ConditionStatus
	reason   string
	message  string
}

func validCondition(status metav1.ConditionStatus, reason, message string) *conditionSpec {
	return &conditionSpec{condType: infrav1alpha1.ConditionInstanceValid, status: status, reason: reason, message: message}
}

// oidcConditionType is the condition reporting OIDC-gate readiness,
// unchanged from the retired application controller so consumers keep
// reading the same signal.
const oidcConditionType = "OIDCConfigured"

func oidcCondition(status metav1.ConditionStatus, reason, message string) *conditionSpec {
	return &conditionSpec{condType: oidcConditionType, status: status, reason: reason, message: message}
}

// exposureOutcome is what applyExposure decided.
type exposureOutcome struct {
	// stampedSpec is true when the Instance's spec.values was updated —
	// the caller must return and let the update event re-drive reconcile.
	stampedSpec bool
	// bridgeOIDC is true when the BYO OIDC client secret must be bridged.
	bridgeOIDC bool
}

// gateDecision is the pure outcome of the gate matrix for one instance
// posture. Exactly one of {cond terminal without stamp, stamp+cond} applies;
// err flags an unknown mode.
type gateDecision struct {
	// cond is the OIDCConfigured condition to report; nil for kinds without
	// an oidc block.
	cond *conditionSpec
	// stamp is whether the platform fields (fqdn, farosCluster, and — when
	// withCredentials — credentialsSecretName) must be stamped.
	stamp bool
	// withCredentials adds the credentialsSecretName stamp.
	withCredentials bool
	// bridgeOIDC is whether the BYO client secret must be bridged.
	bridgeOIDC bool
}

// decideGate runs the gate matrix. Pure so the rules are testable without a
// cluster — they are the difference between "published behind your IdP" and
// "published to everyone", which is not a thing to leave to inspection.
func decideGate(traits templateTraits, exposeEnabled bool, mode string) (gateDecision, error) {
	exposed := traits.publishable && (!traits.optionalExposure || exposeEnabled)

	if !traits.hasOIDC {
		return gateDecision{stamp: exposed && traits.hasFQDN}, nil
	}

	// An optional-exposure instance that has not opted in has no hostname,
	// no route and no oauth2-proxy — nothing to gate and no client secret to
	// bridge. Say so on the instance, because "no OIDC configured" would
	// otherwise read as a half-finished setup rather than the default state.
	if traits.optionalExposure && !exposeEnabled {
		return gateDecision{cond: oidcCondition(metav1.ConditionTrue, "NotExposed",
			"not published on a hostname — reachable only over the platform data plane, authorized per caller")}, nil
	}

	if mode == "" {
		// Matches the template schema default: an instance authored without
		// an oidc block gets the no-gate demo behavior, not a hard error.
		mode = modeNone
	}
	// A workload with no authentication of its own must not be published
	// ungated. Its schema offers no such mode, so reaching here means a
	// hand-edited instance — refuse rather than serve it.
	if traits.gateRequired && mode == modeNone {
		return gateDecision{cond: oidcCondition(metav1.ConditionFalse, "GateRequired",
			"this instance has no authentication of its own, so it cannot be published without an OIDC gate — set oidc.mode=byo or unset expose.enabled")}, nil
	}

	d := gateDecision{stamp: true, withCredentials: traits.hasCredentials}
	switch mode {
	case modeNone:
		d.cond = oidcCondition(metav1.ConditionTrue, "GateDisabled",
			"oidc.mode=none — no auth gate (demo/dev only); anyone with the URL can reach the app")
	case modeBYO:
		d.cond = oidcCondition(metav1.ConditionTrue, "Configured",
			"BYO OIDC client secret bridged from the tenant's cloud-credentials Secret")
		d.bridgeOIDC = true
	case modePlatform:
		// Kept only to fail closed for an instance created from the retired
		// schema value. Platform sign-in belongs to the template access gate.
		d.cond = oidcCondition(metav1.ConditionFalse, "PlatformSSOUnsupported",
			"oidc.mode=platform was retired; configure public or organization invite-only access when publishing the app")
		d.bridgeOIDC = false
	default:
		return gateDecision{}, fmt.Errorf("unknown oidc.mode %q", mode)
	}
	return d, nil
}

// applyExposure runs the gate matrix and stamps the platform-computed
// fields into spec.values. Returns the OIDCConfigured condition to report
// (nil for kinds without an oidc block).
func (c *Controller) applyExposure(ctx context.Context, tenantClient client.Client, tenant string, inst *unstructured.Unstructured, traits templateTraits) (*conditionSpec, exposureOutcome, error) {
	exposeEnabled := nestedBool(inst, "spec", "values", "expose", "enabled")
	mode := nestedString(inst, "spec", "values", "oidc", "mode")

	d, err := decideGate(traits, exposeEnabled, mode)
	if err != nil {
		return nil, exposureOutcome{}, err
	}
	if !d.stamp {
		return d.cond, exposureOutcome{}, nil
	}
	stamped, err := c.stampValues(ctx, tenantClient, tenant, inst, d.withCredentials, traits.hasFarosCluster)
	if err != nil {
		return d.cond, exposureOutcome{}, err
	}
	if stamped {
		return d.cond, exposureOutcome{stampedSpec: true}, nil
	}
	// A PlatformSSOUnsupported outcome must never bridge; the matrix already
	// encoded that in bridgeOIDC.
	return d.cond, exposureOutcome{bridgeOIDC: d.bridgeOIDC}, nil
}

// stampValues computes the fqdn (and, when requested, farosCluster and the
// bridged-Secret name) and writes them into spec.values if not already set.
// Idempotent: a no-op once everything is stamped. Returns whether an update
// was written.
func (c *Controller) stampValues(ctx context.Context, tenantClient client.Client, tenant string, inst *unstructured.Unstructured, withCredentials, withCluster bool) (bool, error) {
	if c.cfg.BaseDomain == "" {
		return false, fmt.Errorf("cannot compute the public hostname: FAROS_APP_BASE_DOMAIN is not configured on this provider")
	}

	prefix := nestedString(inst, "spec", "values", "expose", "hostnamePrefix")
	curFQDN := nestedString(inst, "spec", "values", "expose", "fqdn")

	fqdn, err := apps.Host(prefix, inst.GetName(), tenant, c.cfg.BaseDomain)
	if err != nil {
		return false, fmt.Errorf("computing fqdn: %w", err)
	}

	curCluster := nestedString(inst, "spec", "values", "farosCluster")
	stampCluster := withCluster && curCluster != tenant

	current := curFQDN == fqdn && !stampCluster
	if withCredentials {
		current = current && nestedString(inst, "spec", "values", "credentialsSecretName") == kro.CredentialsSecretName(inst.GetName())
	}
	if current {
		return false, nil
	}
	if err := unstructured.SetNestedField(inst.Object, fqdn, "spec", "values", "expose", "fqdn"); err != nil {
		return false, fmt.Errorf("set spec.values.expose.fqdn: %w", err)
	}
	if stampCluster {
		if err := unstructured.SetNestedField(inst.Object, tenant, "spec", "values", "farosCluster"); err != nil {
			return false, fmt.Errorf("set spec.values.farosCluster: %w", err)
		}
	}
	if withCredentials {
		if err := unstructured.SetNestedField(inst.Object, kro.CredentialsSecretName(inst.GetName()), "spec", "values", "credentialsSecretName"); err != nil {
			return false, fmt.Errorf("set spec.values.credentialsSecretName: %w", err)
		}
	}
	if err := tenantClient.Update(ctx, inst); err != nil {
		return false, fmt.Errorf("stamping spec.values: %w", err)
	}
	return true, nil
}

func nestedString(u *unstructured.Unstructured, fields ...string) string {
	s, _, _ := unstructured.NestedString(u.Object, fields...)
	return s
}

// nestedBool reads an optional boolean, treating absent/wrong-typed as false —
// which for expose.enabled is the safe default: not published.
func nestedBool(u *unstructured.Unstructured, fields ...string) bool {
	b, _, _ := unstructured.NestedBool(u.Object, fields...)
	return b
}
