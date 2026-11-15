---
{"schema":1,"id":"design.components.button","title":"Button","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/button.md","role":"design"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"browser","ref":"Button review fixture at 390px","status":"passing","evidence":"In the local design-book fixture with touch emulation, the primary, secondary, text, danger, disabled, busy-example, and icon controls measured at least 44px high and wide. This is a synthetic specimen, not product-wide or assistive-technology verification."},{"kind":"review","ref":"Product-wide button and assistive-technology audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Button

## Purpose

Trigger a user action.

## Use when

An action changes state, submits a form, or opens a dialog. Give the main next action primary emphasis; use the surface-matching secondary style for adjacent actions.

## Avoid when

The destination is a page or URL: use a link. Do not use a destructive style for a harmless cancel or navigation action.

## Anatomy and variants

1. **Label:** a verb and, when needed, the affected object.
2. **Optional icon:** supports the label; an icon-only action needs an
   accessible name.
3. **Surface:** the variant communicates action priority or consequence.
4. **Pending feedback:** when applicable, a spinner and busy label replace or
   accompany the resting content; the caller owns this state.

The shared `.k-btn` recipe provides a 4px radius, 8px gap, 8px × 14px padding and 13px/600 text. Variants are:

- **Primary (`--primary`):** violet fill for the main next action.
- **Secondary (`--ghost`):** transparent fill and subtle border; a quiet tint appears on hover.
- **Text (`--text`):** borderless, low-emphasis action in its surrounding context.
- **Destructive (`--danger`, `--danger-solid`):** reserve danger tone for destructive operations; solid danger is available for prominent destructive settings actions.
- **Icon action (`.k-icon-action`):** compact, named action; its 32px desktop target expands to 44px for coarse pointers. Specific components may provide a quieter trigger treatment.

Review default, hover, keyboard focus, disabled and busy states. A busy state is consumer-owned, not a CSS button variant.

## Behavior

Use a button for actions and a link for navigation. Prevent duplicate submission while busy and keep the action’s purpose clear in the busy label. If the busy control must retain keyboard focus, keep it focusable, expose `aria-disabled` and `aria-busy`, and guard the handler against repeat activation. `aria-disabled` alone does not block an action. Otherwise a native disabled button removes it from the tab order.

The shared recipe suppresses interactive hover styling for an
`aria-disabled` action. A focusable busy button keeps full text contrast and
uses a progress cursor; the caller still guards activation and provides the
busy label.

## Content

Use an action verb and name the affected object when context alone would be ambiguous. Icon-only actions need the same meaning in their accessible name. Explain why an unavailable action cannot be used where the reason is not already apparent.

## Layout and responsive behavior

Keep actions legible and reachable at narrow widths; allow a row to wrap when necessary. The shared icon action has a 44px coarse-pointer target. Other buttons must be checked in their actual layout rather than assuming the base padding satisfies every touch-target requirement.

## Accessibility

Use a native button, or an actual link for navigation. Name icon-only actions, use `type="button"` for non-submit actions inside forms, and preserve a visible 2px violet keyboard focus outline. Announce the result or error near the relevant task.

## Code and evidence

The [shared stylesheet](../../../provider-sdk/portalkit/railgrid-ui.css) owns these recipes. [Edge Services](../../../providers/edges/portal/src/Services.vue) uses the secondary refresh action, [Agents connections](../../../providers/agents/portal/src/views/Connections.vue) uses primary and secondary form actions, and [Infrastructure detail](../../../providers/infrastructure/portal/src/views/InstanceDetailPage.vue) uses secondary actions. These are observed examples, not a product-wide adoption audit. Open the [Edge Services product fixture](http://127.0.0.1:5199/product.html) for context; it uses fixture data and is not a live backend.

The [button review](http://127.0.0.1:5197/?entry=design.components.button)
shows the shared variants, an icon action, a disabled action with an explanation,
and a focus-preserving busy example. The [Agents Models fixture](http://127.0.0.1:5198/?provider=agents&scenario=spinner&route=%23%2Fmodels)
shows the shipped table action testing a connection. Both use simulated data;
the Models route currently uses the table layout rather than model cards.
In the button review, a focused busy secondary action stays on its neutral
surface under the pointer and uses a progress cursor. The [Agents provider
form fixture](http://127.0.0.1:5197/agents-form-busy.html) shows the same cursor
on a simulated busy primary action. These checks do not establish product-wide
adoption or assistive-technology behavior.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
