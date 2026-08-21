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

package telemetry

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var installationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

const (
	ModeOff  = "off"
	ModeSaaS = "saas"
)

type Config struct {
	Mode            string
	Endpoint        string
	SinkToken       string
	HMACSecret      string
	InstallationID  string
	QueueSize       int
	BatchSize       int
	FlushInterval   time.Duration
	EnqueueTimeout  time.Duration
	SendTimeout     time.Duration
	ShutdownTimeout time.Duration
	MaxRequestBytes int64
	MaxRetries      int
	InitialBackoff  time.Duration
	HTTPClient      *http.Client
}

func (c Config) withDefaults() Config {
	if c.Mode == "" {
		c.Mode = ModeOff
	}
	if c.QueueSize == 0 {
		c.QueueSize = 1024
	}
	if c.BatchSize == 0 {
		c.BatchSize = 100
	}
	if c.FlushInterval == 0 {
		c.FlushInterval = 2 * time.Second
	}
	if c.EnqueueTimeout == 0 {
		c.EnqueueTimeout = 25 * time.Millisecond
	}
	if c.SendTimeout == 0 {
		c.SendTimeout = 5 * time.Second
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = 5 * time.Second
	}
	if c.MaxRequestBytes == 0 {
		c.MaxRequestBytes = 64 * 1024
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = 3
	}
	if c.InitialBackoff == 0 {
		c.InitialBackoff = 100 * time.Millisecond
	}
	return c
}

func (c Config) validate() error {
	if c.Mode != ModeOff && c.Mode != ModeSaaS {
		return fmt.Errorf("mode must be off or saas: %w", ErrInvalidConfig)
	}
	if c.Mode == ModeOff {
		return nil
	}
	if strings.TrimSpace(c.SinkToken) == "" || strings.TrimSpace(c.HMACSecret) == "" || strings.TrimSpace(c.InstallationID) == "" {
		return fmt.Errorf("saas mode requires sink token, HMAC secret, and installation ID: %w", ErrInvalidConfig)
	}
	if !installationIDPattern.MatchString(c.InstallationID) || strings.ContainsAny(c.SinkToken, "\r\n") {
		return fmt.Errorf("installation ID or sink token is invalid: %w", ErrInvalidConfig)
	}
	u, err := url.Parse(strings.TrimSpace(c.Endpoint))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return fmt.Errorf("saas mode requires an HTTP(S) receiver endpoint: %w", ErrInvalidConfig)
	}
	if c.QueueSize < 1 || c.QueueSize > 65536 || c.BatchSize < 1 || c.BatchSize > 1000 || c.BatchSize > c.QueueSize || c.MaxRequestBytes < 1024 || c.MaxRequestBytes > 1024*1024 {
		return fmt.Errorf("queue, batch, or request bounds are invalid: %w", ErrInvalidConfig)
	}
	for _, d := range []time.Duration{c.FlushInterval, c.EnqueueTimeout, c.SendTimeout, c.ShutdownTimeout, c.InitialBackoff} {
		if d <= 0 || d > time.Minute {
			return fmt.Errorf("telemetry timeout is out of bounds: %w", ErrInvalidConfig)
		}
	}
	if c.MaxRetries < 1 || c.MaxRetries > 10 {
		return fmt.Errorf("max retries must be 1..10: %w", ErrInvalidConfig)
	}
	return nil
}
