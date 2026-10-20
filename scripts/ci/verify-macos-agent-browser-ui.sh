#!/usr/bin/env bash
set -euo pipefail

# Both images use native macOS Chrome, the real API, production renderer, Full
# confirmation UI, and this same offline test harness. Only test harness files
# are overlaid on the baseline; its production sources stay at the pinned base.
repo="$(git rev-parse --show-toplevel)"
before_revision="${1:?before product revision is required}"
head_revision="$(git rev-parse HEAD)"
evidence="$repo/build/darwin-chrome-evidence"
before_checkout="$repo/build/mac-browser-before"
[[ "$(uname -s)" == Darwin ]] || { echo 'Native macOS is required'; exit 1; }
[[ ! -e "$before_checkout" ]] || { echo 'Before checkout already exists'; exit 1; }
mkdir -p "$evidence"
if ! git cat-file -e "$before_revision^{commit}" 2>/dev/null; then
  git fetch --no-tags origin "$before_revision"
fi
git worktree add --detach "$before_checkout" "$before_revision"
cleanup() {
  if [[ -d "$before_checkout/output/playwright/workbench-journeys" ]]; then
    cp -R "$before_checkout/output/playwright/workbench-journeys" "$evidence/ui-before-debug"
  fi
  if [[ -d "$repo/output/playwright/workbench-journeys" ]]; then
    cp -R "$repo/output/playwright/workbench-journeys" "$evidence/ui-after-debug"
  fi
  git worktree remove --force "$before_checkout"
}
trap cleanup EXIT
cp "$repo/cmd/workbenchjourney/main.go" "$before_checkout/cmd/workbenchjourney/main.go"
cp "$repo/cmd/workbenchjourney/browser.go" "$before_checkout/cmd/workbenchjourney/browser.go"
cp "$repo/web/e2e/agent-browser.spec.ts" "$before_checkout/web/e2e/agent-browser.spec.ts"
export CGO_ENABLED=1 WORKBENCH_JOURNEY_BROWSER=1
export WORKBENCH_JOURNEY_BROWSER_CHANNEL=chrome
export WORKBENCH_JOURNEY_BROWSER_EVIDENCE="$evidence"
export WORKBENCH_JOURNEY_HARNESS_REVISION="$head_revision"
(
  cd "$before_checkout/web"
  npm ci
  export WORKBENCH_JOURNEY_BROWSER_BEFORE=1 WORKBENCH_JOURNEY_PRODUCT_REVISION="$before_revision"
  npx playwright test --config e2e/playwright.config.ts agent-browser.spec.ts
)
(
  cd "$repo/web"
  export WORKBENCH_JOURNEY_SKIP_BUILD=1 WORKBENCH_JOURNEY_PRODUCT_REVISION="$head_revision"
  npx playwright test --config e2e/playwright.config.ts agent-browser.spec.ts
)
