# Shared development services: acceptance on 2026-10-18

## Behavior

An explicit `command_runtime.start` with `workspace_mode: shared_service` can keep a development server alive while the agent edits files. The launch closes its checkpoint transaction; ordinary commands retain their exclusive lifetime boundary. The existing permission mode, actor identity and sandbox checks still apply.

Settings → Current task permissions → Development services saves a per-Thread `auto` or `exclusive` policy. Auto lets the agent select service mode for each appropriate start. Exclusive prevents new shared services and revokes existing shared services in that Thread. Running state comes from actual Jobs and their process-tree cleanup evidence. Saving a preference does not itself report successful process cleanup.

Source writes retain hash conflict checks. Checkpoints made while a shared service can write are marked as having incomplete write attribution. Restoring or forking a workspace requires all shared services for that physical directory to be terminal and reaped, including services owned by another Run.

## Checks performed

- Runner and gateway checks cover explicit/legacy workspace modes, stable old fingerprints and rejecting service mode on foreground/conditional execution.
- Application tests cover the Docker service's exact durable Job identity, current authority, generation changes, cleanup and lookup beyond 500 historical Jobs. These use the controlled Docker lifecycle transport; this acceptance does not claim a live Docker-daemon service session.
- `TestSharedCommandServiceSurvivesEditsAndKeepsCheckpointCursor` passed with a real Node process. The same PID served two actual file edits; a service-side write caused a stale edit hash to be rejected. Replay of a pending launch reused the original Job and PID. Cancellation followed by waiting for terminal/reaped state preserved the editing checkpoint and cleared the restore guard.
- Store checks passed for per-Thread defaults/persistence, revision conflicts and retries, upgrade from v211, launch rejection under exclusive policy, and physical-directory/unreaped queries. Schema history and the OpenAPI golden document checks passed.
- `TestThreadCommandServicePolicyHTTPControlAndNativeRevocation` passed after the final capability fix (1.715s). It exercises real HTTP service processes, read/control token separation, strict requests and stale revisions. Changing one Thread to exclusive and reconciling reaps only its shared service; another Thread's ordinary HTTP service still responds. Permission-setting control is required even when general Run control is enabled; read access remains available.
- Five affected frontend test files passed: **79 tests**. They cover policy receipts, conflicts without blind write replay, independent permission/execution capabilities, real-state presentation, cleanup-pending states, and settings integration.
- `npm run build` passed with the existing chunk-size advisory.

The first focused test pass exposed fixture mistakes: a historical Job skipped the required running transition; launch recovery was attempted after its tool result was already committed; and cancellation was assumed synchronous. The fixtures now follow the actual state and recovery contracts. The final Node test completed in 1.943s.

## Browser acceptance

The opt-in `TestThreadCommandServiceSettingsBrowserFixture` serves the built frontend, the actual HTTP API, SQLite and an owned native loopback process. The controlled model selects that fixed command; it does not provide simulated service status.

The browser flow used an independent Edge tab and the fixture's read/control credentials. With general Run control disabled but permission-setting and execution controls enabled, the page displayed an active service and enabled the appropriate controls. A real click on “Always exclusive” saved the policy and the page subsequently removed the service only after runtime cleanup. The narrow layout was checked at 390 × 844: the service section's `scrollWidth` equalled `clientWidth` (347 px), with readable wrapping and no horizontal overflow.

Screenshots were shown to the user before submission. The user approved the placement and layout: after task permissions and before the execution environment, with the policy separate from current running state.

Local screenshot artifacts:

- `D:/CodexProjects/service-settings-20261018/permissions-service-auto-desktop.jpg`
- `D:/CodexProjects/service-settings-20261018/permissions-service-exclusive-desktop.jpg`
- `D:/CodexProjects/service-settings-20261018/permissions-service-auto-narrow.jpg`

An earlier browser attempt was interrupted when the Codex host exited. Its old log ended during IAB tab closing/remounting; there is no crash stack proving the cause. That interrupted fixture is not counted as completed acceptance. The successful browser interaction was repeated in independent Edge; no IAB close was repeated.

## OCR delegate review

OCR selected the fixed service range over dependency `4abf35c6` and the uncommitted service changes, and resolved the review rules. The initial review accounted for 60 `(mode, path, status)` entries across 57 files: 55 reviewed and five generated outputs excluded from substantive review. Review depth means the changed implementation and necessary context; large existing files were not all read in full. The generated OpenAPI, frontend schema, store baseline and registry document received focused consistency checks.

Two concrete defects were fixed and rechecked:

1. An expected policy denial propagated through reconciliation as an error and could shut down the entire manager, including unrelated Jobs. Expected revocation now takes the per-Job stop path. The two-Thread real-process regression passed.
2. The settings UI used general Run control for both policy saving and service stopping, while the corresponding operations have separate capabilities. The UI/client now use permission-setting control for policy and execution control for stopping. The policy HTTP handler enforces that capability too. The final eight changed files were reviewed against the earlier snapshots; the 79 frontend tests and HTTP regression passed.

No unresolved finding remained in this scoped review. Detailed local OCR selection, rules, snapshots and coverage are under `D:/CodexProjects/service-settings-20261018/ocr-review/`. The durable repository summary is in `docs/reviews/shared-development-services-20261018.json`.
