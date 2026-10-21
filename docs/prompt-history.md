# 当前聊天的输入历史

在空输入框按 ↑，可回填当前聊天最近一条已发送的文字。浏览历史时，把光标移到全文开头再按 ↑ 可取更早输入；在全文末尾按 ↓ 可取较新输入，越过最新一条后返回原来的空草稿。富文本与纯文本编辑器使用相同规则，回填保留原始 Markdown。

手工编辑后退出历史浏览。中文输入法组合、带修饰键的方向键、选区和正文中间的光标移动仍由编辑器处理。输入已有文字、附件或文件/MCP 引用时，不进入历史浏览；回填只复制文字，不重新加入旧附件。正在读取时若编辑、加入附件、切换聊天、移动选区或失去输入焦点，旧请求不会覆盖草稿。

历史索引逐页读取公开 transcript，并沿用公开消息投影排序与过滤；只包含已保存的用户输入，排除待发送、撤回、内部与临时消息。选中某条历史后再读取其完整已保存正文，因此不受当前聊天窗口页数及预览截断影响。索引或正文读取失败会显示错误并保留草稿，可以再次按 ↑ 重试。

## 真实页面验证

2026-10-22，Windows Edge，1280 × 900，生产前端、生产 API 和 SQLite。基线为 `96ba29bf`。通过 `cmd/workbenchjourney` 的离线模型提供者，从界面发送两条真实请求并读取 README；前后页面使用同一份已保存的对话。历史请求使用真实公开 API，截图未替换页面或接口内容。

| 富文本：修改前，空输入按 ↑ | 富文本：修改后，空输入按 ↑ |
| --- | --- |
| ![富文本修改前](images/prompt-history/rich-before.png) | ![富文本修改后](images/prompt-history/rich-after.png) |

| 纯文本：修改前，空输入按 ↑ | 纯文本：修改后，空输入按 ↑ |
| --- | --- |
| ![纯文本修改前](images/prompt-history/plain-before.png) | ![纯文本修改后](images/prompt-history/plain-after.png) |

浏览器实际验证了两种编辑器的首次回填、更早/更新浏览和返回空草稿，以及手工输入保留、多行光标与选区保护。富文本回填中的粗体正常渲染，切换到纯文本后仍保留 `**the proof**` 原文。以上为生产网页验证；未作为原生打包窗口验证。

## 自动检查

- `npm test -- src/v2/use-prompt-history.test.tsx src/v2/components/composer-history.test.tsx src/v2/components/markdown-input.test.tsx src/v2/components/composer.test.tsx src/v2/components/composer-attachment-settlement.test.tsx`：46 个测试通过。覆盖跨页顺序、长正文、公开消息过滤、分页/正文失败、延迟请求保护、富文本与纯文本、IME、选区、正文光标、手工编辑、附件/引用及现有发送行为。
- `npm run build`：通过。
- `go build -o output/playwright/prompt-history/cyberagent-after.exe ./cmd/cyberagent`：通过，用于真实 API 页面验证。
