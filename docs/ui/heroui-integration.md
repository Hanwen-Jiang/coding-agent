# HeroUI integration

CODE UI uses the official HeroUI v3.2.7 **precompiled CSS distribution**, included verbatim as `internal/web/static/heroui.css` with its Apache 2.0 license. It is served from the app origin and included by Go embed, so production needs neither a CDN nor a Node process. The original semantic DOM, keyboard behavior, event streams and API clients remain in place.

The DOM factory applies verified HeroUI `button`, `button--primary`, `button--outline`, `button--ghost`, `button--danger-soft`, size and icon-only BEM classes. `code-ui.css` maps application tokens and layout to the neutral surfaces, spacing, grouped navigation and compact forms in the official HeroUI Chat and Dashboard templates. The original user-supplied references were `heroui-v3-templates/web/chat` and `hp-components/codes/react`; component APIs were checked with the HeroUI Web MCP. The existing shell uses this CSS integration; conversation content uses the React islands described below.

To refresh the vendored asset from an installed, pinned package:

```sh
node scripts/ui/vendor-heroui.mjs /path/to/node_modules/@heroui/styles
```

The updater verifies package name and version and prints the resulting SHA-256. `internal/web/static/index.html` loads upstream CSS, established application styles, then the small application integration stylesheet. Go's asset versioning fingerprints all three files together.

Settings are reached from the account menu. Personal configuration, Skills and MCP connections are tabs within Settings; the session composer retains its tool selection control. Legacy `#/skills` and `#/mcp` links resolve to their settings sections.

Preserve the established terracotta palette from `style.css`. The composer uses a full-width writing area above PromptInput's Toolbar/ToolbarStart/ToolbarEnd inside one 22px-radius shell. A 32px controls row contains the model, task settings and compact usage, with a separate 36px circular send button at the trailing edge. Keyboard hints are tooltips and screen-reader descriptions, never a second visible footer. Exact cost and token details remain in the usage popover.

Repository creation uses a single picker with multi-select, independent branches and mount directories. A submitted selection is frozen during creation. Each attachment uses the existing async workspace operation and idempotency mechanism. If an attachment fails, the dialog retains the created workspace, reads its current repository list and retries only unfinished attachments. Existing root-layout workspaces remain supported.

Validation includes the existing Go/browser suite and `TestRepositoryMultiSelectAndManagementRoutesInBrowser` in both languages, checking the public request contract, read-only permissions, directory collisions, retained selection during search, frozen submission and retry behavior.

## React conversation islands

Assistant answers and streaming drafts now use real `ChatMessage.Assistant`, `Markdown` and `CodeBlock` components from `@heroui-pro/react` 1.0.0-beta.11, rendered by React 19.3.1. The TypeScript/TSX source is under `ui/chat`. Existing task transport, SSE replay, approval/status control creation, steering and file delivery remain application-owned JavaScript. The activity stream and editor are React-owned as described below. This is a gradual React migration, not a full rewrite.

`mountAnswer(container, props)` returns stable `update(props)` and `unmount()` methods. An answer retains its DOM host while its stream changes; disconnected hosts are unmounted after DOM mutations. Markdown disallows executable HTML and dangerous link protocols, and external links use `noopener noreferrer`. Code copying uses a native action because the library's animated copy button injects inline styles that the application's existing CSP rejects. The real Pro code block and Shiki rendering remain intact. No CSP exception was added.

The bundle uses a finite Shiki grammar set with its JavaScript regex engine, so syntax highlighting requires no CDN, worker or WASM fetch. Unsupported languages render as plain code. Upstream Pro CSS for the three components is enclosed in `@scope (.pro-chat)` and cannot reset the rest of the application.

Rebuild with the licensed, installed component packages supplied by the project:

```sh
npm ci --prefix ui --ignore-scripts --legacy-peer-deps
node ui/build.mjs /path/to/node_modules
```

`@heroui-pro/react` is licensed and is not fetched by this build. Keep the other pinned packages in a directory of their own (`@heroui/react` and `@heroui/styles` 3.2.7, `react` and `react-dom` 19.3.1, `shiki` 4.6.0, `tailwind-variants` 4.x for its `dist/config-entry.js`, plus the Pro package's peer dependencies) and copy the installed Pro package into its `node_modules/@heroui-pro/react`. `npm install` in that directory prunes the copy, so copy it again afterwards. A project that merely has the Pro package installed (for example a HeroUI Next.js template) usually carries other versions of `@heroui/react` and `react`, and the version check rejects it.

The build verifies the package versions, bundles all runtime dependencies into same-origin `chat-ui.js`, writes scoped `chat-ui.css`, and records component/version/size metadata in `ui/build-meta.json`. Production still runs the Go service with checked-in embedded assets; it does not need Node. The existing public API interfaces are unchanged.

## Application sidebar

The left sidebar is the real `Sidebar` from `@heroui-pro/react` (`ui/chat/sidebar.tsx`), with `Dropdown`, `Avatar` and `Kbd` from `@heroui/react`. `app.js` still owns state, routing and every action: `sideProps()` builds serializable data (navigation, workspaces with their sessions, the account, theme and language choices) and `SIDE_ACTIONS` holds the callbacks. The component owns presentation, keyboard behaviour and overlays.

- One React root lives for the whole signed-in session. `renderApp()` rebuilds the layout on every route change and re-attaches the same host element, so the tree keeps its focus and scroll position. `renderLogin()` releases it.
- Until `chat-ui.js` has loaded, the column shows the previous native sidebar (`nativeSide()`); the React sidebar replaces it when the module arrives. A failed load leaves the native one in place.
- Collapsing turns the sidebar into a 56px icon rail (`collapsible="icon"`, `Sidebar.Rail`, Ctrl/⌘ B) instead of hiding it; the grid column follows the sidebar's width. Below 48rem the Pro sidebar becomes a sheet, opened from the top bar's menu button through the handle's `openMobile()`.
- Workspaces are expandable tree items with their sessions as children (arrow keys expand and collapse). Session actions (rename, duplicate, delete) and the account menu (settings, account, appearance, language, sign out) are `Dropdown` menus.
- Sheets, menus and tooltips render in portals under `<body>`, so the upstream `sidebar.css` is included unscoped in `chat-ui.css` (`shellComponents` in `ui/build.mjs`); application glue is `ui/chat/sidebar.css`.

Two application rules had to change for upstream components to work as designed:

- `applyTheme()` always sets `data-theme` to `light` or `dark`, resolving "system" with `prefers-color-scheme`. The upstream dark variables are keyed on `data-theme` only, so without this every unmapped token stayed light under a dark system theme.
- The universal `box-sizing: border-box` rule in `style.css` moved into `@layer base`. Unlayered rules override every layered upstream rule; `.sheet__content` needs `content-box`, and with the override the mobile sheet rendered entirely off-screen.

The application also sets `--radius: 16px`, which upstream multiplies for menu radii; the sidebar's popovers set their radii explicitly.

## Search and dialogs

Search (Ctrl/⌘ K, or the sidebar's Search item) is the Pro `Command` palette (`ui/chat/command.tsx`). `commandProps()` in `app.js` lists sessions, workspaces and a short "Go to" group (new session, new workspace, the three pages, settings); the component filters on each entry's `text`, and `COMMAND_RUN` performs the pick after the palette closes. The previous native palette remains as the fallback while `chat-ui.js` is loading.

Confirmations and single-value prompts go through `askConfirm()` and `askText()` in `app.js`, which return promises and render `AlertDialog` and `Modal` from `@heroui/react` (`ui/chat/dialogs.tsx`). They fall back to the browser's `confirm` and `prompt` before the module has loaded. A confirmation's title names the action and is reused as the confirm button's label (a call without a title gets a generic "Please confirm" and "Confirm"); focus starts on Cancel. Every confirmation in `app.js` goes through `askConfirm()`, and `entry-ui.js` receives it as `ask`. While a native modal `<dialog>` is open (account, repository manager and the other `showModal()` dialogs) it sits in the top layer above any portal under `<body>`, so both helpers use the browser's `confirm` and `prompt` in that case.

A handler that awaits a confirmation must read `event.currentTarget` before the `await`; the browser clears it once the handler yields.

Both render in modal portals, so the upstream `command.css` is global like `sidebar.css`; glue for both is `ui/chat/command.css`. The palette, dialogs and sidebar each get their own React root, created the first time they are needed and released by `renderLogin()`.

`code-ui.css` maps `--danger`, `--success` and `--warning` to the application's `--err`, `--ok` and `--warn`; upstream derives the soft and hover shades from them, so status colours in upstream components follow the application palette in both themes.

`style.css` also resets `button { color: inherit }` outside any layer, which overrides the layered upstream `.button` foreground. The dialogs restore `color: var(--button-fg)` for their own buttons; an upstream `Button` rendered elsewhere needs the same until that reset moves into a layer.

## List pages

Workspaces, Active and Environments are React pages (`ui/chat/pages.tsx`): `Card`, `Chip` and `Button` from `@heroui/react`, `ItemCard` and `EmptyState` from `@heroui-pro/react`, and a `Dropdown` with single selection for the Active page's source filter. `workspacesProps()`, `activityProps()` and `environmentsProps()` in `app.js` build the data; every action calls the existing controller functions (`openDraft`, `jumpTo`, `cancelTask`, `openEnvironmentEditor`, `deleteProject` and so on).

`showPage()` mounts each page through `proPresentation`, so a page keeps one React root while it stays on screen and only receives new props on re-render. Before `chat-ui.js` has loaded the previous native page (`nativeWorkspaces()`, `nativeActivity()`, `nativeEnvironments()`) is drawn instead; once the module is in, pages mount directly without that first native frame.

`style.css` already skins `.card` and `.chip`, including the `run`, `ok`, `warn` and `err` tone classes, so the pages pass those classes alongside the upstream `color` prop and the components keep the application's look. Page layout is `ui/chat/pages.css`. The empty workspace page shows the same three starters as the welcome screen inside an `EmptyState`.

## Main conversation surface

See [Conversation design and migration](conversation-design.md) and the root `DESIGN.md` for the current architecture and visual rules. The task activity stream uses one React root with keyed public messages and compact tool groups. Plans and sub-agent summaries are React-rendered data, not native DOM slots. Groups keep expansion state across event updates, share one border, and progressively disclose large histories without an inner scroll viewport.

The editor uses real `PromptInput.TextArea`, `PromptInput.Send` and `PromptInput.Action` components. Its subscription model bridges task phase, input callbacks and the established send/steer/interrupt actions. Approval, status and existing model/settings/usage controls remain in explicit native slots inside the React surface. The controller preserves the textarea and user draft while streaming. The main conversation and composer share an 800px outer content axis, with responsive padding and the existing 44px topbar/terracotta tokens.

React Aria's fixed pressable touch rule is shipped in the same-origin stylesheet with its standard style ID already present on the link, avoiding runtime inline style injection without loosening CSP. The bundled Pro styles require the `tailwind-variants/config` entry under the installed v4 package. The real Chrome suite checks CSP, component ownership, geometry, live updates, IME, keyboard commands, approval flows and file downloads.

Public progress comes only from persisted `tool_status.progress_message`. Tool statuses coalesce by round, node, step and call index; legacy calls without an index match pending calls by tool and target in order. Late results update their original group. Reasoning deltas and planner goals never become public messages. Snapshot backfill merges by event ID without discarding newer live events or drafts. Internal pipeline details remain in one collapsed **Run details** disclosure after the answer.
