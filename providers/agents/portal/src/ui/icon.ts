// lit wrapper around portalkit's string-returning icon set. portalkit is a
// synced kit (framework-agnostic, string-based); this adapter is the only place
// that unwraps it into a template so components never touch unsafeSVG directly.

import { html, type TemplateResult } from 'lit'
import { unsafeSVG } from 'lit/directives/unsafe-svg.js'
import { ic, type IconName } from '../portalkit/icons'

export type { IconName }

export function icon(name: IconName, extraClass = ''): TemplateResult {
  return html`${unsafeSVG(ic(name, extraClass))}`
}
