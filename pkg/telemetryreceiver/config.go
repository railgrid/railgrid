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
	"time"
)

type Config struct {
	IngestToken    string
	AdminToken     string
	MaxBatchEvents int
	MaxEventBytes  int
	Logger         *slog.Logger
}

func (c Config) withDefaults() Config {
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
	if c.IngestToken == "" || c.AdminToken == "" || c.IngestToken == c.AdminToken || c.MaxBatchEvents <= 0 || c.MaxEventBytes <= 0 {
		return ErrInvalidConfig
	}
	return nil
}

func validateRetention(raw, aggregate, interval time.Duration) error {
	if raw <= 0 || aggregate <= 0 || interval <= 0 {
		return ErrInvalidConfig
	}
	return nil
}
