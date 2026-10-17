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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// Bound journal disk usage independently of optional harness output limits.
const maxJournalBytes int64 = 256 << 20
const journalControlReserve int64 = 1 << 20

type journalWriter struct {
	file  *os.File
	end   int64
	dirty bool
}
type journalPosition struct {
	cursor uint64
	offset int64
}

// The state file commits an offset, so uncommitted crash tails are replaced
// when opening a writer. Legacy state seeds only retained events. The caller
// holds the runner mutex; active attempts reuse their open handles.
func (s *stateStore) appendEvent(id string, offset int64, retained []Event, event Event) (end int64, err error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
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
	limit := maxJournalBytes
	switch event.Type {
	case EventCompleted, EventFailed, EventCancelled, EventNeedsInput:
		limit += journalControlReserve
	}
	if offset > limit-int64(encoded.Len()) {
		return offset, errors.New("event journal byte limit exceeded")
	}
	writer := s.journals[id]
	if writer == nil || writer.end != offset {
		if writer != nil {
			delete(s.eventIndex, id)
		}
		if err := s.closeJournal(id); err != nil {
			return offset, err
		}
		f, err := os.OpenFile(s.eventPath(id), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return offset, fmt.Errorf("open event journal: %w", err)
		}
		good := false
		defer func() {
			if !good {
				_ = f.Close()
			}
		}()
		info, err := f.Stat()
		if err != nil {
			return offset, err
		}
		if info.Size() < offset {
			return offset, errors.New("event journal is shorter than its committed prefix")
		}
		if err := f.Truncate(offset); err != nil {
			return offset, err
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return offset, err
		}
		writer = &journalWriter{file: f, end: offset}
		s.journals[id] = writer
		good = true
	}
	if _, err := writer.file.Write(encoded.Bytes()); err != nil {
		_ = s.closeJournal(id)
		return offset, err
	}
	writer.end += int64(encoded.Len())
	writer.dirty = true
	// A legacy seed can contain several records; let the first replay index it.
	if len(retained) == 0 || offset != 0 {
		s.indexEvent(id, event.Cursor, offset)
	}
	return writer.end, nil
}

func (s *stateStore) indexEvent(id string, cursor uint64, offset int64) {
	if cursor%256 != 1 {
		return
	}
	positions := s.eventIndex[id]
	if len(positions) == 0 || positions[len(positions)-1].cursor < cursor {
		s.eventIndex[id] = append(positions, journalPosition{cursor, offset})
	}
}

func (s *stateStore) syncJournals() error {
	for _, writer := range s.journals {
		if writer.dirty {
			if err := writer.file.Sync(); err != nil {
				return fmt.Errorf("sync event journal: %w", err)
			}
			writer.dirty = false
		}
	}
	return nil
}
func (s *stateStore) closeJournal(id string) error {
	writer := s.journals[id]
	if writer == nil {
		return nil
	}
	delete(s.journals, id)
	return writer.file.Close()
}
func (s *stateStore) closeJournals() error {
	var err error
	for id := range s.journals {
		err = errors.Join(err, s.closeJournal(id))
	}
	return err
}
func (s *stateStore) removeEvents(id string) error {
	err := s.closeJournal(id)
	delete(s.eventIndex, id)
	removeErr := os.Remove(s.eventPath(id))
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(err, removeErr)
}

// A crash before state acceptance can leave an unowned journal. Only remove
// files in our private journal namespace, never worktrees or other artifacts.
func (s *stateStore) pruneJournals(state persistedState) error {
	owned := map[string]bool{}
	for id, attempt := range state.Attempts {
		if attempt != nil {
			owned[s.eventPath(id)] = true
		}
	}
	paths, err := filepath.Glob(filepath.Join(s.dir, "events-*.jsonl"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if !owned[path] {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
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
	var start int64
	for _, position := range s.eventIndex[id] {
		if position.cursor > after+1 || position.offset >= bytes {
			break
		}
		start = position.offset
	}
	replay := &eventReplay{file: f, decoder: json.NewDecoder(io.NewSectionReader(f, start, bytes-start)), through: through}
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		offset := start + replay.decoder.InputOffset()
		// Omitted JSON fields must not inherit values from an earlier event.
		replay.nextEvent = Event{}
		if err := replay.decoder.Decode(&replay.nextEvent); err != nil {
			_ = f.Close()
			return nil, err
		}
		s.indexEvent(id, replay.nextEvent.Cursor, offset)
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
		// RawMessage reuses its backing bytes when decoding into an existing
		// value. Keep lookahead from overwriting the event being returned.
		r.nextEvent = Event{}
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
