# New-conversation image recovery notices

Base: `f1982fa88e207263e30a490216fd44e501ef1b84`. Windows Chromium, real Go API
and production Web assets. Display projection is enabled only on the new
conversation route with a confirmed workspace, where the draft's image-discard
controls are mounted.

A damaged image record previously appeared three times: a generic creation
warning, a global storage toast, and the actionable image row. The current
draft's known invalid image records now appear once, in that existing row.
Storage validation and send guards retain every failure. The projection only
filters `invalid` and invalid-format `read` warnings with the exact current
workspace/empty-thread image prefix. Other workspaces, other Threads, denied
storage reads, unassignable keys, unsaved drafts and operation failures remain
visible. Equal messages from different sources still notify observers.

Creation recovery checks its own journal before the global storage guard, so a
bad creation request has its own visible error even beside a bad image. The
real mixed-record journey leaves that request intact, discards only the image,
then attempts to send and verifies zero creation POSTs and the unchanged draft.

The journeys insert synthetic damaged journal records into actual localStorage;
they do not mock HTTP responses or use user images. Both new assertions fail
on the base build: two generic alerts remain, and the bad creation is masked by
the image's storage error. Both pass after the change. The existing stored-image
response-loss and single-record discard journeys also pass.

Screenshots are unmodified viewport captures. Desktop is 1280 × 800; narrow is
390 × 844. The mixed case contains both a bad image and a bad creation record.

| Case / theme / viewport | Before | After |
| --- | --- | --- |
| Image / Light / 1280 | [Before](../images/image-recovery-notices/before-single-light-1280.png) | [After](../images/image-recovery-notices/after-single-light-1280.png) |
| Image / Dark / 1280 | [Before](../images/image-recovery-notices/before-single-dark-1280.png) | [After](../images/image-recovery-notices/after-single-dark-1280.png) |
| Image / Light / 390 | [Before](../images/image-recovery-notices/before-single-light-390.png) | [After](../images/image-recovery-notices/after-single-light-390.png) |
| Image / Dark / 390 | [Before](../images/image-recovery-notices/before-single-dark-390.png) | [After](../images/image-recovery-notices/after-single-dark-390.png) |
| Mixed / Light / 1280 | [Before](../images/image-recovery-notices/before-mixed-light-1280.png) | [After](../images/image-recovery-notices/after-mixed-light-1280.png) |
| Mixed / Dark / 1280 | [Before](../images/image-recovery-notices/before-mixed-dark-1280.png) | [After](../images/image-recovery-notices/after-mixed-dark-1280.png) |
| Mixed / Light / 390 | [Before](../images/image-recovery-notices/before-mixed-light-390.png) | [After](../images/image-recovery-notices/after-mixed-light-390.png) |
| Mixed / Dark / 390 | [Before](../images/image-recovery-notices/before-mixed-dark-390.png) | [After](../images/image-recovery-notices/after-mixed-dark-390.png) |

Verification: production build/strict TypeScript; four focused test files
(69 tests); four real Go/Chromium journeys; and `git diff --check` passed.
Focused cases cover another scope with the same warning text, another workspace,
leaving the projected scope, denied reads, unsaved data, bad creation plus bad
image, retaining the second bad record after discard, and blocking creation.

OCR delegate selected app, recovery storage and creation recovery. Storage was
read in full; app around route/render ownership and creation around the journal,
load, operation and notice paths; changed tests and captured screenshots were
reviewed. The existing recovery registry and persisted formats are unchanged.
