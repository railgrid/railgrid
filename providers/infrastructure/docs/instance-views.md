# Instance views — template-defined rendering

By default the portal renders an instance's `My instances` row as
Name / Template / Status / Age, and its detail page as a raw-JSON dump of the
instance's `spec`. A template can override both by declaring a **view**, so each
template controls how its own instances look — add an `Endpoint` column, turn a
computed URL into a clickable link, group fields under headings, etc.

The view is authored **on the template** (`spec.view` on the `Template` CRD, or
the `kedge.faros.sh/view` annotation on a kro RGD). It is opaque to the
controller and interpreted entirely by the portal's view resolver
(`portal/src/view.ts`).

## Shape

```yaml
spec:
  view:
    columns:                          # extra instance-list columns (after Name/Template)
      - header: URL
        value: "${status.url}"        # interpolated string
        type: link
      - header: Auth
        path: spec.oidc.mode          # single dot-path
        type: badge
    detail:                           # detail-page field groups (replace the raw dump)
      - title: Access
        fields:
          - label: URL
            value: "${status.url}"
            type: link
          - label: Hostname
            path: status.host
            type: code
```

A template with no `view` keeps the default rendering. `columns` and `detail`
are independent — define either or both.

## Field values

Each column/field resolves a single value two ways (use one):

- `path:` — a dot-path, e.g. `status.host` or `spec.database.version`.
- `value:` — a string with `${…}` tokens, e.g. `"https://${spec.expose.fqdn}/app"`.
  A token that doesn't resolve becomes empty, so partial templates degrade
  gracefully.

Three namespaces are available, matching the CR's own shape:

| Namespace | Source | Example |
|-----------|--------|---------|
| `spec.*` | the instance's input values | `spec.database.version` |
| `status.*` | controller-computed outputs | `status.url`, `status.host` |
| `meta.*` | `name`, `namespace`, `phase`, `template`, `createdAt` | `meta.phase` |

An **unqualified** first segment resolves against `spec`, so `expose.fqdn` is the
same as `spec.expose.fqdn`.

> `status.*` only carries values your template's `status` mapping actually
> projects onto the instance (the `conditions` and `children` arrays are excluded —
> they have their own sections on the detail page). If a column referencing
> `status.*`/`spec.*` shows `—`, the field isn't populated yet (still
> provisioning) or isn't in the status mapping.

## Renderers (`type`)

| `type` | Renders as |
|--------|------------|
| `text` (default) | plain text |
| `link` | clickable anchor opening in a new tab. The href is `href:` if set, else the resolved value; a bare host gets `https://` prepended. |
| `code` | monospace pill with a copy button (good for secret names, hostnames) |
| `badge` | neutral pill (good for enums like an auth mode or version) |

## Worked example

See [`install/templates/application.yaml`](../install/templates/application.yaml)
for a full `spec.view` driving the URL column, an auth badge, and Access /
Configuration / Readiness detail groups.

## Notes for authors

- Different templates may define different columns; the instance list shows the
  ordered union of all present templates' headers, and a row only fills the
  headers its own template defines.
- When a template's columns reference `spec.*`/`status.*`, the portal fetches the
  full instance object per row to populate them — keep the column count modest.
- Keep `spec.view` in lockstep with the embedded CRD: after editing the
  `Template` CRD's Go type, run `make codegen-infrastructure-provider` so the
  generated + embedded CRDs carry the `view` field (otherwise kcp prunes it).
