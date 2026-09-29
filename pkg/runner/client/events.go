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

package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/railgrid/railgrid/pkg/runner"
)

// EventStream is a live, ordered read of one attempt's events.
//
// It is a stream rather than a buffer because that is what the wire is: the
// runner replays what it holds and then keeps the connection open, so a caller
// that waited for the whole response would only ever see the replay. Events
// arrive as Server-Sent Events, framed by pkg/runner/http.go writeSSE as
//
//	id: {cursor}\nevent: {type}\ndata: {the JSON Event}\n\n
//
// The id and event fields are redundant with the JSON, which is authoritative
// here: parsing one representation and trusting another is how two views of the
// same event start to disagree.
//
// An EventStream is not safe for concurrent use; one consumer calls Next until
// it returns an error, then Close.
type EventStream struct {
	body   io.ReadCloser
	reader *bufio.Reader
	cursor uint64
	done   bool
}

func newEventStream(body io.ReadCloser, after uint64) *EventStream {
	// The cap is enforced across the whole stream, not per event: a runner that
	// streams forever must not be able to grow a caller's memory or disk
	// without bound, and 64MiB of events is already far past the point where a
	// caller should have reconciled with Inspect instead.
	return &EventStream{
		body:   body,
		reader: bufio.NewReader(&capReader{r: body, remaining: eventStreamLimit + 1}),
		cursor: after,
	}
}

// Cursor is the cursor of the last event Next returned, or the `after` the
// stream was opened with. It is what a caller reconnects from after the runner
// ends a quiet stream.
func (s *EventStream) Cursor() uint64 { return s.cursor }

// Next returns the next event, io.EOF when the runner ended the stream, or an
// error.
//
// A gap in the cursor sequence is returned as *runner.Error with Code
// ErrorCursorExpired and SnapshotRequired set, the same shape the runner uses
// when a caller asks for a cursor it no longer holds: events are dropped rather
// than queued for a consumer that fell behind (pkg/runner/runner.go appends to
// subscribers with a non-blocking send), so a gap means the caller's view is
// incomplete and only Inspect can restore it.
//
// Cancelling ctx ends the stream: it closes the underlying body, which is what
// unblocks a read that is waiting for the next event. The stream is finished
// afterwards — there is nothing to resume on a connection that has been torn
// down — so a caller that wants to keep reading passes a ctx it does not cancel.
func (s *EventStream) Next(ctx context.Context) (runner.Event, error) {
	if s.done {
		return runner.Event{}, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return runner.Event{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = s.body.Close() })
	defer stop()

	var data []string
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			s.done = true
			if ctxErr := ctx.Err(); ctxErr != nil {
				return runner.Event{}, ctxErr
			}
			if errors.Is(err, io.EOF) {
				// A truncated frame is the same loss of position as a gap:
				// the events after it were never delivered.
				if len(data) > 0 {
					return runner.Event{}, errors.New("runner client: the event stream ended mid-event; reconcile with Inspect")
				}
				return runner.Event{}, io.EOF
			}
			return runner.Event{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			// End of frame. A frame with no data field is a comment or a
			// keep-alive and carries no event.
			if len(data) == 0 {
				continue
			}
			event, err := s.decode(strings.Join(data, "\n"))
			if err != nil {
				s.done = true
				return runner.Event{}, err
			}
			return event, nil
		case strings.HasPrefix(line, ":"):
			// An SSE comment, which is how a server keeps a stream warm.
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// id, event, retry, and any unknown field: the JSON payload is
			// authoritative, so nothing else needs reading.
		}
	}
}

func (s *EventStream) decode(payload string) (runner.Event, error) {
	var event runner.Event
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return runner.Event{}, fmt.Errorf("runner client: undecodable event frame: %w", err)
	}
	if event.Cursor <= s.cursor {
		return runner.Event{}, fmt.Errorf("runner client: the event stream went backwards (cursor %d after %d)", event.Cursor, s.cursor)
	}
	if event.Cursor > s.cursor+1 {
		return runner.Event{}, &runner.Error{
			Code:             runner.ErrorCursorExpired,
			Retryable:        true,
			SnapshotRequired: true,
			Message:          fmt.Sprintf("event cursor gap: %d follows %d; inspect the attempt before replaying", event.Cursor, s.cursor),
		}
	}
	s.cursor = event.Cursor
	return event, nil
}

// Close releases the connection. It is safe to call more than once and after
// Next has returned an error.
func (s *EventStream) Close() error {
	s.done = true
	return s.body.Close()
}

// capReader fails the read that would take a stream past its budget, instead of
// reporting a clean EOF like io.LimitReader. A silently truncated event stream
// looks to a caller exactly like an attempt that stopped emitting, which is the
// one thing it must not be confused with.
type capReader struct {
	r         io.Reader
	remaining int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errors.New("runner client: the event stream exceeds the 64MiB limit; reconcile with Inspect")
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	return n, err
}
