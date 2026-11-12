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

package restapi

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/google/uuid"
)

func v7At(t *testing.T, at time.Time) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(at.UnixMilli())<<16)
	copy(id[:6], ms[:6])
	return id
}

func TestRequestIDFreshStaysInsideTheGracePeriod(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	grace := 30 * 24 * time.Hour
	for name, tc := range map[string]struct {
		minted time.Time
		fresh  bool
	}{
		"now":                         {now, true},
		"client clock a day behind":   {now.Add(-24 * time.Hour), true},
		"client clock slightly fast":  {now.Add(30 * time.Minute), true},
		"too far in the future":       {now.Add(2 * time.Hour), false},
		"older than grace minus skew": {now.Add(orgRequestIDSkew - grace - time.Second), false},
		"a full grace period old":     {now.Add(-grace), false},
	} {
		if got := requestIDFresh(v7At(t, tc.minted), now, grace); got != tc.fresh {
			t.Errorf("%s: fresh = %v, want %v", name, got, tc.fresh)
		}
	}
	if requestIDFresh(v7At(t, now), now, time.Minute) {
		t.Error("a grace period shorter than the skew allowance must refuse every key")
	}
}
