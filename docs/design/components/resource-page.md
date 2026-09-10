---
{"schema":1,"id":"design.components.resource-page","title":"ResourcePage read shell","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"ResourcePage owns the title hierarchy, read-state shell, action slot, and loading semantics while callers own action order, resource content, and fetches. Optional header, showHeader, and fill props support caller-owned compact and remaining-height layouts while preserving read handling."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-page.md#resourcepage-read-shell","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourcePage.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentDetail.vue","role":"reference"},{"path":"providers/agents/portal/src/views/AgentConfig.vue","role":"reference"},{"path":"provider-sdk/agentkit-vue/AIConversationIdentity.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceBackLink.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Current PortalKit parity passed for the canonical and distributed asset sets."},{"kind":"browser","ref":"Historical Agents ResourcePage rendering (CSS v10/v11)","status":"passing","evidence":"Earlier Agents fixture cases covered the pre-v12 ResourcePage and workspace states at desktop/mobile sizes in light/dark themes. This evidence is historical and does not verify current AIWorkspace or CSS/runtime version 13."},{"kind":"browser","ref":"Current Agents header alignment rendered verification (CSS/runtime v14)","status":"passing","evidence":"Current actual Agents production bundle fixtures passed at the 1129px target with a simulated 216px host sidebar and at mobile 390x844, in light and dark themes (8 cases across Agents and App Studio). The loaded workspace header measured 56px with a 32px identity tile, 13px title, 11px context, centered controls, no horizontal overflow, and provider list navigation. Evidence: /tmp/header-alignment/shared-header-agents-final.log, /tmp/header-alignment/shared-header-studio-final-repair.log, /tmp/header-alignment/shared-header-studio-final-mobile.log. Parent visually reviewed four screenshots. No live backend, Tilt, or actual-host browser claim is made."}]},"relatedDocuments":[{"id":"design.patterns.resource-reads","relation":"implements","path":"docs/design/patterns/resource-reads.md"},{"id":"design.foundations.typography","relation":"prerequisite","path":"docs/design/foundations/typography.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.content.ui-copy","relation":"see-also","path":"docs/design/content/ui-copy.md"},{"id":"design.components.conditions-panel","relation":"see-also","path":"docs/design/components/conditions-panel.md"},{"id":"design.components.resource-stat-cards","relation":"see-also","path":"docs/design/components/resource-stat-cards.md"}]}
---

# ResourcePage read shell

## Purpose

`ResourcePage` owns the title hierarchy and read-state shell; callers own
navigation and resource-specific content and fetching. The shell provides one
consistent detail-page boundary for resource reads while leaving resource
content to the caller.

## Use when

Use `ResourcePage` for a resource detail/read surface that needs the shared
title hierarchy, header actions, and read-state behavior. The only
resource-type prop is `kind`; section cards retain their independent
`eyebrow`. Set the optional `fill` prop only when the page is inside a
parent-owned split or chat region and must absorb the remaining height. It
defaults to `false`, preserving content-sized page geometry.
The optional `showHeader` prop defaults to `true`. Set it to `false` when the
caller supplies the title and actions through a parent-owned header; this hides
only ResourcePage's title/action header.

## Avoid when

Do not use the shell to encode resource-specific content or fetch ownership.
Callers must not add title-size overrides. When `showHeader` is `true`, a caller
may use the optional `#header` slot when a route needs a compact
identity/navigation header; that slot replaces only the inner heading/action
content inside ResourcePage's outer header. When `showHeader` is `false`, the
outer title/action header is omitted and the caller owns that composition; all
read-state surfaces remain `ResourcePage`-owned.

## Anatomy and variants

With the default header, source order is fixed: title, optional resource `kind`,
caller `#meta`, optional `#status`, then optional subtitle, with PortalKit-owned
dot separators. The optional `#header` slot replaces that inner heading/action
content for a route-specific compact header while the outer `<header>` remains
present. With `showHeader="false"`, the title/action header is absent; read-state
notices, the loading shell, summary, and body remain present. The optional
`fill` prop adds the `k-resource-page--fill` modifier, which gives the root and
body remaining-height flex sizing; it does not change read semantics or take
ownership of the body. Agents opts into `fill` for Chat, Config, and Runs. For a
valid loaded agent snapshot, Agents sets `showHeader` false and puts an
icon-only `ResourceBackLink` in AgentChat's leading slot after the rail toggles.
The link is named **Back to agents**, points to the `#/agents` list, and is
disabled while deletion is busy. The heading slot uses `AIConversationIdentity`
with the active thread as the primary title and the 13px agent `h1` as secondary
context on two lines; the status badge and workbench toggle use the actions
slot. These controls share AgentChat's existing 56px
`AIConversationHeader`; there is no separate full-width resource toolbar or 18px
gap. Config and Runs are tabs aligned with that conversation header, with
no separate Workbench title or subtitle. When the agent is loading, absent, or
has no loaded snapshot, ResourcePage retains its header and its read states. An
embedded agent-scoped Run Detail adds its own Runs backlink and `h2` title inside
the Runs panel. The shell may render caller-provided `#loading` visuals;
otherwise it supplies the three-bar skeleton fallback. The `#actions` slot is
caller-owned and preserves caller order. A caller may choose a provider-specific
primary action or `Refresh`, but
`ResourcePage` does not synthesize, reorder, or own those controls.

Agents supplies deletion through AgentConfig's final `destructive-actions` slot
from AgentDetail. Its existing confirmation, authority, busy, error, success,
and navigation handling remains provider-owned; this document makes no live
delete claim.

## Behavior

The shell owns polite status and live-region semantics. A successful snapshot
remains visible through later refresh failures, which show a stale/error notice
and `Retry`; the caller owns fetching and receives the retry event. Initial
failures expose the same retry path. Read serialization, authority fencing, and
cancellation follow the [resource-read pattern](../patterns/resource-reads.md).

## Content

Use the resource title and optional `kind`, metadata, status, and subtitle in
the prescribed order. Primary header anchors with an accent background retain
the `text-on-accent`/`--color-on-accent` contrast token in normal and hover
states; changing the background to `accent-hover` must not reduce readable
contrast. Solid accent actions use `--color-on-accent`: near-black text on the
bright dark-theme violet and white text on the light-theme violet. Host and
standalone provider fallbacks share this token so normal-size labels remain
readable.

## Layout and responsive behavior

The responsive title runs from 24px to 32px (22px on mobile), wraps long
identifiers safely, and uses tight tracking and leading. Actions remain
reachable at 44×44px for coarse and hybrid pointers.

## Accessibility

Keep the prescribed source order and expose the shell's polite status and
live-region behavior for loading and read failures. Preserve readable
`text-on-accent`/`--color-on-accent` contrast in both normal and hover states;
the [accessible interaction policy](../accessibility/interaction.md) supplies
the cross-component keyboard, focus, and naming contract.

## Code and evidence

The canonical implementation is
`provider-sdk/portalkit-vue/ResourcePage.vue`. Verify the shared implementation
with `make verify-portalkit`.

## Related guidance

Pair the shell with the [resource reads pattern](../patterns/resource-reads.md),
the [typography foundation](../foundations/typography.md), the [accessible
interaction policy](../accessibility/interaction.md), and the [UI copy
policy](../content/ui-copy.md).
