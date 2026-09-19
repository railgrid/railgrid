// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/railgrid/provider-sdk/vwhealth"
)

func TestStartControllerManagerIsDisabledWithoutAConfig(t *testing.T) {
	if err := startControllerManager(context.Background(), nil, controllerDeps{}, &vwhealth.Readiness{}); !errors.Is(err, errControllerDisabled) {
		t.Fatalf("startControllerManager(nil config) = %v, want errControllerDisabled", err)
	}
}

func TestControllerModeFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want controllerMode
	}{
		{"explicit rest-only", map[string]string{"APP_STUDIO_CONTROLLER_MODE": "rest-only"}, controllerModeRESTOnly},
		{"explicit required", map[string]string{"APP_STUDIO_CONTROLLER_MODE": "required"}, controllerModeRequired},
		{"legacy rest-only flag", map[string]string{"APP_STUDIO_REST_ONLY": "true"}, controllerModeRESTOnly},
		{"kubeconfig in scope", map[string]string{"RAILGRID_PROVIDER_KUBECONFIG": "/tmp/kubeconfig"}, controllerModeRequired},
		{"nothing configured", nil, controllerModeRESTOnly},
		// A typo must fail closed: a pod that meant to run controllers must
		// not quietly advertise a REST-only health contract without them.
		{"unknown mode", map[string]string{"APP_STUDIO_CONTROLLER_MODE": "contoller"}, controllerModeRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"APP_STUDIO_CONTROLLER_MODE", "APP_STUDIO_REST_ONLY", "RAILGRID_PROVIDER_KUBECONFIG", "KUBECONFIG"} {
				t.Setenv(key, "")
			}
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if got := controllerModeFromEnv(); got != tc.want {
				t.Fatalf("controllerModeFromEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A required controller with no credential is the one failure the
// virtual-workspace probe cannot see, so main attaches it to readiness by
// hand. Assert the seam that makes that possible.
func TestCheckerFuncHoldsReadinessDown(t *testing.T) {
	ready := &vwhealth.Readiness{}
	if err := ready.Check(); err != nil {
		t.Fatalf("empty readiness = %v, want ready", err)
	}
	detach := ready.Attach("controllers", checkerFunc(func() error {
		return errors.New("no provider kubeconfig resolved")
	}))
	if err := ready.Check(); err == nil {
		t.Fatal("readiness with a failing controller checker should not be ready")
	}
	detach()
	if err := ready.Check(); err != nil {
		t.Fatalf("readiness after detach = %v, want ready", err)
	}
}
