# Existing account migration

Railgrid Auth v1 identifies existing Railgrid accounts. Before changing an
existing app's account model, inspect the current schema and data, then show a
concrete migration plan to the project owner.

Do not assign all existing rows to whichever Railgrid user signs in first. Do
not infer ownership from a matching email, display name, or unverified client
field. Ask the owner for an explicit mapping from each existing app account to
an approved Railgrid user ID, and a policy for records whose owner is missing
or ambiguous. Migrate from that mapping in a transaction and preserve an audit
record of the mapping used.

The v1 scope is existing Railgrid accounts only. Organization membership and
third-party identity providers are deferred. Do not add either as an implicit
part of account migration.
