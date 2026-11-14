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

Confirmations and single-value prompts go through `askConfirm()` and `askText()` in `app.js`, which return promises and render `AlertDialog` and `Modal` from `@heroui/react` (`ui/chat/dialogs.tsx`). They fall back to the browser's `confirm` and `prompt` before the module has loaded. A confirmation's title names the action and is reused as the confirm button's label (a call without a title gets a generic "Please confirm" and "Confirm"); focus starts on Cancel. Every confirmation in `app.js` goes through `askConfirm()`, and `entry-ui.js` receives it as `ask`. The application's own dialogs are hosted in HeroUI Modals (see "Application dialogs"), so a confirmation from inside one stacks above it. Only a native modal `<dialog>` (opened before `chat-ui.js` has loaded) sits in the top layer above every portal; while one is open, both helpers use the browser's `confirm` and `prompt`.

A handler that awaits a confirmation must read `event.currentTarget` before the `await`; the browser clears it once the handler yields.

Both render in modal portals, so the upstream `command.css` is global like `sidebar.css`; glue for both is `ui/chat/command.css`. The palette, dialogs and sidebar each get their own React root, created the first time they are needed and released by `renderLogin()`.

`code-ui.css` maps `--danger`, `--success` and `--warning` to the application's `--err`, `--ok` and `--warn`; upstream derives the soft and hover shades from them, so status colours in upstream components follow the application palette in both themes.

`style.css` also resets `button { color: inherit }` outside any layer, which overrides the layered upstream `.button` foreground. The dialogs restore `color: var(--button-fg)` for their own buttons; an upstream `Button` rendered elsewhere needs the same until that reset moves into a layer.

## List pages

Workspaces, Active and Environments are React pages (`ui/chat/pages.tsx`): `Card`, `Chip` and `Button` from `@heroui/react`, `ItemCard` and `EmptyState` from `@heroui-pro/react`, and a `Dropdown` with single selection for the Active page's source filter. `workspacesProps()`, `activityProps()` and `environmentsProps()` in `app.js` build the data; every action calls the existing controller functions (`openDraft`, `jumpTo`, `cancelTask`, `openEnvironmentEditor`, `deleteProject` and so on).

`showPage()` mounts each page through `proPresentation`, so a page keeps one React root while it stays on screen and only receives new props on re-render. Before `chat-ui.js` has loaded the previous native page (`nativeWorkspaces()`, `nativeActivity()`, `nativeEnvironments()`) is drawn instead; once the module is in, pages mount directly without that first native frame.

`style.css` already skins `.card` and `.chip`, including the `run`, `ok`, `warn` and `err` tone classes, so the pages pass those classes alongside the upstream `color` prop and the components keep the application's look. Page layout is `ui/chat/pages.css`. The empty workspace page shows the same three starters as the welcome screen inside an `EmptyState`.

## Settings

Settings (`ui/chat/settings.tsx`) is the `Tabs` component from `@heroui/react` in its secondary (underline) variant, with one `Tabs.Panel` for the selected section. The panel's content is still drawn by the controller: the component hands a host element to `onPanel`, and `fillSettings()` in `app.js` draws personal configuration, Skills, MCP connections or entry points into it. React renders no children into that host, so it never touches the controller's DOM.

- Changing tabs goes through `goSettings()` and the route, like every other navigation. The component keys the panel by section, so a new section gets a new host and `onPanel` fires again. When the same section is redrawn (after saving, or after a language change), `renderSettings()` draws into the existing host directly.
- Before `chat-ui.js` has loaded, `nativeSettings()` draws the previous segmented tabs and the same content.
- The tab already names the panel, so each section's own heading is hidden on the page. Personal configuration is split into groups (`settingsGroup()`: instructions, configuration files, default tools, default environment): title and note on the left, controls on the right, stacked in the account dialog and below 860px. Each configuration file is one card (path row with a remove action, contents below). Version, disable and save sit in an action bar that stays in view while the page scrolls.

## Session header menu

The session header's "More actions" button opens a `Dropdown` (`ui/chat/topbar-menu.tsx`), the same menu as the sidebar's session menu: the session context on top, then repository and review links, repository management, files and history, then rename, duplicate and delete. `topbarMenuProps()` in `app.js` builds the items; each action resolves the workspace and session at the moment it is chosen. The header is rebuilt on every render, so the controller re-attaches one host (`topbarMenuIsland`) each time; the first render is synchronous, so the header never shows a frame without the button. Before `chat-ui.js` has loaded the native popup (`topbarMenu()`) is used.

Items keep the attributes other code and tests rely on (`data-topbar-menu` on the trigger, `data-topbar-source`, `data-repo-manage`, `data-topbar-drawer` on items), and the popover keeps the `topbar-menu` class.

A menu opened with the pointer focuses its container, and the application's generic `:focus-visible` outline would frame the whole list; `sidebar.css` turns that off for these menus, since the items draw their own focus state.

## Status panel

The right-hand status panel's sections (environment, context, output, changes, background) are one `Accordion` from `@heroui/react` (`ui/chat/status-panel.tsx`), on a single surface below the subagents and sources panel. `STATUS_SECTIONS` in `app.js` lists each section's icon, title, summary and body. Summaries are data (`{text, tone, busy, added, deleted}`), so the same functions feed the component and the native fallback (`summaryNodes()`).

- Section bodies are still drawn by the controller, as in Settings: the component hands one host element per section to `onPanel`, and `fillStatus()` redraws the expanded sections on every event. A collapsed section keeps its last content, so the closing animation never shows an empty panel. Expanded sections are stored in `cw_sections` as before.
- The hosts keep the old class names (`.sp`, `.sp-body`), so existing probes and controller code that look for them still work.
- Before `chat-ui.js` has loaded, `nativeStatus()` draws the previous collapsible sections.

## Changes, files and history drawer

The drawer is the `Drawer` from `@heroui/react` placed on the right (`ui/chat/workspace-drawer.tsx`), with `Tabs` (secondary) for Changes, Files and History. `openDrawer()`, `renderDrawer()` and `closeDrawer()` keep their signatures. The selected view is drawn by `drawChanges()`, `loadFiles()` or `loadHistory()` into the host the component passes to `onPanel`. A new view gets a new host; redrawing the same view (after a commit, push or sync) draws into the existing host.

- Closing by button, Escape or backdrop only reports `onClose`; the controller clears its state. The application's global Escape handler ignores events a component has already handled (`defaultPrevented`), so Escape that closes the drawer, a menu or a dialog never also cancels the running task.
- The upstream CSS ships the drawer's slide without animation, so `ui/chat/status.css` animates it (`ws-drawer-in`/`ws-drawer-out`, off under reduced motion). React Aria waits for these animations before unmounting.
- Before `chat-ui.js` has loaded, the native drawer (`.drawer`, `.dtab`) is used.

## Application dialogs

The application's own dialogs are HeroUI `Modal`s: account, personal configuration, Skills, MCP connections, network secrets, discovery previews, the environment editor, the repository manager, new workspace and the entry-point dialogs. They are still built in `app.js` (and `entry-ui.js`) as `<dialog>` elements with their own handlers. `showDialog(dialog)` replaces `showModal()`: it sets the `open` attribute, so the element is open but not modal and stays in the page's layer, and hands it to the Modal host (`ui/chat/modal-host.tsx`).

- The Modal provides the backdrop, focus containment and restoration, scroll lock and Escape. Several dialogs stack in the order they were opened, and `askConfirm()` and `askText()` stack above them.
- `dialog.close()` works as before: the `close` event removes the dialog from the host. Escape first dispatches a cancelable `cancel` event on the dialog, as a native modal would, so `preventDefault()` keeps it open (new workspace does this while it is creating). Clicking the backdrop does nothing, as with the native dialogs.
- As `showModal()` would, the host focuses the element marked `autofocus`, else the first focusable one. The host renders synchronously, so code after `showDialog()` can focus or measure inside the dialog.
- Selectors and checks on `dialog`, `dialog[open]` and the dialogs' classes keep working. `modal.css` turns the element into the Modal's surface: one surface, sans-serif heading, no rule under the header.
- Before `chat-ui.js` has loaded, `showDialog()` falls back to `showModal()`. The native search palette (the fallback for `Command`) and the file workspace stay native modal dialogs.

## Form controls

Selects, checkboxes and the new-workspace source switch in the dialogs and in Settings are HeroUI `Select` (with `ListBox`), `Checkbox` and the Pro `Segment` (`ui/chat/fields.tsx`). This covers personal configuration (tool choices, default environment), skill discovery (source), MCP connections (secret, tool permissions), the repository manager (access, primary), new workspace (source, repository choices) and the entry-point editor.

- `selectField()`, `checkField()` and `segmentField()` in `app.js` return a host element that the controller uses as it used the native control. `value`, `checked`, `disabled` and `options` can be read and set, `setOptions()` replaces a select's options, and `click()` toggles a checkbox. When the user changes the control, the host fires `input` and `change`, so existing listeners, `onchange` and the tests keep working. The component's own inner `input` and `change` events stop at the host.
- As with a native `<select>`, a select without a valid value takes its first enabled option, and setting an unknown value clears the selection. An empty-string option value travels under a sentinel key, because React Aria keys cannot be empty.
- Each field is its own React root. It mounts and updates in a microtask, all pending fields in one `flushSync`. Controller code often builds controls while a React island is committing (Settings, status and drawer panels are drawn from layout effects), where React cannot render synchronously; the microtask runs once that commit is done and before the next paint.
- Closing a hosted dialog unmounts the fields inside it, so an open select popover never outlives its dialog. Fields elsewhere are not registered and are collected with their elements. A tool-choice row hidden by search is detached and may come back, so a detached field is not treated as dead.
- Code that disables every control in a form must set `disabled` on the field hosts (`.hero-field`) and leave the elements inside them alone; React would not undo a direct change to its own elements. New workspace does this while it is creating.
- The application's 16px `--radius` would make field and control radii too large, so the hosts set `--radius: .5rem` and `--field-radius` to the 10px of the text fields, as the sidebar does. Upstream fields draw no border (`--border-width-field` is 0) and rely on a shadow that the dark theme drops; the hosts set a 1px border so the controls stay visible in both themes. Styles are in `ui/chat/fields.css`; the Pro `segment.css` is included unscoped.
- Before `chat-ui.js` has loaded, the hosts draw the native controls instead.

Text inputs and text areas are still native elements with the application's `.field` style, which already matches HeroUI's field surface. The composer's task settings (sandbox, environment, network duration), the approval's duration, the session header's repository picker and the entry page's source filter are outside dialogs and still native.

## Icons

Application icons come from Gravity UI (`@gravity-ui/icons` 2.23.0, MIT), the set HeroUI uses inside its own components, so the icons we draw match the components' chevrons and indicators. `ui/icons.mjs` maps the application's icon names to Gravity icons and writes `internal/web/static/icons.js`, which is checked in with the licence. Regenerate it after changing the mapping:

```sh
node ui/icons.mjs /path/to/node_modules
```

Each icon is one path on a 16×16 grid, filled with `currentColor` (`.icon.gi`; 16px, 14px for `xs`). `icon()` in `app.js` and `Icon` in `ui/chat/icon.tsx` draw from the same table; React components that take an `icons` prop receive it too. The GitHub mark and provider brand marks stay separate. The logo keeps its own stroked drawing.

## Testing against the components

Browser tests drive the components with `element.click()`, which React Aria handles as a virtual press. Before `chat-ui.js` has loaded the native fallbacks are on screen, and while an island mounts there can be a frame with neither, so probes wait for whichever is present: `.side-pro-profile` or the native `.profile` for the account menu, `.topbar-menu [role="menuitem"]` or `.pop-item` for the header menu, and the AlertDialog (`[data-pro-component="AlertDialog"]`, buttons `[data-dialog-confirm]` and `[data-dialog-cancel]`) or a stubbed `window.confirm` for confirmations. In the drawer, the selected tab is `.dtab[aria-selected="true"]` (native: `.dtab.active`) and the close button is `[data-drawer-close]` (HeroUI's `Button` drops `title`). Wait for `[data-pro-component="Drawer"]` to disappear after closing before driving other menus. Selects and checkboxes are driven through their hosts, by class or data attribute (`.personal-environment`, `[data-reference]`, `[data-repository]`, `[data-repo-access]`): set `value` or `checked`, or call `click()`, as on the native control. Do not select them as `input` or `select` elements.

## Main conversation surface

See [Conversation design and migration](conversation-design.md) and the root `DESIGN.md` for the current architecture and visual rules. The task activity stream uses one React root with keyed public messages and compact tool groups. Plans and sub-agent summaries are React-rendered data, not native DOM slots. Groups keep expansion state across event updates, share one border, and progressively disclose large histories without an inner scroll viewport.

The editor uses real `PromptInput.TextArea`, `PromptInput.Send` and `PromptInput.Action` components. Its subscription model bridges task phase, input callbacks and the established send/steer/interrupt actions. Approval, status and existing model/settings/usage controls remain in explicit native slots inside the React surface. The controller preserves the textarea and user draft while streaming. The main conversation and composer share an 800px outer content axis, with responsive padding and the existing 44px topbar/terracotta tokens.

React Aria's fixed pressable touch rule is shipped in the same-origin stylesheet with its standard style ID already present on the link, avoiding runtime inline style injection without loosening CSP. The bundled Pro styles require the `tailwind-variants/config` entry under the installed v4 package. The real Chrome suite checks CSP, component ownership, geometry, live updates, IME, keyboard commands, approval flows and file downloads.

Public progress comes only from persisted `tool_status.progress_message`. Tool statuses coalesce by round, node, step and call index; legacy calls without an index match pending calls by tool and target in order. Late results update their original group. Reasoning deltas and planner goals never become public messages. Snapshot backfill merges by event ID without discarding newer live events or drafts. Internal pipeline details remain in one collapsed **Run details** disclosure after the answer.
