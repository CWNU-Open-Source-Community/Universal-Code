# Confirmed plan selection — 2026-10-22

Real Chrome screenshots of the production frontend against an isolated Go API and deterministic local provider. The browser proposed two directions, selected direction two, confirmed it, refreshed, reconnected and reopened **View plan**.

The before build is commit `fc0935d6fc03ca7abccbd148ce52d26c2bc154f8`. The after build contains the implementation in `f26802e9c88492a23676359e6eb9feb18e932ab1`. Both read the same persisted server selection: proposal `plan-proposal-20261021181611-640a34e64617`, direction `2`. The API remained running while the frontend changed.

| Viewport | Before | After |
| --- | --- | --- |
| 1280 × 900 | [Neither direction selected](before-desktop.png) | [Direction two selected and marked 已确认](after-desktop.png) |
| 390 × 844 | [Neither direction selected](before-mobile.png) | [Read-only confirmation visible](after-mobile.png) |
