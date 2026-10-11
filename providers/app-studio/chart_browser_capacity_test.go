/*
Copyright 2026 The Railgrid Authors.

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

package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestChartBrowserBudgetFollowsReplicaCapacity(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	for _, tc := range []struct {
		name             string
		values           []string
		budget, capacity string
		reject           bool
	}{
		{name: "default", budget: "8", capacity: "1"},
		{name: "multiple replicas", values: []string{"replicaCount=3"}, budget: "8", capacity: "3"},
		{name: "autoscaling ceiling", values: []string{"replicaCount=2", "browser.replicaCapacity=4", "browser.sessionBudget=12"}, budget: "12", capacity: "4"},
		{name: "small ceiling", values: []string{"replicaCount=3", "browser.replicaCapacity=2"}, reject: true},
		{name: "fractional capacity", values: []string{"browser.replicaCapacity=1.5"}, reject: true},
		{name: "fractional budget", values: []string{"browser.sessionBudget=8.5"}, reject: true},
		{name: "no budget", values: []string{"browser.sessionBudget=0"}, reject: true},
		{name: "insufficient budget", values: []string{"replicaCount=9"}, reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"template", "app-studio", "deploy/chart"}
			for _, v := range tc.values {
				args = append(args, "--set", v)
			}
			raw, err := exec.Command(helm, args...).CombinedOutput()
			if tc.reject {
				if err == nil || !strings.Contains(string(raw), "browser.sessionBudget must cover") {
					t.Fatalf("expected chart budget error: %v %s", err, raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("render: %v %s", err, raw)
			}
			for _, want := range []string{"name: APP_STUDIO_BROWSER_SESSION_BUDGET\n              value: \"" + tc.budget + "\"", "name: APP_STUDIO_BROWSER_REPLICA_CAPACITY\n              value: \"" + tc.capacity + "\"", "strategy:\n    type: Recreate"} {
				if !strings.Contains(string(raw), want) {
					t.Fatalf("missing %q", want)
				}
			}
		})
	}
}
