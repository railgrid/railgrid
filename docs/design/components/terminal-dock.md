---
{"schema":1,"id":"design.components.terminal-dock","title":"Terminal dock","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"shipped","notes":"The host owns the persistent dock and session store; provider portals request a terminal through the host event bridge."},"appliesTo":["portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/terminal-dock.md#terminal-dock","role":"design"},{"path":"portal/src/components/TerminalDock.vue","role":"implementation"},{"path":"portal/src/components/TerminalInstance.vue","role":"implementation"},{"path":"portal/src/stores/terminalSessions.ts","role":"implementation"},{"path":"providers/edges/portal/src/Detail.vue","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Disconnected terminal dock fixture","status":"passing","evidence":"Sample sessions exercise the host component without an SSH credential. Touch-emulated 390px and 1024px layouts were measured; physical devices, an authenticated connection and assistive technology remain unverified."},{"kind":"review","ref":"Connected terminal and assistive technology","status":"pending"}]},"relatedDocuments":[{"id":"design.components.tabs","relation":"related"},{"id":"design.components.ai-execution-details","relation":"related"},{"id":"design.foundations.colors","relation":"related"},{"id":"design.accessibility.interaction","relation":"related"},{"id":"design.quality.exceptions","relation":"related"}]}
---

# Terminal dock

## Purpose

Keep interactive edge terminal sessions available as people navigate the portal. An edge detail opens a session in the host-owned dock; the dock and its sessions are not owned by an individual provider page.

## Use when

A user explicitly opens a shell on an edge.

## Avoid when

Use execution details for an agent's recorded command and output; those receipts do not represent an interactive shell.

## Anatomy and variants

The dock contains a scrollable row of named session tabs, panel controls, and one or more terminal panes.
## Behavior

The active tab selects a session; split layouts allow sessions to be pinned or unpinned. The panel can be resized, minimized, maximized, or closed; individual sessions can also be closed. Sessions may be reordered by dragging or with Alt+Shift and the arrow keys while a tab is focused. The dock respects the host navigation insets and remains mounted across ordinary route changes.

A pane identifies its connection state and offers reconnect when disconnected. Terminal input and output belong to the remote shell; the fixture below deliberately has no credential or remote connection and does not demonstrate a working SSH session.

## Content

The surrounding panel uses portal surfaces and text roles in either theme. The xterm canvas is a deliberate fixed-dark exception in both themes, with IBM Plex Mono for terminal content. This exception does not authorize fixed-dark cards or controls elsewhere. Terminal output and ANSI colors originate from the remote application; contrast of arbitrary output is not guaranteed.

## Layout and responsive behavior

Keep the tab row scrollable when labels and controls do not fit.
## Accessibility

Icon-only actions have accessible names. Tabs expose selection and support keyboard navigation; panel and pane resize handles expose separator values and keyboard adjustment. On coarse or hybrid pointers, panel and session actions use at least 44-by-44-pixel targets, and session actions remain visible without hover. Check focus and the surrounding shell at narrow widths. Physical-device and screen-reader testing is still needed.

## Code and evidence

The [host dock](../../../portal/src/components/TerminalDock.vue) composes [terminal panes](../../../portal/src/components/TerminalInstance.vue) and the [session store](../../../portal/src/stores/terminalSessions.ts). The [terminal fixture](http://127.0.0.1:5197/terminal-review.html) mounts those product components with disconnected sample sessions; it never opens an SSH connection. The [design-book entry](http://127.0.0.1:5197/?entry=design.components.terminal-dock) links the fixture and source.

## Related guidance

See [tabs](tabs.md), [execution details](ai-execution-details.md), [materials](../foundations/colors.md#materials-and-surface-roles), and [sanctioned exceptions](../quality/exceptions.md).
