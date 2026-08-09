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

import process from "node:process";
import { pathToFileURL } from "node:url";

export const terminalTurnStatuses = new Set([
  "completed",
  "failed",
  "interrupted",
  "aborted",
]);

const terminalItemStatuses = new Set(["completed", "failed", "interrupted"]);
const settingKeys = [
  "provider",
  "model",
  "optimizationMode",
  "toolContractDigest",
  "dynamicToolCatalogDigest",
  "instructionDigest",
];
const requiredExpectedSettingKeys = ["provider", "model"];

const exitCodes = Object.freeze({
  success: 0,
  failed: 2,
  interrupted: 3,
  inconsistent: 4,
  settingsMismatch: 5,
  transport: 6,
  timeout: 7,
});

class EvaluationError extends Error {
  constructor(code, message, details = {}) {
    super(message);
    this.name = "EvaluationError";
    this.code = code;
    this.details = details;
  }
}

function boundedText(value, limit = 512) {
  const text = String(value ?? "").trim();
  return text.length > limit ? `${text.slice(0, limit)}…` : text;
}

function encodePathSegment(value, label) {
  const text = String(value ?? "").trim();
  if (!text) throw new EvaluationError("invalid_input", `${label} is required`);
  return encodeURIComponent(text);
}

function routeURL(baseURL, project, thread, turn, suffix = "") {
  const base = String(baseURL ?? "").trim().replace(/\/+$/, "");
  if (!base) throw new EvaluationError("invalid_input", "base URL is required");
  const projectsBase = base.endsWith("/api/projects") ? base : `${base}/api/projects`;
  return `${projectsBase}/${encodePathSegment(project, "project")}/assistant/threads/${encodePathSegment(thread, "thread")}/turns/${encodePathSegment(turn, "turn")}${suffix}`;
}

function threadItemsURL(baseURL, project, thread) {
  const base = String(baseURL ?? "").trim().replace(/\/+$/, "");
  if (!base) throw new EvaluationError("invalid_input", "base URL is required");
  const projectsBase = base.endsWith("/api/projects") ? base : `${base}/api/projects`;
  return `${projectsBase}/${encodePathSegment(project, "project")}/assistant/threads/${encodePathSegment(thread, "thread")}/items`;
}

function requestHeaders(options = {}) {
  const headers = new Headers(options.headers ?? {});
  if (!headers.has("Accept")) headers.set("Accept", "application/json");
  if (options.token && !headers.has("Authorization")) {
    headers.set("Authorization", `Bearer ${String(options.token).trim()}`);
  }
  if (options.user && !headers.has("X-Kedge-User")) headers.set("X-Kedge-User", String(options.user).trim());
  if (options.tenant && !headers.has("X-Kedge-Tenant")) headers.set("X-Kedge-Tenant", String(options.tenant).trim());
  if (options.cluster && !headers.has("X-Kedge-Cluster")) headers.set("X-Kedge-Cluster", String(options.cluster).trim());
  if (options.org && !headers.has("X-Kedge-Org")) headers.set("X-Kedge-Org", String(options.org).trim());
  if (options.workspace && !headers.has("X-Kedge-Workspace")) headers.set("X-Kedge-Workspace", String(options.workspace).trim());
  return headers;
}

function abortError(signal) {
  const reason = signal?.reason;
  if (reason instanceof Error) return reason;
  const error = new Error("The operation was aborted");
  error.name = "AbortError";
  return error;
}

function withAbortSignal(promise, signal) {
  if (!signal) return promise;
  if (signal.aborted) return Promise.reject(abortError(signal));
  return new Promise((resolve, reject) => {
    let settled = false;
    const cleanup = () => signal.removeEventListener("abort", onAbort);
    const onAbort = () => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(abortError(signal));
    };
    signal.addEventListener("abort", onAbort, { once: true });
    Promise.resolve(promise).then(
      (value) => {
        if (settled) return;
        settled = true;
        cleanup();
        resolve(value);
      },
      (error) => {
        if (settled) return;
        settled = true;
        cleanup();
        reject(error);
      },
    );
  });
}

function timeoutAbortReason() {
  return { code: "timeout", message: "The request deadline expired" };
}

function isTimeoutSignal(signal) {
  return signal?.aborted && signal.reason?.code === "timeout";
}

function requestAbortScope(options = {}) {
  if (options.signal || options.timeoutMs === undefined) {
    return { signal: options.signal, cleanup: () => {} };
  }
  const controller = new AbortController();
  const timeoutMs = normalizeMilliseconds(options.timeoutMs, 120_000, 1);
  const timer = setTimeout(() => controller.abort(timeoutAbortReason()), timeoutMs);
  return { signal: controller.signal, cleanup: () => clearTimeout(timer) };
}

async function fetchJSON(url, options = {}) {
  const fetchImpl = options.fetchImpl ?? globalThis.fetch;
  if (typeof fetchImpl !== "function") throw new EvaluationError("transport_error", "fetch is not available");
  const request = requestAbortScope(options);
  try {
    let response;
    try {
      response = await withAbortSignal(fetchImpl(url, {
        method: "GET",
        headers: requestHeaders(options),
        signal: request.signal,
      }), request.signal);
    } catch (error) {
      if (isTimeoutSignal(request.signal)) {
        throw new EvaluationError("timeout", `GET ${url} exceeded its deadline`, { cause: boundedText(error?.message ?? error) });
      }
      throw new EvaluationError("transport_error", `GET ${url} failed`, { cause: boundedText(error?.message ?? error) });
    }
    let body;
    try {
      body = await withAbortSignal(response.json(), request.signal);
    } catch (error) {
      if (isTimeoutSignal(request.signal)) {
        throw new EvaluationError("timeout", `GET ${url} exceeded its deadline`, { cause: boundedText(error?.message ?? error) });
      }
      throw new EvaluationError("inconsistent", `GET ${url} returned invalid JSON`, { status: response.status, cause: boundedText(error?.message ?? error) });
    }
    if (!response.ok) {
      throw new EvaluationError("transport_error", `GET ${url} returned HTTP ${response.status}`, { status: response.status });
    }
    if (!body || typeof body !== "object" || Array.isArray(body)) {
      throw new EvaluationError("inconsistent", `GET ${url} returned a non-object response`, { status: response.status });
    }
    return body;
  } finally {
    request.cleanup();
  }
}

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function normalizeMilliseconds(value, fallback, minimum = 0) {
  const number = Number(value);
  if (!Number.isFinite(number)) return fallback;
  return Math.max(minimum, Math.floor(number));
}

/** Poll only the requested turn route; /active is intentionally never used. */
export async function pollExactTurn(options = {}) {
  const timeoutMs = normalizeMilliseconds(options.timeoutMs, 120_000, 1);
  const intervalMs = normalizeMilliseconds(options.intervalMs, 500, 0);
  const deadline = Date.now() + timeoutMs;
  const controller = new AbortController();
  const timeoutTimer = setTimeout(() => controller.abort(timeoutAbortReason()), timeoutMs);
  const externalSignal = options.signal;
  const abortFromExternal = () => controller.abort(externalSignal.reason);
  if (externalSignal) {
    if (externalSignal.aborted) abortFromExternal();
    else externalSignal.addEventListener("abort", abortFromExternal, { once: true });
  }
  let attempts = 0;
  let lastTurn;
  try {
    while (true) {
      attempts += 1;
      const payload = await fetchJSON(routeURL(options.baseURL, options.project, options.thread, options.turn), {
        ...options,
        signal: controller.signal,
        timeoutMs: undefined,
      });
      const turn = payload.turn;
      if (!turn || typeof turn !== "object" || Array.isArray(turn)) {
        throw new EvaluationError("inconsistent", "turn detail response omitted the authoritative turn", { attempts });
      }
      if (String(turn.id ?? "").trim() !== String(options.turn ?? "").trim()) {
        throw new EvaluationError("inconsistent", "turn detail response did not match the requested turn", { attempts });
      }
      lastTurn = turn;
      if (terminalTurnStatuses.has(String(turn.status ?? "").trim())) {
        return { payload, turn, attempts };
      }
      const remaining = deadline - Date.now();
      if (remaining <= 0) {
        throw new EvaluationError("timeout", "turn did not reach a terminal state before the deadline", { attempts, status: turn.status });
      }
      await withAbortSignal(wait(Math.min(intervalMs, remaining)), controller.signal);
      if (Date.now() >= deadline && !terminalTurnStatuses.has(String(lastTurn.status ?? "").trim())) {
        throw new EvaluationError("timeout", "turn did not reach a terminal state before the deadline", { attempts, status: lastTurn.status });
      }
    }
  } catch (error) {
    if (isTimeoutSignal(controller.signal)) {
      throw new EvaluationError("timeout", "turn did not reach a terminal state before the deadline", {
        attempts,
        status: lastTurn?.status,
      });
    }
    throw error;
  } finally {
    clearTimeout(timeoutTimer);
    externalSignal?.removeEventListener("abort", abortFromExternal);
  }
}

function itemOrder(item, index) {
  const sequence = Number(item?.sequence);
  return Number.isFinite(sequence) ? sequence : index;
}

function newest(items) {
  return items.reduce((chosen, item, index) => {
    if (!chosen || itemOrder(item, index) >= itemOrder(chosen.item, chosen.index)) return { item, index };
    return chosen;
  }, null)?.item;
}

/**
 * Select the terminal answer for one turn. A phase-less response is accepted
 * only when every candidate is phase-less; this keeps legacy compatibility
 * explicit and prevents a missing final_answer phase from being guessed.
 */
export function selectFinalAnswer(itemsPayload, turnID) {
  if (!itemsPayload || !Array.isArray(itemsPayload.items)) {
    return { item: null, mode: "none", reason: "items_missing" };
  }
  const candidates = itemsPayload.items.filter(
    (item) => item && item.turnID === turnID && item.type === "agentMessage",
  );
  const explicit = candidates.filter((item) => item.phase === "final_answer");
  if (explicit.length > 0) {
    return { item: newest(explicit), mode: "phase", reason: "final_answer" };
  }
  const allPhaseLess = candidates.length > 0 && candidates.every((item) => !Object.hasOwn(item, "phase"));
  const legacy = allPhaseLess
    ? candidates.filter((item) => terminalItemStatuses.has(String(item.status ?? "").trim()))
    : [];
  if (legacy.length > 0) {
    return { item: newest(legacy), mode: "legacy", reason: "phase-less terminal agentMessage" };
  }
  return { item: null, mode: "none", reason: explicit.length === 0 ? "final_answer_missing" : "final_answer_missing" };
}

function normalizeSettings(settings) {
  if (!settings || typeof settings !== "object" || Array.isArray(settings)) return null;
  const normalized = {};
  for (const key of settingKeys) {
    const value = String(settings[key] ?? "").trim();
    if (value) normalized[key] = value;
  }
  return normalized;
}

function missingExpectedSettings(expected) {
  const normalized = normalizeSettings(expected);
  return requiredExpectedSettingKeys.filter((key) => !normalized || !Object.hasOwn(normalized, key));
}

export function settingsMismatches(actual, expected) {
  const got = normalizeSettings(actual);
  const want = normalizeSettings(expected);
  if (!want || Object.keys(want).length === 0) return [];
  if (!got) return settingKeys.filter((key) => Object.hasOwn(want, key)).map((key) => ({ key, expected: want[key], actual: undefined }));
  return settingKeys
    .filter((key) => Object.hasOwn(want, key) && got[key] !== want[key])
    .map((key) => ({ key, expected: want[key], actual: got[key] }));
}

export function classifyTerminalTurn({ turn, effectiveSettings, finalAnswer, finalAnswerMode, expectedSettings } = {}) {
  const status = String(turn?.status ?? "").trim();
  if (!terminalTurnStatuses.has(status)) {
    return { ok: false, classification: "inconsistent", exitCode: exitCodes.inconsistent, reason: "turn_not_terminal", status };
  }
  if (status === "failed") {
    return { ok: false, classification: "failed", exitCode: exitCodes.failed, reason: "turn_failed", status };
  }
  if (status === "interrupted" || status === "aborted") {
    return { ok: false, classification: "interrupted", exitCode: exitCodes.interrupted, reason: "turn_interrupted", status };
  }
  if (!effectiveSettings || typeof effectiveSettings !== "object") {
    return { ok: false, classification: "inconsistent", exitCode: exitCodes.inconsistent, reason: "effective_settings_missing", status };
  }
  const missingExpected = missingExpectedSettings(expectedSettings);
  if (missingExpected.length > 0) {
    return {
      ok: false,
      classification: "inconsistent",
      exitCode: exitCodes.inconsistent,
      reason: "expected_settings_missing",
      status,
      missingExpectedSettings: missingExpected,
    };
  }
  const mismatches = settingsMismatches(effectiveSettings, expectedSettings);
  if (mismatches.length > 0) {
    return { ok: false, classification: "settings_mismatch", exitCode: exitCodes.settingsMismatch, reason: "effective_settings_mismatch", status, mismatches };
  }
  if (!finalAnswer) {
    return { ok: false, classification: "inconsistent", exitCode: exitCodes.inconsistent, reason: "final_answer_missing", status, finalAnswerMode: finalAnswerMode ?? "none" };
  }
  if (finalAnswer.type !== "agentMessage" || finalAnswer.phase !== "final_answer" && finalAnswerMode !== "legacy") {
    return { ok: false, classification: "inconsistent", exitCode: exitCodes.inconsistent, reason: "final_answer_contract_invalid", status };
  }
  if (String(finalAnswer.status ?? "").trim() !== "completed") {
    return { ok: false, classification: "inconsistent", exitCode: exitCodes.inconsistent, reason: "final_answer_not_completed", status, finalAnswerStatus: finalAnswer.status };
  }
  return { ok: true, classification: "success", exitCode: exitCodes.success, reason: "completed", status, finalAnswerMode: finalAnswerMode ?? "phase" };
}

function publicFinalAnswer(item) {
  if (!item) return null;
  return {
    id: boundedText(item.id, 256),
    type: boundedText(item.type, 64),
    phase: boundedText(item.phase, 64) || undefined,
    status: boundedText(item.status, 64),
    content: boundedText(item.content, 16_384),
  };
}

function publicSettings(settings) {
  const normalized = normalizeSettings(settings);
  return normalized && Object.keys(normalized).length > 0 ? normalized : null;
}

export async function evaluateTerminalTurn(options = {}) {
  const missingExpected = missingExpectedSettings(options.expectedSettings);
  if (missingExpected.length > 0) {
    return {
      ok: false,
      classification: "inconsistent",
      exitCode: exitCodes.inconsistent,
      reason: "expected_settings_missing",
      missingExpectedSettings: missingExpected,
    };
  }
  let polled;
  try {
    polled = await pollExactTurn(options);
    const terminalClassification = classifyTerminalTurn({
      turn: polled.turn,
      effectiveSettings: polled.payload.effectiveSettings,
      expectedSettings: options.expectedSettings,
    });
    // A failed or interrupted run is already conclusive; do not let an
    // unavailable items projection hide that terminal status.
    if (terminalClassification.classification === "failed" || terminalClassification.classification === "interrupted") {
      return {
        ...terminalClassification,
        turnID: polled.turn.id,
        turnStatus: polled.turn.status,
        attempts: polled.attempts,
        effectiveSettings: publicSettings(polled.payload.effectiveSettings),
        finalAnswer: null,
      };
    }
    const itemsPayload = await fetchJSON(threadItemsURL(options.baseURL, options.project, options.thread), options);
    const selected = selectFinalAnswer(itemsPayload, String(options.turn).trim());
    const classification = classifyTerminalTurn({
      turn: polled.turn,
      effectiveSettings: polled.payload.effectiveSettings,
      finalAnswer: selected.item,
      finalAnswerMode: selected.mode,
      expectedSettings: options.expectedSettings,
    });
    return {
      ...classification,
      turnID: polled.turn.id,
      turnStatus: polled.turn.status,
      attempts: polled.attempts,
      effectiveSettings: publicSettings(polled.payload.effectiveSettings),
      finalAnswer: publicFinalAnswer(selected.item),
    };
  } catch (error) {
    const evaluationError = error instanceof EvaluationError
      ? error
      : new EvaluationError("transport_error", "terminal turn evaluation failed", { cause: boundedText(error?.message ?? error) });
    const classification = evaluationError.code === "timeout"
      ? { classification: "timeout", exitCode: exitCodes.timeout }
      : evaluationError.code === "inconsistent"
        ? { classification: "inconsistent", exitCode: exitCodes.inconsistent }
        : evaluationError.code === "invalid_input"
          ? { classification: "inconsistent", exitCode: exitCodes.inconsistent }
          : { classification: "transport_error", exitCode: exitCodes.transport };
    return {
      ok: false,
      ...classification,
      reason: boundedText(evaluationError.message, 512),
      details: evaluationError.details,
    };
  }
}

function parseArguments(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === "--help" || argument === "-h") values.help = true;
    else if (argument.startsWith("--") && argument.includes("=")) {
      const separator = argument.indexOf("=");
      values[argument.slice(2, separator)] = argument.slice(separator + 1);
    } else if (argument.startsWith("--")) {
      const key = argument.slice(2);
      const next = argv[index + 1];
      if (next && !next.startsWith("--")) {
        values[key] = next;
        index += 1;
      } else values[key] = true;
    } else {
      throw new EvaluationError("invalid_input", `unexpected argument ${argument}`);
    }
  }
  return values;
}

function parseJSONSetting(value, label) {
  if (value === undefined || value === "") return undefined;
  try {
    const parsed = JSON.parse(value);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("must be an object");
    return parsed;
  } catch (error) {
    throw new EvaluationError("invalid_input", `${label} must be a JSON object`, { cause: boundedText(error?.message ?? error) });
  }
}

function cliOptions(values, environment = process.env) {
  const expected = parseJSONSetting(values["expected-settings"] ?? values.settings ?? environment.KEDGE_EXPECTED_SETTINGS, "expected settings") ?? {};
  const aliases = {
    provider: "expected-provider",
    model: "expected-model",
    optimizationMode: "expected-optimization-mode",
    toolContractDigest: "expected-tool-contract-digest",
    dynamicToolCatalogDigest: "expected-dynamic-tool-catalog-digest",
    instructionDigest: "expected-instruction-digest",
  };
  for (const [key, flag] of Object.entries(aliases)) {
    const value = values[flag];
    if (value !== undefined && value !== true) expected[key] = value;
  }
  return {
    baseURL: values["base-url"] ?? values.url ?? environment.KEDGE_APP_STUDIO_URL ?? environment.APP_STUDIO_URL ?? environment.KEDGE_BATTERY_BASE,
    project: values.project ?? environment.KEDGE_APP_STUDIO_PROJECT,
    thread: values.thread ?? environment.KEDGE_APP_STUDIO_THREAD,
    turn: values.turn ?? environment.KEDGE_APP_STUDIO_TURN,
    token: values.token ?? environment.KEDGE_TOKEN ?? environment.APP_STUDIO_TOKEN,
    user: values.user ?? environment.KEDGE_USER,
    tenant: values.tenant ?? environment.KEDGE_TENANT,
    cluster: values.cluster ?? environment.KEDGE_CLUSTER,
    org: values.org ?? environment.KEDGE_ORG ?? environment.KEDGE_BATTERY_ORG,
    workspace: values.workspace ?? environment.KEDGE_WORKSPACE ?? environment.KEDGE_BATTERY_WORKSPACE,
    expectedSettings: expected,
    timeoutMs: values["timeout-ms"] ?? environment.KEDGE_EVAL_TIMEOUT_MS,
    intervalMs: values["interval-ms"] ?? environment.KEDGE_EVAL_INTERVAL_MS,
  };
}

function usage() {
  return [
    "Poll one exact App Studio assistant turn and verify its terminal contract.",
    "",
    "Usage: node terminal-turn.mjs --base-url URL --project NAME --thread ID --turn ID [options]",
    "  --token TOKEN --user USER --tenant TENANT --cluster CLUSTER",
    "  --expected-settings JSON (or individual --expected-* settings)",
    "  --timeout-ms N --interval-ms N",
  ].join("\n");
}

export async function main(argv = process.argv.slice(2), environment = process.env) {
  try {
    const values = parseArguments(argv);
    if (values.help) {
      process.stdout.write(`${usage()}\n`);
      return 0;
    }
    const result = await evaluateTerminalTurn(cliOptions(values, environment));
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return result.exitCode;
  } catch (error) {
    const evaluationError = error instanceof EvaluationError ? error : new EvaluationError("invalid_input", boundedText(error?.message ?? error));
    const result = { ok: false, classification: "inconsistent", exitCode: exitCodes.inconsistent, reason: boundedText(evaluationError.message, 512), details: evaluationError.details };
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return result.exitCode;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().then((code) => {
    process.exitCode = code;
  });
}
