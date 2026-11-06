# Image upload recovery — 2026-11-07

Base: `cfeee05a8724a23c673b0ed9971f2ef6e8de9674`.

An image upload could commit on the server and lose its response in transit.
The composer kept the text draft but lost the upload key on refresh, leaving no
way to recover the saved image through the interface.

The browser now saves the original upload key, name, SHA-256, byte size and MIME
type in the existing recovery store before POST. Reopening the same project and
draft reads that original request once and restores the matching image. An
unconfirmed request blocks sending; explicit retry only reads its original key.
Selecting the same pending image again cannot create another upload.

The read-only endpoint is
`GET /api/v1/workspaces/{workspace_id}/image-attachments/request`, authenticated
with the read bearer and original `Idempotency-Key`. It reuses
`GetWorkspaceImageByOperationKey`; there is no database migration. A currently
absent result remains pending because the upload can still arrive later.

## Reproduction and real screenshots

The fixture image is a self-created 64 × 32 blue PNG, 135 bytes, SHA-256
`e8447de9a1809d93135193d4b22eb2335ae03fe50aec177d24c4010088220336`.
No user image or external media was used, and no image was sent to a model.

On Windows Chromium, a loopback HTTP proxy forwards the real Go API response
headers and the first bytes of the actual successful upload response, then
closes the connection. Before refreshing, a separate authenticated HTTP GET
reads back the exact stored image ID, hash and byte size. Thus the upload is
already committed when the browser loses its response.

On the base build, the UI reports an invalid envelope; after reload it has zero
images and no original-upload recovery entry. On the patched build, reload
restores the original image and keeps the text draft. The real journey asserts
one POST, the exact original observation key, and a subsequent download of that
same stored image ID. A second reload preserves it without another POST.

The following unmodified viewport captures use the application's Light and Dark
settings. Desktop is 1280 × 800; narrow is 390 × 844. Production Web assets and
the real Go API are served through the same local proxy before and after.
There are no Playwright response mocks or fabricated receipts.

| Theme / viewport | Before refresh recovery | After refresh recovery |
| --- | --- | --- |
| Light / desktop | [Before](../images/image-upload-recovery/before-light-1280.png) | [After](../images/image-upload-recovery/after-light-1280.png) |
| Dark / desktop | [Before](../images/image-upload-recovery/before-dark-1280.png) | [After](../images/image-upload-recovery/after-dark-1280.png) |
| Light / narrow | [Before](../images/image-upload-recovery/before-light-390.png) | [After](../images/image-upload-recovery/after-light-390.png) |
| Dark / narrow | [Before](../images/image-upload-recovery/before-dark-390.png) | [After](../images/image-upload-recovery/after-dark-390.png) |

[The original transport failure](../images/image-upload-recovery/before-disconnected.png)
is retained separately. The manual fixture has no configured vision model, so
the existing model-capability message remains visible after the image recovers.
The image and draft are preserved independently of model availability.

## Verification

- `npm run build`: strict TypeScript and the production bundle passed.
- Focused Web tests for image input/recovery, API image attachments, mixed
  uploads, attachment settlement and file input: six files, 41 tests passed.
- `WORKBENCH_JOURNEY_SKIP_BUILD=1 npm run test:journeys -- --grep "stored image upload"`:
  the actual Go/HTTP/Chromium recovery journey passed. The same journey failed
  on the base build because no original-upload recovery was available.
- `go test ./internal/httpapi -run 'TestWorkspaceImage|TestOpenAPI' -count=1`:
  authenticated lookup, workspace separation, existing image behavior, generated
  contract and live route checks passed.
- `go test ./internal/store -run 'TestWorkspaceImageOperationObservation' -count=1`:
  the existing read-only original-key lookup/reopen check passed.
- `git diff --check`: passed.

Recovery tests also cover editing text during lookup, a late reply after
switching projects, an absent result without polling, mismatched image hashes,
and an unreadable journal that must keep sending blocked.

OCR delegate selected the production files and excluded tests by its default
path rules. `workspace_images.go` and `image-input.tsx` were read in full;
the generator, API facade/domain client and composer were read around their
changed paths. Generated diffs and the new tests were read in full. No finding
remains in the patch; unrelated repository files were not exhaustively read.

## Explicitly discard a damaged local image record

A damaged image-upload journal previously blocked sending permanently. An
invalid payload stopped the reload loop before it populated the pending list;
invalid JSON or an unknown recovery envelope was omitted by the storage reader.
Both paths preserved the original record but offered no removal control.

The image input now retains the actual scoped storage key for each invalid
record. A compact recovery row above the input offers **Discard record** and
states that the image cannot be recovered after discarding. Only that captured
Workspace/Thread record is removed. Other valid attempts remain visible and
resume their original lookup after the damaged records are resolved. Storage
access failures and undecodable storage keys are kept as errors without a
discard control. Unknown versions remain untouched until an explicit discard.

The new real Go/Chromium journey inserts a damaged record into actual
localStorage, along with a structurally valid pending record in another Thread.
Before the fix, the send button stays disabled and no removal action exists.
After the fix, explicit discard removes only the damaged key, preserves the
other record byte-for-byte and keeps the text through another reload. The user
can then send a text request that reads the real fixture README successfully.
The original HTTP response-loss image journey also passes alongside this test.
Both journeys explicitly open a new conversation after connecting, so retained
server conversations from an earlier journey do not change their entry screen.

These are unmodified viewport screenshots. Light/Dark desktop is 1280 × 800;
narrow is 390 × 844. The new journey verifies that the discard button is inside
the viewport and that its center is not covered by another element.

| Theme / viewport | Before | After |
| --- | --- | --- |
| Light / desktop | [Before](../images/image-upload-recovery/corrupt-record/before-light-1280.png) | [After](../images/image-upload-recovery/corrupt-record/after-light-1280.png) |
| Dark / desktop | [Before](../images/image-upload-recovery/corrupt-record/before-dark-1280.png) | [After](../images/image-upload-recovery/corrupt-record/after-dark-1280.png) |
| Light / narrow | [Before](../images/image-upload-recovery/corrupt-record/before-light-390.png) | [After](../images/image-upload-recovery/corrupt-record/after-light-390.png) |
| Dark / narrow | [Before](../images/image-upload-recovery/corrupt-record/before-dark-390.png) | [After](../images/image-upload-recovery/corrupt-record/after-dark-390.png) |

[After discarding](../images/image-upload-recovery/corrupt-record/after-discarded.png),
the text remains and sending is enabled.
The existing creation-recovery warning can remain until the next reload because
that separate reader checks the entire recovery store on mount. This screenshot
retains that observed warning; the journey verifies that reloading clears it
and that the preserved draft can be sent.

Verification: production build and strict TypeScript passed; focused recovery,
image-input, draft-context and workspace-tabs tests passed (five files, 71
tests); both real image-upload journeys passed. The protocol registry check,
including evolution against `cfeee05a8724a23c673b0ed9971f2ef6e8de9674`, passed.
Unit cases cover malformed payload/JSON, unknown envelope/payload versions,
misbound operation keys, Workspace/Thread isolation, and two damaged records
alongside a healthy pending image. OCR delegate resolved the changed production
files; image input and recovery storage were read in full, composer around the
move, registry and generated document by diff, and the changed tests in full.
