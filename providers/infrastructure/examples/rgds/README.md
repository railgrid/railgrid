# Sample ResourceGraphDefinitions

These RGDs ship with the infrastructure provider's Tilt dev loop.
`make dev-kro-up` applies them to the local management kro cluster so
the catalog UI has real templates without the user having to author
their own.

## What each label / annotation buys you

The railgrid provider's RGD discovery (in `providers/infrastructure/
kro/templates.go`) looks for these keys verbatim:

| Key | Purpose |
|---|---|
| `railgrid.ai/expose=true` (label) | gates visibility — required |
| `railgrid.ai/template-name` (label) | catalog slug; defaults to `metadata.name` |
| `railgrid.ai/template-version` (label) | required when provisioning, for safety |
| `railgrid.ai/category` (label) | filter chip in the catalog grid |
| `railgrid.ai/cloud` (label) | filter chip + maps credential schema |
| `railgrid.ai/display-name` (annotation) | human-readable name |
| `railgrid.ai/description` (annotation) | one-line blurb shown on the card |
| `railgrid.ai/icon-url` (annotation) | optional asset URL |
| `railgrid.ai/sample-values` (annotation) | JSON-encoded form pre-fill |

Add your own RGDs here following the same conventions — `dev-kro-seed`
applies the whole directory recursively.

## How the inputs schema renders

The provider converts kro's SimpleSchema (`spec.schema.spec`) into a
JSON-schema-shaped object that the portal's `DynamicForm` consumes.
Supported leaf grammar:

```
field: type
field: type | required=true | default=foo | description="bar"
field: type | enum=a,b,c
field: integer | minimum=1 | maximum=10
```

Types: `string`, `integer`, `number`, `boolean`. Nested objects and
arrays are out-of-scope for v1.
