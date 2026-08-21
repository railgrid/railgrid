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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	cloudevents "github.com/cloudevents/sdk-go/v2"
)

const cloudEventsBatchContentType = "application/cloudevents-batch+json"
const installationHeader = "X-Faros-Installation-ID"

type HTTPSink struct {
	endpoint, token string
	client          *http.Client
}

func NewHTTPSink(endpoint, token string, client *http.Client) *HTTPSink {
	if client == nil {
		client = &http.Client{}
	}
	cloned := *client
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPSink{endpoint: strings.TrimSpace(endpoint), token: strings.TrimSpace(token), client: &cloned}
}

func (s *HTTPSink) Send(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	installationID := records[0].InstallationID
	events := make([]cloudevents.Event, 0, len(records))
	for _, record := range records {
		if record.InstallationID == "" || record.InstallationID != installationID {
			return fmt.Errorf("CloudEvents batch contains mixed installations")
		}
		e := cloudevents.NewEvent()
		e.SetID(record.EventID)
		e.SetSource("faros://installation/" + record.InstallationID + "/hub")
		e.SetType("dev.faros.telemetry." + record.Action)
		e.SetSubject(record.Provider)
		e.SetTime(record.OccurredAt)
		e.SetExtension("tenant", record.InstallationID)
		if err := e.SetData(cloudevents.ApplicationJSON, record); err != nil {
			return fmt.Errorf("encode CloudEvent: %w", err)
		}
		events = append(events, e)
	}
	payload, err := json.Marshal(events)
	if err != nil {
		return fmt.Errorf("encode CloudEvents batch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", cloudEventsBatchContentType)
	req.Header.Set(installationHeader, installationID)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("close receiver response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("receiver returned status %d", resp.StatusCode)
	}
	return nil
}
