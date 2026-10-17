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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func TestEventReplaySurvivesCacheEvictionAndRestart(t *testing.T) {
	source, commit := testGitSource(t)
	final := "![Live UI](artifacts/screenshots/planner.png)"
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		for i := 0; i < 300; i++ {
			if err := emit(harness.Event{Type: EventProgress, Message: fmt.Sprintf("progress-%d", i)}); err != nil {
				return harness.Result{}, err
			}
		}
		if err := emit(harness.Event{Type: EventProgress, Message: final}); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	original := newTestRunner(t, adapter, source, commit)
	original.cfg.MaxEvents = 2
	receipt, err := original.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		current, err := original.Inspect(context.Background(), receipt.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Phase == PhaseCompleted {
			break
		}
		if current.Phase.IsTerminal() || time.Now().After(deadline) {
			t.Fatalf("attempt did not complete: %+v", current)
		}
		time.Sleep(10 * time.Millisecond)
	}
	original.mu.Lock()
	if len(original.state.Events[receipt.AttemptID]) != 2 {
		t.Fatal("hot cache is not bounded")
	}
	through := original.state.Attempts[receipt.AttemptID].Receipt.Cursor
	original.mu.Unlock()
	stateDir := original.cfg.StateDir
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := newTestRunnerAt(t, adapter, stateDir, source, commit)
	for _, after := range []uint64{0, 150, through - 1} {
		request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/runner/v1/attempts/%s/events?after=%d", receipt.AttemptID, after), nil)
		request.Header.Set("Authorization", "Bearer "+reopened.cfg.Token)
		response := httptest.NewRecorder()
		reopened.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("replay status %d: %s", response.Code, response.Body.String())
		}
		next := after + 1
		for _, line := range strings.Split(response.Body.String(), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if event.Cursor != next {
				t.Fatalf("cursor %d, want %d", event.Cursor, next)
			}
			next++
		}
		if next != through+1 {
			t.Fatalf("stopped at %d, want %d", next, through+1)
		}
		if after < 150 && !strings.Contains(response.Body.String(), final) {
			t.Fatal("lost screenshot reference")
		}
	}
	if adapter.runs.Load() != 1 {
		t.Fatal("replay redispatched work")
	}
}

func TestEventJournalCommittedPrefix(t *testing.T) {
	store, err := newStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.closeJournals() })
	first := Event{Cursor: 1, Type: EventProgress, Message: "saved"}
	offset, err := store.appendEvent("attempt", 0, nil, first)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the journal write but before state.json commits it.
	_, err = store.appendEvent("attempt", offset, nil, Event{Cursor: 2, Message: "uncommitted"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.replayEvents(context.Background(), "attempt", offset, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.file.Close() }()
	got, err := replay.next()
	if err != nil || got.Message != "saved" {
		t.Fatalf("replay: %+v %v", got, err)
	}
	if _, err = replay.next(); err != io.EOF {
		t.Fatalf("uncommitted tail visible: %v", err)
	}
	end, err := store.appendEvent("attempt", offset, nil, Event{Cursor: 2, Message: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.eventPath("attempt"))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != end || strings.Contains(string(data), "uncommitted") {
		t.Fatal("orphan tail was not replaced")
	}
	info, err := os.Stat(store.eventPath("attempt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode %v", info.Mode())
	}
	if err := os.Truncate(store.eventPath("attempt"), offset-1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.appendEvent("attempt", offset, nil, Event{Cursor: 2}); err == nil {
		t.Fatal("accepted truncated committed history")
	}
}

func TestEventJournalSeedsOnlyRetainedLegacyHistory(t *testing.T) {
	store, err := newStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.closeJournals() })
	end, err := store.appendEvent("legacy", 0, []Event{{Cursor: 4}, {Cursor: 5}}, Event{Cursor: 6})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.replayEvents(context.Background(), "legacy", end, 0, 6); err == nil {
		t.Fatal("invented evicted legacy history")
	}
	replay, err := store.replayEvents(context.Background(), "legacy", end, 3, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.file.Close() }()
	for want := uint64(4); want <= 6; want++ {
		event, err := replay.next()
		if err != nil || event.Cursor != want {
			t.Fatalf("cursor %d: %+v %v", want, event, err)
		}
	}
}

// Block the first write so the harness can overrun the live subscriber buffer.
// Once the buffered prefix is delivered, notify the test to publish a new event.
type stalledEventWriter struct {
	*httptest.ResponseRecorder
	started chan struct{}
	release chan struct{}
	drained chan struct{}
	writes  int
}

func (w *stalledEventWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		close(w.started)
		<-w.release
	}
	n, err := w.ResponseRecorder.Write(p)
	if w.writes == 65 {
		close(w.drained)
	}
	return n, err
}

func TestSlowSubscriberReconnectsWithoutSkippingEvents(t *testing.T) {
	source, commit := testGitSource(t)
	local := newTestRunner(t, &fakeAdapter{}, source, commit)
	local.cfg.MaxEvents = 2
	local.state.Attempts["slow"] = &attemptRecord{Receipt: Receipt{AttemptID: "slow", Phase: PhaseRunning}}
	appendEvent := func() {
		t.Helper()
		local.mu.Lock()
		defer local.mu.Unlock()
		if _, err := local.appendEventLocked("slow", EventProgress, "progress", nil); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent()
	request := httptest.NewRequest(http.MethodGet, "/runner/v1/attempts/slow/events?after=0", nil)
	request.Header.Set("Authorization", "Bearer "+local.cfg.Token)
	writer := &stalledEventWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), release: make(chan struct{}), drained: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); local.Handler().ServeHTTP(writer, request) }()
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(time.Minute):
			t.Fatal("stream stalled")
		}
	}
	wait(writer.started)
	for i := 0; i < 79; i++ {
		appendEvent()
	}
	close(writer.release)
	wait(writer.drained)
	appendEvent() // cursor 81, after the subscriber dropped 66..80
	wait(done)
	if strings.Contains(writer.Body.String(), "id: 81\n") {
		t.Fatal("sent a cursor gap instead of ending the stream")
	}
	if !strings.Contains(writer.Body.String(), "id: 65\n") {
		t.Fatal("lost the contiguous buffered prefix")
	}
	local.mu.Lock()
	local.state.Attempts["slow"].Receipt.Phase = PhaseCompleted
	local.mu.Unlock()
	request = httptest.NewRequest(http.MethodGet, "/runner/v1/attempts/slow/events?after=65", nil)
	request.Header.Set("Authorization", "Bearer "+local.cfg.Token)
	response := httptest.NewRecorder()
	local.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("reconnect: %d", response.Code)
	}
	for cursor := 66; cursor <= 81; cursor++ {
		if !strings.Contains(response.Body.String(), fmt.Sprintf("id: %d\n", cursor)) {
			t.Fatalf("lost cursor %d", cursor)
		}
	}
}

func TestEventReplayDoesNotReuseOmittedFields(t *testing.T) {
	store, err := newStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.closeJournals() })
	events := []Event{
		{Cursor: 1, Type: EventProgress, Message: "old message", Data: json.RawMessage(`{"result":"old answer"}`)},
		{Cursor: 2, Type: EventProgress, Data: json.RawMessage(`{"short":true}`)},
		{Cursor: 3, Type: EventProgress},
		{Cursor: 4, Type: EventCompleted},
	}
	var end int64
	for _, event := range events {
		end, err = store.appendEvent("fields", end, nil, event)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, after := range []uint64{0, 1, 2} {
		replay, err := store.replayEvents(context.Background(), "fields", end, after, 4)
		if err != nil {
			t.Fatal(err)
		}
		for cursor := after + 1; cursor <= 4; cursor++ {
			event, err := replay.next()
			if err != nil {
				t.Fatal(err)
			}
			want := events[cursor-1]
			if event.Message != want.Message || string(event.Data) != string(want.Data) {
				t.Fatalf("cursor %d changed fields: got %+v, want %+v", cursor, event, want)
			}
		}
		if err := replay.file.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJournalLimitReservesFinalEvent(t *testing.T) {
	store, err := newStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.closeJournals() }()
	f, err := os.Create(store.eventPath("limit"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxJournalBytes); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.appendEvent("limit", maxJournalBytes, nil, Event{Cursor: 1, Type: EventProgress}); err == nil {
		t.Fatal("accepted progress past cap")
	}
	end, err := store.appendEvent("limit", maxJournalBytes, nil, Event{Cursor: 1, Type: EventFailed})
	if err != nil || end <= maxJournalBytes {
		t.Fatalf("reserved final event: %d %v", end, err)
	}
	if _, err := store.appendEvent("limit", maxJournalBytes+journalControlReserve, nil, Event{Cursor: 2, Type: EventFailed}); err == nil {
		t.Fatal("exceeded absolute cap")
	}
}

func TestJournalOwnershipAndHandleReuse(t *testing.T) {
	store, err := newStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.closeJournals() }()
	end, err := store.appendEvent("owned", 0, nil, Event{Cursor: 1})
	if err != nil {
		t.Fatal(err)
	}
	first := store.journals["owned"].file
	if _, err := store.appendEvent("owned", end, nil, Event{Cursor: 2}); err != nil {
		t.Fatal(err)
	}
	if first != store.journals["owned"].file {
		t.Fatal("did not reuse handle")
	}
	if _, err := store.appendEvent("orphan", 0, nil, Event{Cursor: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.closeJournals(); err != nil {
		t.Fatal(err)
	}
	state := emptyPersistedState()
	state.Attempts["owned"] = &attemptRecord{}
	if err := store.pruneJournals(state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.eventPath("owned")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.eventPath("orphan")); !os.IsNotExist(err) {
		t.Fatalf("orphan remains: %v", err)
	}
	if err := store.removeEvents("owned"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.eventPath("owned")); !os.IsNotExist(err) {
		t.Fatalf("removed attempt journal remains: %v", err)
	}
}

func TestJournalSparseReplayAfterRestart(t *testing.T) {
	store, err := newStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.closeJournals() }()
	var end int64
	for cursor := uint64(1); cursor <= 800; cursor++ {
		end, err = store.appendEvent("indexed", end, nil, Event{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.syncJournals(); err != nil {
		t.Fatal(err)
	}
	if err := store.closeJournals(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newStateStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []*stateStore{store, reopened} {
		for _, after := range []uint64{770, 780} {
			replay, err := current.replayEvents(context.Background(), "indexed", end, after, 800)
			if err != nil {
				t.Fatal(err)
			}
			event, err := replay.next()
			if err != nil || event.Cursor != after+1 {
				t.Fatalf("indexed replay: %+v %v", event, err)
			}
			if err := replay.file.Close(); err != nil {
				t.Fatal(err)
			}
		}
		positions := current.eventIndex["indexed"]
		if len(positions) != 4 || positions[3].cursor != 769 || positions[3].offset == 0 {
			t.Fatalf("missing index: %+v", positions)
		}
	}
}

func TestFinishReportsJournalFailure(t *testing.T) {
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	r.mu.Lock()
	defer r.mu.Unlock()
	attempt := &attemptRecord{Receipt: Receipt{AttemptID: "broken", Phase: PhaseRunning}}
	r.state.Attempts["broken"] = attempt
	// Force open to fail without relying on permissions (tests may run as root).
	if err := os.Mkdir(r.store.eventPath("broken"), 0700); err != nil {
		t.Fatal(err)
	}
	r.finishLocked(attempt, PhaseCompleted, "", nil)
	if attempt.Receipt.Phase != PhaseFailed || attempt.Receipt.LastError == nil || !strings.Contains(attempt.Receipt.Blocker, "event journal unavailable") {
		t.Fatalf("silent failure: %+v", attempt.Receipt)
	}
	saved, err := r.store.load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Attempts["broken"].Receipt.LastError == nil {
		t.Fatal("error not persisted")
	}
}

func TestJournalSyncFailureDoesNotCommitState(t *testing.T) {
	store, err := newStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := emptyPersistedState()
	if err := store.save(state); err != nil {
		t.Fatal(err)
	}
	end, err := store.appendEvent("attempt", 0, nil, Event{Cursor: 1})
	if err != nil {
		t.Fatal(err)
	}
	state.Attempts["attempt"] = &attemptRecord{EventJournalBytes: end}
	// A closed dirty handle injects a deterministic Sync failure.
	if err := store.journals["attempt"].file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.save(state); err == nil {
		t.Fatal("committed offset before journal sync")
	}
	saved, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := saved.Attempts["attempt"]; exists {
		t.Fatal("unsynced journal committed")
	}
}

func TestAggregateJournalLimitAfterRestart(t *testing.T) {
	store, err := newStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Sparse files exercise startup accounting without allocating GiBs of disk.
	for i := 0; i < 8; i++ {
		f, err := os.Create(store.eventPath(fmt.Sprintf("old-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(maxJournalBytes); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	store, err = newStateStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.closeJournals() }()
	if err := store.loadJournalUsage(); err != nil {
		t.Fatal(err)
	}
	if store.totalJournalBytes != maxTotalJournalBytes {
		t.Fatalf("restart accounting: %d", store.totalJournalBytes)
	}
	if _, err := store.appendEvent("new", 0, nil, Event{Cursor: 1, Type: EventProgress}); err == nil {
		t.Fatal("exceeded aggregate cap")
	}
	end, err := store.appendEvent("new", 0, nil, Event{Cursor: 1, Type: EventFailed})
	if err != nil {
		t.Fatalf("no aggregate control reserve: %v", err)
	}
	if store.totalJournalBytes != maxTotalJournalBytes+end {
		t.Fatal("append not accounted")
	}
	store.accountJournal("reserved", totalJournalControlReserve)
	if _, err := store.appendEvent("new", end, nil, Event{Cursor: 2, Type: EventFailed}); err == nil {
		t.Fatal("exceeded absolute aggregate cap")
	}
	store.accountJournal("reserved", 0)
	if err := store.removeEvents("old-0"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.appendEvent("new", end, nil, Event{Cursor: 2, Type: EventProgress}); err != nil {
		t.Fatalf("removal did not release capacity: %v", err)
	}
	if err := store.closeJournals(); err != nil {
		t.Fatal(err)
	}
	if err := store.pruneJournals(emptyPersistedState()); err != nil {
		t.Fatal(err)
	}
	if store.totalJournalBytes != 0 {
		t.Fatalf("orphan accounting: %d", store.totalJournalBytes)
	}
}

func TestColdReplayDoesNotHoldRunnerLock(t *testing.T) {
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	r.mu.Lock()
	r.cfg.MaxEvents = 2
	r.state.Attempts["cold"] = &attemptRecord{Receipt: Receipt{AttemptID: "cold", Phase: PhaseCompleted}}
	for i := 0; i < 300; i++ {
		if _, err := r.appendEventLocked("cold", EventProgress, "", nil); err != nil {
			r.mu.Unlock()
			t.Fatal(err)
		}
	}
	r.store.clearEventIndex("cold")
	r.mu.Unlock()
	// Block index access to model a stalled replay, then prove Inspect can
	// acquire the runner lock while the HTTP request is waiting to scan.
	r.store.indexMu.Lock()
	done := make(chan struct{})
	response := httptest.NewRecorder()
	go func() {
		defer close(done)
		r.serveEvents(response, httptest.NewRequest(http.MethodGet, "/?after=1", nil), "cold")
	}()
	deadline := time.Now().Add(10 * time.Second)
	subscribed := false
	for time.Now().Before(deadline) {
		if r.mu.TryLock() {
			subscribed = len(r.subscribers["cold"]) > 0
			r.mu.Unlock()
			if subscribed {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !subscribed {
		r.store.indexMu.Unlock()
		<-done
		t.Fatal("replay blocked the runner state lock")
	}
	_, inspectErr := r.Inspect(context.Background(), "cold")
	r.store.indexMu.Unlock()
	<-done
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", response.Code, response.Body.String())
	}
}

func TestJournalReplayFailureIsDiagnosable(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	r.state.Attempts["missing"] = &attemptRecord{EventJournalBytes: 100, Receipt: Receipt{AttemptID: "missing", Phase: PhaseCompleted, Cursor: 3}}
	r.state.Events["missing"] = []Event{{Cursor: 3}}
	response := httptest.NewRecorder()
	r.serveEvents(response, httptest.NewRequest(http.MethodGet, "/?after=0", nil), "missing")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "event journal replay failed") || strings.Contains(response.Body.String(), string(ErrorCursorExpired)) {
		t.Fatalf("journal loss reported as expiry: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(logs.String(), "event journal replay for missing after 0 failed") {
		t.Fatal("missing replay diagnostic")
	}
	// Deliver one event before encountering a corrupt record during lookahead.
	var corruptEnd int64
	for cursor := uint64(1); cursor <= 2; cursor++ {
		var err error
		corruptEnd, err = r.store.appendEvent("corrupt", corruptEnd, nil, Event{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := r.store.closeJournal("corrupt"); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(r.store.eventPath("corrupt"), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{broken"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	r.state.Attempts["corrupt"] = &attemptRecord{EventJournalBytes: corruptEnd + 7, Receipt: Receipt{AttemptID: "corrupt", Phase: PhaseCompleted, Cursor: 3}}
	r.state.Events["corrupt"] = []Event{{Cursor: 3}}
	response = httptest.NewRecorder()
	r.serveEvents(response, httptest.NewRequest(http.MethodGet, "/?after=0", nil), "corrupt")
	if !strings.Contains(response.Body.String(), "id: 1\n") || !strings.Contains(logs.String(), "event journal replay for corrupt aborted after cursor 1") {
		t.Fatalf("missing mid-stream diagnostic: %s; %s", response.Body.String(), logs.String())
	}
	// Unexpected EOF before the committed cursor must not look like a clean page.
	store := r.store
	end, err := store.appendEvent("short", 0, nil, Event{Cursor: 1})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.replayEvents(context.Background(), "short", end, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.file.Close() }()
	if _, err := replay.next(); err != io.ErrUnexpectedEOF {
		t.Fatalf("truncation looked like completion: %v", err)
	}
}
