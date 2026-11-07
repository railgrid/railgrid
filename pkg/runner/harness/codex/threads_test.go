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

package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func TestRunSubagentsPreservesRootOnStartAndResume(t *testing.T) {
	for _, session := range []string{"", "thread-existing"} {
		t.Run("session="+session, func(t *testing.T) {
			binary := fakeCodexBinary(t, "subagents")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var events []harness.Event
			result, err := New(Config{Binary: binary, Home: t.TempDir()}).Run(ctx, harness.Launch{
				Workdir: t.TempDir(), Instructions: "delegate", SessionID: session, Credential: testCredential(),
			}, func(e harness.Event) error { events = append(events, e); return nil })
			root := firstNonEmpty(session, "thread-1")
			if err != nil || result.Phase != "completed" || result.SessionID != root {
				t.Fatalf("Run = %+v, %v", result, err)
			}
			var messages []string
			childProgress, completions := 0, 0
			for _, e := range events {
				if e.SessionID != root {
					t.Fatalf("foreign runner event: %+v", e)
				}
				if e.Type == "message" {
					messages = append(messages, e.Message)
				}
				if e.Type == "subagent_progress" {
					childProgress++
					if e.TurnID != "turn-1" {
						t.Fatalf("child overwrote parent turn: %+v", e)
					}
				}
				if e.Type == "turn_completed" {
					completions++
					if e.TurnID != "turn-1" {
						t.Fatalf("foreign completion: %+v", e)
					}
				}
			}
			if strings.Join(messages, ",") != "parent final" || childProgress < 5 || completions != 1 {
				t.Fatalf("messages=%v child progress=%d completions=%d", messages, childProgress, completions)
			}
		})
	}
}

func TestRunChildApprovalUsesChildCoordinates(t *testing.T) {
	decisionFile := filepath.Join(t.TempDir(), "decision")
	binary := fakeCodexBinaryWithDecisionFile(t, "subagents-approval", decisionFile)
	asker := &testPermissionAsker{requests: make(chan harness.PermissionRequest, 1), verdicts: make(chan harness.PermissionVerdict, 1)}
	asker.verdicts <- harness.PermissionVerdict{Allow: true}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := New(Config{Binary: binary, Home: t.TempDir()}).Run(ctx, harness.Launch{
		AttemptID: "attempt", Workdir: t.TempDir(), Instructions: "delegate", Credential: testCredential(), Permissions: asker,
	}, nil)
	if err != nil || result.Phase != "completed" || result.SessionID != "thread-1" {
		t.Fatalf("Run = %+v, %v", result, err)
	}
	select {
	case request := <-asker.requests:
		if request.ID != stablePermissionID("attempt", "item/commandExecution/requestApproval", "child", "child-turn", "child-command", "") {
			t.Fatalf("child request = %+v", request)
		}
		if !strings.Contains(request.Input, `"threadId":"child"`) {
			t.Fatalf("child identity lost: %s", request.Input)
		}
	default:
		t.Fatal("child approval did not reach permission broker")
	}
	decision, err := os.ReadFile(decisionFile)
	if err != nil || string(decision) != "accept" {
		t.Fatalf("decision=%q, %v", decision, err)
	}
}

func threadMessage(method, payload string) wireMessage {
	return wireMessage{Method: method, Params: json.RawMessage(payload)}
}

func TestThreadRoutingValidatesAncestryAndIsolatesControls(t *testing.T) {
	var events []harness.Event
	var output bytes.Buffer
	asker := &permissionAskerSpy{}
	s := runState{sessionID: "root", turnID: "root-turn", permissions: asker, conn: &rpcConn{stdin: &output}, emit: func(e harness.Event) error { events = append(events, e); return nil }}
	for _, msg := range []wireMessage{
		threadMessage("thread/started", `{"thread":{"id":"grandchild","parentThreadId":"child"}}`),
		threadMessage("turn/completed", `{"threadId":"grandchild","turn":{"id":"early","status":"completed"}}`),
		threadMessage("thread/started", `{"thread":{"id":"child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"root"}}}}}`),
		threadMessage("turn/started", `{"threadId":"grandchild","turn":{"id":"child-turn"}}`),
		threadMessage("error", `{"threadId":"grandchild","turnId":"child-turn","error":{"message":"authentication required","accessToken":"secret-value"}}`),
		threadMessage("item/completed", `{"threadId":"grandchild","turnId":"child-turn","item":{"type":"requestUserInput"}}`),
	} {
		if err := s.handle(msg); err != nil {
			t.Fatal(err)
		}
	}
	if !s.isChild("grandchild") || s.done || s.turnID != "root-turn" || s.phase != "" {
		t.Fatalf("parent state changed: %+v", s)
	}
	for _, e := range events {
		if e.Type != "subagent_progress" || e.Clarification != nil || strings.Contains(string(e.Data), "secret-value") {
			t.Fatalf("unsafe child event: %+v", e)
		}
	}
	// Unknown, stale, unsupported and child-question RPCs must be answered,
	// without approving anything or parking/failing the root.
	requests := []wireMessage{
		threadMessage("item/commandExecution/requestApproval", `{"threadId":"unknown","turnId":"x","itemId":"i"}`),
		threadMessage("item/commandExecution/requestApproval", `{"threadId":"grandchild","turnId":"stale-turn","itemId":"i"}`),
		threadMessage("item/permissions/requestApproval", `{"threadId":"grandchild","turnId":"child-turn","itemId":"i"}`),
		threadMessage("item/tool/requestUserInput", `{"threadId":"grandchild","turnId":"child-turn","itemId":"i","questions":[]}`),
	}
	for i, msg := range requests {
		msg.ID = json.RawMessage(fmt.Sprint(i + 1))
		if err := s.handle(msg); err != nil {
			t.Fatal(err)
		}
	}
	if asker.calls != 0 || s.done || s.turnID != "root-turn" {
		t.Fatalf("child control escaped: calls=%d state=%+v", asker.calls, s)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) != len(requests) {
		t.Fatalf("responses=%s", output.Bytes())
	}
	for i, line := range lines {
		var response struct {
			ID     int `json:"id"`
			Result struct {
				Decision string `json:"decision"`
			} `json:"result"`
			Error *rpcError `json:"error"`
		}
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatal(err)
		}
		if response.ID != i+1 || (response.Result.Decision != "decline" && response.Error == nil) {
			t.Fatalf("unsafe response: %s", line)
		}
	}
}

func TestThreadRoutingCannotAdoptUnsolicitedRoot(t *testing.T) {
	s := runState{}
	if err := s.handle(threadMessage("thread/started", `{"thread":{"id":"foreign"}}`)); err != nil {
		t.Fatal(err)
	}
	if s.sessionID != "" {
		t.Fatalf("unsolicited root adopted: %q", s.sessionID)
	}
}

func TestThreadRoutingBoundsAncestryAndRejectsCycles(t *testing.T) {
	s := runState{sessionID: "root"}
	for i := 0; i < maxChildThreads+10; i++ {
		msg := threadMessage("thread/started", fmt.Sprintf(`{"thread":{"id":"child-%d","parentThreadId":"child-%d"}}`, i, (i+1)%maxChildThreads))
		if err := s.handle(msg); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.children) != maxChildThreads || s.isChild("child-0") {
		t.Fatalf("unbounded or cyclic ancestry: %d", len(s.children))
	}
}

func TestChildProgressEmitFailureStopsRun(t *testing.T) {
	failure := errors.New("output limit")
	s := runState{sessionID: "root", emit: func(harness.Event) error { return failure }}
	err := s.handle(threadMessage("thread/started", `{"thread":{"id":"child","parentThreadId":"root"}}`))
	if !errors.Is(err, failure) {
		t.Fatalf("emit failure=%v", err)
	}
}

func TestThreadAncestryHandlesSourceUnionAndRejectsReparenting(t *testing.T) {
	for _, source := range []string{`"appServer"`, `{"subAgent":"review"}`, `{"subAgent":"compact"}`, `null`} {
		t.Run(source, func(t *testing.T) {
			s := runState{sessionID: "root"}
			msg := threadMessage("thread/started", fmt.Sprintf(`{"thread":{"id":"child","parentThreadId":"root","source":%s}}`, source))
			if err := s.handle(msg); err != nil {
				t.Fatal(err)
			}
			if !s.isChild("child") {
				t.Fatalf("source union invalidated explicit parent: %s", source)
			}
			if err := s.handle(threadMessage("thread/started", `{"thread":{"id":"child","parentThreadId":"unrelated"}}`)); err != nil {
				t.Fatal(err)
			}
			if !s.isChild("child") {
				t.Fatal("known child was reparented")
			}
		})
	}
	s := runState{sessionID: "root"}
	if err := s.handle(threadMessage("thread/started", `{"thread":{"id":"conflict","parentThreadId":"root","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"unrelated"}}}}}`)); err != nil {
		t.Fatal(err)
	}
	if s.isChild("conflict") {
		t.Fatal("conflicting ancestry accepted")
	}
}

func TestChildApprovalRequiresAnActiveTurn(t *testing.T) {
	var output bytes.Buffer
	asker := &permissionAskerSpy{}
	s := runState{sessionID: "root", turnID: "parent-turn", permissions: asker, conn: &rpcConn{stdin: &output}}
	for _, msg := range []wireMessage{
		threadMessage("thread/started", `{"thread":{"id":"child","parentThreadId":"root"}}`),
		threadMessage("turn/started", `{"threadId":"child","turn":{"id":"current-turn"}}`),
		threadMessage("turn/completed", `{"threadId":"child","turn":{"id":"old-turn","status":"completed"}}`),
	} {
		if err := s.handle(msg); err != nil {
			t.Fatal(err)
		}
	}
	approval := threadMessage("item/commandExecution/requestApproval", `{"threadId":"child","turnId":"current-turn","itemId":"command"}`)
	approval.ID = json.RawMessage(`1`)
	if err := s.handle(approval); err != nil {
		t.Fatal(err)
	}
	if asker.calls != 1 || !strings.Contains(output.String(), `"accept"`) {
		t.Fatalf("valid child approval rejected: %s", output.String())
	}
	if err := s.handle(threadMessage("turn/completed", `{"threadId":"child","turn":{"id":"current-turn","status":"completed"}}`)); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := s.handle(approval); err != nil {
		t.Fatal(err)
	}
	if asker.calls != 1 || !strings.Contains(output.String(), `"decline"`) {
		t.Fatalf("completed child turn approved: %s", output.String())
	}
}
