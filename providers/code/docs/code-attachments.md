# PR screenshot attachments

Agents export final-response.md and screenshots.json; Factory stages these with
Code. The PR description is public to everyone who can read the destination PR.
Screenshots use simple inline Markdown paths under artifacts/screenshots/, up to
8 PNG/JPEG images, 2 MiB each and 8 MiB total. Runner constants define the export
limits; Code independently validates this cross-service contract.

Never capture development tokens, credential forms, terminal secrets, private
customer data, or sensitive internal URLs. Agents redact recognizable GitHub
credentials and Bearer values from the public report, but this is not exhaustive
secret detection. Images are not OCR-scanned or automatically redacted. Inspect
what you capture and use isolated development data. Do not assume repository
privacy or URL obscurity makes uploaded media safe to publish.

## GitHub dependency and validation

The backend uses the undocumented GitHub web-upload endpoint
POST https://uploads.github.com/user-attachments/assets with a supported user
token. This is not a supported public REST contract. GHES and installation tokens
are not supported. The host is pinned; redirects and unexpected response URLs
are refused. An HTTP 4xx other than rate limiting stops attachment retries for
that unchanged spec; transient failures retry at the PR observation interval.
Code still pushes the branch and opens/updates the PR without local image links,
with an explicit missing-evidence note and AttachmentsPublished=False.

On 2026-10-17 (America/Chicago), live probes against the Railgrid repository
accepted a one-pixel PNG and the inspected 55,683-byte RAI-17 Planner screenshot
with HTTP 201. The real screenshot is:
https://github.com/user-attachments/assets/e6081152-3c07-49d2-8e34-a47f4eade6c9
Anonymous retrieval returned 404. Authenticated retrieval returned a signed asset
redirect; following it **without forwarding the GitHub credential** returned
HTTP 200 with identical bytes (SHA-256
fa692da51b60c276c1c515031db0cec743af4211e24272dddcd3b81d3eb607e7).
This verifies the actual endpoint/auth/request/response and retrieval for that
user credential, not all supported credential types. This was a direct upload
probe using saved evidence, not a new Factory run. RAI-17 / Providers PR #79
captured real UI screenshots but did not exercise publication. Full Factory-to-PR
image acceptance remains pending deployment; do not call that run a publication
pass.

Upload receipts are status patches retried after conflicts without repeating
the upload. A process crash or persistent storage failure between remote upload
and durable receipt can still orphan an asset: the undocumented API offers no
idempotency key. Closed/merged PRs do not resolve or upload attachments.
