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
	"fmt"
	"strconv"
	"strings"
)

const browserSessionBudgetEnv = "APP_STUDIO_BROWSER_SESSION_BUDGET"
const browserReplicaCapacityEnv = "APP_STUDIO_BROWSER_REPLICA_CAPACITY"

// BrowserSessionCapacity allocates a conservative share of each shared
// backend's managed-session budget to every configured provider replica.
// It is deployment admission policy, not a distributed runtime quota.
type BrowserSessionCapacity struct {
	Budget          int
	ReplicaCapacity int
	PerReplica      int
}

// ParseBrowserSessionCapacity rejects configuration that would silently
// exceed the deployment budget or leave a replica unable to open a session.
// ReplicaCapacity may exceed the current replica count for autoscaling.
func ParseBrowserSessionCapacity(lookup func(string) string) (BrowserSessionCapacity, error) {
	read := func(name string, fallback int) (int, error) {
		raw := strings.TrimSpace(lookup(name))
		if raw == "" {
			return fallback, nil
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return 0, fmt.Errorf("%s must be a positive integer", name)
		}
		return value, nil
	}
	replicas, err := read(projectAssistantReplicaCountEnv, 1)
	if err != nil {
		return BrowserSessionCapacity{}, err
	}
	capacity, err := read(browserReplicaCapacityEnv, replicas)
	if err != nil {
		return BrowserSessionCapacity{}, err
	}
	budget, err := read(browserSessionBudgetEnv, projectAssistantBrowserSessionLimitPerRef)
	if err != nil {
		return BrowserSessionCapacity{}, err
	}
	if capacity < replicas {
		return BrowserSessionCapacity{}, fmt.Errorf("%s must be at least %s", browserReplicaCapacityEnv, projectAssistantReplicaCountEnv)
	}
	if budget < capacity {
		return BrowserSessionCapacity{}, fmt.Errorf("%s must be at least %s so every replica has a session slot", browserSessionBudgetEnv, browserReplicaCapacityEnv)
	}
	return BrowserSessionCapacity{Budget: budget, ReplicaCapacity: capacity, PerReplica: budget / capacity}, nil
}
