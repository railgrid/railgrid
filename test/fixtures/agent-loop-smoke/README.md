# Disposable Agents review/fix fixture

This draft PR is an explicitly authorized Railgrid Agents integration test.
It must not be merged. The fixture does not participate in the application.

Acceptance criteria: clamp returns the input unchanged inside inclusive bounds,
returns the closest bound outside them, handles equal bounds, and raises
ValueError when lower exceeds upper. Preserve the tests and fix clamp.py.

Run: cd test/fixtures/agent-loop-smoke && python3 -m unittest -v

The initial implementation intentionally contains defects so an independent
reviewer can identify them and a separate fixer can repair them.
