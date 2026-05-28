// Entry script the kedge portal loads via:
//   <script src="/ui/providers/mcp/main.js?v=...">
//
// IIFE build (see vite.config.ts) means the side effects fire immediately
// after the tag finishes parsing: the custom element is registered, and
// from that point on the portal's ProviderFrame can append
// <kedge-provider-mcp> anywhere and it will mount its Vue app.

import './element'
