// Copyright 2026 The Faros Authors.
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

package telemetryreceiver

import (
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/faroshq/faros/telemetry/generated"
)

type Config struct {
	// IngestTokens binds each opaque hub installation ID to its own bearer.
	// Sharing one token across installations would let any producer forge
	// another installation's otherwise-valid pseudonymous events.
	IngestTokens   map[string]string
	AdminToken     string
	MaxBatchEvents int
	MaxEventBytes  int
	Logger         *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.IngestTokens != nil {
		cloned := make(map[string]string, len(c.IngestTokens))
		for installationID, token := range c.IngestTokens {
			cloned[installationID] = token
		}
		c.IngestTokens = cloned
	}
	if c.MaxBatchEvents == 0 {
		c.MaxBatchEvents = 1000
	}
	if c.MaxEventBytes == 0 {
		c.MaxEventBytes = 256 * 1024
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

func (c Config) validate() error {
	if len(c.IngestTokens) == 0 || len(c.AdminToken) < 16 || tokenHasSpaceOrControl(c.AdminToken) || c.MaxBatchEvents <= 0 || c.MaxEventBytes <= 0 {
		return ErrInvalidConfig
	}
	seenTokens := make(map[string]struct{}, len(c.IngestTokens))
	for installationID, token := range c.IngestTokens {
		normalized, ok := normalizeTenantID(installationID)
		if !ok || normalized != installationID || len(token) < 16 || tokenHasSpaceOrControl(token) || token == c.AdminToken {
			return ErrInvalidConfig
		}
		if _, duplicate := seenTokens[token]; duplicate {
			return ErrInvalidConfig
		}
		seenTokens[token] = struct{}{}
	}
	return nil
}

func tokenHasSpaceOrControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0
}

func validateRetention(raw, aggregate, interval time.Duration) error {
	if raw <= 0 || aggregate <= 0 || interval <= 0 {
		return ErrInvalidConfig
	}
	for _, metric := range generated.MustRegistry().Metrics {
		if metric.Status != "active" {
			continue
		}
		days, err := metricWindowDays(metric.TimeFrame)
		if err != nil {
			return ErrInvalidConfig
		}
		minimum := time.Duration(days) * 24 * time.Hour
		if days > 0 && ((metric.MetricKind == "funnel" && raw < minimum) || (metric.MetricKind == "counter" && aggregate < minimum)) {
			return ErrInvalidConfig
		}
	}
	return nil
}

// ValidateRetention verifies that operator limits can represent every active
// catalog window before the receiver begins serving.
func ValidateRetention(raw, aggregate, interval time.Duration) error {
	return validateRetention(raw, aggregate, interval)
}
