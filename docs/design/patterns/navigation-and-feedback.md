---
{"schema":1,"id":"design.patterns.navigation-and-feedback","title":"Navigation, feedback, and state composition","kind":"pattern","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The host shell and PortalKit recipes implement the navigation, feedback, and state composition rules."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/patterns/navigation-and-feedback.md#navigation-feedback-and-state-composition","role":"design"},{"path":"portal/src/components/AppLayout.vue","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"}]},"relatedDocuments":[{"id":"design.foundations.principles","relation":"prerequisite"},{"id":"design.foundations.recipes","relation":"prerequisite"},{"id":"design.foundations.iconography","relation":"prerequisite"},{"id":"design.components.tabs","relation":"related"},{"id":"design.components.menu","relation":"related"},{"id":"design.components.toast","relation":"related"},{"id":"design.components.tooltip","relation":"related"},{"id":"design.quality.review-checklist","relation":"see-also"}]}
---

# Navigation, feedback, and state composition

Modals and dialogs are 6px `surface-raised` hairlined surfaces with heavy
elevation; use the [confirmation component](../components/confirm-dialog.md).
The scrim is `bg-surface/60` or a surface-derived `color-mix`, never black or
text-derived.

Shell/sidebar idle items are muted text on nothing. An active shell item is
accent text on `accent-subtle` with a `0 0 14px` nav glow. Provider route tabs
are the separate [PortalKit tabs](../components/tabs.md) pattern and never glow
or shadow. Section headers are 9px mono uppercase with a trailing hairline.

The sidebar is a 56px icon rail by default. Labels are a click away through a
persisted browser toggle; collapsed rows are centered icon-only controls with a
native `title`; category groups collapse to hairline rules. Sub-navigation,
tenant chip, and theme switch appear only when expanded; expanded width is
208px.

Chat bubbles may use the sanctioned 12–14px soft radius: counterpart bubbles
use `surface-overlay`, user bubbles `accent-subtle`, and neither glows. Empty
states use restrained contour-grid texture, an eyebrow, one-line explanation,
and one primary action.

A settings page may contain one primary action for each independently
persisted, visibly named form region. Each such region owns its own dirty,
busy, success, and error state, so saving one region cannot imply that another
region was saved. This is a persistence boundary, not permission to place
multiple competing primary actions in one form or task.

Native checkboxes and radios inherit `accent-color: var(--color-accent)` from
`body`; do not restyle them with raw blue. Custom toggles use a sharp 3px track
(`bg-accent` on, `bg-border-default` off) and 2px `bg-text-primary` knob, never
an iOS pill. Dense composite checkboxes use `.k-checkbox`: 14×14px with zero
min dimensions, native accent color, no ordinary focus shadow, and only a 3px
`accent-subtle` focus-visible ring. The composite row owns keyboard focus;
visually present checkboxes may be `tabindex="-1"`/`aria-hidden="true"` and
route pointer activation back to the row. Labels are 12px secondary text with
8px gap.

Progress uses a 2px (`rounded-xs`) `surface-overlay` track with semantic fill,
never a pill. Skeletons use `.shimmer` in the exact geometry of loaded state.
Motion uses `.stagger-item` (`stagger-in`) for entry, `.live-dot` (`live-pulse`)
for live state, component-owned feedback entry, and 120–200ms hover/focus or
control-state eases. The `.k-progress__bar` uses a 300ms transform transition
for progress updates, with immediate updates under reduced motion.

## Scoped destinations

Workspace pages and settings use `/ui/{orgID}/{workspaceID}/...`. Organization
settings retain that workspace scope when opened from a workspace; the
organization-only `/ui/{orgID}/settings/organizations` destination remains
available without an active workspace. IDs remain stable when display names change. The
address bar is sufficient to share an existing resource with an authorized
teammate; query parameters and fragments remain part of the destination.

The host resolves explicit context before mounting scoped content and preserves
it through sign-in. Unscoped entry (including ordinary sign-in) resumes the
last visited organization and workspace after checking current access. The
browser remembers IDs per account across sign-out; explicit links take priority.
With multiple available organizations and no valid remembered organization,
show the chooser instead of selecting the personal or first organization. A
sole available organization keeps direct entry. Organization entry resumes its last accessible workspace, remembered separately per
account and organization. With exactly one available workspace, enter it when ready.
Multiple workspaces without a valid preference open `/ui/{orgID}/workspaces`, a
workspace chooser with direct Open actions. Pending workspaces show preparation
status and refresh for up to a minute; Retry resumes checking. Empty organizations
offer creation when allowed, otherwise explain how to get access. New organization
creation enters this flow with a preparation hint while bootstrap runs.
Workspace settings show the active workspace directly, including its access and
lifecycle controls. The profile menu's Settings action opens those settings.
Organization settings contain the cross-workspace inventory in a card after the
organization overview, using the shared queryable ResourceTable for search,
lifecycle filtering, pagination, and read states. It includes provisioning and
deleting workspaces with shared row actions for recovery. Workspace switching stays
in the picker; the inventory has no separate workspace inspection selection or open
action. Member rosters, app access grants, and service accounts also use queryable
ResourceTables with shared search, pagination, and loading/error/retry states.
Member and service-account tables offer role filters; role editors remain native
selects. Table state resets when its organization or workspace changes. Explicit workspace settings
and resource links resolve the workspace encoded in their URL. The sidebar workspace menu
shows and searches only the current organization’s workspaces. It does not load
other organizations’ workspace lists. A quiet Change action beside the current organization name opens the
organization chooser (accessible label: Change organization), preserving the current destination for Back or continuing
in the same organization. Other organizations are shown only in that chooser.
Create workspace is the picker footer action when permitted. It opens a focused
name-entry dialog for the current organization and enters the new workspace once
it is ready. Creation errors retain the draft, and readiness retries check the
created workspace without creating another. The picker has no persistent
explanatory banner.
A workspace switch navigates to its dashboard. Back/Forward restores the context encoded
in each history entry. A failed destination retains its URL and offers Retry,
Switch account, and Choose organization without substituting another workspace.
Use shared navigation helpers for native links and keep asset URLs separate.
