# File preview and structure controls — 2026-11-10

Real headless Chrome loaded the newly compiled React components under the same
same-origin script/style CSP. The HTTP fixture serves explicit synthetic source
metadata, an inert raster image, and saved Notebook JSON. Screenshots are marked
as an interaction fixture; these are UI/request tests, not parser/OCR accuracy
claims.

The final 390px device-emulated run passed 18 assertions:

- Reject 11 PDF pages before calling the API; `1,3` becomes `[0,2]`.
- Thumbnail selection follows actual artifact page coordinates.
- Keyboard percentage entry maps to normalized `[left,top,right,bottom]`.
- Regional OCR retains the selected PDF page and bbox.
- Displaying saved Notebook cells never executes them.
- Only code cells can be selected. Execution requires selected cells, explicit
  confirmation and write permission; the request uses `cell_indices`.
- Only explicitly checked archive file members are extracted; directories cannot
  be selected for extraction.
- Saved candidate outputs are fetched only on request and displayed as plain
  text. The fixture's `text/html` payload is not rendered or executed.
- The full candidate remains downloadable; previews over 2 MiB are rejected.
- Low-confidence OCR is visibly labeled with its actual confidence.
- Citation navigation forwards the exact version and original anchor.
- No page-level horizontal overflow at 390px.

A separate actual CDP mouse drag (not synthetic DOM pointer events) selected
approximately `[0.1,0.2,0.8,0.9]` on page 0, with body width exactly 390px. The
small pixel rounding is recorded in `pointer-390.json`. The earlier 1280px run
passed the initial 15 selection/citation/permission checks before the bounded
Notebook-output viewer was added; the final 390px run covers that viewer too.

Temporary fixture/build/probe sources remain in `/tmp/file-preview-probe/` and
are not production dependencies. Production inputs remain authorized through
Runtime's versioned content and artifact endpoints. Components never execute
Notebook HTML/JavaScript outputs or follow external preview URLs.
