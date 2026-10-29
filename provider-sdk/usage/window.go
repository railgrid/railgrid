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

package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// DefaultGranularity is the grid windows are aligned to when Options leaves
// it unset: the hour, top of the hour UTC, which is what the metering
// pipeline expects by default (contrib-metering docs/usage-contract.md §2).
const DefaultGranularity = time.Hour

// AlignWindow returns the half-open grid window [start, end) that contains
// t. It is the same arithmetic as contrib-metering's
// v1alpha1.AlignWindow, kept here so a provider does not import the
// metering module for two lines; the window grid is part of the contract,
// not of the module.
func AlignWindow(t time.Time, granularity time.Duration) (start, end time.Time) {
	if granularity <= 0 {
		granularity = DefaultGranularity
	}
	t = t.UTC()
	start = t.Truncate(granularity)
	return start, start.Add(granularity)
}

// RecordName is the deterministic UsageRecord name for one series and
// window, the idempotency key that makes a re-emit an upsert
// (docs/usage-contract.md §3):
//
//	"u-" + sha256(subjectCluster | meter | start | end)[:32 hex]
//
// with the times in RFC 3339 UTC. It must stay byte-for-byte equal to
// contrib-metering's v1alpha1.UsageRecordName, because the metering
// controller's GC and restatement rely on the same name for the same
// series; TestRecordNameMatchesContract pins the formula.
func RecordName(subjectCluster, meter string, start, end time.Time) string {
	h := sha256.Sum256([]byte(subjectCluster + "|" + meter + "|" +
		start.UTC().Format(time.RFC3339) + "|" + end.UTC().Format(time.RFC3339)))
	return "u-" + hex.EncodeToString(h[:])[:32]
}
