# Final server UI regression — 2026-11-10

Tests use the dedicated project-server directory
`/root/proj/agent-projs/.file-ui-validation-20261110` and bounded
`fi-ui-20261110-*` containers. Production workloads are not changed.
The browser is real Chromium from `fi-20261110-browser-ci:1`.

## Settings popup regression

The first whole-suite run failed one existing UI test: the unlimited network
settings popup occasionally disappeared when reopened. The unchanged focused
case reproduced the failure in a ten-run sample (`settings-repro.log`).
A temporary controlled browser probe called the real `updateComposer(true)`
while its toolbar popup was open. It reproduced the same visible loss; its trace
shows the forced redraw detached the popup anchor, then ResizeObserver positioning
closed the popup (`pop-force.log`). A delayed first React composer bind calls
this same forced-refresh path in normal use.

The one-condition fix defers both forced and changed-key toolbar redraws while
the popup is anchored there, sets `barDirty`, and flushes after close. Account and
route cleanup still closes the popup. The deterministic before/after probe
passed five times after the fix (`pop-force-fixed.log`), and the original
unmodified unlimited test passed ten times (`settings-fixed.log`). No timeout,
permission assertion or test expectation was relaxed. Diagnostic hooks and
server-only test edits were removed before the final whole-suite run.

The original five workbench static assets remain covered by the 28-case fixture;
its final title-only update is verified separately on the current asset snapshot.
The final server source manifest includes the actual app.js and bundle plus all
140 Go/static build inputs and is compared to the local worktree before testing.
Final whole-suite numbers and raw-log hashes are in `result.json`: **257 top-level tests, 335 passing test events, zero failures**, with `go vet` passing. The default opt-in public-discovery integration is one skip; it was already separately enabled and verified during the preceding server acceptance. Latest Runtime file/task API packages also passed `-race` and `go vet`, with all 35 compiled Go inputs matching local source. The full server/local Coding manifest matches all 140 inputs before and after the run. Synthetic component fixtures verify UI/request semantics, not file
parser output or production deployment.
