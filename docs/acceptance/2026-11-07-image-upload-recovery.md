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
