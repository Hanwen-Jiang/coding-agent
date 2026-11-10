# Saved Notebook preview boundaries — 2026-11-10

Actual headless Chrome loaded a freshly compiled `StructurePanel` with controlled
synthetic HTTP responses. Ten assertions passed: small valid notebooks with and
without a `Content-Length` header display the explicitly selected cell, omit
unselected cells, and render output markup as inert text; both oversized header
and streaming bodies are refused, while full candidate download remains exposed.

The small response is 313 bytes; oversized responses are 2,097,465 bytes. The
viewer limit remains 2 MiB and 30 seconds. This verifies UI fetch/render bounds,
not Notebook engine execution or production deployment. `result.json` records
actual checks and component-source SHA. The temporary fixture is retained at
`/tmp/file-notebook-preview-audit/` in this workspace.

A review initially suspected operator precedence in `size > 2 << 20`. Direct
JavaScript evaluation showed that this expression already means `size > (2 << 20)`;
it was not a functional bug. Parentheses were made explicit for readability.
The earlier preview test's success is therefore not reclassified as a false pass.
