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
never carries Git credentials. The browser-facing coding-web request still uses
its existing internal-cluster route. Runtime must support the async service before
this coding-web version is deployed; there is no silent synchronous fallback.

Validation (2026-11-07): full Go server suite; targeted multi-repository state,
read-only, tenant isolation, failed attach, explicit dirty detach, async retry,
cancellation and wire-shape tests; actual Chromium UI flows in Chinese and English
for attach/failure/selection/read-only commit/writable commit/dirty detach, plus
existing workspace and environment browser regressions. No deployment performed.
