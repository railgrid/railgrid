# Design-document metadata schema

Design entries are Markdown documents whose first line is `---`, followed by a
JSON object, followed by a closing `---`. JSON is a strict, dependency-free
frontmatter format and is also valid data in common YAML tooling. The body
after the closing fence is ordinary Markdown and must not be empty.

## Shape

```json
{
  "schema": 1,
  "id": "design.example.surface",
  "title": "Example surface",
  "kind": "pattern",
  "status": "draft",
  "authority": { "design": "normative", "implementation": "reference" },
  "implementation": { "state": "planned", "notes": "Explain the boundary." },
  "appliesTo": ["portal"],
  "owner": "design-system",
  "canonicalSource": [
    { "path": "docs/design/patterns/example.md#example-surface", "role": "design" },
    { "path": "portal/src/assets/main.css", "role": "implementation" }
  ],
  "verification": {
    "state": "unverified",
    "checks": [{ "kind": "review", "ref": "design review", "status": "not-run" }]
  },
  "relatedDocuments": []
}
```

All top-level fields in the example are required. Unknown keys are rejected.

| Field | Contract |
| --- | --- |
| `schema` | Integer `1`. |
| `id` | Stable lowercase identifier matching `a-z`, digits, `.` and `-`, beginning with a letter; unique under `docs/design`. Do not include dates or branch names. |
| `title` | Non-empty human title. |
| `kind` | `system`, `principle`, `token`, `recipe`, `component`, `pattern`, `journey`, `policy`, `decision`, or `reference`. |
| `status` | `active`, `draft`, `proposed`, `deprecated`, or `superseded`; document lifecycle, not implementation health. |
| `authority.design` | `normative` when consumers must follow the intent; `informative` for context only. |
| `authority.implementation` | `canonical` for authoritative implementation paths, `reference` for illustrative paths, or `none` when no implementation is claimed. |
| `implementation.state` | `shipped`, `partial`, `planned`, `not-started`, `not-applicable`, or `retired`; state is separate from document lifecycle. Use `not-applicable` for a policy or contract with no runtime implementation. |
| `implementation.notes` | Optional short explanation, especially for partial, planned, or retired state. |
| `appliesTo` | Non-empty unique lowercase surface/audience slugs. |
| `owner` | Lowercase accountable team or owner slug. |
| `canonicalSource` | Non-empty `{path, role, label?}` objects. Roles are `design`, `implementation`, or `reference`; paths are repository-relative files/directories or `https://`/`mailto:` URLs. |
| `verification.state` | `verified`, `partial`, or `unverified`. |
| `verification.checks` | `{kind, ref, status, evidence?}` objects. `kind` is `command`, `test`, `browser`, or `review`; status is `passing`, `failing`, `pending`, or `not-run`. |
| `relatedDocuments` | Required array, possibly empty, of `{id, relation, path?}`. Each relation is directed from the current document to its target: `implements` means the current document implements the target. Relations are `related`, `extends`, `implements`, `supersedes`, `prerequisite`, or `see-also`; IDs resolve to catalog entries. |

Every entry has a design source. A shipped or partial implementation has at
least one implementation source. Planned and not-started entries may omit one;
retired entries retain one to show what was retired. `not-applicable` is the
explicit no-runtime state: it must pair with
`authority.implementation: "none"`, and `none` must always use
`implementation.state: "not-applicable"`. A no-runtime entry cannot list an
implementation source.

Implementation maturity and verification evidence are independent dimensions.
`implementation.state: "shipped"` is valid with `verification.state` of
`verified`, `partial`, or `unverified`; use the latter two when the design is
shipped but evidence is incomplete or has not been collected. Only a verified
entry needs at least one passing check and no failing checks. Partial and
unverified entries may have pending, not-run, failing, or no checks. These rules
keep volatile implementation and verification claims honest without changing
the normative intent in the body.

`relatedDocuments` is a directed document graph. Record an implementation edge
on the implementing document (for example, a component `implements` a pattern);
the target may link back with `related` or `see-also`, but must not repeat the
inverse as another `implements` edge.

## Component document body contract

Entries with `kind: "component"` must contain these exact level-two headings,
in this order:

1. `Purpose`
2. `Use when`
3. `Avoid when`
4. `Anatomy and variants`
5. `Behavior`
6. `Content`
7. `Layout and responsive behavior`
8. `Accessibility`
9. `Code and evidence`
10. `Related guidance`

Keep the component guidance under those headings. The validator ignores fenced
code blocks and reports a `missing-component-section` diagnostic naming the
missing heading and the exact `##` heading to add. Other document kinds may use
their own body structure.

## References and catalog

Repository-local `canonicalSource.path`, `relatedDocuments.path`, and Markdown
links are checked from the repository root or the entry's directory as
appropriate. Missing files and Markdown heading anchors fail validation.
External HTTP(S) and `mailto:` links are accepted but never fetched.

```sh
make verify-design-docs
node hack/verify-design-docs.mjs --catalog
```

The catalog is deterministic and sorted by stable ID and path. It contains the
metadata and an `errors` array; the command exits non-zero when errors exist.
