# Local Claude adapter copy

This package is copied from
[`github.com/cloudwego/eino-ext/components/model/claude`](https://github.com/cloudwego/eino-ext/tree/0910e2fd6ed466fbd9c1d92a50ec1958fe53d42/components/model/claude)
at tag `components/model/claude/v0.1.23` (commit `0910e2f`). The upstream Apache 2.0 license is
preserved in [`LICENSE`](./LICENSE), and each copied Go source file retains its
upstream copyright and license header.

The only behavioral change is in `ChatModel.Stream`: concatenating buffered
empty messages uses a goroutine-local error variable. Upstream assigns to the
method's named return error from the asynchronous stream goroutine, which races
with the method returning. The native Anthropic SDK request and response
conversions remain unchanged.

The package/type comments satisfy the repository linter. Two line-scoped lint
annotations retain the upstream Vertex credential call and unused private field
without changing their behavior or layout.

Remove this copy when upstream publishes a Claude adapter release that fixes
the stream error race and remains compatible with the provider SDK's supported
Eino version. Verify the native Anthropic wire and race tests before switching
back to the upstream package.
