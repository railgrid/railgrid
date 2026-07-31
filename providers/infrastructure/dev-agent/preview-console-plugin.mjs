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

import { readFileSync } from "node:fs";

export const previewConsoleJWKSPath = "/kedge/bin/preview-console-jwks.json";

const previewConsoleClient = String.raw`(() => {
  "use strict";

  const TRUSTED_VERIFICATION_KEYS = __KEDGE_PREVIEW_CONSOLE_VERIFICATION_KEYS__;
  const VERSION = 1;
  const READY = "kedge.preview-console.ready";
  const PROBE = "kedge.preview-console.probe";
  const START = "kedge.preview-console.start";
  const CONNECTED = "kedge.preview-console.connected";
  const EVENTS = "kedge.preview-console.events";
  const MAX_EVENTS = 200;
  const MAX_PORT_BATCH_EVENTS = 16;
  const MAX_PROPERTIES = 20;
  const MAX_STRING = 1000;
  const MAX_MESSAGE = 1200;
  const MAX_STACK = 600;
  const MAX_DEPTH = 2;
  const MAX_EVENT_BYTES = 1900;
  const clock = () => new Date().toISOString();
  const path = () => location.pathname;
  const randomID = () => {
    if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
    const raw = new Uint8Array(16);
    crypto.getRandomValues(raw);
    raw[6] = (raw[6] & 0x0f) | 0x40;
    raw[8] = (raw[8] & 0x3f) | 0x80;
    const hex = Array.from(raw, (value) => value.toString(16).padStart(2, "0")).join("");
    return hex.slice(0, 8) + "-" + hex.slice(8, 12) + "-" + hex.slice(12, 16) + "-" +
      hex.slice(16, 20) + "-" + hex.slice(20);
  };

  const documentID = randomID();
  const ring = [];
  let sequence = 0;
  let port = null;
  let connected = false;
  let connecting = false;
  let flushScheduled = false;
  let probedOrigin = "";
  let sessionID = "";
  let generation = null;
  let droppedCount = 0;
  const consumedCapabilityIDs = new Set();

  const truncate = (value, limit = MAX_STRING) => {
    const text = String(value);
    return text.length > limit ? text.slice(0, limit) + "…" : text;
  };

  const serialize = (value, depth = 0, seen = new WeakSet()) => {
    if (value === null || typeof value === "boolean" || typeof value === "number") return value;
    if (typeof value === "string") return truncate(value);
    if (typeof value === "undefined") return "[undefined]";
    if (typeof value === "bigint") return value.toString() + "n";
    if (typeof value === "symbol") return truncate(value.toString());
    if (typeof value === "function") return "[Function]";
    if (typeof Element !== "undefined" && value instanceof Element) {
      return "[DOM Element]";
    }
    if (typeof Node !== "undefined" && value instanceof Node) {
      return "[DOM Node]";
    }
    if (value instanceof Error) {
      const descriptors = Object.getOwnPropertyDescriptors(value);
      return {
        type: "Error",
        name: descriptors.name && "value" in descriptors.name && typeof descriptors.name.value === "string"
          ? truncate(descriptors.name.value)
          : "Error",
        message: descriptors.message && "value" in descriptors.message && typeof descriptors.message.value === "string"
          ? truncate(descriptors.message.value)
          : "",
        stack: descriptors.stack && "value" in descriptors.stack && typeof descriptors.stack.value === "string"
          ? sanitizeStack(descriptors.stack.value)
          : "",
      };
    }
    if (depth >= MAX_DEPTH) return "[Object]";
    if (seen.has(value)) return "[Circular]";
    seen.add(value);
    let array;
    try {
      array = Array.isArray(value);
      if (!array) {
        const prototype = Object.getPrototypeOf(value);
        if (prototype !== Object.prototype && prototype !== null) return "[Object]";
      }
    } catch {
      return "[Unavailable]";
    }
    const out = array ? [] : {};
    let descriptors;
    try {
      descriptors = Object.getOwnPropertyDescriptors(value);
    } catch {
      return "[Unavailable]";
    }
    let count = 0;
    for (const key of Object.keys(descriptors)) {
      if (key === "length") continue;
      if (count >= MAX_PROPERTIES) {
        out["…"] = "[truncated]";
        break;
      }
      const descriptor = descriptors[key];
      const safeKey = truncate(key);
      if (!descriptor || !("value" in descriptor)) {
        out[safeKey] = "[Accessor]";
        count++;
        continue;
      }
      try {
        out[safeKey] = serialize(descriptor.value, depth + 1, seen);
      } catch {
        out[safeKey] = "[Unavailable]";
      }
      count++;
    }
    return out;
  };

  const readable = (value) => {
    if (typeof value === "string") return truncate(value);
    if (value instanceof Error) {
      const descriptor = Object.getOwnPropertyDescriptor(value, "message");
      return descriptor && "value" in descriptor && typeof descriptor.value === "string"
        ? truncate(descriptor.value)
        : "Error";
    }
    const safe = serialize(value);
    if (typeof safe === "string") return safe;
    try {
      return truncate(JSON.stringify(safe));
    } catch {
      return "[Unavailable]";
    }
  };

  const readableArgs = (args) =>
    truncate(Array.from(args).slice(0, MAX_PROPERTIES).map(readable).join(" "), MAX_MESSAGE);

  const errorStack = (value) => {
    if (!(value instanceof Error)) return "";
    const descriptor = Object.getOwnPropertyDescriptor(value, "stack");
    return descriptor && "value" in descriptor && typeof descriptor.value === "string"
      ? sanitizeStack(descriptor.value)
      : "";
  };

  const sanitizeURL = (value) => {
    if (typeof value !== "string" || value.length === 0) return "";
    try {
      const parsed = new URL(value, location.href);
      if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return "";
      parsed.username = "";
      parsed.password = "";
      parsed.search = "";
      parsed.hash = "";
      return truncate(parsed.href);
    } catch {
      return "";
    }
  };

  const sanitizeStack = (value) => truncate(String(value || "").replace(
    /https?:\/\/[^\s)]+/g,
    (candidate) => sanitizeURL(candidate) || "[URL]",
  ), MAX_STACK);

  const jsonBytes = (value) => {
    try {
      return new TextEncoder().encode(JSON.stringify(value)).byteLength;
    } catch {
      return Number.POSITIVE_INFINITY;
    }
  };

  const boundEvent = (event) => {
    if (jsonBytes(event) <= MAX_EVENT_BYTES) return event;
    if (event.sourceURL) event.sourceURL = truncate(event.sourceURL, 300);
    if (event.stack) event.stack = truncate(event.stack, 300);
    event.message = truncate(event.message, 700);
    while (jsonBytes(event) > MAX_EVENT_BYTES && event.message.length > 80) {
      event.message = truncate(event.message, Math.max(80, Math.floor(event.message.length * 0.7)));
    }
    if (jsonBytes(event) > MAX_EVENT_BYTES) delete event.stack;
    if (jsonBytes(event) > MAX_EVENT_BYTES) delete event.sourceURL;
    if (jsonBytes(event) > MAX_EVENT_BYTES) event.message = "[console event truncated]";
    return event;
  };

  const scheduleFlush = () => {
    if (!connected || !port || flushScheduled || ring.length === 0) return;
    flushScheduled = true;
    const run = () => {
      flushScheduled = false;
      if (!connected || !port || ring.length === 0) return;
      const events = ring.splice(0, MAX_PORT_BATCH_EVENTS);
      const reportedDroppedCount = droppedCount;
      try {
        port.postMessage({
          type: EVENTS,
          version: VERSION,
          sessionID,
          generation,
          documentID,
          path: path(),
          droppedCount: reportedDroppedCount,
          events,
        });
        droppedCount -= reportedDroppedCount;
        if (ring.length > 0) scheduleFlush();
      } catch {
        ring.unshift(...events.slice(-MAX_EVENTS));
        connected = false;
        try { port.close(); } catch {}
        port = null;
      }
    };
    if (typeof queueMicrotask === "function") queueMicrotask(run);
    else Promise.resolve().then(run);
  };

  const capture = (level, message, stack = "", sourceURL = location.href) => {
    try {
      const event = {
        sequence: ++sequence,
        documentID,
        level,
        message: truncate(message, MAX_MESSAGE),
        clientTime: clock(),
      };
      if (stack) event.stack = sanitizeStack(stack);
      if (sourceURL) event.sourceURL = sanitizeURL(sourceURL);
      ring.push(boundEvent(event));
      if (ring.length > MAX_EVENTS) {
        const overflow = ring.length - MAX_EVENTS;
        ring.splice(0, overflow);
        droppedCount += overflow;
      }
      scheduleFlush();
    } catch {
      // Console observation must never interfere with the application.
    }
  };

  const consoleObject = window.console;
  if (consoleObject) {
    for (const level of ["debug", "log", "info", "warn", "error"]) {
      const original = consoleObject[level];
      if (typeof original !== "function") continue;
      try {
        consoleObject[level] = function (...args) {
          try {
            capture(level, readableArgs(args), args.map(errorStack).filter(Boolean).join("\n"));
          } catch {
            // Serialization is observational and must not change console behavior.
          }
          return original.apply(this, args);
        };
      } catch {
        // A frozen console object is unusual, but the app must still start.
      }
    }
  }

  window.addEventListener("error", (event) => {
    try {
      capture("pageerror", event.message || "Uncaught error", errorStack(event.error), event.filename || "");
    } catch {}
  }, true);
  window.addEventListener("unhandledrejection", (event) => {
    try {
      capture(
        "unhandledrejection",
        readable(event.reason || "Unhandled promise rejection"),
        errorStack(event.reason),
      );
    } catch {}
  }, true);

  const readyMessage = () => ({
    type: READY,
    version: VERSION,
    documentID,
    path: path(),
  });

  const emitReady = (targetOrigin = "*") => {
    try {
      window.parent.postMessage(readyMessage(), targetOrigin);
    } catch {
      // The bridge is optional; a parent that cannot receive it is harmless.
    }
  };

  const decodeBase64URL = (value) => {
    const normalized = value.replace(/-/g, "+").replace(/_/g, "/");
    const padded = normalized + "=".repeat((4 - normalized.length % 4) % 4);
    const binary = atob(padded);
    return Uint8Array.from(binary, (char) => char.charCodeAt(0));
  };

  const decodeJSON = (value) => JSON.parse(new TextDecoder().decode(decodeBase64URL(value)));

  const claimMatches = (claims, data, parentOrigin) => {
    const now = Math.floor(Date.now() / 1000);
    return claims &&
      claims.iss === "app-studio" &&
      claims.aud === "preview-console-events" &&
      claims.v === VERSION &&
      String(claims.sid || "") === String(data.sessionID || "") &&
      String(claims.gen) === String(data.generation) &&
      claims.po === location.origin &&
      claims.ao === parentOrigin &&
      Number.isFinite(claims.exp) && claims.exp > now &&
      Number.isFinite(claims.iat) && claims.iat <= now + 60 &&
      typeof claims.jti === "string" && claims.jti.length > 0;
  };

  const verifyCapability = async (compact, data, parentOrigin) => {
    if (!crypto.subtle || typeof compact !== "string") return null;
    const parts = compact.split(".");
    if (parts.length !== 3 || parts.some((part) => part.length === 0)) return null;
    let header;
    let claims;
    try {
      header = decodeJSON(parts[0]);
      claims = decodeJSON(parts[1]);
    } catch {
      return null;
    }
    if (header.alg !== "ES256" || typeof header.kid !== "string" || !claimMatches(claims, data, parentOrigin)) {
      return null;
    }
    const candidates = TRUSTED_VERIFICATION_KEYS.filter((key) =>
      key.kid === header.kid &&
      (!key.alg || key.alg === "ES256") &&
      (!key.use || key.use === "sig")
    );
    if (candidates.length === 0) return null;
    const signed = new TextEncoder().encode(parts[0] + "." + parts[1]);
    let signature;
    try {
      signature = decodeBase64URL(parts[2]);
    } catch {
      return null;
    }
    for (const jwk of candidates) {
      try {
        const key = await crypto.subtle.importKey(
          "jwk",
          jwk,
          { name: "ECDSA", namedCurve: "P-256" },
          false,
          ["verify"],
        );
        if (await crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, key, signature, signed)) {
          return claims;
        }
      } catch {
        // Try the previous/current key candidate with the same kid, if any.
      }
    }
    return null;
  };

  const onMessage = async (event) => {
    if (event.source !== window.parent || !event.data || event.data.version !== VERSION) return;
    if (event.data.type === PROBE) {
      if (typeof event.origin !== "string" || event.origin.length === 0 || event.origin === "null") return;
      // A probe intentionally carries no generation: this document announces
      // its own unforgeable documentID in READY, and the backend-issued
      // capability must bind that value as generation before START can pass.
      probedOrigin = event.origin;
      emitReady(event.origin);
      return;
    }
    if (event.data.type !== START || connecting) return;
    if (typeof event.origin !== "string" || event.origin.length === 0 || event.origin === "null") return;
    if (probedOrigin && event.origin !== probedOrigin) return;
    if (String(event.data.generation || "") !== documentID) return;
    const nextPort = event.ports && event.ports[0];
    if (!nextPort) return;
    connecting = true;
    const claims = await verifyCapability(
      event.data.capability,
      event.data,
      event.origin,
    );
    connecting = false;
    if (!claims || consumedCapabilityIDs.has(claims.jti)) {
      try { nextPort.close(); } catch {}
      return;
    }
    consumedCapabilityIDs.add(claims.jti);
    const previousPort = port;
    connected = true;
    port = nextPort;
    sessionID = String(event.data.sessionID || "");
    generation = documentID;
    if (previousPort && previousPort !== nextPort) {
      try { previousPort.close(); } catch {}
    }
    if (typeof port.start === "function") port.start();
    try {
      port.postMessage({
        type: CONNECTED,
        version: VERSION,
        sessionID: event.data.sessionID,
        generation: event.data.generation,
        documentID,
        path: path(),
      });
    } catch {
      connected = false;
      try { port.close(); } catch {}
      port = null;
      return;
    }
    scheduleFlush();
  };

  window.addEventListener("message", onMessage);
  emitReady();
})();`;

function verificationKeys(configuration) {
  if (!configuration || !Array.isArray(configuration.keys)) {
    throw new Error("preview console JWKS must be an object with a keys array");
  }
  if (configuration.keys.length < 1 || configuration.keys.length > 2) {
    throw new Error("preview console JWKS must contain the current key and optional previous key");
  }
  const seen = new Set();
  return configuration.keys.map((key) => {
    if (!key || key.kty !== "EC" || key.crv !== "P-256" || key.alg && key.alg !== "ES256" ||
        key.use && key.use !== "sig" || typeof key.kid !== "string" || key.kid.length === 0 ||
        typeof key.x !== "string" || typeof key.y !== "string" || "d" in key) {
      throw new Error("preview console JWKS contains an invalid or private ES256 key");
    }
    if (seen.has(key.kid)) throw new Error("preview console JWKS key ids must be unique");
    seen.add(key.kid);
    return {
      kty: "EC",
      crv: "P-256",
      kid: key.kid,
      x: key.x,
      y: key.y,
      alg: "ES256",
      use: "sig",
    };
  });
}

export function previewConsoleClientSource(configuration) {
  const keys = verificationKeys(configuration);
  const encoded = JSON.stringify(keys).replaceAll("<", "\\u003c");
  return previewConsoleClient.replace("__KEDGE_PREVIEW_CONSOLE_VERIFICATION_KEYS__", encoded);
}

export function createPreviewConsolePlugin(configuration) {
  const client = previewConsoleClientSource(configuration);
  return {
    name: "kedge-preview-console-v1",
    enforce: "pre",
    apply: "serve",
    transformIndexHtml() {
      return [{
        tag: "script",
        attrs: { "data-kedge-preview-console": "v1" },
        children: client,
        injectTo: "head-prepend",
      }];
    },
  };
}

export default function previewConsolePlugin() {
  const configuration = JSON.parse(readFileSync(previewConsoleJWKSPath, "utf8"));
  return createPreviewConsolePlugin(configuration);
}
