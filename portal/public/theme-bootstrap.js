// Apply theme before first paint to prevent flash. Unset preference
// defaults to dark. Following the OS is an explicit `system` preference;
// any failure (no matchMedia, storage access, or thrown error) renders
// dark, which matches the CSS base in main.css.
// Must stay in lockstep with stores/theme.ts — a mismatch here IS the
// flash this script exists to prevent.
//
// This is a static file rather than an inline <script> in index.html on
// purpose: the portal CSP is `script-src 'self'` with no 'unsafe-inline'
// (pkg/hub/portal_security.go), so every script the document runs must be a
// same-origin file. It is loaded synchronously from <head>, before the module
// entry, so it still wins the race with the first frame.
(function() {
  try {
    var stored = localStorage.getItem('faros-theme');
    var t = stored === 'light' || stored === 'dark' || stored === 'system' ? stored : 'dark';
    var hasMatchMedia = typeof window.matchMedia === 'function';
    var prefersDark = hasMatchMedia
      && window.matchMedia('(prefers-color-scheme: dark)').matches;
    var d = t === 'system'
      ? (hasMatchMedia ? (prefersDark ? 'dark' : 'light') : 'dark')
      : t;
    var scheme = document.getElementById('faros-color-scheme');
    if (scheme) scheme.setAttribute('content', d);
    document.documentElement.className = d;
    document.documentElement.style.colorScheme = d;
  } catch (e) {
    var scheme = document.getElementById('faros-color-scheme');
    if (scheme) scheme.setAttribute('content', 'dark');
    document.documentElement.className = 'dark';
    document.documentElement.style.colorScheme = 'dark';
  }
})();
