# User sources and subagent context

The composer accepts file selection, file drag/drop and pasted clipboard images. Plain text paste retains normal editor behavior. A new-conversation draft keeps bytes in memory until its session exists; an existing idle session uploads immediately. Files show pending/uploading/ready/error state, support explicit retry and removal from the current draft, and disable submission while incomplete. File-only submissions use a short default request.

The right inspector shows subagent counts derived from persisted `node_started(mode=sub_agent)` and `node_finished` events. Keys include task, round and node, so repeated node names do not merge. Success, failure and cancellation have distinct counts; a terminal parent with an incomplete child record is unconfirmed rather than falsely running or successful. Rows expose the actual latest tool activity.

Sources are the current user's files in the current session. The first three appear immediately, with a view-all disclosure. Images have same-origin raster previews; other files have type icons. Clicking a source opens its image preview or downloads it. The row's plus button reuses the existing source in the next task without uploading it again. Removing a draft attachment does not delete its stored source. History messages retain their referenced source IDs and file links after reload.

## Limits and capability boundaries

- 50 MiB per file, at most 10 source references and 200 MiB total per task. The browser negotiates these limits with Runtime and keeps a conservative legacy limit until capabilities are known.
- 500 canonical sources / 2 GiB per session workspace. Project-wide storage quotas belong to the later project catalog milestone.
- Uploading identical bytes with the same basename under the same session/user reuses the existing source ID.
- Attachments are submitted with a new task. Appending files to an already running task is not implemented; its add/reuse controls explain this state.
- UTF-8 text is readable through `context_read`. Other formats can be imported into the task's isolated `.agent-inputs` directory for installed sandbox parsers/OCR tools. Successful upload does not imply successful parsing.
- PNG/JPEG/static WebP can now use native image input when the updated Gateway shares the ObjectStore and an eligible model profile declares verified `image_input` support and its image token reserve. Unsupported/old gateways fail explicitly instead of silently dropping images. PDF/other formats remain on the sandbox parser path. The source reuse button explicitly includes an older image in a new task; automatic inheritance is not part of v1.

## API and storage

Browser endpoints, all authenticated and scoped through the existing session lookup:

- `POST /api/projects/{project}/sessions/{session}/sources`: multipart, one `file` field; returns `{source}`.
- `GET .../sources`: returns `{sources}`.
- `GET .../sources/{source}/content`: authenticated download.
- `GET .../sources/{source}/preview`: PNG/JPEG/GIF/WebP only.
- Task submission accepts `source_ids`; TaskRecord persists them, and source views expose `task_ids`.

A public source contains `id`, `name`, `mime_type`, `size_bytes`, `sha256`, `created_at`, `url` and optional `preview_url`. Object URIs and file bytes do not enter browser storage. Local image object URLs are revoked when removed, submitted or logged out. Signing out aborts pending uploads and changes a generation guard so an old batch cannot continue under the next account. Late source-list responses merge server identities instead of erasing freshly uploaded files. Background responses cannot repaint a settings page as a conversation.

Coding Web proxies bytes to Runtime's authenticated `POST /v1/uploads?workspace_id=...&filename=...`. Runtime now owns File/initial FileVersion metadata in the tenant-bound relational catalog. Coding Web only reads legacy JSON as migration input and records a per-owner migration receipt; new uploads never write that JSON. Runtime uses the configured ObjectStore and validates uploaded owner/workspace references before task admission. It pins a task-specific immutable manifest before the workflow is started. The Worker exposes only those exact references through `context_read` and `attachment_import`; no protobuf or schema change is required. Binary imports use normal sandbox lease/archive behavior and remain excluded from Git commits.

Legacy source IDs and object URIs remain resolvable through catalog aliases, so deduplication does not break historical task links. The application uses temporary-file spooling and streaming hashes for upload/download. Runtime bounds the existing byte-array ObjectStore transport to two full-file operations per instance; this is a logical payload limit, not a fixed RSS guarantee. Both download layers validate stored length/SHA; filename handling supports Chinese, spaces and URL-reserved characters, and rejects control characters. HTML/SVG cannot become inline executable previews. CSP adds `blob:` only to `img-src` for local previews; script/style policies are unchanged.

## Activation and verification

This feature requires ObjectStore configured in Runtime and the updated task-service, Python worker and coding-web to be running. It is not a frontend-only deploy. Native vision additionally requires the updated Model Gateway and a verified vision-capable model profile; see the Runtime `docs/reference/native-image-input.md` rollout guide. The local screenshot preview uses replay fixtures and is not a live model backend.

Coding Web browser tests cover actual multipart bytes, a 500×400 PNG thumbnail, clipboard behavior, failures/retries, file limits, source reuse, history replay, child status events and responsive layouts. Separate race checks cover a late source-list response and a 401 midway through a batch followed by another account login. Backend checks cover source reference isolation and filename/content roundtrips.

## M0.1 Runtime file authority

Authenticated Runtime endpoints:

- `GET /v1/files/capabilities?workspace_id=...`: advertised file/task/workspace limits.
- `GET /v1/files?workspace_id=...&limit=50&cursor=...`: stable newest-first pages (limit 1–100).
- `GET /v1/files/{source_id}?workspace_id=...`: canonical File and immutable head FileVersion; legacy aliases are accepted.
- `POST /v1/files/adopt?workspace_id=...`: `{object_uris:[...]}`, up to 500 verified legacy objects, atomically registered. The response preserves input order; it never trusts client-provided owner/MIME/hash.

The migration imports current-owner source metadata only after checking actual ObjectStore bytes and ownership. A failed batch does not expose partial catalog writes. Runtime outage is an explicit error rather than fallback to local authority. The source list projects `file_id`, `version_id`, and `alias_ids` without exposing ObjectStore URIs. Project/session capabilities are proxied at their corresponding `/sources/capabilities` routes.

Deploy Runtime migration 410 and the updated Task API before the new Coding Web proxy. Retain the old source JSON as migration input; it is no longer the write authority. Back up the relational database and object store together. Rollback must preserve the new catalog and aliases: do not roll back to local-only source writes after migration. Shared project grants, deletion/GC and multiple editable versions are not implemented by this initial catalog slice.
