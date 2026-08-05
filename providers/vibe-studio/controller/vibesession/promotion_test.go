// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package vibesession

import (
	"strings"
	"testing"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/session"
)

func promoted(phase string, bindings ...vibev1alpha1.ProjectProviderBindingStatus) *vibev1alpha1.Project {
	p := &vibev1alpha1.Project{}
	p.Spec.Environments = []vibev1alpha1.ProjectEnvironmentSpec{
		{Name: "development", Mode: vibev1alpha1.ProjectEnvironmentModeLive},
		{Name: productionEnvironment, Mode: vibev1alpha1.ProjectEnvironmentModeArtifact, Revision: "2613af6c0ff3"},
	}
	if phase != "" {
		p.Status.Environments = []vibev1alpha1.ProjectEnvironmentStatus{
			{Name: productionEnvironment, Phase: phase, Bindings: bindings},
		}
	}
	return p
}

func TestPromotionCheckpointAbsentUntilPromoted(t *testing.T) {
	p := &vibev1alpha1.Project{}
	p.Spec.Environments = []vibev1alpha1.ProjectEnvironmentSpec{{Name: "development"}}
	if _, ok := promotionCheckpoint(p); ok {
		t.Error("an unpromoted project should report no production checkpoint")
	}
}

func TestPromotionCheckpointPendingWhileProvisioning(t *testing.T) {
	cp, ok := promotionCheckpoint(promoted("Provisioning"))
	if !ok {
		t.Fatal("want a checkpoint once production is in the spec")
	}
	if cp.State != session.CheckpointPending {
		t.Errorf("state = %q, want pending", cp.State)
	}
	if !strings.Contains(cp.Reason, "2613af6") {
		t.Errorf("reason = %q, want the short revision", cp.Reason)
	}
}

func TestPromotionCheckpointDoneCarriesURL(t *testing.T) {
	cp, ok := promotionCheckpoint(promoted("Ready",
		vibev1alpha1.ProjectProviderBindingStatus{Phase: "Ready", URL: "https://shop.example"}))
	if !ok {
		t.Fatal("want a checkpoint")
	}
	if cp.State != session.CheckpointDone {
		t.Errorf("state = %q, want done", cp.State)
	}
	if !strings.Contains(cp.Reason, "https://shop.example") || !strings.Contains(cp.Reason, "2613af6") {
		t.Errorf("reason = %q, want the address and the revision", cp.Reason)
	}
}

func TestPromotionCheckpointSurfacesInstanceError(t *testing.T) {
	cp, ok := promotionCheckpoint(promoted("Provisioning",
		vibev1alpha1.ProjectProviderBindingStatus{Phase: "Invalid", Outputs: map[string]string{"error": "image not found"}}))
	if !ok {
		t.Fatal("want a checkpoint")
	}
	if cp.State != session.CheckpointError || cp.Reason != "image not found" {
		t.Errorf("checkpoint = %+v, want the instance error verbatim", cp)
	}
}
