// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package kcp

import (
	"reflect"
	"testing"

	serveroptions "github.com/kcp-dev/kcp/pkg/server/options"
)

func TestEmbeddedQuotaBypassRequiresExplicitOption(t *testing.T) {
	t.Setenv("RAILGRID_LOCAL_E2E_BYO_QUOTA_BYPASS", "1")

	defaultKCP := NewEmbeddedKCP(EmbeddedKCPOptions{})
	kcpOptions := serveroptions.NewOptions(t.TempDir())
	before := append([]string(nil), kcpOptions.GenericControlPlane.Admission.GenericAdmission.DisablePlugins...)
	configureLocalE2EQuotaBypass(kcpOptions, defaultKCP.opts.LocalE2EQuotaBypass)
	if !reflect.DeepEqual(before, kcpOptions.GenericControlPlane.Admission.GenericAdmission.DisablePlugins) {
		t.Fatalf("default embedded KCP option changed disabled plugins: %v", kcpOptions.GenericControlPlane.Admission.GenericAdmission.DisablePlugins)
	}

	explicitKCP := NewEmbeddedKCP(EmbeddedKCPOptions{LocalE2EQuotaBypass: true})
	kcpOptions = serveroptions.NewOptions(t.TempDir())
	before = append([]string(nil), kcpOptions.GenericControlPlane.Admission.GenericAdmission.DisablePlugins...)
	configureLocalE2EQuotaBypass(kcpOptions, explicitKCP.opts.LocalE2EQuotaBypass)
	want := append(before, "ResourceQuota", "KCPKubeResourceQuota")
	if got := kcpOptions.GenericControlPlane.Admission.GenericAdmission.DisablePlugins; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit local E2E option disabled plugins %v, want %v", got, want)
	}
}
