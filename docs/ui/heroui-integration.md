# HeroUI integration

CODE UI uses the official HeroUI v3.2.7 **precompiled CSS distribution**, included verbatim as `internal/web/static/heroui.css` with its Apache 2.0 license. It is served from the app origin and included by Go embed, so production needs neither a CDN nor a Node process. The original semantic DOM, keyboard behavior, event streams and API clients remain in place.

The DOM factory applies verified HeroUI `button`, `button--primary`, `button--outline`, `button--ghost`, `button--danger-soft`, size and icon-only BEM classes. `code-ui.css` maps application tokens and layout to the neutral surfaces, spacing, grouped navigation and compact forms in the official HeroUI Chat and Dashboard templates. The original user-supplied references were `heroui-v3-templates/web/chat` and `hp-components/codes/react`; component APIs were checked with the HeroUI Web MCP. This is a CSS integration, not a React or React Native conversion.

To refresh the vendored asset from an installed, pinned package:

```sh
node scripts/ui/vendor-heroui.mjs /path/to/node_modules/@heroui/styles
```

The updater verifies package name and version and prints the resulting SHA-256. `internal/web/static/index.html` loads upstream CSS, established application styles, then the small application integration stylesheet. Go's asset versioning fingerprints all three files together.

Settings are reached from the account menu. Personal configuration, Skills and MCP connections are tabs within Settings; the session composer retains its tool selection control. Legacy `#/skills` and `#/mcp` links resolve to their settings sections.

Repository creation uses a single picker with multi-select, independent branches and mount directories. A submitted selection is frozen during creation. Each attachment uses the existing async workspace operation and idempotency mechanism. If an attachment fails, the dialog retains the created workspace, reads its current repository list and retries only unfinished attachments. Existing root-layout workspaces remain supported.

Validation includes the existing Go/browser suite and `TestRepositoryMultiSelectAndManagementRoutesInBrowser` in both languages, checking the public request contract, read-only permissions, directory collisions, retained selection during search, frozen submission and retry behavior.
