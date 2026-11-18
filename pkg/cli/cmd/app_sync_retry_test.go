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

package cmd

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	busyWorkspaceMessage = "another workspace operation, such as a development sync, is in progress; retry manually synchronizing the development workspace in a few seconds"
	legacyBusyMessage    = "wait for or stop the active assistant run before manually synchronizing the development workspace"
	activeRunMessage     = "resolve or stop the active assistant run before manually synchronizing the development workspace"
)

// recordAppSyncSleeps replaces the retry wait with one that returns at once
// and records each requested delay.
func recordAppSyncSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var delays []time.Duration
	previous := appSyncSleep
	appSyncSleep = func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	}
	t.Cleanup(func() { appSyncSleep = previous })
	return &delays
}

// serveAppSync answers hydrate with a fixed tree and sync-development with
// the given conflicts (Retry-After, message) in order, then success. It
// returns a pointer to the number of sync-development calls.
func serveAppSync(hub *fakeHub, conflicts [][2]string) *int {
	syncs := 0
	hub.handle("POST "+appStudioProjects+"/shop/hydrate-workspace", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]any{"repositoryRef": "shop", "ref": "main", "commitSHA": "1234567890ab"})
	})
	hub.handle("POST "+appStudioProjects+"/shop/sync-development", func(w http.ResponseWriter, _ *http.Request) {
		syncs++
		if syncs <= len(conflicts) {
			conflict := conflicts[syncs-1]
			if conflict[0] != "" {
				w.Header().Set("Retry-After", conflict[0])
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			writeTestJSON(w, map[string]any{"kind": "Status", "status": "Failure", "reason": "Conflict", "code": 409, "message": conflict[1]})
			return
		}
		writeTestJSON(w, map[string]any{"result": map[string]any{"web": map[string]any{"phase": "Synced", "sourceRevision": 8}}})
	})
	return &syncs
}

func TestAppSyncWaitsOutABusyWorkspace(t *testing.T) {
	delays := recordAppSyncSleeps(t)
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")
	syncs := serveAppSync(hub, [][2]string{{"5", busyWorkspaceMessage}, {"5", busyWorkspaceMessage}})

	out, err := runRoot(t, path, "app", "sync", "shop")
	if err != nil {
		t.Fatal(err)
	}
	if *syncs != 3 {
		t.Fatalf("sync-development calls = %d, want 3 (two busy answers, then success)", *syncs)
	}
	if want := []time.Duration{5 * time.Second, 5 * time.Second}; !reflect.DeepEqual(*delays, want) {
		t.Fatalf("waits = %v, want %v from Retry-After", *delays, want)
	}
	if !strings.Contains(out, "/web: Synced") {
		t.Fatalf("output missing the sync result:\n%s", out)
	}
}

// A hub without the Retry-After answer words the same lost reservation as an
// assistant run; the CLI still waits it out, at the default delay.
func TestAppSyncWaitsOutALegacyBusyAnswer(t *testing.T) {
	delays := recordAppSyncSleeps(t)
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")
	syncs := serveAppSync(hub, [][2]string{{"", legacyBusyMessage}})

	if _, err := runRoot(t, path, "app", "sync", "shop"); err != nil {
		t.Fatal(err)
	}
	if *syncs != 2 {
		t.Fatalf("sync-development calls = %d, want 2", *syncs)
	}
	if want := []time.Duration{appSyncBusyDefaultDelay}; !reflect.DeepEqual(*delays, want) {
		t.Fatalf("waits = %v, want %v", *delays, want)
	}
}

// A run that is really active is not something a few seconds fix: the CLI
// reports it at once.
func TestAppSyncDoesNotRetryAnActiveAssistantRun(t *testing.T) {
	delays := recordAppSyncSleeps(t)
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")
	syncs := serveAppSync(hub, [][2]string{{"", activeRunMessage}})

	_, err := runRoot(t, path, "app", "sync", "shop")
	if err == nil || !strings.Contains(err.Error(), activeRunMessage) {
		t.Fatalf("err = %v, want the active-run conflict", err)
	}
	if *syncs != 1 || len(*delays) != 0 {
		t.Fatalf("sync-development calls = %d, waits = %v; want one call and no retry", *syncs, *delays)
	}
}

// A workspace that stays busy is reported once the retry budget is spent, and
// a long Retry-After is capped so one answer cannot use the whole budget.
func TestAppSyncGivesUpOnAWorkspaceThatStaysBusy(t *testing.T) {
	delays := recordAppSyncSleeps(t)
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")
	conflicts := make([][2]string, 20)
	for i := range conflicts {
		conflicts[i] = [2]string{"30", busyWorkspaceMessage}
	}
	syncs := serveAppSync(hub, conflicts)

	_, err := runRoot(t, path, "app", "sync", "shop")
	var apiErr *hubAPIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusConflict {
		t.Fatalf("err = %v, want the busy conflict after the budget", err)
	}
	want := []time.Duration{appSyncBusyMaxDelay, appSyncBusyMaxDelay, appSyncBusyMaxDelay, appSyncBusyMaxDelay}
	if !reflect.DeepEqual(*delays, want) {
		t.Fatalf("waits = %v, want %v (capped, within %s)", *delays, want, appSyncBusyRetryBudget)
	}
	if *syncs != len(want)+1 {
		t.Fatalf("sync-development calls = %d, want %d", *syncs, len(want)+1)
	}
}

func TestWithRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header string
		want         time.Duration
	}{
		{"seconds", "5", 5 * time.Second},
		{"http date", now.Add(8 * time.Second).Format(http.TimeFormat), 8 * time.Second},
		{"past date", now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"zero", "0", 0},
		{"negative", "-3", 0},
		{"garbage", "soon", 0},
		{"absent", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			if tc.header != "" {
				header.Set("Retry-After", tc.header)
			}
			err := withRetryAfter(&hubAPIError{Code: http.StatusConflict}, header, now)
			var apiErr *hubAPIError
			if !errors.As(err, &apiErr) || apiErr.RetryAfter != tc.want {
				t.Fatalf("RetryAfter = %v, want %v", apiErr.RetryAfter, tc.want)
			}
		})
	}
	plain := errors.New("not an API error")
	if got := withRetryAfter(plain, http.Header{"Retry-After": {"5"}}, now); got != plain {
		t.Fatalf("withRetryAfter changed a non-API error: %v", got)
	}
}
