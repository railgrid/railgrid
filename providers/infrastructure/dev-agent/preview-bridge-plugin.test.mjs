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

import assert from "node:assert/strict";
import { webcrypto } from "node:crypto";
import test from "node:test";
import vm from "node:vm";

import {
  createPreviewBridgePlugin,
  previewBridgeClientSource,
  previewBridgeFrameAncestors,
} from "./preview-bridge-plugin.mjs";

const portalOrigin = "https://studio.test";
const configuration = { portalOrigins: [portalOrigin] };

const tick = () => new Promise((resolve) => setImmediate(resolve));
const waitForPortMessage = async (port, type) => {
  for (let attempt = 0; attempt < 40; attempt++) {
    const message = port.messages.find((entry) => entry.type === type);
    if (message) return message;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  return null;
};

test("plugin injects the v1 bridge before application scripts", () => {
  const plugin = createPreviewBridgePlugin(configuration);
  const tags = plugin.transformIndexHtml();
  assert.equal(plugin.apply, "serve");
  assert.equal(tags[0].injectTo, "head-prepend");
  assert.equal(tags[0].attrs["data-railgrid-preview-bridge"], "v1");
  assert.match(tags[0].children, /railgrid\.preview-bridge\.ready/);
  assert.doesNotMatch(tags[0].children, /__RAILGRID_PREVIEW_BRIDGE_PORTAL_ORIGINS__/);
  assert.match(tags[0].children, /"https:\/\/studio\.test"/);
  assert.doesNotMatch(tags[0].children, /window\.console/);
  assert.doesNotMatch(tags[0].children, /unhandledrejection/);
  assert.doesNotMatch(tags[0].children, /railgrid\.preview-bridge\.events/);
});

test("bridge answers only the configured portal origin", async () => {
  const source = previewBridgeClientSource(configuration);

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
  const fakeConsole = Object.fromEntries(
    ["debug", "log", "info", "warn", "error"].map((level) => [level, () => {}]),
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
  assert.equal(windowListeners.has("error"), false, "bridge must not capture page errors");
  assert.equal(windowListeners.has("unhandledrejection"), false, "bridge must not capture promise rejections");
  assert.equal(window.console.error, fakeConsole.error, "bridge must not wrap window.console");
  assert.equal(window.console.log, fakeConsole.log, "bridge must not wrap window.console");

  assert.equal(parentMessages.length, 1);
  assert.equal(parentMessages[0].message.type, "railgrid.preview-bridge.ready");
  const documentID = parentMessages[0].message.documentID;
  assert.match(documentID, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  assert.equal(parentMessages[0].origin, portalOrigin, "initial READY must be addressed to the configured origin, never *");
  const sessionID = "session-1";

  const probesBefore = parentMessages.length;
  await windowListeners.get("message")({
    source: parent,
    origin: "https://attacker.test",
    data: { type: "railgrid.preview-bridge.probe", version: 1 },
    ports: [],
  });
  assert.equal(parentMessages.length, probesBefore, "a foreign parent origin must not receive a handshake endpoint");

  await windowListeners.get("message")({
    source: parent,
    origin: portalOrigin,
    data: { type: "railgrid.preview-bridge.probe", version: 1 },
    ports: [],
  });
  assert.equal(parentMessages.at(-1).origin, portalOrigin);

  const stalePort = parentMessages.at(-1).ports[0];
  stalePort.postMessage({
    type: "railgrid.preview-bridge.start",
    version: 1,
    sessionID,
    generation: "a-previous-document",
  });
  await tick();
  assert.equal(stalePort.messages.some((message) => message.type === "railgrid.preview-bridge.connected"), false, "START must echo this document's ID");

  const trustedPort = stalePort;
  trustedPort.postMessage({
    type: "railgrid.preview-bridge.start",
    version: 1,
    sessionID,
    generation: documentID,
  });
  assert.ok(await waitForPortMessage(trustedPort, "railgrid.preview-bridge.connected"));
  const connected = trustedPort.messages.find((message) => message.type === "railgrid.preview-bridge.connected");
  assert.equal(connected.generation, documentID);
  assert.equal(connected.sessionID, sessionID);
  assert.equal(trustedPort.messages.some((message) => message.type === "railgrid.preview-bridge.events"), false, "connected bridge must not emit console events");
  assert.equal(window.console.error, fakeConsole.error, "bridge must not wrap window.console");
  assert.equal(window.console.log, fakeConsole.log, "bridge must not wrap window.console");

  await windowListeners.get("message")({
    source: parent,
    origin: portalOrigin,
    data: { type: "railgrid.preview-bridge.probe", version: 1 },
  });
  const renewalPort = parentMessages.at(-1).ports[0];
  renewalPort.postMessage({
    type: "railgrid.preview-bridge.start",
    version: 1,
    sessionID: "session-2",
    generation: documentID,
  });
  assert.ok(await waitForPortMessage(renewalPort, "railgrid.preview-bridge.connected"));
  assert.equal(trustedPort.closed, true, "a new handshake replaces the previous session port");

  window.console.log("console observation is disabled");
  await tick();
  assert.equal(renewalPort.messages.some((message) => message.type === "railgrid.preview-bridge.events"), false, "console calls must not emit event batches");
});

test("bridge configuration accepts only exact http(s) origins", () => {
  for (const portalOrigins of [
    undefined,
    [],
    ["https://studio.test/"],
    ["https://studio.test/path"],
    ["javascript:alert(1)"],
    ["*"],
    ["not a url"],
    Array.from({ length: 9 }, (_, index) => `https://studio${index}.test`),
  ]) {
    assert.throws(() => previewBridgeClientSource({ portalOrigins }), undefined, JSON.stringify(portalOrigins));
  }
  assert.equal(
    previewBridgeFrameAncestors({ portalOrigins: ["https://a.test", "http://localhost:9443"] }),
    "frame-ancestors 'self' https://a.test http://localhost:9443",
  );
});

test("client source embeds origins verbatim, without replacement patterns", () => {
  const origins = ["http://a$&b.test", "http://c$'d.test", "http://e$`f.test"];
  const source = previewBridgeClientSource({ portalOrigins: origins });
  assert.ok(source.includes(`const PORTAL_ORIGINS = ${JSON.stringify(origins)};`));
});

test("plugin sets frame-ancestors on HTML documents only", () => {
  const plugin = createPreviewBridgePlugin(configuration);
  let middleware;
  plugin.configureServer({ middlewares: { use(handler) { middleware = handler; } } });
  const run = (method, accept) => {
    const headers = {};
    let nextCalled = false;
    middleware({ method, headers: { accept } }, { setHeader(name, value) { headers[name] = value; } }, () => { nextCalled = true; });
    assert.equal(nextCalled, true);
    return headers["Content-Security-Policy"];
  };
  assert.equal(run("GET", "text/html,application/xhtml+xml"), `frame-ancestors 'self' ${portalOrigin}`);
  assert.equal(run("GET", "*/*"), undefined);
  assert.equal(run("POST", "text/html"), undefined);
});

test("connected annotation mode captures bounded targets without activating the app", async () => {
  const source = previewBridgeClientSource(configuration);
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
    focus() { document.activeElement = this; }
    scrollIntoView() { this.scrolled = true; }
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
    getComputedStyle(element) { return element.style; },
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
  const sessionID = "annotation-session";
  await windowListeners.get("message")({
    source: parent,
    origin: portalOrigin,
    data: { type: "railgrid.preview-bridge.probe", version: 1 },
    ports: [],
  });
  const port = parentMessages.at(-1).ports[0];
  port.postMessage({
    type: "railgrid.preview-bridge.start",
    version: 1,
    sessionID,
    generation: documentID,
  });
  assert.ok(await waitForPortMessage(port, "railgrid.preview-bridge.connected"));

  const target = new FakeElement("button", "Account settings");
  target.setAttribute("data-railgrid-id", "account-card");
  target.setAttribute("aria-label", "Open account settings");
  target.setAttribute("value", "secret-value");
  target.setAttribute("style", "color: red");
  target.setAttribute("onclick", "steal()");
  document.body.append(target);
  document.pointTarget = target;
  target.focus();
  document.body.append(new FakeElement("button", "Duplicate label"));
  document.body.append(new FakeElement("button", "Duplicate label"));

  port.postMessage({
      type: "railgrid.preview-bridge.annotation.start",
      version: 1,
      sessionID,
      generation: documentID,
  });
  await tick();
  assert.equal(documentListeners.has("pointermove"), true);
  assert.equal(documentListeners.has("click"), true);
  assert.equal(documentListeners.has("keydown"), true);
  assert.equal(document.documentElement.getAttribute("data-railgrid-annotation-mode"), "true");
  const cursorStyle = document.documentElement.children.find((child) => child.getAttribute("data-railgrid-annotation-cursor") === "true");
  const keyboardHelp = document.documentElement.children.find((child) => child.getAttribute("data-railgrid-annotation-keyboard") === "true");
  assert.equal(document.activeElement, keyboardHelp);
  assert.match(keyboardHelp.getAttribute("aria-label"), /Arrow|arrow/);
  assert.ok(cursorStyle);
  assert.match(cursorStyle.textContent, /data:image\/svg\+xml/);
  assert.match(cursorStyle.textContent, /crosshair !important/);

  port.postMessage({
      type: "railgrid.preview-bridge.annotation.pins",
      version: 1,
      sessionID,
      generation: documentID,
      pins: [
        { id: "first", number: 1, documentID, pagePath: "/app", boundingRect: { x: 12, y: 24, width: 16, height: 16 }, target: { locator: '[data-railgrid-id="account-card"]', locatorStrategy: "css" }, anchor: { x: 0.25, y: 0.75 }, comment: "Make this blue" },
        { id: "stale", number: 99, documentID: "stale-document", boundingRect: { x: 40, y: 50, width: 16, height: 16 } },
        { id: "second", number: 2, documentID, pagePath: "/app", boundingRect: { x: 64, y: 72, width: 16, height: 16 }, target: { locator: '[data-railgrid-id="account-card"]', locatorStrategy: "css" }, comment: "Clarify this heading" },
        { id: "ambiguous", number: 3, documentID, pagePath: "/app", target: { locator: "Duplicate label", locatorStrategy: "text" } },
        { id: "invalid-anchor", number: 4, documentID, pagePath: "/app", target: { locator: '[data-railgrid-id="account-card"]', locatorStrategy: "css" }, anchor: { x: 2, y: 0.5 } },
      ],
  });
  await tick();
  const pinLayer = document.body.children.find((child) => child.getAttribute("data-railgrid-annotation-pins") === "true");
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
  const renderedPins = port.messages.find((message) => message.type === "railgrid.preview-bridge.annotation.pins-rendered");
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
    .filter((message) => message.type === "railgrid.preview-bridge.annotation.pin-hover")
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
  const selectedPin = port.messages.find((message) => message.type === "railgrid.preview-bridge.annotation.pin-selected");
  assert.deepEqual(JSON.parse(JSON.stringify(selectedPin)), {
    type: "railgrid.preview-bridge.annotation.pin-selected",
    version: 1,
    sessionID,
    generation: documentID,
    documentID,
    path: "/app",
    id: "first",
    rect: { x: 52, y: 48, width: 0, height: 0 },
    viewport: { width: 1024, height: 768 },
  });
  assert.equal(port.messages.some((message) => message.type === "railgrid.preview-bridge.annotation.selected"), false, "clicking a pin must edit it instead of selecting the marker DOM");

  const pinTail = firstPin.children[0];
  const pinSelectionsBeforeTailClick = port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.pin-selected").length;
  const newSelectionsBeforeTailClick = port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.selected").length;
  const pinTailClick = {
    target: pinTail,
    composedPath() { return [pinTail, firstPin, pinLayer, document.documentElement, window]; },
    preventDefault() {},
    stopPropagation() {},
    stopImmediatePropagation() {},
  };
  windowListeners.get("click")(pinTailClick);
  assert.equal(
    port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.pin-selected").length,
    pinSelectionsBeforeTailClick + 1,
    "clicking marker chrome must select the existing annotation",
  );
  assert.equal(
    port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.selected").length,
    newSelectionsBeforeTailClick,
    "marker descendants must never become new annotation targets",
  );

  const stalePin = new FakeElement("button", "9");
  stalePin.setAttribute("data-railgrid-annotation-pin", "true");
  document.body.append(stalePin);
  windowListeners.get("click")({
    target: stalePin,
    composedPath() { return [stalePin, document.body, window]; },
    preventDefault() {},
    stopPropagation() {},
    stopImmediatePropagation() {},
  });
  assert.equal(
    port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.selected").length,
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
  const offRoute = port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.pins-rendered").at(-1);
  assert.equal(offRoute.path, "/admin.html");
  assert.deepEqual(JSON.parse(JSON.stringify(offRoute.pins)), [
    { id: "first", resolved: false },
    { id: "second", resolved: false },
    { id: "ambiguous", resolved: false },
  ]);
  context.location.pathname = "/app";
  windowListeners.get("scroll")({});
  assert.equal(firstPin.hidden, false, "a route-bound pin must re-resolve when its page becomes active again");
  const returnedRoute = port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.pins-rendered").at(-1);
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
    target: { locator: '[data-railgrid-id="account-card"]', locatorStrategy: "css" },
  }));
  port.postMessage({
    type: "railgrid.preview-bridge.annotation.pins",
    version: 1,
    sessionID,
    generation: documentID,
    pins: acceptedPins,
  });
  await tick();
  const acceptedPinLayer = document.body.children.find((child) => child.getAttribute("data-railgrid-annotation-pins") === "true");
  assert.equal(acceptedPinLayer.children.length, 64, "the bridge must render all 64 accepted pins");
  const acceptedRenderedPins = port.messages
    .filter((message) => message.type === "railgrid.preview-bridge.annotation.pins-rendered")
    .at(-1);
  assert.equal(acceptedRenderedPins.pins.length, 64);

  const hoverTarget = new FakeElement("p", "Pointer target");
  document.body.append(hoverTarget);
  documentListeners.get("pointermove")({ target: hoverTarget });
  assert.match(keyboardHelp.children[1].textContent, /Pointer target/);
  assert.equal(hoverTarget.scrolled, undefined, "pointer hover must not scroll the page");
  hoverTarget.remove();
  documentListeners.get("pointermove")({ target });
  assert.match(keyboardHelp.children[1].textContent, /Open account settings/);
  const overlay = document.body.children.find((child) => child.getAttribute("data-railgrid-annotation-overlay") === "true");
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
  const selected = port.messages.find((message) => message.type === "railgrid.preview-bridge.annotation.selected");
  assert.deepEqual(JSON.parse(JSON.stringify(selected.target)), {
    tag: "button",
    role: "button",
    name: "Open account settings",
    text: "Account settings",
    rect: { x: 12, y: 24, width: 160, height: 32 },
    ancestors: ["body"],
    locator: '[data-railgrid-id="account-card"]',
    locatorStrategy: "css",
  });
  assert.deepEqual(JSON.parse(JSON.stringify(selected.anchor)), { x: 0.25, y: 0.75 });
  assert.equal(Object.hasOwn(selected.target, "value"), false);
  assert.equal(Object.hasOwn(selected.target, "style"), false);
  assert.equal(Object.hasOwn(selected.target, "onclick"), false);

  const staticText = new FakeElement("p", "Static content");
  const hiddenText = new FakeElement("p", "Hidden content");
  hiddenText.hidden = true;
  const inertText = new FakeElement("p", "Inert content");
  inertText.setAttribute("inert", "");
  const transparent = new FakeElement("p", "Transparent content");
  transparent.style.opacity = "0";
  const clip = new FakeElement("div", "Clip container");
  clip.style.overflowX = "hidden";
  clip.style.overflowY = "clip";
  clip.rect = { left: 0, top: 0, width: 100, height: 100 };
  const clipped = new FakeElement("p", "Clipped content");
  clipped.rect = { left: 150, top: 10, width: 20, height: 20 };
  const scroller = new FakeElement("div", "Scrollable container");
  scroller.rect = { left: 0, top: 0, width: 100, height: 100 };
  scroller.style.overflowX = "auto";
  scroller.clientWidth = 100;
  scroller.scrollWidth = 200;
  const scrollableText = new FakeElement("p", "Scrollable content");
  scrollableText.rect = { left: 150, top: 10, width: 20, height: 20 };
  scroller.append(scrollableText);
  clip.append(clipped, scroller);
  document.body.append(hiddenText, inertText, transparent, clip, staticText);
  const keyEvent = (key) => ({
    key, prevented: false, stopped: false, immediateStopped: false,
    preventDefault() { this.prevented = true; },
    stopPropagation() { this.stopped = true; },
    stopImmediatePropagation() { this.immediateStopped = true; },
  });
  // The first candidate is the account button. Skip the two other buttons,
  // then the hidden element and bridge chrome to reach static content.
  let sawScrollable = false;
  for (let i = 0; i < 6; i++) {
    const down = keyEvent("ArrowDown");
    windowListeners.get("keydown")(down);
    assert.equal(down.prevented && down.immediateStopped, true);
    assert.doesNotMatch(keyboardHelp.children[1].textContent, /Transparent content|Clipped content/);
    if (keyboardHelp.children[1].textContent.includes("Scrollable content")) sawScrollable = true;
  }
  assert.equal(sawScrollable, true, "offscreen scrollable content remains reachable");
  assert.equal(staticText.scrolled, true);
  assert.match(keyboardHelp.children[1].textContent, /Static content/);
  const enter = keyEvent("Enter");
  windowListeners.get("keydown")(enter);
  assert.equal(enter.prevented && enter.immediateStopped, true);
  const keyboardSelection = port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.selected").at(-1);
  assert.equal(keyboardSelection.target.tag, "p");
  assert.equal(keyboardSelection.target.text, "Static content");
  assert.deepEqual(JSON.parse(JSON.stringify(keyboardSelection.anchor)), { x: 0.5, y: 0.5 });
  const selectionCount = port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.selected").length;
  const repeat = keyEvent("Enter");
  repeat.repeat = true;
  windowListeners.get("keydown")(repeat);
  assert.equal(port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.selected").length, selectionCount);

  // visibility is inherited but can be overridden: a visible child of a
  // visibility:hidden parent is on screen and reachable; the parent is not.
  const invisibleParent = new FakeElement("div", "Invisible parent");
  invisibleParent.style.visibility = "hidden";
  const visibleChild = new FakeElement("p", "Visible child");
  visibleChild.style.visibility = "visible";
  invisibleParent.append(visibleChild);
  document.body.append(invisibleParent);
  const visibleDown = keyEvent("ArrowDown");
  windowListeners.get("keydown")(visibleDown);
  assert.match(keyboardHelp.children[1].textContent, /Visible child/);
  invisibleParent.remove();

  const editor = new FakeElement("div", "typed secret that must not cross the bridge");
  editor.setAttribute("contenteditable", "true");
  editor.setAttribute("role", "textbox");
  editor.setAttribute("aria-label", "Description");
  editor.setAttribute("data-railgrid-id", "description-editor");
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
  const selectedEditor = port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.selected").at(-1);
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
  const selectedNumericID = port.messages.filter((message) => message.type === "railgrid.preview-bridge.annotation.selected").at(-1);
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
    type: "railgrid.preview-bridge.annotation.pins",
    version: 1,
    sessionID,
    generation: documentID,
    pins: Array.from({ length: 65 }, (_, index) => ({
      id: "oversized-" + index,
      number: index + 1,
      documentID,
      target: { locator: '[data-railgrid-id="account-card"]', locatorStrategy: "css" },
    })),
  });
  await tick();
  const rejectedPins = port.messages.find((message) => message.type === "railgrid.preview-bridge.annotation.pins-rendered" && message.rejectedCount);
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
  assert.equal(document.documentElement.getAttribute("data-railgrid-annotation-mode"), null);
  assert.equal(cursorStyle.removed, true);
  assert.equal(keyboardHelp.removed, true);
  assert.equal(document.activeElement, target);
  assert.equal(port.messages.at(-2).type, "railgrid.preview-bridge.annotation.mode");
  assert.equal(port.messages.at(-2).active, false);
  assert.equal(port.messages.at(-1).type, "railgrid.preview-bridge.annotation.cancelled");

  port.postMessage({
      type: "railgrid.preview-bridge.annotation.start",
      version: 1,
      sessionID,
      generation: documentID,
  });
  await tick();
  port.postMessage({
      type: "railgrid.preview-bridge.annotation.stop",
      version: 1,
      sessionID,
      generation: documentID,
  });
  await tick();
  assert.equal(documentListeners.has("click"), false);
  assert.equal(overlay.removed, true);
  assert.equal(pinLayer.removed, true, "oversized pin state should clean up the previous marker layer");
});

// loadBridge evaluates one preview document's bridge client against a fake
// window whose parent records every postMessage with its targetOrigin.
function loadBridge(bridgeConfiguration, parent = { messages: [] }) {
  const windowListeners = new Map();
  parent.postMessage ??= (message, origin, ports = []) => parent.messages.push({ message, origin, ports });
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
        this.peer.onmessage?.({ data: copy });
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
  class FakeNode {}
  const window = {
    parent,
    console: {},
    addEventListener(type, listener) {
      windowListeners.set(type, listener);
    },
    removeEventListener(type, listener) {
      if (windowListeners.get(type) === listener) windowListeners.delete(type);
    },
  };
  const context = vm.createContext({
    URL, Error, Element: FakeNode, Node: FakeNode, TextDecoder, TextEncoder, Uint8Array, atob,
    crypto: { subtle: webcrypto.subtle, getRandomValues: webcrypto.getRandomValues.bind(webcrypto) },
    location: { href: "https://preview.test/", origin: "https://preview.test", pathname: "/", search: "", hash: "" },
    Promise, Date, JSON, Object, Array, String, Number, Boolean, Math, WeakSet, queueMicrotask,
    MessageChannel: FakeMessageChannel,
    window,
  });
  vm.runInContext(previewBridgeClientSource(bridgeConfiguration), context);
  const deliver = (event) => windowListeners.get("message")({ ports: [], ...event });
  return { parent, deliver, FakeMessageChannel };
}

test("bridge gives a non-allowlisted parent no READY, port or session", async () => {
  // The preview is framed by a page on an origin outside the allowlist.
  const foreignOrigin = "https://attacker.test";
  const { parent, deliver, FakeMessageChannel } = loadBridge(configuration);

  // The initial announcement is addressed only to the configured origin, so
  // the browser drops it for any other parent; never "*".
  assert.equal(parent.messages.length, 1);
  assert.deepEqual(parent.messages.map((entry) => entry.origin), [portalOrigin]);
  const announced = parent.messages.length;

  // A PROBE from the foreign parent is ignored, including near-miss origins.
  for (const origin of [foreignOrigin, "http://studio.test", "https://studio.test:8443", "https://evil.studio.test", "null"]) {
    await deliver({ source: parent, origin, data: { type: "railgrid.preview-bridge.probe", version: 1 } });
  }
  assert.equal(parent.messages.length, announced, "no READY or handshake port for a non-allowlisted origin");

  // A sender that is not the parent cannot probe even with the right origin.
  await deliver({ source: { postMessage() {} }, origin: portalOrigin, data: { type: "railgrid.preview-bridge.probe", version: 1 } });
  assert.equal(parent.messages.length, announced, "only window.parent may probe");

  // START is honoured only on the handshake port, never as a window message
  // carrying the sender's own port.
  const forged = new FakeMessageChannel();
  await deliver({
    source: parent,
    origin: foreignOrigin,
    data: { type: "railgrid.preview-bridge.start", version: 1, sessionID: "forged", generation: parent.messages[0].message.documentID },
    ports: [forged.port2],
  });
  await tick();
  assert.equal(forged.port1.messages.length, 0, "a forged START must not open a session");
  assert.equal(parent.messages.length, announced);
});

test("a reloaded preview document refuses a session bound to its predecessor", async () => {
  const parent = { messages: [] };
  loadBridge(configuration, parent);
  const previousDocumentID = parent.messages[0].message.documentID;

  // Reload: a new document runs the bridge again for the same parent.
  parent.messages.length = 0;
  loadBridge(configuration, parent);
  const ready = parent.messages[0];
  assert.notEqual(ready.message.documentID, previousDocumentID, "each document announces a fresh ID");

  const port = ready.ports[0];
  port.postMessage({ type: "railgrid.preview-bridge.start", version: 1, sessionID: "s", generation: previousDocumentID });
  await tick();
  assert.equal(port.messages.some((message) => message.type === "railgrid.preview-bridge.connected"), false,
    "a START echoing the previous document's ID must not connect");

  port.postMessage({ type: "railgrid.preview-bridge.start", version: 1, sessionID: "s", generation: ready.message.documentID });
  const connected = await waitForPortMessage(port, "railgrid.preview-bridge.connected");
  assert.equal(connected?.documentID, ready.message.documentID);
});
