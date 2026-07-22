// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit the copies vendored
// into individual portals (providers/*/portal/src/portalkit/); edit here and run
// `make sync-portalkit`. Builds must stay self-contained (each portal ships its
// own copy, no workspace/symlink), so the kit is synced rather than imported.
//
// Inline SVG icon set — a small, self-contained Lucide-style stroke library
// (MIT-derived paths, hand-inlined so the bundle has no runtime dependency and
// stays CSP-safe). Icons inherit the current text color and font size (1em),
// so they drop into buttons, labels, chips and headings without extra styling.
// For the string-building (vanilla-TS) portals — agents, kuery, quickstart. Vue
// portals use lucide-vue-next instead.
//
// Usage: `${ic('trash')}` inside an HTML template literal. Prefer these over
// emoji everywhere for a consistent, professional look across light/dark.

export type IconName =
  | 'bot'
  | 'plug'
  | 'wrench'
  | 'package'
  | 'clock'
  | 'zap'
  | 'cpu'
  | 'inbox'
  | 'message'
  | 'workflow'
  | 'settings'
  | 'sliders'
  | 'trash'
  | 'pencil'
  | 'play'
  | 'pause'
  | 'plus'
  | 'x'
  | 'check'
  | 'link'
  | 'key'
  | 'search'
  | 'send'
  | 'eye'
  | 'brain'
  | 'puzzle'
  | 'globe'
  | 'mail'
  | 'github'
  | 'discord'
  | 'megaphone'
  | 'swap'
  | 'maximize'
  | 'arrow-left'
  | 'chevron-right'
  | 'corner-down-right'
  | 'circle'
  | 'sparkles'
  | 'gauge'
  | 'dollar'
  | 'refresh'
  | 'flask'

// Each entry is the inner markup of a 24×24 stroke icon.
const PATHS: Record<IconName, string> = {
  bot: '<path d="M12 8V4H8"/><rect x="4" y="8" width="16" height="12" rx="2"/><path d="M2 14h2M20 14h2M9 13v2M15 13v2"/>',
  plug: '<path d="M9 2v6M15 2v6M7 8h10v3a5 5 0 0 1-10 0zM12 16v6"/>',
  wrench: '<path d="M14.7 6.3a4 4 0 0 0-5.4 5.4L3 18l3 3 6.3-6.3a4 4 0 0 0 5.4-5.4l-2.6 2.6-2.4-.6-.6-2.4z"/>',
  package: '<path d="M12 2 3 7v10l9 5 9-5V7z"/><path d="M3 7l9 5 9-5M12 12v10"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  zap: '<path d="M13 2 4 14h7l-1 8 9-12h-7z"/>',
  cpu: '<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/>',
  inbox: '<path d="M22 12h-6l-2 3h-4l-2-3H2"/><path d="M5 5h14l3 7v6a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1v-6z"/>',
  message: '<path d="M21 15a2 2 0 0 1-2 2H8l-4 4V5a2 2 0 0 1 2-2h13a2 2 0 0 1 2 2z"/>',
  workflow: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/><path d="M6.5 10v3a2 2 0 0 0 2 2H14"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M12 2v3M12 19v3M4.2 4.2l2.1 2.1M17.7 17.7l2.1 2.1M2 12h3M19 12h3M4.2 19.8l2.1-2.1M17.7 6.3l2.1-2.1"/>',
  sliders: '<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6"/>',
  trash: '<path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14"/><path d="M10 11v6M14 11v6"/>',
  pencil: '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>',
  play: '<path d="M6 4l14 8-14 8z"/>',
  pause: '<rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  x: '<path d="M18 6 6 18M6 6l12 12"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  link: '<path d="M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1.5 1.5M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1.5-1.5"/>',
  key: '<circle cx="7.5" cy="15.5" r="4.5"/><path d="M10.5 12.5 20 3M16 7l3 3M14 9l2 2"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>',
  send: '<path d="M22 2 11 13M22 2l-7 20-4-9-9-4z"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/>',
  brain: '<path d="M9 3a3 3 0 0 0-3 3 3 3 0 0 0-2 5 3 3 0 0 0 2 5 3 3 0 0 0 6 0V4a3 3 0 0 0-3-1zM15 3a3 3 0 0 1 3 3 3 3 0 0 1 2 5 3 3 0 0 1-2 5 3 3 0 0 1-6 0"/>',
  puzzle: '<path d="M9 3a2 2 0 0 1 4 0c0 .5-.2 1 .5 1H16a1 1 0 0 1 1 1v2.5c0 .7.5.5 1 .5a2 2 0 0 1 0 4c-.5 0-1-.2-1 .5V19a1 1 0 0 1-1 1h-2.5c-.7 0-.5-.5-.5-1a2 2 0 0 0-4 0c0 .5.2 1-.5 1H6a1 1 0 0 1-1-1v-2.5c0-.7-.5-.5-1-.5a2 2 0 0 1 0-4c.5 0 1 .2 1-.5V6a1 1 0 0 1 1-1h2.5c.7 0 .5-.5.5-1z"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18 14 14 0 0 1 0-18z"/>',
  mail: '<rect x="2" y="4" width="20" height="16" rx="2"/><path d="m2 6 10 7 10-7"/>',
  github:
    '<path d="M9 19c-4 1.4-4-2-6-2.5M15 21v-3.6c0-1 .1-1.4-.5-2 2.8-.3 5.5-1.4 5.5-6a4.6 4.6 0 0 0-1.3-3.2 4.3 4.3 0 0 0-.1-3.2s-1-.3-3.4 1.3a11.6 11.6 0 0 0-6 0C6.3 2.1 5.3 2.4 5.3 2.4a4.3 4.3 0 0 0-.1 3.2A4.6 4.6 0 0 0 4 8.8c0 4.6 2.7 5.7 5.5 6-.6.6-.6 1.2-.5 2V21"/>',
  discord: '<path d="M8 11h.01M16 11h.01M7.5 16.5C10 18 14 18 16.5 16.5M8 4.5C6 5 4 6 3.5 7.5 2 11 2 15 3.5 18c1 1 3 2 4.5 2l1-2M16 4.5c2 .5 4 1.5 4.5 3 1.5 3.5 1.5 7.5 0 10.5-1 1-3 2-4.5 2l-1-2"/>',
  megaphone: '<path d="M3 11v2a1 1 0 0 0 1 1h2l6 4V6L6 10H4a1 1 0 0 0-1 1zM16 8a4 4 0 0 1 0 8M12 6l8-3v18l-8-3"/>',
  swap: '<path d="M8 3 4 7l4 4M4 7h13M16 21l4-4-4-4M20 17H7"/>',
  maximize: '<path d="M8 3H5a2 2 0 0 0-2 2v3M16 3h3a2 2 0 0 1 2 2v3M21 16v3a2 2 0 0 1-2 2h-3M3 16v3a2 2 0 0 0 2 2h3"/>',
  'arrow-left': '<path d="M19 12H5M12 19l-7-7 7-7"/>',
  'chevron-right': '<path d="m9 6 6 6-6 6"/>',
  'corner-down-right': '<path d="M15 10l5 5-5 5M4 4v7a4 4 0 0 0 4 4h12"/>',
  circle: '<circle cx="12" cy="12" r="6" fill="currentColor" stroke="none"/>',
  sparkles: '<path d="M12 3l1.8 4.9L19 9.5l-5.2 1.6L12 16l-1.8-4.9L5 9.5l5.2-1.6zM19 15l.8 2.2L22 18l-2.2.8L19 21l-.8-2.2L16 18l2.2-.8z"/>',
  gauge: '<path d="M12 15l4-4M21 12a9 9 0 1 0-18 0 9 9 0 0 0 2 5.6h14A9 9 0 0 0 21 12z"/>',
  dollar: '<path d="M12 2v20M17 6a4 4 0 0 0-4-3H11a3.5 3.5 0 0 0 0 7h2a3.5 3.5 0 0 1 0 7h-2a4 4 0 0 1-4-3"/>',
  refresh: '<path d="M21 12a9 9 0 1 1-3-6.7L21 8M21 3v5h-5"/>',
  flask: '<path d="M9 3h6M10 3v6l-5 9a2 2 0 0 0 1.8 3h10.4A2 2 0 0 0 19 18l-5-9V3M7.5 14h9"/>',
}

// Self-inject the .ic sizing rule once (browser only), so any portal that
// imports ic() gets correct icon sizing without wiring a separate stylesheet —
// important for the raw-CSS-injection portals (agents/kuery/quickstart).
const ICON_STYLE_ID = 'kedge-portalkit-icons-css'
if (typeof document !== 'undefined' && !document.getElementById(ICON_STYLE_ID)) {
  const s = document.createElement('style')
  s.id = ICON_STYLE_ID
  s.textContent = '.ic{width:1.05em;height:1.05em;display:inline-block;vertical-align:-0.16em;flex:none;stroke-width:2}'
  document.head.appendChild(s)
}

// ic returns the inline SVG markup for an icon, optionally with extra classes.
export function ic(name: IconName, extraClass = ''): string {
  const cls = extraClass ? `ic ${extraClass}` : 'ic'
  return `<svg class="${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${PATHS[name]}</svg>`
}
