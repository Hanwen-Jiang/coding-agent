# HeroUI integration

CODE UI uses the official HeroUI v3.2.7 **precompiled CSS distribution**, included verbatim as `internal/web/static/heroui.css` with its Apache 2.0 license. It is served from the app origin and included by Go embed, so production needs neither a CDN nor a Node process. The original semantic DOM, keyboard behavior, event streams and API clients remain in place.

The DOM factory applies verified HeroUI `button`, `button--primary`, `button--outline`, `button--ghost`, `button--danger-soft`, size and icon-only BEM classes. `code-ui.css` maps application tokens and layout to the neutral surfaces, spacing, grouped navigation and compact forms in the official HeroUI Chat and Dashboard templates. The original user-supplied references were `heroui-v3-templates/web/chat` and `hp-components/codes/react`; component APIs were checked with the HeroUI Web MCP. The existing shell uses this CSS integration; conversation content uses the React islands described below.

To refresh the vendored asset from an installed, pinned package:

```sh
node scripts/ui/vendor-heroui.mjs /path/to/node_modules/@heroui/styles
```

The updater verifies package name and version and prints the resulting SHA-256. `internal/web/static/index.html` loads upstream CSS, established application styles, then the small application integration stylesheet. Go's asset versioning fingerprints all three files together.

Settings are reached from the account menu. Personal configuration, Skills and MCP connections are tabs within Settings; the session composer retains its tool selection control. Legacy `#/skills` and `#/mcp` links resolve to their settings sections.

Preserve the established terracotta palette from `style.css`. The composer follows PromptInput's ToolbarStart/ToolbarEnd layout: one 32px row with model and task settings on the left and compact usage on the right. Keyboard hints are tooltips and screen-reader descriptions, never a second visible footer. Exact cost and token details remain in the usage popover.

Repository creation uses a single picker with multi-select, independent branches and mount directories. A submitted selection is frozen during creation. Each attachment uses the existing async workspace operation and idempotency mechanism. If an attachment fails, the dialog retains the created workspace, reads its current repository list and retries only unfinished attachments. Existing root-layout workspaces remain supported.

Validation includes the existing Go/browser suite and `TestRepositoryMultiSelectAndManagementRoutesInBrowser` in both languages, checking the public request contract, read-only permissions, directory collisions, retained selection during search, frozen submission and retry behavior.

## React conversation islands

Assistant answers and streaming drafts now use real `ChatMessage.Assistant`, `Markdown` and `CodeBlock` components from `@heroui-pro/react` 1.0.0-beta.11, rendered by React 19.3.1. The TypeScript/TSX source is under `ui/chat`. Existing task transport, SSE replay, approvals, steering, file delivery and the composer remain application-owned JavaScript. This is a gradual React migration, not a full rewrite.

`mountAnswer(container, props)` returns stable `update(props)` and `unmount()` methods. An answer retains its DOM host while its stream changes; disconnected hosts are unmounted after DOM mutations. Markdown disallows executable HTML and dangerous link protocols, and external links use `noopener noreferrer`. Code copying uses a native action because the library's animated copy button injects inline styles that the application's existing CSP rejects. The real Pro code block and Shiki rendering remain intact. No CSP exception was added.

The bundle uses a finite Shiki grammar set with its JavaScript regex engine, so syntax highlighting requires no CDN, worker or WASM fetch. Unsupported languages render as plain code. Upstream Pro CSS for the three components is enclosed in `@scope (.pro-chat)` and cannot reset the rest of the application.

Rebuild with the licensed, installed component packages supplied by the project:

```sh
npm ci --prefix ui --ignore-scripts --legacy-peer-deps
node ui/build.mjs /path/to/node_modules
```

The build verifies the package versions, bundles all runtime dependencies into same-origin `chat-ui.js`, writes scoped `chat-ui.css`, and records component/version/size metadata in `ui/build-meta.json`. Production still runs the Go service with checked-in embedded assets; it does not need Node. The existing public API interfaces are unchanged.

## Main conversation surface

The main conversation now uses real Pro `ChainOfThought` and `ChatTool` components for the public execution event timeline, `ChatAttachment` for delivered files, and `PromptInput.Shell`/`Content` around the existing native composer controls. It does not manufacture reasoning, tool output or upload capability. Completed execution details start collapsed; the current running action remains visible. Plans, sub-agent status and raw public events stay available inside the disclosure. Stable island handles preserve expansion state across SSE updates and unmount on navigation.

The composer bridge deliberately uses the stacked shell: Pro's compact mode reserves named grid areas for its own textarea and toolbar, which would shrink a native control adapter into an automatic grid column. Existing send/steer/interrupt/approval callbacks, textarea identity, draft value and keyboard handling remain application-owned; only their presentation is adopted. Conversation and composer share an 800px outer content axis, with responsive padding and the existing 44px topbar/terracotta tokens preserved.

React Aria's fixed pressable touch rule is shipped in the same-origin stylesheet with its standard style ID already present on the link, avoiding runtime inline style injection without loosening CSP. The bundled Pro styles require the `tailwind-variants/config` entry under the installed v4 package. Browser checks cover actual component loading under strict CSP, inner textarea width and send alignment (not just outer shell width), progress/plan/raw-event reachability, file download binding, draft retention, three viewport sizes, both themes, streaming and the existing task approval/steering flows.
