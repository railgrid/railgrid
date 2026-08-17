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
const waitForPortMessage = async (port, type) => {
  for (let attempt = 0; attempt < 40; attempt++) {
    const message = port.messages.find((entry) => entry.type === type);
    if (message) return message;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  return null;
};

test("plugin injects the v1 bridge before application scripts", async () => {
  const trusted = await signingKey("current");
  const plugin = createPreviewConsolePlugin({ keys: [trusted.publicKey] });
  const tags = plugin.transformIndexHtml();
  assert.equal(plugin.apply, "serve");
  assert.equal(tags[0].injectTo, "head-prepend");
  assert.equal(tags[0].attrs["data-faros-preview-console"], "v1");
  assert.match(tags[0].children, /faros\.preview-console\.ready/);
  assert.doesNotMatch(tags[0].children, /__FAROS_PREVIEW_CONSOLE_VERIFICATION_KEYS__/);
});

test("bridge ignores attacker-supplied keys and accepts a platform-trusted capability", async () => {
  const trusted = await signingKey("current");
  const attacker = await signingKey("attacker");
  const source = previewConsoleClientSource({ keys: [trusted.publicKey] });

  const windowListeners = new Map();
  const parentMessages = [];
  class FakePort {
    constructor() {
      this.messages = [];
      this.closed = false;
      this.throwOnPost = false;
      this.peer = null;
      this.onmessage = null;
    }
    postMessage(message) {
      if (this.throwOnPost) throw new Error("port is closed");
      const copy = structuredClone(message);
      this.messages.push(copy);
      if (this.peer) {
        this.peer.messages.push(copy);
        this.peer?.onmessage?.({ data: copy });
      }
    }
    close() {
      this.closed = true;
      if (this.peer) this.peer.closed = true;
    }
    start() {}
  }
  class FakeMessageChannel {
    constructor() {
      this.port1 = new FakePort();
      this.port2 = new FakePort();
      this.port1.peer = this.port2;
      this.port2.peer = this.port1;
    }
  }
  const parent = {
    postMessage(message, origin, ports = []) {
      parentMessages.push({ message, origin, ports });
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
    MessageChannel: FakeMessageChannel,
    window,
  });
  vm.runInContext(source, context);

  assert.equal(typeof windowListeners.get("click"), "function", "bridge must register window capture before app listeners");

  assert.equal(parentMessages.length, 1);
  assert.equal(parentMessages[0].message.type, "faros.preview-console.ready");
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
    data: { type: "faros.preview-console.probe", version: 1 },
    ports: [],
  });
  assert.equal(parentMessages.at(-1).origin, parentOrigin);

  const attackerPort = parentMessages.at(-1).ports[0];
  attackerPort.postMessage({
    type: "faros.preview-console.start",
    version: 1,
    sessionID,
    generation: documentID,
    capability: await capability(attacker, claims),
    verificationKeys: { keys: [attacker.publicKey] },
  });
  await tick();
  await tick();
  assert.equal(attackerPort.closed, true);

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

  await windowListeners.get("message")({
    source: parent,
    origin: parentOrigin,
    data: { type: "faros.preview-console.probe", version: 1 },
    ports: [],
  });
  const trustedPort = parentMessages.at(-1).ports[0];
  trustedPort.postMessage({
    type: "faros.preview-console.start",
    version: 1,
    sessionID,
    generation: documentID,
    capability: await capability(trusted, claims),
  });
  assert.ok(await waitForPortMessage(trustedPort, "faros.preview-console.connected"));

  assert.equal(getterCalls, 0);
  assert.equal(trustedPort.messages.find((message) => message.type === "faros.preview-console.connected").type, "faros.preview-console.connected");
  assert.equal(trustedPort.messages[0].generation, documentID);
  const batch = trustedPort.messages.find((message) => message.type === "faros.preview-console.events");
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
  await windowListeners.get("message")({
    source: parent,
    origin: parentOrigin,
    data: { type: "faros.preview-console.probe", version: 1 },
  });
  const renewalPort = parentMessages.at(-1).ports[0];
  renewalPort.postMessage({
    type: "faros.preview-console.start",
    version: 1,
    sessionID: renewalClaims.sid,
    generation: documentID,
    capability: renewalCapability,
  });
  assert.ok(await waitForPortMessage(renewalPort, "faros.preview-console.connected"));
  assert.equal(trustedPort.closed, true);
  assert.equal(renewalPort.messages.find((message) => message.type === "faros.preview-console.connected").type, "faros.preview-console.connected");

  const beforeHugeEvent = renewalPort.messages.length;
  window.console.log("🔥".repeat(5_000));
  await tick();
  const hugeBatch = renewalPort.messages.slice(beforeHugeEvent)
    .find((message) => message.type === "faros.preview-console.events");
  assert.ok(hugeBatch);
  assert.ok(new TextEncoder().encode(JSON.stringify(hugeBatch.events[0])).byteLength <= 1_900);

  const beforeBurst = renewalPort.messages.length;
  for (let index = 0; index < 210; index++) {
    window.console.log("burst", index);
  }
  await tick();
  const burstBatches = renewalPort.messages.slice(beforeBurst)
    .filter((message) => message.type === "faros.preview-console.events");
  assert.ok(burstBatches.length > 1);
  assert.ok(burstBatches.every((message) => message.events.length <= 16));
  assert.equal(burstBatches.reduce((total, message) => total + message.events.length, 0), 200);
  assert.equal(burstBatches.reduce((total, message) => total + message.droppedCount, 0), 10);

  await windowListeners.get("message")({
    source: parent,
    origin: parentOrigin,
    data: { type: "faros.preview-console.probe", version: 1 },
  });
  const replayPort = parentMessages.at(-1).ports[0];
  replayPort.postMessage({
    type: "faros.preview-console.start",
    version: 1,
    sessionID: renewalClaims.sid,
    generation: documentID,
    capability: renewalCapability,
  });
  await waitForPortMessage(replayPort, "faros.preview-console.connected");
  assert.equal(replayPort.closed, true);
  assert.equal(replayPort.messages.some((message) => message.type === "faros.preview-console.connected"), false);
  assert.equal(renewalPort.closed, false);
  renewalPort.peer.throwOnPost = true;
  window.console.log("port failure cleanup");
  await tick();
  assert.equal(renewalPort.closed, true, "a failed event send must tear down the authenticated port");
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

test("authenticated annotation mode captures bounded targets without activating the app", async () => {
  const trusted = await signingKey("current");
  const source = previewConsoleClientSource({ keys: [trusted.publicKey] });
  const windowListeners = new Map();
  const documentListeners = new Map();
  const parentMessages = [];
  class FakePort {
    constructor() {
      this.messages = [];
      this.closed = false;
      this.peer = null;
      this.onmessage = null;
    }
    postMessage(message) {
      const copy = structuredClone(message);
      this.messages.push(copy);
      if (this.peer) {
        this.peer.messages.push(copy);
        queueMicrotask(() => this.peer?.onmessage?.({ data: copy }));
      }
    }
    close() {
      this.closed = true;
      if (this.peer) this.peer.closed = true;
    }
    start() {}
  }
  class FakeMessageChannel {
    constructor() {
      this.port1 = new FakePort();
      this.port2 = new FakePort();
      this.port1.peer = this.port2;
      this.port2.peer = this.port1;
    }
  }
  const parent = {
    postMessage(message, origin, ports = []) {
      parentMessages.push({ message, origin, ports });
    },
  };

  class FakeElement {
    constructor(tagName, text = "") {
      this.nodeType = 1;
      this.tagName = tagName.toUpperCase();
      this.children = [];
      this.parentElement = null;
      this.attributes = new Map();
      this.innerText = text;
      this.textContent = text;
      this.style = {};
      this.hidden = false;
      this.rect = { x: 12, y: 24, left: 12, top: 24, width: 160, height: 32 };
    }
    append(...children) {
      for (const child of children) {
        child.parentElement = this;
        this.children.push(child);
      }
    }
    remove() {
      if (this.parentElement) {
        this.parentElement.children = this.parentElement.children.filter((child) => child !== this);
      }
      this.parentElement = null;
      this.removed = true;
    }
    setAttribute(name, value) {
      this.attributes.set(name, String(value));
    }
    removeAttribute(name) {
      this.attributes.delete(name);
    }
    getAttribute(name) {
      return this.attributes.has(name) ? this.attributes.get(name) : null;
    }
    closest() {
      return this;
    }
    getBoundingClientRect() {
      return this.rect;
    }
  }

  class FakeDocument {
    constructor() {
      this.body = new FakeElement("body");
      this.documentElement = this.body;
      this.pointTarget = null;
    }
    createElement(tagName) {
      return new FakeElement(tagName);
    }
    addEventListener(type, listener) {
      documentListeners.set(type, listener);
    }
    removeEventListener(type, listener) {
      if (documentListeners.get(type) === listener) documentListeners.delete(type);
    }
    elementFromPoint() {
      return this.pointTarget;
    }
    getElementById() {
      return null;
    }
    allElements() {
      const elements = [];
      const visit = (element) => {
        elements.push(element);
        for (const child of element.children) visit(child);
      };
      visit(this.documentElement);
      return elements;
    }
    querySelector(selector) {
      return this.querySelectorAll(selector)[0] || null;
    }
    querySelectorAll(selector) {
      const attribute = /^\[([^=]+)=\"([^\"]*)\"\]$/.exec(selector);
      if (attribute) return this.allElements().filter((element) => element.getAttribute(attribute[1]) === attribute[2]);
      if (selector.startsWith("#")) return this.allElements().filter((element) => element.getAttribute("id") === selector.slice(1));
      const leaf = selector.split(" > ").at(-1) || selector;
      const nth = /^([a-z0-9-]+):nth-of-type\((\d+)\)$/i.exec(leaf);
      if (nth) {
        return this.allElements().filter((element) => element.tagName === nth[1].toUpperCase() &&
          element.parentElement?.children.filter((sibling) => sibling.tagName === element.tagName).indexOf(element) === Number(nth[2]) - 1);
      }
      const tagName = selector === "*" ? "" : leaf.toUpperCase();
      return this.allElements().filter((element) => !tagName || element.tagName === tagName);
    }
  }

  const document = new FakeDocument();
  const fakeConsole = Object.fromEntries(
    ["debug", "log", "info", "warn", "error"].map((level) => [level, () => {}]),
  );
  const window = {
    parent,
    console: fakeConsole,
    innerWidth: 1024,
    innerHeight: 768,
    scrollX: 0,
    scrollY: 0,
    addEventListener(type, listener) {
      windowListeners.set(type, listener);
    },
    removeEventListener(type, listener) {
      if (windowListeners.get(type) === listener) windowListeners.delete(type);
    },
  };
  const context = vm.createContext({
    URL,
    Error,
    Element: FakeElement,
    Node: FakeElement,
    TextDecoder,
    TextEncoder,
    Uint8Array,
    atob,
    crypto: {
      subtle: webcrypto.subtle,
      getRandomValues: webcrypto.getRandomValues.bind(webcrypto),
    },
    location: {
      href: "https://preview.test/app",
      origin: "https://preview.test",
      pathname: "/app",
      search: "",
      hash: "",
    },
    document,
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
    MessageChannel: FakeMessageChannel,
    window,
  });
  vm.runInContext(source, context);

  assert.equal(typeof windowListeners.get("click"), "function", "bridge must register window capture before app listeners");

  const documentID = parentMessages[0].message.documentID;
  const parentOrigin = "https://studio.test";
  const sessionID = "annotation-session";
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
    jti: "annotation-capability",
  };
  await windowListeners.get("message")({
    source: parent,
    origin: parentOrigin,
    data: { type: "faros.preview-console.probe", version: 1 },
    ports: [],
  });
  const port = parentMessages.at(-1).ports[0];
  port.postMessage({
    type: "faros.preview-console.start",
    version: 1,
    sessionID,
    generation: documentID,
    capability: await capability(trusted, claims),
  });
  assert.ok(await waitForPortMessage(port, "faros.preview-console.connected"));

  const target = new FakeElement("button", "Account settings");
  target.setAttribute("data-faros-id", "account-card");
  target.setAttribute("aria-label", "Open account settings");
  target.setAttribute("value", "secret-value");
  target.setAttribute("style", "color: red");
  target.setAttribute("onclick", "steal()");
  document.body.append(target);
  document.pointTarget = target;
  document.body.append(new FakeElement("button", "Duplicate label"));
  document.body.append(new FakeElement("button", "Duplicate label"));

  port.postMessage({
      type: "faros.preview-console.annotation.start",
      version: 1,
      sessionID,
      generation: documentID,
  });
  await tick();
  assert.equal(documentListeners.has("pointermove"), true);
  assert.equal(documentListeners.has("click"), true);
  assert.equal(documentListeners.has("keydown"), true);
  assert.equal(document.documentElement.getAttribute("data-faros-annotation-mode"), "true");
  const cursorStyle = document.documentElement.children.find((child) => child.getAttribute("data-faros-annotation-cursor") === "true");
  assert.ok(cursorStyle);
  assert.match(cursorStyle.textContent, /data:image\/svg\+xml/);
  assert.match(cursorStyle.textContent, /crosshair !important/);

  port.postMessage({
      type: "faros.preview-console.annotation.pins",
      version: 1,
      sessionID,
      generation: documentID,
      pins: [
        { id: "first", number: 1, documentID, pagePath: "/app", boundingRect: { x: 12, y: 24, width: 16, height: 16 }, target: { locator: '[data-faros-id="account-card"]', locatorStrategy: "css" }, anchor: { x: 0.25, y: 0.75 }, comment: "Make this blue" },
        { id: "stale", number: 99, documentID: "stale-document", boundingRect: { x: 40, y: 50, width: 16, height: 16 } },
        { id: "second", number: 2, documentID, pagePath: "/app", boundingRect: { x: 64, y: 72, width: 16, height: 16 }, target: { locator: '[data-faros-id="account-card"]', locatorStrategy: "css" }, comment: "Clarify this heading" },
        { id: "ambiguous", number: 3, documentID, pagePath: "/app", target: { locator: "Duplicate label", locatorStrategy: "text" } },
        { id: "invalid-anchor", number: 4, documentID, pagePath: "/app", target: { locator: '[data-faros-id="account-card"]', locatorStrategy: "css" }, anchor: { x: 2, y: 0.5 } },
      ],
  });
  await tick();
  const pinLayer = document.body.children.find((child) => child.getAttribute("data-faros-annotation-pins") === "true");
  assert.ok(pinLayer);
  assert.equal(pinLayer.parentElement, document.documentElement, "pins must be rooted outside a positioned/transformed body");
  assert.deepEqual(pinLayer.children.map((child) => child.textContent), ["1", "2", "3"]);
  const firstPin = pinLayer.children[0];
  assert.equal(firstPin.tagName, "BUTTON");
  assert.equal(firstPin.style.pointerEvents, "auto");
  assert.equal(firstPin.getAttribute("aria-label"), "Annotation 1");
  assert.equal(firstPin.style.left, "38px");
  assert.equal(firstPin.style.top, "34px");
  assert.equal(firstPin.children.length, 1, "the preview marker must not render a comment tooltip");
  const renderedPins = port.messages.find((message) => message.type === "faros.preview-console.annotation.pins-rendered");
  assert.deepEqual(JSON.parse(JSON.stringify(renderedPins.pins)), [
    { id: "first", resolved: true },
    { id: "second", resolved: true },
    { id: "ambiguous", resolved: false },
  ]);
  firstPin.onmouseenter();
  firstPin.onfocus();
  firstPin.onmouseleave();
  firstPin.onblur();
  const pinHoverMessages = port.messages
    .filter((message) => message.type === "faros.preview-console.annotation.pin-hover")
    .map(({ id, active, rect }) => ({ id, active, rect }));
  assert.deepEqual(pinHoverMessages, [
    { id: "first", active: true, rect: { x: 52, y: 48, width: 0, height: 0 } },
    { id: "first", active: false, rect: { x: 52, y: 48, width: 0, height: 0 } },
  ]);
  assert.equal(Object.hasOwn(pinHoverMessages[0], "comment"), false);

  const pinClick = {
    target: firstPin,
    prevented: false,
    stopped: false,
    immediateStopped: false,
    preventDefault() { this.prevented = true; },
    stopPropagation() { this.stopped = true; },
    stopImmediatePropagation() { this.immediateStopped = true; },
  };
  windowListeners.get("click")(pinClick);
  assert.equal(pinClick.prevented, true);
  assert.equal(pinClick.stopped, true);
  assert.equal(pinClick.immediateStopped, true);
  const selectedPin = port.messages.find((message) => message.type === "faros.preview-console.annotation.pin-selected");
  assert.deepEqual(JSON.parse(JSON.stringify(selectedPin)), {
    type: "faros.preview-console.annotation.pin-selected",
    version: 1,
    sessionID,
    generation: documentID,
    documentID,
    path: "/app",
    id: "first",
    rect: { x: 52, y: 48, width: 0, height: 0 },
    viewport: { width: 1024, height: 768 },
  });
  assert.equal(port.messages.some((message) => message.type === "faros.preview-console.annotation.selected"), false, "clicking a pin must edit it instead of selecting the marker DOM");

  const pinTail = firstPin.children[0];
  const pinSelectionsBeforeTailClick = port.messages.filter((message) => message.type === "faros.preview-console.annotation.pin-selected").length;
  const newSelectionsBeforeTailClick = port.messages.filter((message) => message.type === "faros.preview-console.annotation.selected").length;
  const pinTailClick = {
    target: pinTail,
    composedPath() { return [pinTail, firstPin, pinLayer, document.documentElement, window]; },
    preventDefault() {},
    stopPropagation() {},
    stopImmediatePropagation() {},
  };
  windowListeners.get("click")(pinTailClick);
  assert.equal(
    port.messages.filter((message) => message.type === "faros.preview-console.annotation.pin-selected").length,
    pinSelectionsBeforeTailClick + 1,
    "clicking marker chrome must select the existing annotation",
  );
  assert.equal(
    port.messages.filter((message) => message.type === "faros.preview-console.annotation.selected").length,
    newSelectionsBeforeTailClick,
    "marker descendants must never become new annotation targets",
  );

  const stalePin = new FakeElement("button", "9");
  stalePin.setAttribute("data-faros-annotation-pin", "true");
  document.body.append(stalePin);
  windowListeners.get("click")({
    target: stalePin,
    composedPath() { return [stalePin, document.body, window]; },
    preventDefault() {},
    stopPropagation() {},
    stopImmediatePropagation() {},
  });
  assert.equal(
    port.messages.filter((message) => message.type === "faros.preview-console.annotation.selected").length,
    newSelectionsBeforeTailClick,
    "stale annotation chrome must fail closed instead of becoming a new annotation",
  );

  target.rect = { x: 40, y: 80, left: 40, top: 80, width: 200, height: 40 };
  window.scrollX = 30;
  window.scrollY = 60;
  windowListeners.get("scroll")({});
  assert.equal(firstPin.style.left, "76px", "pin should preserve its clicked horizontal point as the element moves and resizes");
  assert.equal(firstPin.style.top, "96px", "pin should preserve its clicked vertical point as the element moves and resizes");
  assert.equal(pinLayer.style.position, "fixed");
  assert.equal(firstPin.style.position, "absolute");
  target.rect = { x: 12, y: 24, left: 12, top: 24, width: 160, height: 32 };

  context.location.pathname = "/admin.html";
  windowListeners.get("scroll")({});
  assert.equal(firstPin.hidden, true, "a pin must hide when its annotated page is not active");
  const offRoute = port.messages.filter((message) => message.type === "faros.preview-console.annotation.pins-rendered").at(-1);
  assert.equal(offRoute.path, "/admin.html");
  assert.deepEqual(JSON.parse(JSON.stringify(offRoute.pins)), [
    { id: "first", resolved: false },
    { id: "second", resolved: false },
    { id: "ambiguous", resolved: false },
  ]);
  context.location.pathname = "/app";
  windowListeners.get("scroll")({});
  assert.equal(firstPin.hidden, false, "a route-bound pin must re-resolve when its page becomes active again");
  const returnedRoute = port.messages.filter((message) => message.type === "faros.preview-console.annotation.pins-rendered").at(-1);
  assert.equal(returnedRoute.path, "/app");
  assert.deepEqual(JSON.parse(JSON.stringify(returnedRoute.pins)), [
    { id: "first", resolved: true },
    { id: "second", resolved: true },
    { id: "ambiguous", resolved: false },
  ]);

  const acceptedPins = Array.from({ length: 64 }, (_, index) => ({
    id: "accepted-" + index,
    number: index + 1,
    documentID,
    pagePath: "/app",
    target: { locator: '[data-faros-id="account-card"]', locatorStrategy: "css" },
  }));
  port.postMessage({
    type: "faros.preview-console.annotation.pins",
    version: 1,
    sessionID,
    generation: documentID,
    pins: acceptedPins,
  });
  await tick();
  const acceptedPinLayer = document.body.children.find((child) => child.getAttribute("data-faros-annotation-pins") === "true");
  assert.equal(acceptedPinLayer.children.length, 64, "the bridge must render all 64 accepted pins");
  const acceptedRenderedPins = port.messages
    .filter((message) => message.type === "faros.preview-console.annotation.pins-rendered")
    .at(-1);
  assert.equal(acceptedRenderedPins.pins.length, 64);

  documentListeners.get("pointermove")({ target });
  const overlay = document.body.children.find((child) => child.getAttribute("data-faros-annotation-overlay") === "true");
  assert.equal(overlay.hidden, false);
  assert.equal(overlay.style.left, "12px");
  assert.equal(overlay.style.top, "24px");
  assert.equal(overlay.style.width, "160px");
  assert.equal(overlay.style.height, "32px");

  const click = {
    target,
    clientX: 52,
    clientY: 48,
    prevented: false,
    stopped: false,
    immediateStopped: false,
    preventDefault() { this.prevented = true; },
    stopPropagation() { this.stopped = true; },
    stopImmediatePropagation() { this.immediateStopped = true; },
  };
  windowListeners.get("click")(click);
  assert.equal(click.prevented, true);
  assert.equal(click.stopped, true);
  assert.equal(click.immediateStopped, true);
  const selected = port.messages.find((message) => message.type === "faros.preview-console.annotation.selected");
  assert.deepEqual(JSON.parse(JSON.stringify(selected.target)), {
    tag: "button",
    role: "button",
    name: "Open account settings",
    text: "Account settings",
    rect: { x: 12, y: 24, width: 160, height: 32 },
    ancestors: ["body"],
    locator: '[data-faros-id="account-card"]',
    locatorStrategy: "css",
  });
  assert.deepEqual(JSON.parse(JSON.stringify(selected.anchor)), { x: 0.25, y: 0.75 });
  assert.equal(Object.hasOwn(selected.target, "value"), false);
  assert.equal(Object.hasOwn(selected.target, "style"), false);
  assert.equal(Object.hasOwn(selected.target, "onclick"), false);

  const editor = new FakeElement("div", "typed secret that must not cross the bridge");
  editor.setAttribute("contenteditable", "true");
  editor.setAttribute("role", "textbox");
  editor.setAttribute("aria-label", "Description");
  editor.setAttribute("data-faros-id", "description-editor");
  document.body.append(editor);
  const editorClick = {
    target: editor,
    clientX: 52,
    clientY: 48,
    preventDefault() {},
    stopPropagation() {},
    stopImmediatePropagation() {},
  };
  windowListeners.get("click")(editorClick);
  const selectedEditor = port.messages.filter((message) => message.type === "faros.preview-console.annotation.selected").at(-1);
  assert.equal(selectedEditor.target.text, "", "contenteditable values must not be exposed as annotation text");

  const numericID = new FakeElement("button", "Numeric ID");
  numericID.setAttribute("id", "123");
  document.body.append(numericID);
  windowListeners.get("click")({
    target: numericID,
    clientX: 52,
    clientY: 48,
    preventDefault() {},
    stopPropagation() {},
    stopImmediatePropagation() {},
  });
  const selectedNumericID = port.messages.filter((message) => message.type === "faros.preview-console.annotation.selected").at(-1);
  assert.equal(selectedNumericID.target.locator, '[id="123"]', "numeric IDs must use a valid attribute selector");

  const extractionFailure = {
    preventDefaultCalled: false,
    stopPropagationCalled: false,
    stopImmediatePropagationCalled: false,
    get target() { throw new Error("cross-realm target unavailable"); },
    preventDefault() { this.preventDefaultCalled = true; },
    stopPropagation() { this.stopPropagationCalled = true; },
    stopImmediatePropagation() { this.stopImmediatePropagationCalled = true; },
  };
  windowListeners.get("click")(extractionFailure);
  assert.equal(extractionFailure.preventDefaultCalled, true, "target extraction failure must still prevent activation");
  assert.equal(extractionFailure.stopPropagationCalled, true);
  assert.equal(extractionFailure.stopImmediatePropagationCalled, true);

  port.postMessage({
    type: "faros.preview-console.annotation.pins",
    version: 1,
    sessionID,
    generation: documentID,
    pins: Array.from({ length: 65 }, (_, index) => ({
      id: "oversized-" + index,
      number: index + 1,
      documentID,
      target: { locator: '[data-faros-id="account-card"]', locatorStrategy: "css" },
    })),
  });
  await tick();
  const rejectedPins = port.messages.find((message) => message.type === "faros.preview-console.annotation.pins-rendered" && message.rejectedCount);
  assert.equal(rejectedPins.rejectedCount, 1, "oversized pin state must report rejection instead of truncating silently");
  assert.equal(acceptedPinLayer.removed, true, "a rejected replacement must clean up the previous marker layer");

  for (const tagName of ["script", "style"]) {
    const excluded = new FakeElement(tagName, "do not select");
    document.body.append(excluded);
    const excludedClick = {
      target: excluded,
      prevented: false,
      stopped: false,
      immediateStopped: false,
      preventDefault() { this.prevented = true; },
      stopPropagation() { this.stopped = true; },
      stopImmediatePropagation() { this.immediateStopped = true; },
    };
    windowListeners.get("click")(excludedClick);
    assert.equal(excludedClick.prevented, true, `${tagName} click was not suppressed`);
    assert.equal(excludedClick.stopped, true, `${tagName} click propagated during annotation mode`);
    assert.equal(excludedClick.immediateStopped, true, `${tagName} click was not intercepted`);
  }

  documentListeners.get("keydown")({
    key: "Escape",
    preventDefault() {},
    stopPropagation() {},
  });
  assert.equal(documentListeners.has("click"), false);
  assert.equal(windowListeners.has("click"), true, "the early window guard remains installed while inactive");
  assert.equal(document.documentElement.getAttribute("data-faros-annotation-mode"), null);
  assert.equal(cursorStyle.removed, true);
  assert.equal(port.messages.at(-2).type, "faros.preview-console.annotation.mode");
  assert.equal(port.messages.at(-2).active, false);
  assert.equal(port.messages.at(-1).type, "faros.preview-console.annotation.cancelled");

  port.postMessage({
      type: "faros.preview-console.annotation.start",
      version: 1,
      sessionID,
      generation: documentID,
  });
  await tick();
  port.postMessage({
      type: "faros.preview-console.annotation.stop",
      version: 1,
      sessionID,
      generation: documentID,
  });
  await tick();
  assert.equal(documentListeners.has("click"), false);
  assert.equal(overlay.removed, true);
  assert.equal(pinLayer.removed, true, "oversized pin state should clean up the previous marker layer");
});
