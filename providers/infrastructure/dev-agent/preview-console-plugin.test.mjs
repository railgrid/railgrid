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
import { webcrypto } from "node:crypto";
import test from "node:test";
import vm from "node:vm";

import {
  createPreviewConsolePlugin,
  previewConsoleClientSource,
} from "./preview-console-plugin.mjs";

const base64url = (value) => Buffer.from(value).toString("base64url");

async function signingKey(kid) {
  const pair = await webcrypto.subtle.generateKey(
    { name: "ECDSA", namedCurve: "P-256" },
    true,
    ["sign", "verify"],
  );
  const publicKey = await webcrypto.subtle.exportKey("jwk", pair.publicKey);
  return {
    privateKey: pair.privateKey,
    publicKey: { ...publicKey, kid, alg: "ES256", use: "sig" },
  };
}

async function capability(key, claims) {
  const header = base64url(JSON.stringify({ alg: "ES256", kid: key.publicKey.kid }));
  const payload = base64url(JSON.stringify(claims));
  const signed = new TextEncoder().encode(`${header}.${payload}`);
  const signature = await webcrypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" },
    key.privateKey,
    signed,
  );
  return `${header}.${payload}.${Buffer.from(signature).toString("base64url")}`;
}

const tick = () => new Promise((resolve) => setImmediate(resolve));

test("plugin injects the v1 bridge before application scripts", async () => {
  const trusted = await signingKey("current");
  const plugin = createPreviewConsolePlugin({ keys: [trusted.publicKey] });
  const tags = plugin.transformIndexHtml();
  assert.equal(plugin.apply, "serve");
  assert.equal(tags[0].injectTo, "head-prepend");
  assert.equal(tags[0].attrs["data-kedge-preview-console"], "v1");
  assert.match(tags[0].children, /kedge\.preview-console\.ready/);
  assert.doesNotMatch(tags[0].children, /__KEDGE_PREVIEW_CONSOLE_VERIFICATION_KEYS__/);
});

test("bridge ignores attacker-supplied keys and accepts a platform-trusted capability", async () => {
  const trusted = await signingKey("current");
  const attacker = await signingKey("attacker");
  const source = previewConsoleClientSource({ keys: [trusted.publicKey] });

  const windowListeners = new Map();
  const parentMessages = [];
  const parent = {
    postMessage(message, origin) {
      parentMessages.push({ message, origin });
    },
  };
  let originalConsoleCalls = 0;
  const fakeConsole = Object.fromEntries(
    ["debug", "log", "info", "warn", "error"].map((level) => [level, () => { originalConsoleCalls++; }]),
  );
  const window = {
    parent,
    console: fakeConsole,
    addEventListener(type, listener) {
      windowListeners.set(type, listener);
    },
    removeEventListener(type, listener) {
      if (windowListeners.get(type) === listener) windowListeners.delete(type);
    },
  };
  class FakeNode {
    constructor() {
      this.nodeType = 1;
    }
  }
  const context = vm.createContext({
    URL,
    Error,
    Element: FakeNode,
    Node: FakeNode,
    TextDecoder,
    TextEncoder,
    Uint8Array,
    atob,
    crypto: {
      subtle: webcrypto.subtle,
      getRandomValues: webcrypto.getRandomValues.bind(webcrypto),
    },
    location: {
      href: "https://preview.test/app?user-state=visible#route",
      origin: "https://preview.test",
      pathname: "/app",
      search: "?user-state=visible",
      hash: "#route",
    },
    Promise,
    Date,
    JSON,
    Object,
    Array,
    String,
    Number,
    Boolean,
    Math,
    WeakSet,
    queueMicrotask,
    window,
  });
  vm.runInContext(source, context);

  assert.equal(parentMessages.length, 1);
  assert.equal(parentMessages[0].message.type, "kedge.preview-console.ready");
  const documentID = parentMessages[0].message.documentID;
  assert.match(documentID, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  const parentOrigin = "https://studio.test";
  const sessionID = "session-1";
  const now = Math.floor(Date.now() / 1000);
  const claims = {
    iss: "app-studio",
    aud: "preview-console-events",
    v: 1,
    sid: sessionID,
    gen: documentID,
    po: "https://preview.test",
    ao: parentOrigin,
    iat: now,
    exp: now + 60,
    jti: "unique-capability",
  };

  await windowListeners.get("message")({
    source: parent,
    origin: parentOrigin,
    data: { type: "kedge.preview-console.probe", version: 1 },
    ports: [],
  });
  assert.equal(parentMessages.at(-1).origin, parentOrigin);

  const attackerPort = {
    messages: [],
    closed: false,
    postMessage(message) { this.messages.push(message); },
    close() { this.closed = true; },
    start() {},
  };
  await windowListeners.get("message")({
    source: parent,
    origin: parentOrigin,
    data: {
      type: "kedge.preview-console.start",
      version: 1,
      sessionID,
      generation: documentID,
      capability: await capability(attacker, claims),
      verificationKeys: { keys: [attacker.publicKey] },
    },
    ports: [attackerPort],
  });
  assert.equal(attackerPort.closed, true);
  assert.deepEqual(attackerPort.messages, []);

  let getterCalls = 0;
  const objectWithGetter = {};
  Object.defineProperty(objectWithGetter, "secret", {
    enumerable: true,
    get() {
      getterCalls++;
      return "must-not-run";
    },
  });
  const pageError = vm.runInContext("new Error('page exploded')", context);
  Object.defineProperty(pageError, "stack", {
    configurable: true,
    value: "Error: page exploded\n    at https://user:pass@preview.test/app.js?token=secret#fragment",
  });
  window.console.error("render failed", objectWithGetter, new FakeNode(), { nestedError: pageError });
  const hostileProxy = new Proxy({}, {
    ownKeys() {
      throw new Error("hostile ownKeys trap");
    },
  });
  window.console.error(hostileProxy);
  assert.equal(originalConsoleCalls, 2);
  windowListeners.get("error")({
    message: "page exploded",
    error: pageError,
    filename: "https://user:pass@preview.test/app.js?token=secret#fragment",
  });

  const trustedPort = {
    messages: [],
    closed: false,
    postMessage(message) { this.messages.push(message); },
    close() { this.closed = true; },
    start() {},
  };
  await windowListeners.get("message")({
    source: parent,
    origin: parentOrigin,
    data: {
      type: "kedge.preview-console.start",
      version: 1,
      sessionID,
      generation: documentID,
      capability: await capability(trusted, claims),
    },
    ports: [trustedPort],
  });
  await tick();

  assert.equal(getterCalls, 0);
  assert.equal(trustedPort.messages[0].type, "kedge.preview-console.connected");
  assert.equal(trustedPort.messages[0].generation, documentID);
  const batch = trustedPort.messages.find((message) => message.type === "kedge.preview-console.events");
  assert.equal(batch.sessionID, sessionID);
  assert.equal(batch.generation, documentID);
  assert.equal(batch.events.length, 3);
  assert.deepEqual(
    Object.keys(batch.events[0]).sort(),
    ["clientTime", "documentID", "level", "message", "sequence", "sourceURL"].sort(),
  );
  assert.equal(batch.events[0].level, "error");
  assert.match(batch.events[0].message, /\[Accessor\]/);
  assert.match(batch.events[0].message, /\[DOM Element\]/);
  assert.match(batch.events[0].message, /"name":"Error"/);
  assert.match(batch.events[0].message, /"stack":"Error: page exploded/);
  assert.doesNotMatch(batch.events[0].message, /token=secret/);
  assert.equal(batch.events[0].sourceURL, "https://preview.test/app");
  assert.equal(batch.events[1].message, "[Unavailable]");
  assert.equal(batch.events[2].level, "pageerror");
  assert.equal(batch.events[2].sourceURL, "https://preview.test/app.js");
  assert.doesNotMatch(batch.events[2].stack, /token=secret/);

  const renewalClaims = {
    ...claims,
    sid: "session-2",
    jti: "renewed-capability",
  };
  const renewalCapability = await capability(trusted, renewalClaims);
  const renewalPort = {
    messages: [],
    closed: false,
    postMessage(message) { this.messages.push(message); },
    close() { this.closed = true; },
    start() {},
  };
  await windowListeners.get("message")({
    source: parent,
    origin: parentOrigin,
    data: {
      type: "kedge.preview-console.start",
      version: 1,
      sessionID: renewalClaims.sid,
      generation: documentID,
      capability: renewalCapability,
    },
    ports: [renewalPort],
  });
  assert.equal(trustedPort.closed, true);
  assert.equal(renewalPort.messages[0].type, "kedge.preview-console.connected");

  const beforeHugeEvent = renewalPort.messages.length;
  window.console.log("🔥".repeat(5_000));
  await tick();
  const hugeBatch = renewalPort.messages.slice(beforeHugeEvent)
    .find((message) => message.type === "kedge.preview-console.events");
  assert.ok(hugeBatch);
  assert.ok(new TextEncoder().encode(JSON.stringify(hugeBatch.events[0])).byteLength <= 1_900);

  const beforeBurst = renewalPort.messages.length;
  for (let index = 0; index < 210; index++) {
    window.console.log("burst", index);
  }
  await tick();
  const burstBatches = renewalPort.messages.slice(beforeBurst)
    .filter((message) => message.type === "kedge.preview-console.events");
  assert.ok(burstBatches.length > 1);
  assert.ok(burstBatches.every((message) => message.events.length <= 16));
  assert.equal(burstBatches.reduce((total, message) => total + message.events.length, 0), 200);
  assert.equal(burstBatches.reduce((total, message) => total + message.droppedCount, 0), 10);

  const replayPort = {
    messages: [],
    closed: false,
    postMessage(message) { this.messages.push(message); },
    close() { this.closed = true; },
    start() {},
  };
  await windowListeners.get("message")({
    source: parent,
    origin: parentOrigin,
    data: {
      type: "kedge.preview-console.start",
      version: 1,
      sessionID: renewalClaims.sid,
      generation: documentID,
      capability: renewalCapability,
    },
    ports: [replayPort],
  });
  assert.equal(replayPort.closed, true);
  assert.deepEqual(replayPort.messages, []);
  assert.equal(renewalPort.closed, false);
});

test("bridge rejects private verification material", () => {
  assert.throws(
    () => previewConsoleClientSource({
      keys: [{
        kty: "EC",
        crv: "P-256",
        kid: "private",
        x: "x",
        y: "y",
        d: "private",
      }],
    }),
    /invalid or private/,
  );
});
