# Linear provider

Linear is a standalone provider under `providers/linear`. It exposes namespaced
Connection, Operation and Event APIs through `linear.providers.faros.sh`.
Human and machine callers author the same immutable Operation resources; the
provider reconciles them against Linear and stores observed outcomes in status.
Uncertain writes remain explicit and are never automatically replayed.

The provider discovers tenant APIs through its APIExportEndpointSlice. It reads
referenced Secrets through its declared Secret-get claim, with Connection team
policy applied to issue operations and webhook events. Portal and MCP command
submission use caller credentials against the tenant API. Bootstrap and runtime
identities are separate in the Helm deployment. No Gru implementation or private
company configuration belongs in this provider.

See the [provider contract and usage](../providers/linear/README.md),
[typed API](../providers/linear/apis/v1alpha1/types.go),
[chart reference](../providers/linear/deploy/chart/README.md), and
[current acceptance evidence](../providers/linear/stage-4-status.md).
