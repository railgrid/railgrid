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

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Journals live with private runner state and have the same lifetime as their
// attempt. They contain the existing protocol events, never launch credentials.
func (s *stateStore) eventPath(attemptID string) string {
	sum := sha256.Sum256([]byte(attemptID))
	return filepath.Join(s.dir, "events-"+hex.EncodeToString(sum[:])+".jsonl")
}

// The state file commits an offset, so a partial/uncommitted tail after a crash
// is ignored by readers and replaced by the next append. Old state has no
// journal: seed only its retained prefix, without inventing lost events.
func (s *stateStore) appendEvent(id string, offset int64, retained []Event, event Event) (int64, error) {
	f, err := os.OpenFile(s.eventPath(id), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return offset, fmt.Errorf("open event journal: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return offset, err
	}
	if info.Size() < offset {
		return offset, fmt.Errorf("event journal is shorter than its committed prefix")
	}
	if err := f.Truncate(offset); err != nil {
		return offset, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	encoder := json.NewEncoder(f)
	if offset == 0 {
		for _, old := range retained {
			if err := encoder.Encode(old); err != nil {
				return offset, err
			}
		}
	}
	if err := encoder.Encode(event); err != nil {
		return offset, err
	}
	if err := f.Sync(); err != nil {
		return offset, err
	}
	// stateStore.save subsequently syncs this same directory after committing
	// the receipt and offset, including the journal's directory entry.
	return f.Seek(0, io.SeekCurrent)
}

type eventReplay struct {
	file      *os.File
	decoder   *json.Decoder
	nextEvent Event
	through   uint64
}

func (s *stateStore) replayEvents(ctx context.Context, id string, bytes int64, after, through uint64) (*eventReplay, error) {
	f, err := os.Open(s.eventPath(id))
	if err != nil {
		return nil, err
	}
	replay := &eventReplay{file: f, decoder: json.NewDecoder(io.NewSectionReader(f, 0, bytes)), through: through}
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := replay.decoder.Decode(&replay.nextEvent); err != nil {
			_ = f.Close()
			return nil, err
		}
		if replay.nextEvent.Cursor > after {
			break
		}
	}
	if replay.nextEvent.Cursor != after+1 {
		_ = f.Close()
		return nil, fmt.Errorf("event journal does not retain requested cursor")
	}
	return replay, nil
}

// Replay one event at a time: slow consumers do not load the whole transcript
// into memory or hold the runner's state mutex while writing to the network.
func (r *eventReplay) next() (Event, error) {
	event := r.nextEvent
	if event.Cursor > r.through {
		return Event{}, io.EOF
	}
	if event.Cursor < r.through {
		if err := r.decoder.Decode(&r.nextEvent); err != nil {
			return Event{}, err
		}
		if r.nextEvent.Cursor != event.Cursor+1 {
			return Event{}, fmt.Errorf("event journal contains a cursor gap")
		}
	} else {
		r.nextEvent.Cursor++
	}
	return event, nil
}
