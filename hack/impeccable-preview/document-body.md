# Design System: Railgrid

## Overview

**Creative North Star: "Violet Circuit — dense, precise, alive only where state changes."**

Railgrid feels like an instrument panel for infrastructure. Compact rows, thin
dividers, and aligned numbers let operators read a workspace at a glance.
Light mode has a cool paper ground and clear panel edges; dark mode uses a
near-black violet ground with the same structure and visual hierarchy.

Violet signals where the operator is, what they can do, or where focus has
landed. System facts speak in small mono text. Most surfaces stay quiet so a
change in health, an unfinished action, or a request for approval stands out.

**Key Characteristics:**

- Cool surfaces separated by hairlines.
- Small, sharp corners and compact operational controls.
- A single interactive accent; distinct status colors.
- Full light and dark themes with a light default.
- Mono for machine facts; ordinary sans for reading and action.

This is an implementation scan for visual authoring. The
[Railgrid design knowledge base](docs/design/README.md) remains the authority for
detailed behavior, accessibility, provider integration, and exceptions.

## Colors

The light palette uses Circuit Violet over Cool Paper and White Panel. The dark
palette uses a brighter Violet over Night Ground and a raised violet slate.
The tokens above are named by role, with a separate `dark-` value for every
color that changes. The same semantic token is switched in CSS at runtime.

### Primary

- **Circuit Violet** marks the current destination, a primary action, or
  keyboard focus. An accent tint provides a quiet selected surface and the
  glow is reserved for the four cases below.
- **On accent** keeps primary action text readable on each theme's solid fill.

### Neutral and semantic

- **Cool Paper / Night Ground** is the page. Raised, overlay, and hover levels
  distinguish the panel, field, popover, and hover surface without heavy depth.
- **Ink, secondary, and muted text** serve titles, reading, and supporting
  labels respectively. Keep supporting text readable on all surface steps.
- **Success, warning, and danger** encode result and urgency. Their subtle fills
  are surfaces for labels and contextual feedback. A warning is amber; it is
  never signaled with the violet action color.

**The One Accent Rule.** Use violet for interaction and the semantic palette
for status; use the shared variables for both.

The portal boots to light when preference is unavailable. Users can select
light, dark, or system. Dex is a separate fixed-dark login surface with local
tokens; it has no light theme. See [host tokens](portal/src/assets/main.css#L40),
[theme store](portal/src/stores/theme.ts), and
[Dex styles](hack/dex/web/static/main.css).

## Typography

- **Brand display:** Expanded Archivo Variable at 125% stretch, reserved for the
  wordmark and occasional promotional brand surfaces. Size follows that use.
- **Functional headings:** Instrument Sans Variable at normal width, typically
  semibold. Shared creation and detail titles own their own geometry.
- **UI body and controls:** Instrument Sans Variable, generally 12–13px. Compact
  nav and small labels use 11px where the surface warrants it.
- **Technical labels:** IBM Plex Mono for IDs, statuses, code, paths, table
  headings, and timestamps. Badges and micro headings use a tracked uppercase
  treatment, around 9–10px. Aligned figures use tabular numerals; KPI numerals
  keep the ordinary UI face.

**The Machine Facts Rule.** Use mono for what the system reports and ordinary
sans for what people read or do.

[Font imports](portal/src/main.ts) are self-hosted. The display width axis
and inherited typography come from [host CSS](portal/src/assets/main.css#L137).
Dex uses its local Instrument Sans and IBM Plex Mono faces and omits Archivo.

## Layout

The portal shell owns the available content column. The desktop navigation
can run as a 56px icon rail or a 208px labeled sidebar; the shell adapts on
narrow screens. Ordinary resource inventories expand into the available
column. Long prose and simple controls keep their own reading measure; dense
forms may use the available width. Use the measured recipe gaps rather than
adding a new global spacing scale: for example, tabs use 4px between entries
and buttons use 8px between icon and text.

**The Fluid Column Rule.** Let the shell size the route; constrain only the
reading surface or simple form inside it.

See [AppLayout](portal/src/components/AppLayout.vue) and
[fluid composition](docs/design/patterns/fluid-shell.md).

## Elevation & Depth

Page, raised, and overlay tones establish depth together with thin borders.
Ordinary cards have an optional 0 1px 2px lift; the flat card removes it.
Dialog and popover elevation separates an active layer from the page. The
dialog scrim takes its tint from the surface.

**The Glow Means Alive Rule.** Current shell navigation, solid accent actions,
focused fields, and live indicators may glow. Provider tabs, danger actions,
and decorative surfaces stay flat.

Read the exact state declarations in
[the shared stylesheet](provider-sdk/portalkit/railgrid-ui.css).

## Shapes

Use 6px panels, 4px controls, 3px status tags, and 2px progress tracks. The
shared switch uses the implemented 4px track and 3px knob. The prominent
solid danger action is a 36px-high, 6px-radius settings variant. True circles
belong to dots, avatars, and other documented round affordances. Conversation
bubbles have their own documented radius exception.

**The Sharp Scale Rule.** Use the shared shape vocabulary and keep status
tags square; do not turn controls or tags into pills.

See [host radius tokens](portal/src/assets/main.css#L47) and
[PortalKit recipes](provider-sdk/portalkit/railgrid-ui.css).

## Components

### Buttons

Primary uses the solid accent, readable on-accent text, and a stronger glow
on hover. Ghost uses a quiet overlay, a hairline, and a surface step on hover.
Text is visually quiet and inherits its label color. Tinted danger uses a
semantic fill and danger text. Solid danger uses the danger fill, on-accent
text, and a keyboard outline; it never glows. Shared button targets grow to
44px on coarse or hybrid pointers.

### Fields

Overlay fields have a thin default border and an accent border plus ring on
focus. Creation fields follow the shared baseline. Labels precede their
control; hint or error follows it. An invalid FormSelect uses a danger border
and an icon beside its corrective message, while retaining the focus cue.
Native select, checkbox, and radio behavior
remain appropriate where the product calls for them.

### Cards and status tags

Cards use the raised surface and a subtle border; the flat variant keeps the
surface but loses the lift. A status tag is a square tracked mono label with a
semantic surface and a fine border. Keep the readable label even when a dot
or icon carries the same state.

### Navigation

Current shell navigation uses violet text, a subtle tint, and glow. Provider
section navigation uses a flat tint, a current-page indication, and a visible
keyboard outline. Use the shared Tabs component; the caller owns routing.

Sources: [buttons](provider-sdk/portalkit/railgrid-ui.css#L298),
[danger and fields](provider-sdk/portalkit/railgrid-ui.css#L691),
[cards](provider-sdk/portalkit/railgrid-ui.css#L73),
[status tags](provider-sdk/portalkit-vue/StatusBadge.vue), and
[provider tabs](provider-sdk/portalkit-vue/Tabs.vue).

## Do's and Don'ts

- **Do** check light and dark, including focus and disabled states.
- **Do** keep one obvious primary action for each task.
- **Do** use readable status words, mono IDs, and aligned tabular figures.
- **Do** compare the quiet resting surface with its active and failure states.
- **Don't** put glow on danger actions or provider section tabs.
- **Don't** substitute hardcoded colors, pill shapes, or decorative shadows.
- **Don't** use expanded Archivo for functional headings or KPIs.
- **Don't** treat the generated specimens as proof of live provider behavior.
