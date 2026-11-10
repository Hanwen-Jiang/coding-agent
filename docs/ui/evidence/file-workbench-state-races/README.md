# File workbench state-race verification — 2026-11-10

An independent real headless Chrome run loaded the actual new React
FileWorkbench and its panels. The fixture controls only network response data and
response timing; it does not replace the component's event handlers or state.
The screenshot is marked synthetic. No actual parser/edit/publication execution
is claimed by this UI test.

Four bounded interleavings passed, recorded as six assertions in `result.json`:

1. With A/v2 current, select A/v1's completed read job. The version control moves
   to v1, the old content is visible, and the subsequent real UI read action sends
   `inputs[0].version_id=v1`.
2. Publish an A/v2 candidate, hold the following A/details response, select B/v1,
   then release A's response. The UI remains B/v1; the old publication cannot
   overwrite the new selection's title or version.
3. Hold the initial source-list response, upload X and observe it in the list,
   then release the earlier A/B-only listing. X remains visible.
4. Restore a running job, select that same running job in its history, and return
   running from the first polls. The UI continues requesting status and reaches
   the later completed result. This detects an invalidated epoch with no effect
   restart, not merely a successful one-time status fetch.

The file SHA is the exact reviewed implementation. No production source was
changed by this verification subtask. Temporary fixture sources and capture logs
are in `/tmp/file-workbench-races/`; the earlier three-scenario result remains at
`../file-workbench-races-20261110.json` as chronological evidence. The final run
used a clean Chrome profile and a freshly bundled entry.
