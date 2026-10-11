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

package api

import (
	"errors"
	"fmt"
	"testing"
)

func TestBrowserSessionCapacityAllocatesDeploymentBudget(t *testing.T) {
	for _, tc := range []struct {
		name          string
		values        map[string]string
		per, capacity int
		invalid       bool
	}{
		{name: "defaults", per: 8, capacity: 1},
		{name: "three replicas reserve remainder", values: map[string]string{projectAssistantReplicaCountEnv: "3"}, per: 2, capacity: 3},
		{name: "autoscaling ceiling", values: map[string]string{projectAssistantReplicaCountEnv: "2", browserReplicaCapacityEnv: "4", browserSessionBudgetEnv: "12"}, per: 3, capacity: 4},
		{name: "too small ceiling", values: map[string]string{projectAssistantReplicaCountEnv: "3", browserReplicaCapacityEnv: "2"}, invalid: true},
		{name: "too small budget", values: map[string]string{projectAssistantReplicaCountEnv: "9"}, invalid: true},
		{name: "malformed", values: map[string]string{browserSessionBudgetEnv: "many"}, invalid: true},
		{name: "zero", values: map[string]string{browserSessionBudgetEnv: "0"}, invalid: true},
		{name: "negative replicas", values: map[string]string{projectAssistantReplicaCountEnv: "-1"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := ParseBrowserSessionCapacity(func(key string) string { return tc.values[key] })
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid budget accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if config.PerReplica != tc.per || config.ReplicaCapacity != tc.capacity || config.PerReplica*config.ReplicaCapacity > config.Budget {
				t.Fatalf("unexpected allocation: %+v", config)
			}
			// Independent processes each enforce their share of the same backend.
			admitted := 0
			ref := dataPlaneRef{Resource: "instances", Name: "browser", IsolatedMCPContexts: true}
			for replica := 0; replica < config.ReplicaCapacity; replica++ {
				manager := newProjectAssistantBrowserSessionManager()
				defer manager.closeAll()
				manager.sessionLimit = config.PerReplica
				for i := 0; i < config.PerReplica+1; i++ {
					owner := browserSessionOwner{AssistantRunID: fmt.Sprintf("replica-%d-run-%d", replica, i)}
					entry, err := manager.entryWithPolicy(owner, ref)
					if i == config.PerReplica {
						if entry != nil || !errors.Is(err, errProjectAssistantBrowserSessionLimit) {
							t.Fatalf("over-budget admission: %v", err)
						}
						continue
					}
					if err != nil {
						t.Fatal(err)
					}
					admitted++
				}
			}
			if admitted > config.Budget {
				t.Fatalf("admitted %d > budget %d", admitted, config.Budget)
			}
		})
	}
}
