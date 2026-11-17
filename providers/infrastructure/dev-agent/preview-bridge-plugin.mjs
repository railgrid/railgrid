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

import { readFileSync } from "node:fs";

export const previewBridgeConfigPath = "/railgrid/bin/preview-bridge-config.json";
const maxPortalOrigins = 8;

const previewBridgeClient = String.raw`(() => {
  "use strict";

  // Platform-configured portal origins. The browser stamps event.origin on
  // every message and enforces postMessage targetOrigin, so an exact match is
  // the whole trust decision: no other parent can open or drive the bridge.
  const PORTAL_ORIGINS = __RAILGRID_PREVIEW_BRIDGE_PORTAL_ORIGINS__;
  const VERSION = 1;
  const READY = "railgrid.preview-bridge.ready";
  const PROBE = "railgrid.preview-bridge.probe";
  const START = "railgrid.preview-bridge.start";
  const CONNECTED = "railgrid.preview-bridge.connected";
  const ANNOTATION_START = "railgrid.preview-bridge.annotation.start";
  const ANNOTATION_STOP = "railgrid.preview-bridge.annotation.stop";
  const ANNOTATION_PINS = "railgrid.preview-bridge.annotation.pins";
  const ANNOTATION_PINS_RENDERED = "railgrid.preview-bridge.annotation.pins-rendered";
  const ANNOTATION_PIN_HOVER = "railgrid.preview-bridge.annotation.pin-hover";
  const ANNOTATION_PIN_SELECTED = "railgrid.preview-bridge.annotation.pin-selected";
  const ANNOTATION_SELECTED = "railgrid.preview-bridge.annotation.selected";
  const ANNOTATION_CANCELLED = "railgrid.preview-bridge.annotation.cancelled";
  const ANNOTATION_MODE = "railgrid.preview-bridge.annotation.mode";
  const MAX_ANNOTATION_STRING = 240;
  const MAX_ANNOTATION_TEXT = 320;
  const MAX_ANNOTATION_SELECTOR = 320;
  const MAX_ANNOTATION_PINS = 64;
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
  let port = null;
  let connected = false;
  let sessionID = "";
  let generation = null;
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
  let annotationKeyboardTarget = null;
  let annotationKeyboardStatus = null;
  let annotationPreviousFocus = null;

  const truncate = (value, limit = 1000) => {
    const text = String(value);
    return text.length > limit ? text.slice(0, limit) + "…" : text;
  };

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
    const railgridID = annotationAttribute(element, "data-railgrid-id");
    const id = annotationAttribute(element, "id");
    const standardTestID = annotationAttribute(element, "data-testid");
    const alternateTestID = annotationAttribute(element, "data-test-id");
    const testID = standardTestID || alternateTestID;
    const candidates = [];
    if (railgridID) candidates.push({ locator: attributeSelector("data-railgrid-id", railgridID), strategy: "css" });
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
    annotationCursorStyle.setAttribute("data-railgrid-annotation-cursor", "true");
    annotationCursorStyle.textContent =
      "html[data-railgrid-annotation-mode=\"true\"]," +
      "html[data-railgrid-annotation-mode=\"true\"] * {" +
      "cursor: url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='28' height='28' viewBox='0 0 28 28'%3E%3Cpath d='M5 3.5h16a3.5 3.5 0 0 1 3.5 3.5v10a3.5 3.5 0 0 1-3.5 3.5h-7l-5.5 4v-4H5A3.5 3.5 0 0 1 1.5 17V7A3.5 3.5 0 0 1 5 3.5Z' fill='%238b6bff' stroke='white' stroke-width='2'/%3E%3Ccircle cx='8' cy='12' r='1.3' fill='white'/%3E%3Ccircle cx='13' cy='12' r='1.3' fill='white'/%3E%3Ccircle cx='18' cy='12' r='1.3' fill='white'/%3E%3C/svg%3E\") 5 5, crosshair !important;" +
      "}";
    (document.head || document.documentElement)?.append(annotationCursorStyle);
    document.documentElement?.setAttribute("data-railgrid-annotation-mode", "true");
  };

  const removeAnnotationCursor = () => {
    if (typeof document === "undefined") {
      annotationCursorStyle = null;
      return;
    }
    document.documentElement?.removeAttribute("data-railgrid-annotation-mode");
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
          current.getAttribute?.("data-railgrid-annotation-pin") === "true" ||
          current.getAttribute?.("data-railgrid-annotation-pins") === "true" ||
          current.getAttribute?.("data-railgrid-annotation-overlay") === "true" ||
          current.getAttribute?.("data-railgrid-annotation-cursor") === "true" ||
          current.getAttribute?.("data-railgrid-annotation-keyboard") === "true"
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

  // Navigate page elements without changing the page's focus or tabindex. This
  // also makes static text and containers available to keyboard users.
  const keyboardCandidates = () => {
    // Ancestors are shared by many candidates: read each node's style and
    // clip box once per scan, not once per descendant, so a key press stays
    // linear in the size of the page.
    const styles = new Map(), clips = new Map();
    const styleOf = (node) => {
      if (!styles.has(node)) styles.set(node, window.getComputedStyle?.(node) || null);
      return styles.get(node);
    };
    const clipOf = (node) => {
      if (!clips.has(node)) {
        const bounds = node.getBoundingClientRect();
        clips.set(node, {
          left: bounds.left + (node.clientLeft || 0), top: bounds.top + (node.clientTop || 0),
          width: node.clientWidth ?? bounds.width, height: node.clientHeight ?? bounds.height,
        });
      }
      return clips.get(node);
    };
    try {
      return Array.from(document.querySelectorAll("*")).filter((element) => {
        if (element === document.documentElement || element === document.body || annotationChromeElement(element)) return false;
        const tag = String(element.tagName || "").toLowerCase();
        if (["head", "script", "style", "noscript", "template", "meta", "link"].includes(tag)) return false;
        const rect = element.getBoundingClientRect();
        if (rect.width <= 0 || rect.height <= 0) return false;
        let scrollableX = false, scrollableY = false;
        for (let node = element; node; node = node.parentElement) {
          if (node.hidden || node.getAttribute?.("aria-hidden") === "true" || node.getAttribute?.("inert") !== null) return false;
          const style = styleOf(node);
          if (!style) continue;
          // Computed visibility is inherited, and a descendant may set it back
          // to visible, so only the element's own value decides.
          if (node === element && ["hidden", "collapse"].includes(style.visibility)) return false;
          if (style.display === "none" || Number(style.opacity) === 0 && style.opacity !== "") return false;
          // A scrollable ancestor can reveal its descendants, so do not reject
          // them merely because their current rect is outside a higher clip.
          if (node !== element && node !== document.body && node !== document.documentElement) {
            const { left, top, width, height } = clipOf(node);
            if (!scrollableX && ["hidden", "clip"].includes(style.overflowX) && (rect.left + rect.width <= left || rect.left >= left + width)) return false;
            if (!scrollableY && ["hidden", "clip"].includes(style.overflowY) && (rect.top + rect.height <= top || rect.top >= top + height)) return false;
          }
          if (["auto", "scroll"].includes(style.overflowX) && node.scrollWidth > node.clientWidth) scrollableX = true;
          if (["auto", "scroll"].includes(style.overflowY) && node.scrollHeight > node.clientHeight) scrollableY = true;
        }
        return true;
      });
    } catch { return []; }
  };

  const setKeyboardTarget = (element, scroll = true) => {
    annotationKeyboardTarget = element || null;
    if (!element) {
      hideAnnotationOverlay();
      if (annotationKeyboardStatus) annotationKeyboardStatus.textContent = "No visible page elements available.";
      return;
    }
    if (scroll) {
      try { element.scrollIntoView?.({ block: "nearest", inline: "nearest" }); } catch {}
    }
    updateAnnotationOverlay(element);
    const target = annotationTarget(element);
    if (annotationKeyboardStatus && target) {
      annotationKeyboardStatus.textContent = [target.role || target.tag, target.name || target.text].filter(Boolean).join(": ") || "Page element";
    }
  };

  const interceptAnnotationKeydown = (event) => {
    if (!annotationMode || !annotationKeydown) return;
    annotationKeydown(event);
  };

  const stopAnnotationMode = (reason = "stopped", announce = true) => {
    if (annotationPointerMove) document.removeEventListener("pointermove", annotationPointerMove, true);
    if (annotationClick) document.removeEventListener("click", annotationClick, true);
    if (annotationKeydown) document.removeEventListener("keydown", annotationKeydown, true);
    annotationPointerMove = null;
    annotationClick = null;
    annotationKeydown = null;
    annotationMode = false;
    annotationKeyboardTarget = null;
    annotationKeyboardStatus?.parentElement?.remove();
    annotationKeyboardStatus = null;
    try { annotationPreviousFocus?.focus?.(); } catch {}
    annotationPreviousFocus = null;
    removeAnnotationOverlay();
    removeAnnotationCursor();
    if (announce) postAnnotation(ANNOTATION_MODE, { active: false, reason });
  };

  const startAnnotationMode = () => {
    if (!connected || !port || typeof document === "undefined") return false;
    stopAnnotationMode("restarted", false);
    annotationPreviousFocus = document.activeElement;
    annotationOverlay = document.createElement("div");
    annotationOverlay.setAttribute("data-railgrid-annotation-overlay", "true");
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
    const keyboardHelp = document.createElement("div");
    keyboardHelp.setAttribute("data-railgrid-annotation-keyboard", "true");
    keyboardHelp.setAttribute("tabindex", "-1");
    keyboardHelp.setAttribute("role", "region");
    keyboardHelp.setAttribute("aria-label", "Annotation picker. Use up and down arrows to move through page elements, left and right arrows for parent and child. Press Enter or Space to select, or Escape to cancel.");
    // Keep the focus target and announcements available to assistive technology.
    // The selection outline already shows sighted users which element is active.
    Object.assign(keyboardHelp.style, {
      position: "fixed",
      top: "0",
      left: "0",
      width: "1px",
      height: "1px",
      overflow: "hidden",
      clipPath: "inset(50%)",
      pointerEvents: "none",
    });
    annotationKeyboardStatus = document.createElement("div");
    annotationKeyboardStatus.setAttribute("role", "status");
    annotationKeyboardStatus.setAttribute("aria-live", "polite");
    keyboardHelp.append(annotationKeyboardStatus);
    document.documentElement?.append(keyboardHelp);
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
      setKeyboardTarget(element, false);
    };
    annotationClick = interceptAnnotationClick;
    annotationKeydown = (event) => {
      if (!annotationMode || !["Escape", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Enter", " "].includes(event.key)) return;
      event.preventDefault?.();
      event.stopPropagation?.();
      event.stopImmediatePropagation?.();
      if (event.key === "Escape") {
        stopAnnotationMode("escape");
        postAnnotation(ANNOTATION_CANCELLED, { reason: "escape" });
        return;
      }
      if ((event.key === "Enter" || event.key === " ") && event.repeat) return;
      if ((event.key === "Enter" || event.key === " ") && annotationEventContext(event).pinRecord) {
        interceptAnnotationClick(event);
        return;
      }
      const candidates = keyboardCandidates();
      let index = candidates.indexOf(annotationKeyboardTarget);
      if (event.key === "Enter" || event.key === " ") {
        const element = index >= 0 ? candidates[index] : null;
        const target = element && annotationTarget(element);
        if (target) postAnnotation(ANNOTATION_SELECTED, {
          target, anchor: { x: 0.5, y: 0.5 },
          viewport: { width: Math.max(1, Math.round(window.innerWidth || 1)), height: Math.max(1, Math.round(window.innerHeight || 1)) },
        });
        return;
      }
      if (event.key === "ArrowDown") index = Math.min(index + 1, candidates.length - 1);
      else if (event.key === "ArrowUp") index = index < 0 ? candidates.length - 1 : Math.max(0, index - 1);
      else if (event.key === "ArrowRight") {
        const child = candidates.find((candidate) => candidate.parentElement === annotationKeyboardTarget);
        if (child) index = candidates.indexOf(child);
      } else {
        const parent = annotationKeyboardTarget?.parentElement;
        if (candidates.includes(parent)) index = candidates.indexOf(parent);
      }
      setKeyboardTarget(candidates[index] || null);
    };
    document.addEventListener("pointermove", annotationPointerMove, true);
    document.addEventListener("click", annotationClick, true);
    document.addEventListener("keydown", annotationKeydown, true);
    annotationMode = true;
    setKeyboardTarget(keyboardCandidates()[0] || null);
    try { keyboardHelp.focus?.(); } catch {}
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
    annotationPinLayer.setAttribute("data-railgrid-annotation-pins", "true");
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
      pin.setAttribute("data-railgrid-annotation-pin", "true");
      pin.setAttribute("data-railgrid-annotation-id", id);
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

  const readyMessage = () => ({
    type: READY,
    version: VERSION,
    documentID,
    path: path(),
  });

  const portalOrigin = (origin) => typeof origin === "string" && PORTAL_ORIGINS.includes(origin);

  // One outstanding handshake endpoint per configured origin. Only the copy
  // addressed to the real parent's origin is ever delivered.
  const handshakePorts = new Map();

  const closeHandshakePorts = () => {
    for (const candidate of handshakePorts.values()) {
      try { candidate.close(); } catch {}
    }
    handshakePorts.clear();
  };

  const emitReady = (targetOrigin) => {
    if (typeof MessageChannel !== "function" || !portalOrigin(targetOrigin)) return;
    const channel = new MessageChannel();
    const nextPort = channel.port1;
    try { handshakePorts.get(targetOrigin)?.close(); } catch {}
    handshakePorts.set(targetOrigin, nextPort);
    nextPort.onmessage = (event) => onStart(event, nextPort, targetOrigin);
    if (typeof nextPort.start === "function") nextPort.start();
    try {
      // targetOrigin is an exact configured portal origin, so the browser
      // delivers the handshake endpoint only to a parent on that origin.
      window.parent.postMessage(readyMessage(), targetOrigin, [channel.port2]);
    } catch {
      // The bridge is optional; a parent that cannot receive it is harmless.
      try { nextPort.close(); } catch {}
      if (handshakePorts.get(targetOrigin) === nextPort) handshakePorts.delete(targetOrigin);
    }
  };

  const onStart = (event, nextPort, origin) => {
    const data = event?.data;
    if (!data || data.version !== VERSION || data.type !== START) return;
    if (handshakePorts.get(origin) !== nextPort || String(data.generation || "") !== documentID) return;
    const nextSessionID = typeof data.sessionID === "string" ? data.sessionID.slice(0, 128) : "";
    if (!nextSessionID) return;
    handshakePorts.delete(origin);
    closeHandshakePorts();
    const previousPort = port;
    stopAnnotationMode("session-replaced", false);
    connected = true;
    port = nextPort;
    sessionID = nextSessionID;
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
      const reportConnected = () => {
        if (port !== nextPort) return;
        try {
          nextPort.postMessage({
            type: CONNECTED,
            version: VERSION,
            sessionID: nextSessionID,
            generation: documentID,
            documentID,
            path: path(),
          });
        } catch {
          failPort();
        }
      };
      // Bootstrap runs in the document head. Wait for the page's resources so
      // the workbench can reveal this document after its styles have loaded.
      if (document.readyState === "complete") reportConnected();
      else window.addEventListener("load", reportConnected, { once: true });
    } catch {
      failPort();
      return;
    }
  };

  const onMessage = (event) => {
    if (event.source !== window.parent || !event.data || event.data.version !== VERSION) return;
    if (event.data.type === PROBE) {
      if (!portalOrigin(event.origin)) return;
      // A probe intentionally carries no generation: this document announces
      // its own documentID in READY, and START must echo it, so a handshake
      // can never bind to a predecessor document.
      emitReady(event.origin);
      return;
    }
  };

  window.addEventListener("message", onMessage);
  // Register before application scripts so an app-level window capture
  // listener cannot run before annotation interception is enabled later.
  window.addEventListener("click", interceptAnnotationClick, true);
  window.addEventListener("keydown", interceptAnnotationKeydown, true);
  // START is intentionally handled only on the endpoint sent outbound in
  // READY. No transferred port is exposed to same-document application
  // message listeners. Announce to each configured origin; the browser drops
  // every copy except the one addressed to the actual parent's origin.
  if (window.parent !== window) {
    for (const origin of PORTAL_ORIGINS) emitReady(origin);
  }
})();`;

function portalOrigins(configuration) {
  if (!configuration || !Array.isArray(configuration.portalOrigins)) {
    throw new Error("preview bridge configuration must have a portalOrigins array");
  }
  const origins = configuration.portalOrigins;
  if (origins.length < 1 || origins.length > maxPortalOrigins) {
    throw new Error(`preview bridge configuration must list 1-${maxPortalOrigins} portal origins`);
  }
  const seen = new Set();
  for (const origin of origins) {
    let parsed;
    try {
      parsed = new URL(origin);
    } catch {
      throw new Error(`preview bridge portal origin ${JSON.stringify(origin)} is not a URL`);
    }
    if ((parsed.protocol !== "https:" && parsed.protocol !== "http:") || parsed.origin !== origin) {
      throw new Error(`preview bridge portal origin ${JSON.stringify(origin)} must be an exact http(s) origin`);
    }
    seen.add(origin);
  }
  return [...seen];
}

export function previewBridgeClientSource(configuration) {
  const encoded = JSON.stringify(portalOrigins(configuration)).replaceAll("<", "\\u003c");
  // A replacer function: a string replacement would expand $&, $` and $' in
  // the origins, and `$` is a legal host character.
  return previewBridgeClient.replace("__RAILGRID_PREVIEW_BRIDGE_PORTAL_ORIGINS__", () => encoded);
}

// frame-ancestors keeps every other site from embedding the development
// preview at all. Top-level navigation (a new tab, the agent browser) is not
// affected. It is set only on HTML documents the dev server returns.
export function previewBridgeFrameAncestors(configuration) {
  return `frame-ancestors 'self' ${portalOrigins(configuration).join(" ")}`;
}

export function createPreviewBridgePlugin(configuration) {
  const client = previewBridgeClientSource(configuration);
  const frameAncestors = previewBridgeFrameAncestors(configuration);
  return {
    name: "railgrid-preview-bridge-v1",
    enforce: "pre",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use((request, response, next) => {
        const accept = String(request.headers?.accept || "");
        if (request.method === "GET" && accept.includes("text/html")) {
          response.setHeader("Content-Security-Policy", frameAncestors);
        }
        next();
      });
    },
    transformIndexHtml() {
      return [{
        tag: "script",
        attrs: { "data-railgrid-preview-bridge": "v1" },
        children: client,
        injectTo: "head-prepend",
      }];
    },
  };
}

export default function previewBridgePlugin() {
  const configuration = JSON.parse(readFileSync(previewBridgeConfigPath, "utf8"));
  return createPreviewBridgePlugin(configuration);
}
