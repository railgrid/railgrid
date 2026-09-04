---
{
  "schema": 1,
  "id": "design.example.surface",
  "title": "Replace with a concise contract title",
  "kind": "pattern",
  "status": "draft",
  "authority": { "design": "normative", "implementation": "reference" },
  "implementation": { "state": "planned", "notes": "Explain the boundary or migration state; use not-applicable only with authority.implementation none." },
  "appliesTo": ["portal"],
  "owner": "design-system",
  "canonicalSource": [
    { "path": "docs/design/patterns/example.md#example-surface", "role": "design" },
    { "path": "portal/src/assets/main.css", "role": "implementation" }
  ],
  "verification": {
    "state": "unverified",
    "checks": [{ "kind": "review", "ref": "replace with evidence", "status": "not-run" }]
  },
  "relatedDocuments": []
}
---

# Replace with the design contract

State the user-facing intent, boundaries, examples, and migration notes. Keep
implementation state and verification evidence in metadata; `shipped` does not
require verified evidence, and `not-applicable` is reserved for contracts with
`authority.implementation: "none"`. Never imply that this template proves a
surface is shipped.
