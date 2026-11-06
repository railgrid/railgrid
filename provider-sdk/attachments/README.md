# Shared assistant attachments

The shared attachment policy defines immutable receipt metadata, media-type
normalization, content validation, format classification, and finite upload
limits. App Studio and Agents use this package through their storage adapters.
It has no provider, transport, or persistence dependency.

Supported PNG/JPEG/WebP images and plain/Markdown text within their respective
bounds can be model inputs. Other files, including PDFs and images/text beyond
the model-content bound, are opaque files up to the overall file limit. Accepting
an upload does not imply that its contents are available for model analysis.

Providers own tenant/owner/session scope, uploader authorization, draft expiry,
quota admission, atomic binding to accepted turns, encrypted storage, and cleanup.
A receipt is a pointer to immutable data; authorize its scope and compare every
immutable field before reading bytes. Never put bytes or data URLs in durable
transcripts, checkpoints, or logs.

Run `go test ./attachments` from the provider SDK for format and receipt tests;
provider store and request tests cover authorization and lifecycle behavior.
