---
{"schema":1,"id":"design.foundations.layering","title":"Layering and floating surfaces","kind":"token","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"Documents current shared layers and component behavior; local stacking contexts and full product adoption remain to be audited."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/implementation/layering.md#layering-and-floating-surfaces","role":"design"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ActionMenu.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/FormSelect.vue","role":"implementation"},{"path":"provider-sdk/agentkit/agent-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"browser","ref":"Native dialog keyboard review in design-book fixture","status":"passing","evidence":"ActionMenu, FormSelect, icon tooltip and timestamp tooltip were checked inside a native dialog in the local browser; this does not cover all overlays or real product routes."},{"kind":"review","ref":"Product-wide floating surfaces and screen-reader review","status":"pending"}]},"relatedDocuments":[{"id":"design.foundations.colors","relation":"related"},{"id":"design.components.menu","relation":"related"},{"id":"design.components.tooltip","relation":"related"},{"id":"design.components.confirm-dialog","relation":"related"},{"id":"design.components.toast","relation":"related"}]}
---

# Layering and floating surfaces

Layers describe visual ordering, but a z-index is meaningful only inside its stacking context. Overflow, transforms, native dialogs and fullscreen elements can change what appears above or receives input. This is an inventory of current shared recipes, not proof that every combination or provider is compatible.

## Current shared layers

| Surface | Current default | Ownership and boundary |
| --- | --- | --- |
| Anchored menus | `--k-layer-menu: 1000` | ActionMenu, FormSelect, and ResourceTableFilter use the nearest native dialog or promote a panel inside an ARIA modal when Popover is supported. ModelIDSelector uses the nearest open native dialog or body. AgentKit also uses the menu layer for several popovers. |
| Tooltip | `1100` | The shared tooltip layer is fixed and uses the nearest open native dialog as its host when the trigger is inside one, otherwise the body. It is not an interactive popover. |
| Fullscreen | `--k-layer-fullscreen: 2000` | Shared fullscreen surfaces own their viewport; browser top-layer behavior must be considered separately. |
| Dialog | `--k-layer-modal: 2147483000` | Shared confirmation uses a native modal top layer and scrim. AgentKit also has dialog-adjacent layers. |
| Toast | `--k-layer-toast: 2147483001` | The toast host is teleported to the body and owns its queue and focus behavior. |

Some components use local values for internal content. The numbers above do not replace inspecting the component's actual root, stacking context, and overlay host. Do not assume that a body-level tooltip or menu automatically works inside a native modal's browser top layer.

## Interaction ownership

A menu and a select have different keyboard and selection contracts; use their shared implementations instead of building a surface from layer numbers. A tooltip supplements a named control and must not contain an action. A dialog owns its interruption and focus behavior. A toast is independent feedback, while a contextual failure normally belongs beside its task. Escape, outside interaction, focus return, scroll and resize behavior must be evaluated for the particular component and nesting context.

## Review and evidence

The review example opens real shared menu, select, confirmation and toast components with simulated actions. Use the linked [Edge Services fixture](http://127.0.0.1:5199/) and [agent run fixture](http://127.0.0.1:5200/) for context. The review includes an ActionMenu, FormSelect, table filters, ModelIDSelector and two tooltip types inside a native dialog. Keyboard selection and Escape dismissal were checked in the local browser. In the same fixture, the model picker was checked inside the dialog: its search gained focus and Escape returned focus to its trigger without closing the dialog; the panel remained within a 390px viewport with touch emulation. Other nested overlays, browser fullscreen, provider-local overlays, screen readers and touch behavior require separate verification; these examples do not establish universal layering compatibility.
