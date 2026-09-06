/*
Copyright 2026 The Faros Authors.

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

package store

import (
	"context"
	"time"
)

type organizationSpendKey struct {
	orgUUID     string
	periodStart time.Time
}

func (s *MemoryStore) AddOrganizationSpend(_ context.Context, orgUUID string, at time.Time, delta OrganizationSpendDelta, now time.Time) (OrganizationSpend, error) {
	orgUUID, periodStart, err := normalizeOrganizationSpendRequest(orgUUID, at, delta)
	if err != nil {
		return OrganizationSpend{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	key := organizationSpendKey{orgUUID: orgUUID, periodStart: periodStart}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.organizationSpend == nil {
		s.organizationSpend = map[organizationSpendKey]OrganizationSpend{}
	}
	spend, ok := s.organizationSpend[key]
	if !ok {
		spend = OrganizationSpend{OrgUUID: orgUUID, PeriodStart: periodStart}
	}
	spend.InputTokens += delta.InputTokens
	spend.OutputTokens += delta.OutputTokens
	spend.USDMicros += delta.USDMicros
	spend.UpdatedAt = now.UTC()
	s.organizationSpend[key] = spend
	return spend, nil
}

func (s *MemoryStore) GetOrganizationSpend(_ context.Context, orgUUID string, at time.Time) (OrganizationSpend, error) {
	orgUUID, periodStart, err := normalizeOrganizationSpendRequest(orgUUID, at, OrganizationSpendDelta{})
	if err != nil {
		return OrganizationSpend{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if spend, ok := s.organizationSpend[organizationSpendKey{orgUUID: orgUUID, periodStart: periodStart}]; ok {
		return spend, nil
	}
	return OrganizationSpend{OrgUUID: orgUUID, PeriodStart: periodStart}, nil
}
