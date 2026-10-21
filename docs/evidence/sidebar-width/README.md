# Sidebar width

The desktop sidebar edge can be dragged between 232 and 420 px. Its default remains 299 px. The current browser profile remembers the released width; a smaller window temporarily limits the sidebar to leave room for the conversation. Widening the window restores the preferred width. A click without a width change does not replace that preference.

The edge is keyboard accessible: Left/Right adjusts 12 px, Home/End selects the available minimum/maximum, and Enter or a double-click restores the default. Escape, pointer cancellation, losing pointer capture, or leaving the window cancels an unfinished drag. At 720 px and below, the existing overlay drawer retains its own width and the resize handle is unavailable.

## Actual browser verification

Windows Chrome, production Go API and SQLite, using the offline `workbenchjourney` provider. Both bundles displayed the same saved project and conversation. No screenshot content or styles were injected.

| Capture | Result |
| --- | --- |
| [Before](before-fixed.png) | Base `3dac0ecc`, 1440 × 1000; fixed 299 px sidebar truncates the sample names. |
| [After](after-expanded.png) | This change, 1440 × 1000; real pointer drag to 420 px reveals more of the same names. |
| [Dark theme](after-dark.png) | Existing dark theme selected through Settings, 420 px sidebar. |
| [Narrow screen](after-narrow.png) | 390 × 844; existing 286 px overlay, no resize handle. |

Observed interactions:

- A 121 px pointer move changed 299 px to 420 px during the drag and on release. The draft stayed intact.
- At an 800 px viewport, the sidebar displayed 380 px while retaining the saved 420 px. A click on the edge kept the stored preference; widening restored 420 px.
- Keyboard Left changed 420 to 408 px; Enter restored 299 px. End restored the available 420 px maximum.
- Navigating away and reconnecting restored both the saved width and the draft. Settings also uses the same sidebar width.
- The narrow drawer still opens and closes through its normal controls. Returning to a wide viewport restores the desktop width.
- A smaller conversation area exposed the existing user-message tooltip overflow already addressed separately by #441. This PR does not duplicate that patch.

68 related component, app and search tests passed. After fixing the no-drag preference case, the 5 resize tests passed again. Production build, protocol registration check against `3dac0ecc`, and `git diff --check` passed. The removed legacy resize component and CSS had no remaining consumers. Native packaged desktop and touch hardware were not tested.
