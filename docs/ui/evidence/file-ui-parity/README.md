# File UI parity browser acceptance — 2026-11-10

Real headless Google Chrome loaded a fixed copy of the actual compiled React /
HeroUI Pro bundle. The five served assets were copied before the final run;
their SHA256 values match the workspace after that run. Every screenshot retains
an explicit **synthetic UI fixture** banner. The HTTP fixture models current
Runtime response shapes and records submitted bodies; it is not a claim of real
OCR, document edits, SQL execution or production deployment.

The final clean-profile run passed **28 checks**, including:

- PDF 11-page rejection before network; one-based selection and normalized OCR
  bbox; actual CDP pointer drag creates the expected normalized image crop.
- Office files with `application/zip` upload MIME still expose the correct UI.
- Notebook selected code-cell execution needs explicit confirmation and sends
  `cell_indices`, without executing on upload/read.
- Archive checkbox extraction sends only selected member paths.
- Multi-file SQL passes immutable input versions and each table's range/header.
- PPTX image replacement references an uploaded image version; L2 sends exactly
  the user-declared page/region, with no implicit expansion.
- Candidate publishing requires review; a 409 remains an error, not success.
- Read-only shared sources cannot edit/publish/manage grants.
- Source citation chooses the immutable source version and exact page;
  explicitly using a historical version keeps its selected version.
- Two parsed selections of the same file can both enter an index generation.
- Upload failure/retry preserves the file; restore explains private-only access.
- Resumed operations carry the exact cancellable consumer; expired and
  cancelled states stop polling; late results do not replace a different file.
- Desktop 1440px and phone 390px, light/dark and reduced motion; no horizontal
  page overflow and no page-level JavaScript errors.

`results.json` includes the check list and captured request payloads. Some fixture
responses (for example a publication conflict) are deliberately synthetic to
exercise the UI state. Actual backend probes are in the Runtime repository's
`docs/plan/evidence/file-ui-contracts/`.

The temporary replay harness is `/tmp/file-ui-parity/{server.py,browser.mjs,
browser-extra.mjs}`. Its server listens only on loopback. No new permanent Go or
pytest test file was added. The codebase's existing Go/Chromium regression suites
are run independently by the root integrator.


Final finite-audit recheck: all 28 checks were repeated from fresh Chrome
profiles against the recorded final static-asset snapshot after the “My
candidate changes” owner-only heading and explicit Notebook byte-limit
parentheses. No fixture assertion or expected request was removed. Header- and
stream-length Notebook preview limits additionally passed the separate ten-case
check in `../notebook-output-bounds/`. The full app controller's settings save
race and server routes remain covered separately by the final server suite.
