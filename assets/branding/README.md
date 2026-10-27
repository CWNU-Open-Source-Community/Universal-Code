# Universal-Code Brand Mark

`traverse-board-mark.png` is the maintainer-approved canonical project icon:
a glass globe with a terminal prompt and a segmented navy ring on a soft white rounded tile.

- Canvas: 1254×1254 RGB PNG
- Background: opaque white with the supplied rounded-tile treatment
- SHA-256: `a97e71e5f373d5d48ce24e39360cd390be2beabfe731037ac114ec38f12407d6`
- Source treatment: preserved exactly as supplied; no generative redraw

Web, Windows, and macOS assets use a deterministic centered crop of the middle
60% of this master, followed by resizing, rounded-alpha masking and platform
container encoding. Removing the presentation padding makes the navy mark occupy
about 75% of each icon, including the 16px and 32px taskbar/favicon sizes. This
also applies to the checked-in 300×300 Partner Center listing icon. The master
and the artwork remain unchanged. Regenerate every tracked derivative with:

```powershell
pwsh ./scripts/generate-brand-assets.ps1
```
