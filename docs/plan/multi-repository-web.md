# D-25 coding-web implementation

The workspace layout is selected at creation. ROOT keeps the existing behavior;
MULTI creates an empty container and attaches repositories individually through
Runtime's approved Workspace API. Baseline repository management changes future
session copies; each existing session can manage its own repositories separately.
An attach failure leaves all previously attached repositories intact.

For a MULTI session, the repository picker scopes diff, patch, log, commit,
discard, push/PR, sync and reset. The server resolves the repository against
Runtime ListRepos on every Git request before issuing credentials. Missing or
foreign repository names fail; READ repositories cannot commit or publish.
Credentials are acquired for the selected repository URL only. Files and agent
tasks retain the whole workspace root. Repository-relative diff paths are prefixed
only when marking entries in the workspace-wide file browser.

Session records store branch, PR, pushed head, base branch and base commit under
`repo_states[name]`. Copying a session preserves its original base branch/commit,
not its agent publishing branch or PR. Per-repository GitHub webhook/CI projection
is not introduced here; existing ROOT webhook handling remains unchanged.

Detach first sends `discardChanges: false`. Runtime `repo_dirty` is exposed as a
stable `code` and the UI requires a second explicit confirmation before sending
`true`. The response reports discarded files and commits.

All long Runtime mutations use StartWorkspaceOperation plus short Get polls.
One UUID and unchanged inputs are reused when a start response is lost; polling
never carries Git credentials. The browser-facing request also returns an operation resource as described below.
Runtime must support the async service before this coding-web version is deployed; there is no silent synchronous fallback.

Validation (2026-11-07): full Go server suite; targeted multi-repository state,
read-only, tenant isolation, failed attach, explicit dirty detach, async retry,
cancellation and wire-shape tests; actual Chromium UI flows in Chinese and English
for attach/failure/selection/read-only commit/writable commit/dirty detach, plus
existing workspace and environment browser regressions. No deployment performed.

## Browser request completion and recovery

The browser now opts into `X-Coding-Async: 1` and sends a stable
`Idempotency-Key`. Coding-web acknowledges a persisted operation with HTTP 202;
`GET /api/workspace-operations/{operation}` returns RUNNING, COMPLETED or FAILED
and the original HTTP response. Project/session creation and deletion, copies,
repository attach/detach, commit/discard/sync/reset and publish all use this path.
Legacy callers without the opt-in header retain their existing response shape.
The browser retries a lost admission with the same key, polls via short requests,
and discovers pending operations again after reload. Recent failed operations
remain discoverable and their results remain queryable after process restart.

The journal is stored alongside the existing coding-web single-writer metadata.
It keeps only whitelisted application inputs, the original owner, Runtime IDs,
required projection inputs and the terminal response. Authentication headers,
cookies and Git credentials are never persisted. URLs containing userinfo or
query credentials are rejected before admission. Runtime continues to own the
actual durable execution through Temporal. A web process restart resumes
projection only on a fresh authenticated request from the original tenant/user;
it does not load or renew a saved JWT. Running requests refresh their Runtime
JWT only while the original browser session remains valid.

Resource IDs and Runtime request IDs are stable across re-entry. Recovery queries
an existing Runtime operation before starting one, so a renewed Git installation
token cannot turn a lost Start response into duplicate execution. This uses the
existing Runtime ID formula `wso-` plus SHA-256 of the NUL-terminated tenant,
user and request ID components; preserve this cross-repository contract or add a
request-ID lookup before changing it. Projection insertion deduplicates projects
and sessions, while PR publication retains the existing branch-based remote
reconciliation. A web runner timeout leaves the record resumable; it does not
report a still-running Runtime workflow as a failed operation.

Validation of this follow-up: `go test ./internal/server -run
'^TestWorkspaceHTTP' -count=1` passes four in-process HTTP transport tests for
short admission, disconnect, concurrent duplicate requests, input conflicts,
owner/tenant isolation, lost Start response, fresh-token restart recovery,
projection deduplication, persisted failures and credential rejection. `go vet
./internal/server` passes. The existing Chinese/English Chromium multi-repository
suite now opts into the async path and asserts admission/poll traffic. Actual
browser and listening-server suites are **not newly certified** here: the current
execution sandbox rejects socket creation with EPERM. The Go race run additionally
requires a C compiler, which this shell does not have. Final integration must run
these checks in the permitted host/CI environment and deploy before acceptance.
