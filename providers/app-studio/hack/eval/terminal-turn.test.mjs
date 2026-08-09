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

import assert from "node:assert/strict";
import test from "node:test";

import {
  classifyTerminalTurn,
  evaluateTerminalTurn,
  pollExactTurn,
  selectFinalAnswer,
  settingsMismatches,
} from "./terminal-turn.mjs";

const settings = {
  provider: "openai-compatible",
  model: "test-model",
  optimizationMode: "codex_poc",
  toolContractDigest: "sha256:tools",
  dynamicToolCatalogDigest: "sha256:catalog",
  instructionDigest: "sha256:instructions",
};

test("selects only the newest explicit final_answer for the requested turn", () => {
  const selected = selectFinalAnswer({
    items: [
      { id: "wrong-turn", turnID: "other", type: "agentMessage", phase: "final_answer", status: "completed" },
      { id: "commentary", turnID: "turn-1", type: "agentMessage", phase: "commentary", status: "completed" },
      { id: "answer-old", turnID: "turn-1", type: "agentMessage", phase: "final_answer", status: "completed", sequence: 4 },
      { id: "answer-new", turnID: "turn-1", type: "agentMessage", phase: "final_answer", status: "completed", sequence: 8 },
    ],
  }, "turn-1");
  assert.equal(selected.mode, "phase");
  assert.equal(selected.item.id, "answer-new");
});

test("uses an explicit phase-less legacy fallback and refuses mixed-phase guesses", () => {
  const legacy = selectFinalAnswer({
    items: [
      { id: "legacy-progress", turnID: "turn-legacy", type: "agentMessage", status: "in_progress" },
      { id: "legacy-answer", turnID: "turn-legacy", type: "agentMessage", status: "completed", content: "legacy" },
    ],
  }, "turn-legacy");
  assert.equal(legacy.mode, "legacy");
  assert.equal(legacy.item.id, "legacy-answer");

  const mixed = selectFinalAnswer({
    items: [
      { id: "commentary", turnID: "turn-mixed", type: "agentMessage", phase: "commentary", status: "completed" },
      { id: "old-answer", turnID: "turn-mixed", type: "agentMessage", status: "completed" },
    ],
  }, "turn-mixed");
  assert.equal(mixed.item, null);
  assert.equal(mixed.reason, "final_answer_missing");
});

test("classifies terminal failures, interruptions, inconsistent payloads, and settings mismatches", () => {
  assert.deepEqual(
    classifyTerminalTurn({ turn: { status: "failed" }, effectiveSettings: settings }),
    { ok: false, classification: "failed", exitCode: 2, reason: "turn_failed", status: "failed" },
  );
  assert.equal(classifyTerminalTurn({ turn: { status: "interrupted" } }).classification, "interrupted");
  assert.equal(classifyTerminalTurn({ turn: { status: "running" } }).classification, "inconsistent");
  const mismatch = classifyTerminalTurn({
    turn: { status: "completed" }, effectiveSettings: settings,
    expectedSettings: { provider: settings.provider, model: "different-model" },
    finalAnswer: { type: "agentMessage", phase: "final_answer", status: "completed" },
  });
  assert.equal(mismatch.classification, "settings_mismatch");
  assert.deepEqual(settingsMismatches(settings, { model: "different-model", provider: settings.provider }), [
    { key: "model", expected: "different-model", actual: "test-model" },
  ]);
});

test("fails closed when a completed turn has no expected provider and model", () => {
  const result = classifyTerminalTurn({
    turn: { status: "completed" },
    effectiveSettings: { provider: "wrong-provider", model: "wrong-model" },
    finalAnswer: { type: "agentMessage", phase: "final_answer", status: "completed" },
  });
  assert.equal(result.ok, false);
  assert.equal(result.classification, "inconsistent");
  assert.equal(result.reason, "expected_settings_missing");
  assert.deepEqual(result.missingExpectedSettings, ["provider", "model"]);
});

test("polls exact turn then exact thread items and returns a successful evaluation", async () => {
  const calls = [];
  const fetchImpl = async (url) => {
    calls.push(String(url));
    if (String(url).endsWith("/turns/turn-1")) {
      return new Response(JSON.stringify({ turn: { id: "turn-1", status: "completed" }, effectiveSettings: settings }), { status: 200 });
    }
    if (String(url).endsWith("/assistant/threads/thread-1/items")) {
      return new Response(JSON.stringify({ items: [{ id: "answer", turnID: "turn-1", type: "agentMessage", phase: "final_answer", status: "completed", content: "done" }] }), { status: 200 });
    }
    return new Response("{}", { status: 404 });
  };
  const result = await evaluateTerminalTurn({
    baseURL: "https://app-studio.example/services/providers/app-studio",
    project: "demo project", thread: "thread-1", turn: "turn-1", token: "caller-token",
    expectedSettings: settings, intervalMs: 0, timeoutMs: 100, fetchImpl,
  });
  assert.equal(result.ok, true);
  assert.equal(result.classification, "success");
  assert.equal(result.finalAnswer.id, "answer");
  assert.deepEqual(calls, [
    "https://app-studio.example/services/providers/app-studio/api/projects/demo%20project/assistant/threads/thread-1/turns/turn-1",
    "https://app-studio.example/services/providers/app-studio/api/projects/demo%20project/assistant/threads/thread-1/items",
  ]);
  assert.equal(calls.some((url) => url.endsWith("/turns/active")), false);
});

test("completed turn with commentary only is inconsistent", async () => {
  const fetchImpl = async (url) => {
    if (String(url).endsWith("/turns/turn-commentary")) {
      return new Response(JSON.stringify({ turn: { id: "turn-commentary", status: "completed" }, effectiveSettings: settings }), { status: 200 });
    }
    if (String(url).endsWith("/assistant/threads/thread-commentary/items")) {
      return new Response(JSON.stringify({ items: [{ id: "commentary", turnID: "turn-commentary", type: "agentMessage", phase: "commentary", status: "completed", content: "progress" }] }), { status: 200 });
    }
    return new Response("{}", { status: 404 });
  };
  const result = await evaluateTerminalTurn({
    baseURL: "https://app-studio.example", project: "demo", thread: "thread-commentary", turn: "turn-commentary",
    expectedSettings: settings, intervalMs: 0, timeoutMs: 100, fetchImpl,
  });
  assert.equal(result.ok, false);
  assert.equal(result.classification, "inconsistent");
  assert.equal(result.reason, "final_answer_missing");
});

test("evaluateTerminalTurn cannot pass a wrong provider or model without expected settings", async () => {
  let calls = 0;
  const fetchImpl = async (url) => {
    calls += 1;
    if (String(url).endsWith("/turns/turn-wrong-settings")) {
      return new Response(JSON.stringify({
        turn: { id: "turn-wrong-settings", status: "completed" },
        effectiveSettings: { provider: "wrong-provider", model: "wrong-model" },
      }), { status: 200 });
    }
    if (String(url).endsWith("/assistant/threads/thread-wrong-settings/items")) {
      return new Response(JSON.stringify({ items: [{
        id: "answer", turnID: "turn-wrong-settings", type: "agentMessage", phase: "final_answer", status: "completed", content: "done",
      }] }), { status: 200 });
    }
    return new Response("{}", { status: 404 });
  };
  const result = await evaluateTerminalTurn({
    baseURL: "https://app-studio.example", project: "demo", thread: "thread-wrong-settings", turn: "turn-wrong-settings",
    intervalMs: 0, timeoutMs: 100, fetchImpl,
  });
  assert.equal(result.ok, false);
  assert.equal(result.classification, "inconsistent");
  assert.equal(result.reason, "expected_settings_missing");
  assert.equal(calls, 0);
});

test("pollExactTurn rejects a response for another turn", async () => {
  await assert.rejects(
    pollExactTurn({
      baseURL: "https://example.test", project: "demo", thread: "thread", turn: "wanted", timeoutMs: 50,
      fetchImpl: async () => new Response(JSON.stringify({ turn: { id: "other", status: "completed" } }), { status: 200 }),
    }),
    (error) => error.code === "inconsistent" && /did not match/.test(error.message),
  );
});

test("pollExactTurn aborts a never-settling fetch at the deadline", async () => {
  let requestSignal;
  const startedAt = Date.now();
  await assert.rejects(
    pollExactTurn({
      baseURL: "https://example.test", project: "demo", thread: "thread", turn: "wanted", timeoutMs: 25,
      fetchImpl: async (_url, request) => {
        requestSignal = request.signal;
        return new Promise(() => {});
      },
    }),
    (error) => error.code === "timeout",
  );
  assert.ok(requestSignal instanceof AbortSignal);
  assert.equal(requestSignal.aborted, true);
  assert.ok(Date.now() - startedAt < 500);
});
