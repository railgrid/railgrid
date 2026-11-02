// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package byolimits bounds organization traffic inside a single embedded kcp
// process. Persistent object counts remain the responsibility of native quotas.
package byolimits

import (
	"fmt"
	"math"
	"time"
)

// Config has no production defaults. The zero value disables the guard; an
// enabled configuration must supply every limit. Limits are per organization,
// shared across the main shard and virtual-workspace handlers in this process.
type Config struct {
	MaxObjectBytes    int64
	MaxRequestBytes   int64
	RequestsPerSecond float64
	Burst             int
	// MaxConcurrentRequests covers all requests, including streams.
	MaxConcurrentRequests int
	// MaxConcurrentStreams is an additional cap within the total above.
	MaxConcurrentStreams int
	// MaxOrganizations bounds lazily allocated limiter accounts in this
	// process, not persisted organization inventory or aggregate storage.
	MaxOrganizations int
	RequestTimeout   time.Duration
	StreamTimeout    time.Duration
}

func (c Config) Enabled() bool { return c != (Config{}) }

func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if c.MaxObjectBytes <= 0 || c.MaxRequestBytes < c.MaxObjectBytes || c.RequestsPerSecond <= 0 || math.IsInf(c.RequestsPerSecond, 0) || math.IsNaN(c.RequestsPerSecond) || c.Burst <= 0 || c.MaxConcurrentRequests <= 0 || c.MaxConcurrentStreams <= 0 || c.MaxOrganizations <= 0 || c.RequestTimeout <= 0 || c.StreamTimeout <= 0 {
		return fmt.Errorf("BYO limits require positive object/request bytes, finite QPS, burst, request/stream concurrency, organization capacity and timeouts; request bytes must cover object bytes")
	}
	return nil
}
