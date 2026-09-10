# statuspage

`statuspage` renders the small Faros documents used when a browser flow ends
outside the portal shell, such as an OAuth callback or an app-access result.
The helper owns the complete self-contained HTML presentation. It does not own
response headers, authentication, redirects, CSP, or callback behavior; the
caller keeps those responsibilities.

```go
err := statuspage.Render(w, statuspage.Page{
    Title:   "Connected",
    Heading: "Connection complete",
    Message: "You can return to the portal.",
    State:   statuspage.Success,
})
```

`Page.Title`, `Heading`, and `Message` are display strings and are escaped by
the `html/template` renderer. `State` is `Success` or `Error`; unknown values
are rendered as success. Error pages use `role="alert"` with assertive live
announcements, while success pages use `role="status"` with polite
announcements. The document has a viewport declaration, bounded and wrapping
content, reduced-motion rules, and accessible heading/message relationships.

`Script` is an optional `template.HTML` value for a trusted caller-owned script
element. The helper inserts it after the status markup without inspecting or
rewriting it. Callers must construct that value deliberately with
`html/template`; arbitrary user or provider display text belongs in the normal
escaped fields.

The page is fixed dark and self-contained. Its inline stylesheet owns the
current Faros surface, border, accent, success, danger, and text tokens and a
small inline Hexagon mark. It uses a system-font fallback (`ui-sans-serif`,
`system-ui`, and monospace fallbacks), contains no external font or other asset
request, and makes no CSP change. The fixed-dark system-font choice is specific
to this standalone status document; it is separate from Dex's standalone
stylesheet, which embeds its actual Instrument Sans and IBM Plex Mono faces.

The package is intentionally presentation-only so existing auth and browser
flow contracts can adopt the same bounded status surface without giving the
shared helper access to credentials or headers.
