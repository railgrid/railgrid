# Faros Design Book compatibility pointer

The design book was split into the browsable [Faros design knowledge base](design/).
Use [design/README.md](design/README.md) as the constitution and router, then
follow its foundation, component, pattern, and quality contracts.

<!--
  Legacy deep-link anchors from the former monolithic book. Keep these empty
  anchors while links migrate to docs/design; they intentionally preserve every
  former ATX heading slug without restoring a second design source.
-->
<a id="faros-design-book-violet-circuit"></a>
<a id="1-principles"></a>
<a id="2-color-tokens"></a>
<a id="3-radius-law"></a>
<a id="4-typography"></a>
<a id="5-the-recipes-k--classes"></a>
<a id="6-component-patterns"></a>
<a id="fluid-page-composition"></a>
<a id="7-theming-mechanics"></a>
<a id="8-sanctioned-exceptions"></a>
<a id="9-provider-portals-how-the-system-reaches-them"></a>
<a id="10-extended-component-specs"></a>
<a id="tooltip-implemented-as-data-k-tip-faros-uicss"></a>
<a id="toast-snackbar-implemented-as-portalkittoastts"></a>
<a id="toast-snackbar-implemented-as-vue-portalkit"></a>
<a id="dropdown-context-menu-implemented-as-k-menu-faros-uicss"></a>
<a id="layout-selector-implemented-as-portalkit-vuelayoutselectorvue"></a>
<a id="provider-route-tabs-implemented-as-portalkit-tabs"></a>
<a id="resource-instance-pages-implemented-with-portalkit"></a>
<a id="resource-reads-and-background-refresh"></a>
<a id="resource-creation"></a>
<a id="select-combobox"></a>
<a id="checkbox-radio"></a>
<a id="toggle-switch-implemented-as-k-toggle-faros-uicss"></a>
<a id="progress-bar-implemented-as-k-progress-faros-uicss"></a>
<a id="avatar-implemented-as-k-avatar-faros-uicss"></a>
<a id="shortcut-hint-implemented-as-k-kbd-faros-uicss"></a>
<a id="file-dropzone-implemented-as-k-dropzone-faros-uicss"></a>
<!-- GitHub-style slugging can retain the literal `kbd` from the backticked tag. -->
<a id="kbd-shortcut-hint-implemented-as-k-kbd-faros-uicss"></a>
<a id="slider-range-input"></a>
<a id="pagination"></a>
<a id="date-time-picker"></a>
<a id="command-palette-k"></a>
<a id="still-open-oddities"></a>
<a id="11-iconography"></a>
<a id="taste-abstract-over-literal"></a>
<a id="stroke-size-law"></a>
<a id="semantic-vocabulary"></a>
<a id="provider-identity-icons"></a>
<a id="12-review-checklist"></a>

Stable machine references now use catalog IDs such as
`design.quality.exceptions`; numeric section references are historical only.
This physical file remains so existing links resolve during the migration. It
contains no additional normative rules.
