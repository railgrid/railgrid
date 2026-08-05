// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/faroshq/provider-vibe-studio/session"
	"github.com/faroshq/provider-vibe-studio/sessionlog"
	"github.com/faroshq/provider-vibe-studio/store"
)

// TestWizardFlowOverHTTP is the Phase 0 exit criterion: prompt → questions →
// answers → blueprint review → approve → provisioning checkpoints → studio,
// entirely through the HTTP surface against the scripted engine.
func TestWizardFlowOverHTTP(t *testing.T) {
	st := store.NewMemoryStore()
	srv := NewServer(st, &session.ScriptedEngine{}, nil, "test-tenant")
	mux := http.NewServeMux()
	srv.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	post := func(path string, body any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			var e map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&e)
			t.Fatalf("POST %s: %d %v", path, resp.StatusCode, e)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	getView := func(id string) map[string]any {
		t.Helper()
		resp, err := http.Get(ts.URL + "/api/sessions/" + id)
		if err != nil {
			t.Fatalf("GET session: %v", err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	waitFor := func(id string, pred func(map[string]any) bool, what string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			v := getView(id)
			if pred(v) {
				return v
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s; last view: %v", what, getView(id))
		return nil
	}

	// 1. Create from a free-form prompt; the async intake turn must surface
	// the scripted template question.
	created := post("/api/sessions", map[string]string{"input": "a todo app for my team"})
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id: %v", created)
	}
	v := waitFor(id, func(v map[string]any) bool {
		qs, _ := v["questions"].([]any)
		return len(qs) > 0
	}, "wizard questions")
	if v["nextAction"] != string(session.ActionAwaitAnswers) {
		t.Fatalf("nextAction = %v, want await-answers", v["nextAction"])
	}

	// 2. Answer → converged blueprint in review.
	post("/api/sessions/"+id+"/submissions", map[string]any{
		"kind":    "answers",
		"answers": map[string]string{"template": "application"},
	})
	v = waitFor(id, func(v map[string]any) bool {
		return v["phase"] == string(session.PhaseReview)
	}, "blueprint review")
	bp, _ := v["blueprint"].(map[string]any)
	tmpl, _ := bp["template"].(map[string]any)
	if tmpl["name"] != "application" {
		t.Fatalf("blueprint template = %v, want application", tmpl)
	}

	// 3. Approve hands off to the controller: the HTTP layer only records
	// the decision, and the session sits in provisioning with the work owed
	// to the Session reconciler (which needs a cluster, so this test stands
	// in for it below rather than exercising it).
	post("/api/sessions/"+id+"/submissions", map[string]any{"kind": "approve"})
	v = waitFor(id, func(v map[string]any) bool {
		return v["phase"] == string(session.PhaseProvisioning)
	}, "provisioning phase")
	if v["nextAction"] != string(session.ActionRunProvision) {
		t.Fatalf("nextAction = %v, want run-provision (owed to the reconciler)", v["nextAction"])
	}

	// Stand in for the Session reconciler completing provisioning — the same
	// command it submits when the sandbox is up and the scaffold is synced.
	if _, err := sessionlog.Submit(context.Background(), st, store.Scope{Tenant: "test-tenant"}, id,
		session.CmdProvisionCompleted{}, false); err != nil {
		t.Fatalf("simulating the reconciler: %v", err)
	}
	v = waitFor(id, func(v map[string]any) bool {
		return v["phase"] == string(session.PhaseStudio)
	}, "studio phase")

	// Entering studio queues the first build turn from the approved
	// blueprint, and the view kicks it: the user never has to type "build it"
	// to get the app they just described. Wait it out before chatting — the
	// portal disables the composer for exactly this window.
	waitFor(id, func(v map[string]any) bool {
		return v["nextAction"] == string(session.ActionAwaitUser)
	}, "the automatic first build turn")

	// 4. Studio chat round-trips through the scripted engine.
	post("/api/sessions/"+id+"/submissions", map[string]any{"kind": "input", "text": "add dark mode"})
	waitFor(id, func(v map[string]any) bool {
		return v["nextAction"] == string(session.ActionAwaitUser)
	}, "studio turn completion")

	// 5. The event log replays the whole story in order.
	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "/events")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	var eventsOut struct {
		Items []session.Event `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&eventsOut)
	if len(eventsOut.Items) == 0 {
		t.Fatalf("no events in log")
	}
	for i, e := range eventsOut.Items {
		if e.Ordinal != int64(i+1) {
			t.Fatalf("event %d has ordinal %d — log not dense", i, e.Ordinal)
		}
	}
	final := session.Fold(eventsOut.Items)
	if final.Phase != session.PhaseStudio {
		t.Fatalf("refolded phase = %s, want studio", final.Phase)
	}

	// 6. Sessions listing shows the session with its current phase.
	lresp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("GET sessions: %v", err)
	}
	defer lresp.Body.Close()
	var listing struct {
		Items []store.SessionRecord `json:"items"`
	}
	_ = json.NewDecoder(lresp.Body).Decode(&listing)
	if len(listing.Items) != 1 || listing.Items[0].Phase != session.PhaseStudio {
		t.Fatalf("listing = %+v", listing.Items)
	}
}

// TestTenantIsolation verifies a foreign tenant cannot read a session.
func TestTenantIsolation(t *testing.T) {
	st := store.NewMemoryStore()
	srv := NewServer(st, &session.ScriptedEngine{}, nil, "")
	mux := http.NewServeMux()
	srv.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	do := func(method, path, tenant string, body []byte) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if tenant != "" {
			req.Header.Set("X-Kedge-Tenant", tenant)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// No tenant header → 401.
	resp := do("GET", "/api/sessions", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("headerless = %d, want 401", resp.StatusCode)
	}

	resp = do("POST", "/api/sessions", "tenant-a", []byte(`{"input":"an app"}`))
	var created struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == "" {
		t.Fatalf("create failed")
	}

	resp = do("GET", "/api/sessions/"+created.ID, "tenant-b", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant read = %d, want 404", resp.StatusCode)
	}
}
