# #334 扩展导入与 Catalog 信任验收

日期：2026-10-16。基线：`7fee2d48284dff41f2e340a5035e397d9c71c77f`。
本记录覆盖 #348 合并后剩余的固定 URL/Git 导入、Web 原生目录上传和旧 Skill Catalog 发布者信任。
协议与拒绝路径见 [ADR 0190](../adr/0190-extension-source-and-directory-import-controls.md)。

## 浏览器实操

Windows、Microsoft Edge，使用构建后的 Web 页面、真实 Go HTTP API、隔离的 SQLite 和包对象目录。
通过界面填写临时读/控制令牌后操作；服务使用既有离线验收 Provider，本轮扩展操作没有调用外部模型。

| 操作 | 实际结果 |
| --- | --- |
| 选择含 `SKILL.md` 和 `references/proof.txt` 的目录 | 界面显示 2 个文件、213 字节；确认前暂存按钮不可用。 |
| 确认上传目录 | 返回 `staged`；在扩展清单中显示真实原生名称和 `skills` 声明。 |
| 分别确认审查、选择 `skills` 并启用 | 状态依次为 `staged`、`approved`、`enabled`，代次依次为 1、2、3。读取 API 验证最终状态和能力。 |
| 新建 Catalog 发布者 | 使用本次生成的 Ed25519 公钥；确认前按钮不可用，确认后返回 `trusted`。 |
| 撤销后重新信任同一发布者 | 两次操作分别要求新的确认；状态为 `revoked` 后恢复 `trusted`，每次时间戳均变化。 |
| 刷新页面、重新连接并打开 Catalog | 仍显示同一发布者、`trusted` 和最后一次写入的精确时间戳。 |
| URL 导入输入 HTTP 地址及合法 SHA-256 | 点击获取后明确拒绝非 HTTPS 地址，保留输入供修改。 |

本机截图和只读回查保存在忽略目录 `output/playwright/issue334/`：
`native-upload-staged.png`、`native-upload-enabled.png`、`catalog-trust-restored.png`、`ui-readback.json`。
本次没有用第三方线上 Plugin 完成浏览器下载；固定来源的成功、拒绝和重放由以下真实 TLS/Git 集成测试覆盖。

## 来源与持久化检查

- HTTP 定向回归使用真实 TLS 服务，覆盖成功获取、SHA-256 不匹配、重定向拒绝、超过普通 HTTP 写时限的响应、取消和到期。
- 使用真实 SHA-1 和 SHA-256 Git 仓库，读取固定旧提交的 ZIP，验证不会跟随较新的 HEAD；生产入口继续拒绝本地路径和非 HTTPS URL。
- 来源重放核对本次 acquisition 与安装保留的 ZIP 摘要；不同来源复用保留原始来源，撤销后重放保持撤销，ZIP 字节不同则冲突。
- 目录测试覆盖普通资源和二进制字节、空文件、确定性打包、内容修订、路径/容量拒绝、数据库重开及撤销后重放。
- Catalog 真实 Store 测试覆盖事务内版本比较、CLI 竞争、审计和旧决定不能恢复撤销状态。
- 旧 CLI 的换行 Base64 公钥经 HTTP 投影规范化，原数据库内容、指纹和时间戳不改写；真实响应可由客户端读取并用于撤销。
- 旧 CLI 的 128 字符团队名（ASCII、中文、补充平面字符）撤销后均可重新授信；129 字符和无效 UTF-8 被拒绝。

## 已运行检查

- 原生目录、固定来源、Catalog 与能力声明的 Go 定向检查通过。
- `go test ./internal/plugins ./internal/httpapi -count=1` 全包检查通过（5.618 秒 / 271.657 秒）。
- OpenAPI 路由/请求/响应检查通过；两个生成快照已同步。
- 前端全量：221 个测试文件、2,165 项测试通过。随后 Catalog 兼容和布局增量的 4 项定向测试及生产构建再次通过。
- `go vet ./...` 通过。
- `go test -tags 'desktop,wv2runtime.error' -count=1 ./cmd/cyberagent-desktop ./internal/desktop ./internal/webui` 通过。
- 协议 registry 基线演进和 Surface 检查通过；本次沿用既有 Surface 层级。
- 提交前使用 OCR delegate 的文件选择和规则解析，由主代理与独立代理审阅实际生产改动。发现的响应超时、取消错误码、来源重放绑定、旧能力声明、SHA-256 Git 和 Catalog 历史兼容问题均已修复并回归。

Windows 原生打包应用的完整用户操作验收由 #340 单独跟踪；本记录的浏览器结果不代替该验收。
