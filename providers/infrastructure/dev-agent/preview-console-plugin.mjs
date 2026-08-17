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

export const previewConsoleJWKSPath = "/faros/bin/preview-console-jwks.json";

const previewConsoleClient = String.raw`(() => {
  "use strict";

  const TRUSTED_VERIFICATION_KEYS = __FAROS_PREVIEW_CONSOLE_VERIFICATION_KEYS__;
  const VERSION = 1;
  const READY = "faros.preview-console.ready";
  const PROBE = "faros.preview-console.probe";
  const START = "faros.preview-console.start";
  const CONNECTED = "faros.preview-console.connected";
  const EVENTS = "faros.preview-console.events";
  const ANNOTATION_START = "faros.preview-console.annotation.start";
  const ANNOTATION_STOP = "faros.preview-console.annotation.stop";
  const ANNOTATION_PINS = "faros.preview-console.annotation.pins";
  const ANNOTATION_PINS_RENDERED = "faros.preview-console.annotation.pins-rendered";
  const ANNOTATION_PIN_HOVER = "faros.preview-console.annotation.pin-hover";
  const ANNOTATION_PIN_SELECTED = "faros.preview-console.annotation.pin-selected";
  const ANNOTATION_SELECTED = "faros.preview-console.annotation.selected";
  const ANNOTATION_CANCELLED = "faros.preview-console.annotation.cancelled";
  const ANNOTATION_MODE = "faros.preview-console.annotation.mode";
  const MAX_EVENTS = 200;
  const MAX_PORT_BATCH_EVENTS = 16;
  const MAX_PROPERTIES = 20;
  const MAX_STRING = 1000;
  const MAX_MESSAGE = 1200;
  const MAX_STACK = 600;
  const MAX_DEPTH = 2;
  const MAX_EVENT_BYTES = 1900;
  const MAX_ANNOTATION_STRING = 240;
  const MAX_ANNOTATION_TEXT = 320;
  const MAX_ANNOTATION_SELECTOR = 320;
  const MAX_ANNOTATION_PINS = 64;
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
  let handshakePort = null;
  let connected = false;
  let connecting = false;
  let flushScheduled = false;
  let probedOrigin = "";
  let sessionID = "";
  let generation = null;
  let droppedCount = 0;
  const consumedCapabilityIDs = new Set();
  let annotationMode = false;
  let annotationOverlay = null;
  let annotationPinLayer = null;
  let annotationPinRecords = [];
  let annotationPinMutationObserver = null;
  let annotationPinResizeObserver = null;
  let annotationPinSync = null;
  let annotationPinStateSignature = "";
  let annotationCursorStyle = null;
  let annotationPointerMove = null;
  let annotationClick = null;
  let annotationKeydown = null;

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

  const annotationString = (value, limit = MAX_ANNOTATION_STRING) => {
    if (typeof value !== "string") return "";
    return truncate(value.replace(/[\u0000-\u001f\u007f]/g, " ").trim(), limit);
  };

  const annotationAttribute = (element, name, limit = MAX_ANNOTATION_STRING) => {
    try {
      return annotationString(element.getAttribute(name), limit);
    } catch {
      return "";
    }
  };

  const annotationValueField = (element) => {
    let current = element;
    for (let depth = 0; current && depth < 32; depth++) {
      const tagName = annotationString(current.tagName || "").toUpperCase();
      if (["INPUT", "TEXTAREA", "SELECT", "OPTION"].includes(tagName)) return true;
      const contentEditable = annotationAttribute(current, "contenteditable").toLowerCase();
      if (contentEditable && contentEditable !== "false") return true;
      const role = annotationAttribute(current, "role").toLowerCase();
      if (["textbox", "searchbox", "combobox", "spinbutton"].includes(role)) return true;
      current = current.parentElement;
    }
    return false;
  };

  const annotationText = (element) => {
    try {
      if (["SCRIPT", "STYLE", "NOSCRIPT", "TEMPLATE"].includes(element.tagName) || annotationValueField(element)) return "";
      return annotationString(element.innerText || element.textContent || "", MAX_ANNOTATION_TEXT)
        .replace(/\s+/g, " ").trim();
    } catch {
      return "";
    }
  };

  const annotationRole = (element) => {
    const explicit = annotationAttribute(element, "role");
    if (explicit) return explicit;
    const implicit = {
      A: "link",
      BUTTON: "button",
      SUMMARY: "button",
      IMG: "img",
      INPUT: "textbox",
      TEXTAREA: "textbox",
      SELECT: "combobox",
      FORM: "form",
      NAV: "navigation",
      MAIN: "main",
      ASIDE: "complementary",
      HEADER: "banner",
      FOOTER: "contentinfo",
    };
    return implicit[element.tagName] || "";
  };

  // CSS.escape is not available in every preview runtime. This is the
  // standards-defined identifier escaping algorithm with the same bounded
  // input used by the rest of the annotation descriptor.
  const cssEscape = (value) => {
    const input = annotationString(value, MAX_ANNOTATION_STRING);
    let output = "";
    for (let index = 0; index < input.length; index++) {
      const code = input.charCodeAt(index);
      const character = input[index];
      if (code === 0) {
        output += "\\FFFD ";
      } else if ((code >= 1 && code <= 31) || code === 127 || (index === 0 && code >= 48 && code <= 57) ||
                 (index === 1 && code >= 48 && code <= 57 && input[0] === "-")) {
        output += "\\" + code.toString(16) + " ";
      } else if (index === 0 && character === "-" && input.length === 1) {
        output += "\\-";
      } else if (code >= 128 || character === "-" || character === "_" ||
                 (code >= 48 && code <= 57) || (code >= 65 && code <= 90) || (code >= 97 && code <= 122)) {
        output += character;
      } else {
        output += "\\" + character;
      }
    }
    return output;
  };

  const cssStringEscape = (value) => annotationString(value, MAX_ANNOTATION_STRING)
    .replace(/[\\"\n\r\f]/g, (character) => "\\" + character);

  const attributeSelector = (name, value) =>
    "[" + name + "=\"" + cssStringEscape(value) + "\"]";

  const idSelector = (value) => {
    const id = annotationString(value);
    // A hash selector cannot begin with a digit (or otherwise form an
    // identifier), so use an escaped attribute selector for those IDs.
    return /^[A-Za-z_]|^-[A-Za-z_]/.test(id)
      ? "#" + cssEscape(id)
      : attributeSelector("id", id);
  };

  const selectorMatchesElement = (selector, element) => {
    if (!selector || typeof document === "undefined") return false;
    try {
      if (typeof document.querySelectorAll !== "function") return false;
      const matches = Array.from(document.querySelectorAll(selector));
      return matches.length === 1 && matches[0] === element;
    } catch {
      return false;
    }
  };

  const semanticLocatorIsUnique = (element, strategy, value) => {
    if (!value || typeof document === "undefined" || typeof document.querySelectorAll !== "function") return false;
    try {
      const tag = annotationString(element.tagName || "", 64).toLowerCase() || "*";
      const candidates = Array.from(document.querySelectorAll(tag)).filter((candidate) => {
        if (!(candidate instanceof Element)) return false;
        if (annotationRole(candidate) !== annotationRole(element)) return false;
        return strategy === "aria"
          ? annotationAccessibleName(candidate) === value
          : annotationText(candidate) === value;
      });
      return candidates.length === 1 && candidates[0] === element;
    } catch {
      return false;
    }
  };

  const directAnnotationLocator = (element) => {
    const farosID = annotationAttribute(element, "data-faros-id");
    const id = annotationAttribute(element, "id");
    const standardTestID = annotationAttribute(element, "data-testid");
    const alternateTestID = annotationAttribute(element, "data-test-id");
    const testID = standardTestID || alternateTestID;
    const candidates = [];
    if (farosID) candidates.push({ locator: attributeSelector("data-faros-id", farosID), strategy: "css" });
    if (id) candidates.push({ locator: idSelector(id), strategy: "css" });
    if (testID) candidates.push({
      locator: attributeSelector(standardTestID ? "data-testid" : "data-test-id", testID),
      strategy: "testID",
    });
    return candidates.find((candidate) => selectorMatchesElement(candidate.locator, element)) || null;
  };

  const annotationSelector = (element) => {
    try {
      const parts = [];
      let current = element;
      for (let depth = 0; current && depth < 32 && current.nodeType === 1; depth++) {
        const tag = annotationString(current.tagName || "").toLowerCase();
        if (!tag || ["script", "style", "noscript", "template"].includes(tag)) return "";
        let selector = tag;
        const parent = current.parentElement;
        if (parent) {
          const siblings = Array.from(parent.children).filter((candidate) => candidate.tagName === current.tagName);
          if (siblings.length > 1) selector += ":nth-of-type(" + (siblings.indexOf(current) + 1) + ")";
        }
        parts.unshift(selector);
        current = parent;
      }
      const selector = annotationString(parts.join(" > "), MAX_ANNOTATION_SELECTOR);
      return selectorMatchesElement(selector, element) ? selector : "";
    } catch {
      return "";
    }
  };

  const annotationAccessibleName = (element) => {
    const aria = annotationAttribute(element, "aria-label");
    if (aria) return aria;
    const labelledBy = annotationAttribute(element, "aria-labelledby");
    if (labelledBy && typeof document !== "undefined") {
      try {
      const labels = labelledBy.split(/\s+/).slice(0, 4).map((id) => document.getElementById(id));
        const text = labels.filter(Boolean).map(annotationText).filter(Boolean).join(" ");
        if (text) return annotationString(text);
      } catch {}
    }
    return annotationAttribute(element, "alt") || annotationAttribute(element, "title");
  };

  const annotationTarget = (element) => {
    if (typeof Element === "undefined" || !(element instanceof Element)) return null;
    // Annotation chrome is bridge-owned UI, never application content. Keep
    // this guard at the descriptor boundary as defense in depth: even if a
    // marker is between render records, it must not become a new annotation.
    if (annotationChromeElement(element)) return null;
    const tagName = annotationString(element.tagName || "").toLowerCase();
    if (!tagName || ["script", "style", "noscript", "template"].includes(tagName)) return null;
    let rect;
    try { rect = element.getBoundingClientRect(); } catch { return null; }
    const target = {
      tag: tagName,
      role: annotationRole(element),
      name: annotationAccessibleName(element),
      text: annotationText(element),
      rect: {
        x: Number.isFinite(rect.x) ? Math.round(rect.x * 100) / 100 : 0,
        y: Number.isFinite(rect.y) ? Math.round(rect.y * 100) / 100 : 0,
        width: Number.isFinite(rect.width) ? Math.round(rect.width * 100) / 100 : 0,
        height: Number.isFinite(rect.height) ? Math.round(rect.height * 100) / 100 : 0,
      },
      ancestors: [],
    };
    const directLocator = directAnnotationLocator(element);
    const selector = annotationSelector(element);
    if (directLocator) {
      target.locator = directLocator.locator;
      target.locatorStrategy = directLocator.strategy;
    } else if (selector) {
      // Always retain a same-document DOM path when one can be generated.
      // Name/text remain descriptive facts for the model, while the CSS path
      // is the positioning identity used by annotation pins.
      target.locator = selector;
      target.locatorStrategy = "css";
    } else if (target.name && semanticLocatorIsUnique(element, "aria", target.name)) {
      target.locator = target.name;
      target.locatorStrategy = "aria";
    } else if (target.text && semanticLocatorIsUnique(element, "text", target.text)) {
      target.locator = target.text;
      target.locatorStrategy = "text";
    }
    let ancestor = element.parentElement;
    while (ancestor && target.ancestors.length < 16) {
      const ancestorTag = annotationString(ancestor.tagName || "", MAX_ANNOTATION_STRING).toLowerCase();
      if (ancestorTag && !["script", "style", "noscript", "template"].includes(ancestorTag)) target.ancestors.push(ancestorTag);
      ancestor = ancestor.parentElement;
    }
    // Never read form values, inline styles, event handlers, or arbitrary
    // attributes. The descriptor is intentionally semantic and bounded.
    return target;
  };

  const annotationMessage = (type, payload = {}) => ({
    type,
    version: VERSION,
    sessionID,
    generation,
    documentID,
    path: path(),
    ...payload,
  });

  const postAnnotation = (type, payload = {}) => {
    if (!connected || !port) return;
    try {
      port.postMessage(annotationMessage(type, payload));
    } catch {
      failPort();
    }
  };

  const failPort = () => {
    const failedPort = port;
    connected = false;
    connecting = false;
    port = null;
    sessionID = "";
    generation = null;
    stopAnnotationMode("port-error", false);
    removeAnnotationPins();
    try { failedPort?.close(); } catch {}
  };

  const removeAnnotationOverlay = () => {
    if (annotationOverlay) annotationOverlay.remove();
    annotationOverlay = null;
  };

  const hideAnnotationOverlay = () => {
    if (annotationOverlay) annotationOverlay.hidden = true;
  };

  const removeAnnotationPins = () => {
    for (const record of annotationPinRecords) {
      if (record.hoverActive) {
        record.hoverActive = false;
        emitAnnotationPinHover(record, false);
      }
    }
    if (annotationPinSync && typeof window !== "undefined") {
      window.removeEventListener("scroll", annotationPinSync, true);
      window.removeEventListener("resize", annotationPinSync);
      document.removeEventListener("load", annotationPinSync, true);
    }
    annotationPinMutationObserver?.disconnect();
    annotationPinResizeObserver?.disconnect();
    annotationPinMutationObserver = null;
    annotationPinResizeObserver = null;
    annotationPinSync = null;
    annotationPinRecords = [];
    annotationPinStateSignature = "";
    if (annotationPinLayer) annotationPinLayer.remove();
    annotationPinLayer = null;
  };

  const resolveAnnotationPinElement = (target) => {
    if (!target || typeof target !== "object" || typeof document === "undefined") return null;
    const locator = annotationString(target.locator, MAX_ANNOTATION_SELECTOR);
    const strategy = annotationString(target.locatorStrategy, 32).toLowerCase();
    if (!locator || !strategy) return null;
    if (strategy === "css" || strategy === "testid") {
      try {
        const matches = typeof document.querySelectorAll === "function"
          ? Array.from(document.querySelectorAll(locator))
          : [];
        if (matches.length === 1 && matches[0] instanceof Element) return matches[0];
      } catch {}
      if (strategy === "testid") {
        try {
          const matches = Array.from(document.querySelectorAll(attributeSelector("data-testid", locator)));
          if (matches.length === 1 && matches[0] instanceof Element) return matches[0];
        } catch {}
      }
      return null;
    }
    if (!["aria", "role", "text"].includes(strategy) || typeof document.querySelectorAll !== "function") return null;
    let candidates = [];
    try { candidates = Array.from(document.querySelectorAll(annotationString(target.tag, 64) || "*")).slice(0, 5000); } catch { return null; }
    const matches = candidates.filter((candidate) => {
      if (!(candidate instanceof Element)) return false;
      if (target.role && annotationRole(candidate) !== target.role) return false;
      if ((strategy === "aria" || strategy === "role") && annotationAccessibleName(candidate) === locator) return true;
      return strategy === "text" && annotationText(candidate) === locator;
    });
    return matches.length === 1 ? matches[0] : null;
  };

  const annotationPinRect = (element) => {
    if (!element?.getBoundingClientRect) return null;
    try {
      const rect = element.getBoundingClientRect();
      const x = Number.isFinite(rect.x) ? rect.x : rect.left;
      const y = Number.isFinite(rect.y) ? rect.y : rect.top;
      const width = Number.isFinite(rect.width) ? rect.width : 0;
      const height = Number.isFinite(rect.height) ? rect.height : 0;
      if (![x, y, width, height].every(Number.isFinite)) return null;
      return { x, y, width, height };
    } catch { return null; }
  };

  const boundedAnnotationRect = (value) => {
    try {
      if (!value || typeof value !== "object") return null;
      const raw = value;
      const number = (candidate) => typeof candidate === "number" && Number.isFinite(candidate) ? candidate : null;
      const x = number(raw.x);
      const y = number(raw.y);
      const width = number(raw.width);
      const height = number(raw.height);
      if (x === null || y === null || width === null || height === null || width < 0 || height < 0) return null;
      return {
        x: Math.max(-100_000, Math.min(100_000, x)),
        y: Math.max(-100_000, Math.min(100_000, y)),
        width: Math.max(0, Math.min(100_000, width)),
        height: Math.max(0, Math.min(100_000, height)),
      };
    } catch {
      return null;
    }
  };

  const boundedAnnotationAnchor = (value) => {
    try {
      if (!value || typeof value !== "object") return null;
      const x = typeof value.x === "number" && Number.isFinite(value.x) && value.x >= 0 && value.x <= 1 ? value.x : null;
      const y = typeof value.y === "number" && Number.isFinite(value.y) && value.y >= 0 && value.y <= 1 ? value.y : null;
      return x === null || y === null ? null : { x, y };
    } catch {
      return null;
    }
  };

  const annotationAnchorFromEvent = (event, element) => {
    const rect = annotationPinRect(element);
    const clientX = typeof event?.clientX === "number" && Number.isFinite(event.clientX) ? event.clientX : null;
    const clientY = typeof event?.clientY === "number" && Number.isFinite(event.clientY) ? event.clientY : null;
    if (!rect || clientX === null || clientY === null || rect.width <= 0 || rect.height <= 0) return null;
    if (clientX < rect.x || clientX > rect.x + rect.width || clientY < rect.y || clientY > rect.y + rect.height) return null;
    return {
      x: Math.round(((clientX - rect.x) / rect.width) * 1_000_000) / 1_000_000,
      y: Math.round(((clientY - rect.y) / rect.height) * 1_000_000) / 1_000_000,
    };
  };

  const annotationPinPoint = (record, rect) => {
    const anchor = boundedAnnotationAnchor(record?.anchor);
    if (!rect || !anchor) return null;
    return {
      x: rect.x + rect.width * anchor.x,
      y: rect.y + rect.height * anchor.y,
    };
  };

  const annotationPinHoverRect = (record) => {
    const rect = boundedAnnotationRect(annotationPinRect(record.element) || record.rect || record.boundingRect || record.target?.rect);
    const point = annotationPinPoint(record, rect);
    return point ? { x: point.x, y: point.y, width: 0, height: 0 } : rect;
  };

  const annotationPagePath = (value) => {
    const pagePath = annotationString(value, 1024);
    if (!pagePath.startsWith("/") || pagePath.startsWith("//") || /[?#\\]/.test(pagePath)) return "";
    return pagePath;
  };

  const emitAnnotationPinHover = (record, active) => {
    const rect = annotationPinHoverRect(record);
    if (!rect) return;
    postAnnotation(ANNOTATION_PIN_HOVER, { id: record.id, active, rect });
  };

  const updateAnnotationPinHover = (record, active) => {
    if (record.hoverActive === active) return;
    record.hoverActive = active;
    emitAnnotationPinHover(record, active);
  };

  const syncAnnotationPinPositions = () => {
    if (!annotationPinLayer) return;
    const states = [];
    for (const record of annotationPinRecords) {
      const routeMatches = record.pagePath === annotationPagePath(path());
      const connectedElement = routeMatches && record.element && (typeof record.element.isConnected !== "boolean" || record.element.isConnected)
        ? record.element
        : null;
      const previousElement = record.element;
      record.element = routeMatches ? (connectedElement || resolveAnnotationPinElement(record.target)) : null;
      if (record.element && record.element !== previousElement) {
        try { annotationPinResizeObserver?.observe(record.element); } catch {}
      }
      const rect = annotationPinRect(record.element);
      const resolved = Boolean(rect);
      states.push({ id: record.id, resolved });
      if (rect) record.rect = rect;
      record.pin.hidden = !rect;
      if (!rect) continue;
      const point = annotationPinPoint(record, rect);
      const visible = point
        ? point.x >= 0 && point.y >= 0 && point.x <= window.innerWidth && point.y <= window.innerHeight
        : rect.x + rect.width >= 0 && rect.y + rect.height >= 0 && rect.x <= window.innerWidth && rect.y <= window.innerHeight;
      record.pin.hidden = !visible;
      // The pin layer is a fixed child of <html>, not <body>. Body-level
      // positioning and transforms therefore cannot distort this viewport
      // coordinate calculation. Scrolling re-runs this calculation from the
      // target's current rect.
      const left = point ? point.x - 14 : rect.x + rect.width - 13;
      const top = point ? point.y - 14 : rect.y + Math.min(28, rect.height / 2) - 13;
      record.pin.style.left = String(Math.max(0, Math.min(window.innerWidth - 28, left))) + "px";
      record.pin.style.top = String(Math.max(0, Math.min(window.innerHeight - 28, top))) + "px";
    }
    const signature = states.map((state) => state.id + ":" + String(state.resolved)).join("|");
    if (signature !== annotationPinStateSignature) {
      annotationPinStateSignature = signature;
      postAnnotation(ANNOTATION_PINS_RENDERED, { pins: states });
    }
  };

  const installAnnotationCursor = () => {
    if (annotationCursorStyle || typeof document === "undefined") return;
    annotationCursorStyle = document.createElement("style");
    annotationCursorStyle.setAttribute("data-faros-annotation-cursor", "true");
    annotationCursorStyle.textContent =
      "html[data-faros-annotation-mode=\"true\"]," +
      "html[data-faros-annotation-mode=\"true\"] * {" +
      "cursor: url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='28' height='28' viewBox='0 0 28 28'%3E%3Cpath d='M5 3.5h16a3.5 3.5 0 0 1 3.5 3.5v10a3.5 3.5 0 0 1-3.5 3.5h-7l-5.5 4v-4H5A3.5 3.5 0 0 1 1.5 17V7A3.5 3.5 0 0 1 5 3.5Z' fill='%238b6bff' stroke='white' stroke-width='2'/%3E%3Ccircle cx='8' cy='12' r='1.3' fill='white'/%3E%3Ccircle cx='13' cy='12' r='1.3' fill='white'/%3E%3Ccircle cx='18' cy='12' r='1.3' fill='white'/%3E%3C/svg%3E\") 5 5, crosshair !important;" +
      "}";
    (document.head || document.documentElement)?.append(annotationCursorStyle);
    document.documentElement?.setAttribute("data-faros-annotation-mode", "true");
  };

  const removeAnnotationCursor = () => {
    if (typeof document === "undefined") {
      annotationCursorStyle = null;
      return;
    }
    document.documentElement?.removeAttribute("data-faros-annotation-mode");
    if (annotationCursorStyle) annotationCursorStyle.remove();
    annotationCursorStyle = null;
  };

  const updateAnnotationOverlay = (element) => {
    if (!annotationOverlay || !element?.getBoundingClientRect) return;
    try {
      const rect = element.getBoundingClientRect();
      annotationOverlay.style.left = String(Math.max(0, rect.left)) + "px";
      annotationOverlay.style.top = String(Math.max(0, rect.top)) + "px";
      annotationOverlay.style.width = String(Math.max(0, rect.width)) + "px";
      annotationOverlay.style.height = String(Math.max(0, rect.height)) + "px";
      annotationOverlay.hidden = false;
    } catch { annotationOverlay.hidden = true; }
  };

  const elementFromAnnotationEvent = (event) => {
    try {
      const target = event?.target;
      if (typeof Element !== "undefined" && target instanceof Element) return target;
      const point = typeof event?.clientX === "number" && typeof event?.clientY === "number"
        ? document.elementFromPoint(event.clientX, event.clientY)
        : null;
      return point instanceof Element ? point : null;
    } catch {
      return null;
    }
  };

  const annotationChromeElement = (element) => {
    let current = element;
    for (let depth = 0; current && depth < 8; depth++) {
      try {
        if (
          current.getAttribute?.("data-faros-annotation-pin") === "true" ||
          current.getAttribute?.("data-faros-annotation-pins") === "true" ||
          current.getAttribute?.("data-faros-annotation-overlay") === "true" ||
          current.getAttribute?.("data-faros-annotation-cursor") === "true"
        ) return current;
      } catch {
        return null;
      }
      current = current.parentElement;
    }
    return null;
  };

  const annotationEventContext = (event) => {
    const elements = [];
    const addElement = (candidate) => {
      if (typeof Element !== "undefined" && candidate instanceof Element && !elements.includes(candidate)) {
        elements.push(candidate);
      }
    };
    try {
      if (typeof event?.composedPath === "function") {
        for (const candidate of event.composedPath().slice(0, 32)) addElement(candidate);
      }
    } catch {}
    const target = elementFromAnnotationEvent(event);
    addElement(target);
    let ancestor = target?.parentElement;
    for (let depth = 0; ancestor && depth < 8; depth++) {
      addElement(ancestor);
      ancestor = ancestor.parentElement;
    }
    const pinRecord = annotationPinRecords.find((record) => elements.includes(record.pin)) || null;
    const chrome = elements.map(annotationChromeElement).find(Boolean) || null;
    return { element: target, pinRecord, chrome };
  };

  const interceptAnnotationClick = (event) => {
    if (!annotationMode) return;
    // This listener is installed at window capture before application
    // listeners. Interception is deliberately fail-closed: even if target
    // extraction, geometry, or descriptor construction fails, annotation
    // mode must not activate the application.
    try { event.preventDefault?.(); } catch {}
    try { event.stopPropagation?.(); } catch {}
    try { event.stopImmediatePropagation?.(); } catch {}
    const { element, pinRecord, chrome } = annotationEventContext(event);
    if (pinRecord) {
      const rect = annotationPinHoverRect(pinRecord);
      if (pinRecord.element) updateAnnotationOverlay(pinRecord.element);
      if (rect) postAnnotation(ANNOTATION_PIN_SELECTED, {
        id: pinRecord.id,
        rect,
        viewport: {
          width: Number.isFinite(window.innerWidth) ? Math.max(1, Math.round(window.innerWidth)) : 1,
          height: Number.isFinite(window.innerHeight) ? Math.max(1, Math.round(window.innerHeight)) : 1,
        },
      });
      return;
    }
    // Fail closed for stale or partially-rendered bridge UI. A marker without
    // a current record is still never an application element.
    if (chrome) {
      hideAnnotationOverlay();
      return;
    }
    let target = null;
    try { target = annotationTarget(element); } catch {}
    const anchor = annotationAnchorFromEvent(event, element);
    if (!target || !anchor) {
      hideAnnotationOverlay();
      return;
    }
    updateAnnotationOverlay(element);
    postAnnotation(ANNOTATION_SELECTED, {
      target,
      anchor,
      viewport: {
        width: Number.isFinite(window.innerWidth) ? Math.max(1, Math.round(window.innerWidth)) : 1,
        height: Number.isFinite(window.innerHeight) ? Math.max(1, Math.round(window.innerHeight)) : 1,
      },
    });
  };

  const stopAnnotationMode = (reason = "stopped", announce = true) => {
    if (annotationPointerMove) document.removeEventListener("pointermove", annotationPointerMove, true);
    if (annotationClick) document.removeEventListener("click", annotationClick, true);
    if (annotationKeydown) document.removeEventListener("keydown", annotationKeydown, true);
    annotationPointerMove = null;
    annotationClick = null;
    annotationKeydown = null;
    annotationMode = false;
    removeAnnotationOverlay();
    removeAnnotationCursor();
    if (announce) postAnnotation(ANNOTATION_MODE, { active: false, reason });
  };

  const startAnnotationMode = () => {
    if (!connected || !port || typeof document === "undefined") return false;
    stopAnnotationMode("restarted", false);
    annotationOverlay = document.createElement("div");
    annotationOverlay.setAttribute("data-faros-annotation-overlay", "true");
    annotationOverlay.setAttribute("aria-hidden", "true");
    Object.assign(annotationOverlay.style, {
      position: "fixed",
      pointerEvents: "none",
      zIndex: "2147483646",
      display: "block",
      boxSizing: "border-box",
      border: "2px solid #8b6bff",
      background: "rgba(139,107,255,.12)",
      borderRadius: "3px",
      transition: "left 60ms linear, top 60ms linear, width 60ms linear, height 60ms linear",
    });
    annotationOverlay.hidden = true;
    document.documentElement?.append(annotationOverlay);
    installAnnotationCursor();
    annotationPointerMove = (event) => {
      if (!annotationMode) return;
      const { element, pinRecord, chrome } = annotationEventContext(event);
      if (pinRecord) {
        if (pinRecord.element) updateAnnotationOverlay(pinRecord.element);
        else hideAnnotationOverlay();
        return;
      }
      if (chrome) { hideAnnotationOverlay(); return; }
      if (!element) { hideAnnotationOverlay(); return; }
      updateAnnotationOverlay(element.closest?.("*") || element);
    };
    annotationClick = interceptAnnotationClick;
    annotationKeydown = (event) => {
      if (!annotationMode || event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      stopAnnotationMode("escape");
      postAnnotation(ANNOTATION_CANCELLED, { reason: "escape" });
    };
    document.addEventListener("pointermove", annotationPointerMove, true);
    document.addEventListener("click", annotationClick, true);
    document.addEventListener("keydown", annotationKeydown, true);
    annotationMode = true;
    postAnnotation(ANNOTATION_MODE, { active: true });
    return true;
  };

  const renderAnnotationPins = (pins) => {
    removeAnnotationPins();
    if (typeof document === "undefined" || !Array.isArray(pins) || pins.length === 0) {
      postAnnotation(ANNOTATION_PINS_RENDERED, { pins: [] });
      return;
    }
    if (pins.length > MAX_ANNOTATION_PINS) {
      // Do not silently discard user annotations. The controller rejects
      // oversized desired state; this remains an explicit defensive guard for
      // a malformed or hostile port message.
      postAnnotation(ANNOTATION_PINS_RENDERED, {
        pins: [],
        rejectedCount: pins.length - MAX_ANNOTATION_PINS,
      });
      return;
    }
    annotationPinLayer = document.createElement("div");
    annotationPinLayer.setAttribute("data-faros-annotation-pins", "true");
    Object.assign(annotationPinLayer.style, {
      position: "fixed",
      left: "0",
      top: "0",
      width: "0",
      height: "0",
      pointerEvents: "none",
      zIndex: "2147483647",
    });
    for (const raw of pins) {
      if (!raw || typeof raw !== "object" || raw.documentID !== documentID) continue;
      const id = annotationString(raw.id, 96);
      const pagePath = annotationPagePath(raw.pagePath);
      if (!id || !pagePath || !raw.target || typeof raw.target !== "object") continue;
      const anchor = boundedAnnotationAnchor(raw.anchor);
      if (raw.anchor !== undefined && !anchor) continue;
      const pin = document.createElement("button");
      const label = typeof raw.number === "number" && Number.isFinite(raw.number)
        ? annotationString(String(raw.number), 3)
        : "";
      pin.textContent = label || "?";
      pin.setAttribute("type", "button");
      pin.setAttribute("aria-label", "Annotation " + (label || "?"));
      pin.setAttribute("data-faros-annotation-pin", "true");
      pin.setAttribute("data-faros-annotation-id", id);
      Object.assign(pin.style, {
        position: "absolute",
        left: "0",
        top: "0",
        width: "28px",
        height: "28px",
        padding: "0",
        boxSizing: "border-box",
        border: "2px solid #fff",
        background: "#8b6bff",
        color: "#fff",
        font: "600 12px sans-serif",
        lineHeight: "24px",
        textAlign: "center",
        borderRadius: "50%",
        boxShadow: "0 2px 10px rgba(0,0,0,.3)",
        cursor: "default",
        outline: "none",
        overflow: "visible",
        pointerEvents: "auto",
      });
      const tail = document.createElement("span");
      tail.setAttribute("aria-hidden", "true");
      Object.assign(tail.style, {
        position: "absolute",
        left: "1px",
        bottom: "-3px",
        width: "8px",
        height: "8px",
        borderLeft: "2px solid #fff",
        borderBottom: "2px solid #fff",
        background: "#8b6bff",
        transform: "rotate(-18deg) skew(-18deg)",
        borderBottomLeftRadius: "3px",
      });
      const record = {
        id,
        pagePath,
        target: raw.target,
        anchor,
        boundingRect: raw.boundingRect,
        element: pagePath === annotationPagePath(path()) ? resolveAnnotationPinElement(raw.target) : null,
        pin,
        rect: null,
        hoverActive: false,
      };
      let pointerHover = false;
      let focused = false;
      const syncPinHover = () => updateAnnotationPinHover(record, pointerHover || focused);
      pin.onmouseenter = () => { pointerHover = true; syncPinHover(); };
      pin.onmouseleave = () => { pointerHover = false; syncPinHover(); };
      pin.onfocus = () => { focused = true; syncPinHover(); };
      pin.onblur = () => { focused = false; syncPinHover(); };
      pin.append(tail);
      annotationPinLayer.append(pin);
      annotationPinRecords.push(record);
    }
    document.documentElement?.append(annotationPinLayer);
    annotationPinSync = syncAnnotationPinPositions;
    window.addEventListener("scroll", annotationPinSync, true);
    window.addEventListener("resize", annotationPinSync);
    document.addEventListener("load", annotationPinSync, true);
    if (typeof MutationObserver === "function") {
      annotationPinMutationObserver = new MutationObserver(annotationPinSync);
      annotationPinMutationObserver.observe(document.documentElement, { childList: true, subtree: true });
    }
    if (typeof ResizeObserver === "function") {
      annotationPinResizeObserver = new ResizeObserver(annotationPinSync);
      for (const record of annotationPinRecords) if (record.element) annotationPinResizeObserver.observe(record.element);
    }
    syncAnnotationPinPositions();
  };

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
        failPort();
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
    if (typeof MessageChannel !== "function") return;
    const channel = new MessageChannel();
    const previousHandshakePort = handshakePort;
    handshakePort = channel.port1;
    handshakePort.onmessage = onStart;
    try { previousHandshakePort?.close(); } catch {}
    if (typeof handshakePort.start === "function") handshakePort.start();
    try {
      window.parent.postMessage(readyMessage(), targetOrigin, [channel.port2]);
    } catch {
      // The bridge is optional; a parent that cannot receive it is harmless.
      try { handshakePort?.close(); } catch {}
      handshakePort = null;
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

  const onStart = async (event) => {
    const data = event?.data;
    if (!data || data.version !== VERSION || data.type !== START || connecting) return;
    const nextPort = handshakePort;
    if (!nextPort || !probedOrigin || String(data.generation || "") !== documentID) return;
    connecting = true;
    let claims = null;
    try {
      claims = await verifyCapability(data.capability, data, probedOrigin);
    } catch {
      claims = null;
    } finally {
      connecting = false;
    }
    if (!claims || consumedCapabilityIDs.has(claims.jti)) {
      try { nextPort.close(); } catch {}
      if (handshakePort === nextPort) handshakePort = null;
      return;
    }
    consumedCapabilityIDs.add(claims.jti);
    const previousPort = port;
    stopAnnotationMode("session-replaced", false);
    connected = true;
    port = nextPort;
    handshakePort = null;
    sessionID = String(data.sessionID || "");
    generation = documentID;
    if (previousPort && previousPort !== nextPort) {
      try { previousPort.close(); } catch {}
    }
    try {
      if (typeof port.start === "function") port.start();
      port.onmessage = (portEvent) => {
        const message = portEvent?.data;
        if (!message || message.version !== VERSION || message.sessionID !== sessionID || message.generation !== generation) return;
        try {
          if (message.type === ANNOTATION_START) {
            startAnnotationMode();
            return;
          }
          if (message.type === ANNOTATION_STOP) {
            stopAnnotationMode("stopped");
            return;
          }
          if (message.type === ANNOTATION_PINS) {
            renderAnnotationPins(message.pins);
          }
        } catch {
          failPort();
        }
      };
      port.postMessage({
        type: CONNECTED,
        version: VERSION,
        sessionID: data.sessionID,
        generation: data.generation,
        documentID,
        path: path(),
      });
    } catch {
      failPort();
      return;
    }
    scheduleFlush();
  };

  const onMessage = (event) => {
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
  };

  window.addEventListener("message", onMessage);
  // Register before application scripts so an app-level window capture
  // listener cannot run before annotation interception is enabled later.
  window.addEventListener("click", interceptAnnotationClick, true);
  // START is intentionally handled only on the endpoint sent outbound in
  // READY. No capability or transferred port is exposed to same-document
  // application message listeners.
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
  return previewConsoleClient.replace("__FAROS_PREVIEW_CONSOLE_VERIFICATION_KEYS__", encoded);
}

export function createPreviewConsolePlugin(configuration) {
  const client = previewConsoleClientSource(configuration);
  return {
    name: "faros-preview-console-v1",
    enforce: "pre",
    apply: "serve",
    transformIndexHtml() {
      return [{
        tag: "script",
        attrs: { "data-faros-preview-console": "v1" },
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
