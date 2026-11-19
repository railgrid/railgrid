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

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// umamiSink delivers events to an Umami instance's collection endpoint
// (POST {url}/api/send), the same endpoint Umami's browser tracker uses.
type umamiSink struct {
	endpoint string
	website  string
	hostname string
	client   *http.Client
}

// NewUmamiSink returns the sink for an Umami backend.
func NewUmamiSink(cfg Config) Sink {
	return &umamiSink{
		endpoint: strings.TrimRight(cfg.URL, "/") + "/api/send",
		website:  cfg.WebsiteID,
		hostname: cfg.Hostname,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

// umamiPayload is the subset of Umami's collection payload the hub fills in.
//
// ip and userAgent are the browser's, not the hub's: Umami hashes them (with
// a rotating salt) into its anonymous session ID and does not store the
// address. Sending the browser's values is what makes a console session the
// same session as that visitor's on the marketing site when both report to
// one website ID. No distinct ID is sent, so nothing here names a user.
type umamiPayload struct {
	Website   string            `json:"website"`
	Hostname  string            `json:"hostname,omitempty"`
	URL       string            `json:"url"`
	Title     string            `json:"title,omitempty"`
	Referrer  string            `json:"referrer,omitempty"`
	Screen    string            `json:"screen,omitempty"`
	Language  string            `json:"language,omitempty"`
	Name      string            `json:"name,omitempty"`
	Data      map[string]string `json:"data,omitempty"`
	IP        string            `json:"ip,omitempty"`
	UserAgent string            `json:"userAgent,omitempty"`
}

func (s *umamiSink) Send(ctx context.Context, client Client, event Event) error {
	payload := umamiPayload{
		Website:   s.website,
		Hostname:  s.hostname,
		URL:       event.Route,
		Referrer:  event.Referrer,
		Screen:    event.Screen,
		Language:  event.Language,
		IP:        client.IP,
		UserAgent: client.UserAgent,
	}
	if event.Type == "page" {
		// Umami records an event without a name as a page view; the route
		// name stands in for the title, which the portal never sends.
		payload.Title = event.Name
	} else {
		payload.Name = event.Name
		payload.Data = event.Props
	}
	body, err := json.Marshal(map[string]any{"type": "event", "payload": payload})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Umami discards requests whose User-Agent looks like a bot, and Go's
	// default one does.
	req.Header.Set("User-Agent", client.UserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("umami answered %s", resp.Status)
	}
	return nil
}
