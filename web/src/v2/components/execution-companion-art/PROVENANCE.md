# Model Office character artwork

The eight WebP files in this directory are adaptations of the animation sheets in [hunmer/aboutme-pets, revision df864c0](https://github.com/hunmer/aboutme-pets/tree/df864c0c7db75857854740b301f6677e50d0b29c/pets).

Each source is `pets/<pack>/spritesheet/<action>.webp`. Universal Code selected twelve frames from each action, cropped the transparent margins, aligned the feet, resized each frame to 160 × 160 and encoded a 1920 × 160 WebP strip. Frame numbers below are zero-based and listed in playback order.

| Bundled strip | Pack | Original action | Source frames |
| --- | --- | --- | --- |
| `claude-idle.webp` | `claude` | 冲可可 | 59, 62, 65, 68, 71, 74, 77, 80, 83, 86, 89, 92 |
| `claude-working.webp` | `claude` | 写代码 | 31, 36, 41, 46, 51, 56, 61, 66, 71, 76, 81, 26 |
| `deepseek-idle.webp` | `dsh` | 摇扇纳凉 | 40, 45, 50, 55, 60, 65, 70, 75, 80, 85, 90, 35 |
| `deepseek-working.webp` | `dsh` | 轻快记录 | 61, 64, 67, 70, 73, 76, 79, 82, 85, 52, 55, 58 |
| `gemini-idle.webp` | `gemini` | 原地小憩沉眠 | 48, 52, 56, 60, 64, 68, 72, 76, 32, 36, 40, 44 |
| `gemini-working.webp` | `gemini` | 工作状态-清点归档 | 31, 34, 37, 40, 43, 46, 49, 52, 55, 58, 25, 28 |
| `gpt-idle.webp` | `gpt` | 超大伸懒腰 | 53, 58, 63, 68, 73, 78, 83, 88, 93, 38, 43, 48 |
| `gpt-working.webp` | `gpt` | 写代码 | 42, 47, 52, 57, 62, 67, 72, 77, 82, 30, 33, 37 |

## Artwork licenses

Claude, Gemini and GPT animations were created by hunmer from character illustrations by lokicorvus / CrossPet. CrossPet credits ZipZipPipe for GPT's white dragon design and cites ai-school-op and openpet-ai-girls as references for the community designs of Claude and Gemini. The six corresponding strips are shared under CC BY-NC-SA 4.0, requiring attribution, noncommercial use and the same license for shared adaptations. [Animation collection terms](https://github.com/hunmer/aboutme-pets/blob/df864c0c7db75857854740b301f6677e50d0b29c/README.md#来源与许可) · [CrossPet credits and license](https://github.com/lokicorvus/crosspet/blob/50691433a7da3a23fdba9a780f7e94378b377f4a/characters/LICENSE.md) · [CC BY-NC-SA 4.0](https://creativecommons.org/licenses/by-nc-sa/4.0/)

The DeepSeek blue maid animations were created by PC2005-cloud/dsh-pet and converted to WebP by hunmer. PC2005-cloud credits 上善无形 for the original 溟月 character and ZipZipPipe for the blue whale maid design. Their character design carries CC BY-NC-SA 4.0. Universal Code shares its frame adaptations under that license. The original animation terms permit noncommercial open-source use and require public derivatives to credit the original repository. [PC2005-cloud/dsh-pet](https://github.com/PC2005-cloud/dsh-pet) · [Original animation and design terms](https://github.com/PC2005-cloud/dsh-pet/blob/b78e3fcf92ac78cee569aed7cc27eeb2a8d51757/README.md#许可) · [Source pack notice](https://github.com/hunmer/aboutme-pets/blob/df864c0c7db75857854740b301f6677e50d0b29c/pets/dsh/NOTICE.txt)

The application code carries the repository's Apache-2.0 license. The distributed app includes the character credits and their artwork licenses in its [third-party notices](../../../../public/THIRD-PARTY-NOTICES.txt).
