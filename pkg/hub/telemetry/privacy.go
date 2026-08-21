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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type normalizer struct {
	key            []byte
	installationID string
}

func (n normalizer) record(provider string, e Event) (Record, error) {
	eventID, err := newEventID()
	if err != nil {
		return Record{}, err
	}
	return Record{
		EventID:        eventID,
		InstallationID: n.installationID,
		Provider:       provider,
		Action:         e.Action,
		OccurredAt:     e.OccurredAt.UTC(),
		Identifiers: Identifiers{
			Org: n.hash("org", e.OrgID), Workspace: n.hash("workspace", e.WorkspaceID),
			Project: n.hash("project", e.ProjectID), Resource: n.hash("resource", e.ResourceID),
			Actor: n.hash("actor", e.Actor),
		},
		Properties: e.Properties,
	}, nil
}

func newEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate telemetry event ID: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func (n normalizer) hash(kind, raw string) string {
	if raw == "" {
		return ""
	}
	h := hmac.New(sha256.New, n.key)
	_, _ = h.Write([]byte("faros-telemetry-v1\x00" + kind + "\x00" + raw))
	return hex.EncodeToString(h.Sum(nil))
}
