# Conversation design and React migration

The old execution view repeated a bordered card for every call, allowed long shell commands to wrap in headings, and created a scroll area inside the conversation. Even though it used Pro components, the result read like a log viewer. The revised view gives public progress text the main reading level and groups only consecutive tool activity.

## Research

The user's Claude screenshot establishes the visual reference: paragraph → activity summary → compact expandable rows → another paragraph. Public documentation supports the event model, without revealing the private Claude web implementation:

- [Claude SDK streaming output](https://code.claude.com/docs/en/agent-sdk/streaming-output) separates streamed text and tool-use blocks from the eventual result.
- [Claude Code interactive mode](https://code.claude.com/docs/en/interactive-mode) documents compact tool activity and a more detailed transcript view.
- [Codex App Server](https://learn.chatgpt.com/docs/app-server) describes independent turn items, stable item IDs, deltas and completed items; agent messages distinguish commentary, partial answers and final answers.

These sources motivate the hierarchy in `DESIGN.md`; they are not claims about either product's unpublished frontend code.

## Implemented

`conversation-flow.js` projects persisted public progress and actual calls. The React `Conversation` component owns all progress paragraphs, groups, plans, sub-agent summaries and tool disclosures for a task, using stable keys. Expansion is local React state rather than a controller-triggered redraw of the entire turn. More than eight calls can be disclosed without nested scrolling. Full targets, elapsed metadata and errors remain reachable. Running activity and failure counts are visible with the group collapsed.

`Composer` now owns the real Pro textarea and send/menu buttons. A narrow subscription adapter preserves the existing application task actions. The textarea fills the upper tier and the real Pro Toolbar integrates model/settings/usage controls and the send action in the lower tier of one shell. Application-owned status, approval and tool controls use explicit slots; the input is no longer a native textarea wrapped by a Pro shell. The Markdown answer and attachments continue using real Pro components.

The current `tool_status` contract does not include stdout. The view does not invent it. Public paragraphs come from persisted `progress_message`, not reasoning or planner text. The projector matches Runtime's 2000-Unicode-code-point limit.

`RunDetails` is also React-owned. Its collapsed entry shows a concise call/event summary. The inspector uses inline round/call/event metrics, six recent event rows, independently expandable descriptions and one raw JSON disclosure with a copy action. Parent, row and raw-data expansion survives new events; there is no fixed-height event-list viewport. The raw JSON code surface alone has a bounded scroll area.

The end-to-end checks also found and repaired an existing async workspace boundary mismatch: `create_session` rejected the UI's structured `task_settings`. The journal adapter now strictly decodes and validates the supported schema before persistence, including rejection of unknown nested fields.

## Remaining architecture

This change migrates the core conversation experience. Routing, workspace/settings pages, sidebar/topbar, task transport and approval control creation still live in `app.js`. A full-site React rewrite is a separate architectural change; this repository does not claim that the whole application is React-owned.

## Rebuild and verify

Use the licensed pinned packages supplied in the sibling HeroUI workspace:

```sh
node ui/build.mjs /Users/haven/projs./heroui-pro/npx-resource/node_modules
node --test ui/chat/conversation-flow.test.mjs
CODING_TEST_BROWSER='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' go test ./internal/web -count=1
```

New browser coverage includes more/less, long command details, one group border, no nested scroll, failure summaries, SSE stability and real React composer keyboard behavior. Existing tests exercise historical replay, approvals, downloads, both languages and responsive layouts.
