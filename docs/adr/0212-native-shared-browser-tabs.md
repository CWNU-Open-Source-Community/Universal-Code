# ADR 0212: Native shared browser tabs

Date: 2026-11-12

## Decision

The Windows desktop embeds independent WebView2 controllers inside its workspace.
The workspace has one tab strip, a navigation/address row, and a content region.
The content region shows either the application's new-tab page or the actual
native web page. Split view, full workspace view, and operating-system fullscreen
use the same container. A hidden native surface keeps its document and login.

External pages have neither the Wails bridge nor the application's API tokens.
Each chat owns a browser profile, separate from the application's renderer and
the user's regular browser. Tabs in a chat share cookies. A draft browser is
explicitly adopted by the newly created chat before its first turn is submitted.
The profile index migration is atomic and retries do not consume a later draft.
Recent pages on the new-tab screen are a bounded, session-local list of successful
native navigations; no sample visits or recommendations are manufactured.

The Agent attaches to the exact host-owned page. The attachment creates no new
browser context or substitute page. Both DOM/reference actions and coordinate
actions use the existing authorized browser tool path and that same page.
Coordinate actions validate the document epoch and current viewport. The native
Agent pointer is separate from the operator's system pointer and cannot receive
mouse input or keyboard focus.

Operator input stays enabled during Agent work. Clicking, typing, or changing
the displayed tab does not cancel the Agent. An attachment retains its stable
target tab when the operator views another tab. Navigation and resizing invalidate
old references/coordinates so a stale action must observe the current document.
Closing the target page or revoking Run authority invalidates its Agent lease.
Releasing that lease preserves the user-owned page and profile; its cleanup
receipt records detachment, not destruction of a browser process or profile.

Native login popups use the same WebView2 environment and the NewWindowRequested
deferral/NewWindow protocol. This preserves window.opener and the browser's own
navigation, POST, and window-closing behavior. The desktop registers the page
that the native host created instead of opening a second copy of the URL.

## Permissions and platform boundaries

The existing Run, tool-call, document, and network authorities remain mandatory
for Agent operations. Every WebView installs all-source WebResourceRequested
filters before its first navigation, including shared and service workers.
Only one Agent network lease can govern a profile at a time. Revocation aborts
pending requests; a paused Agent request cannot become an operator request.

WebView2 cannot reliably attribute an asynchronous HTTP request to a human input
versus an Agent input. During an Agent attachment, the profile's HTTP requests
therefore share the Run's origin and HTTP-write policy, including operator-triggered
requests. Existing WebSocket/WebRTC messages are not covered by the HTTP request
filter. This implementation does not provide per-message approval for those
transports. These limits must not be reported as complete network containment.

Windows runtimes without the required request-source filter interface can host
manual pages but reject Agent attachment. Non-Windows desktop builds retain
their existing browser behavior. Windows amd64 has native runtime verification;
Windows ARM64 has compilation/ABI coverage and still needs device verification.

## Lifecycle and compatibility

The desktop binding is `desktop_shared_browser.v1`, advertised by
`shared_browser_enabled`. Old renderers and unsupported hosts retain their
existing behavior. Surface updates carry monotonically increasing revisions,
CSS bounds, and the renderer's device scale; stale geometry cannot re-show a
hidden or replaced surface. HTML menus and modal overlays temporarily hide the
native child to respect Windows child-window stacking.

COM work is marshalled onto the Wails STA through an owned dispatcher window.
Shutdown first detaches Agent work and closes controllers while the message loop
is still running, then exits the application. Native pages and cursor overlays
also close when their parent window is destroyed.

Rollback retains browser profile directories and the profile index. It does not
delete login data or reinterpret existing Agent tool records. The browser_move
tool's durable registration and new migration preserve the existing browser
authority checks and historical migration checksums.

## Verification

The Windows integration test uses a hidden native parent window, the actual
WebView2 runtime, SQLite-backed workspace/chat ownership, and the desktop bridge.
It checks a redirect login with an HttpOnly cookie, attachment to that exact
authenticated page, DOM-reference clicks before and after another tab is selected,
login popups and opener callbacks, visit history, and draft profile adoption and
isolation. It runs without model calls or operating-system mouse input:

```powershell
$env:UC_NATIVE_BROWSER_PROBE='1'
go test -race ./internal/desktopbrowser ./internal/desktop -run 'TestNative|TestSharedBrowser' -count=1 -timeout 120s
```

React tests cover the shared tab strip, keyboard focus across tool and website
tabs, surface geometry, fullscreen restoration, and adoption before the first
turn. Headless layout screenshots exercise the application styles with a test
bridge. These screenshots do not establish native-window positioning. Interactive
foreground acceptance of the complete desktop app remains unperformed after the
operator stopped desktop input automation.
