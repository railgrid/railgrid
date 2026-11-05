// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import "sync"

// Only active organizations are retained. No queue or unbounded identity cache
// is needed: rejected requests consume no work slot, and release removes a key.
type orgBootstrapAdmission struct {
	mu            sync.Mutex
	active        int
	organizations map[string]int
}

func newOrgBootstrapAdmission() *orgBootstrapAdmission {
	return &orgBootstrapAdmission{organizations: map[string]int{}}
}

func (a *orgBootstrapAdmission) acquire(org string) (func(), bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if org == "" || a.active >= 8 || a.organizations[org] >= 2 {
		return nil, false
	}
	a.active++
	a.organizations[org]++
	return sync.OnceFunc(func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.active--
		a.organizations[org]--
		if a.organizations[org] == 0 {
			delete(a.organizations, org)
		}
	}), true
}
