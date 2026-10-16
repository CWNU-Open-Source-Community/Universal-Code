# ADR 0190: 固定来源、原生目录导入与 Skill Catalog 信任控制

Date: 2026-10-16
Status: Accepted for [Issue #334](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/334).

## 背景与决策

[ADR 0168](0168-extension-first-onboarding.md) 提供扩展暂存入口，
[ADR 0187](0187-skill-package-inventory-and-removal.md) 至
[0189](0189-installed-skill-selection-control.md) 补齐清单、发布者信任与按任务选择。
本切片补充操作者显式选择的 URL/Git Plugin ZIP、浏览器原生 Skill 目录上传，
以及旧 Skill Catalog 的公钥信任管理。Web/Desktop 继续通过既有 Go 服务控制，
不新增产品 Surface、执行器或 SQLite 迁移。

### 固定 URL/Git 来源

`POST /api/v1/extensions/plugins/import` 保留 `extension-control.v1` 请求和
`extension-onboarding.v1` 响应。本地 ZIP 的 `archive_base64` 与
`archive_sha256` 继续有效；新增可选 `source` 与 ZIP 字节互斥：

- HTTPS：`source={kind:"https",url}`，必须同时提供 `archive_sha256`。
- Git：`source={kind:"git",url,commit,archive_path?}`，commit 必须为完整小写
  40/64 位十六进制，缺省路径为 `plugin.zip`；可另提供 ZIP 的 SHA-256。

Go 复用 CLI 的固定来源获取器：仅固定 HTTPS 地址，拒绝凭据、query、fragment
与重定向；ZIP 最大 4 MiB。Git 临时 bare 仓库按 commit 长度选择 SHA-1/SHA-256，
核对取回的精确 commit 后只读取普通 ZIP blob；不检出、不运行 hooks、submodule
或构建。HTTPS 超时 30 秒，Git 获取上限 5 分钟；HTTP 只为来源分支设置
“获取上限 + 30 秒”的有界响应写期限，保留全局写超时和调用者取消。

来源响应必须包含本次已验证的
`acquisition={kind,url,sha256,commit?,archive_path?}`。客户端在新建和重放时均核对
地址、commit、规范 ZIP 路径与实际摘要，并要求摘要等于安装保留的 archive 摘要。
安装自身的 `source` 保留历史来源；它可能来自 CLI 或另一地址，不能用它代替本次获取
的绑定。同内容不同 ZIP 编码若命中旧 package fingerprint、但摘要不同，则返回冲突。
上传的旧响应仍可缺少 `acquisition`。

重试重新获取并校验固定来源，再复用已有 package-fingerprint 暂存重放。返回当前
安装状态和代次，撤销态不复活，也不自动批准或启用。丢失响应后可读取清单，或以原 pin
重试。非法来源、字节漂移与获取失败不暂存；获取中的取消和到期分别保留
`CANCELLED`、`DEADLINE_EXCEEDED`。提交后丢失响应按已有状态重放收敛。

### 原生 Skill 目录上传

沿用 `POST /api/v1/skills/packages/install`、控制 bearer、`Idempotency-Key` 和
`plugin-installation.v2`。请求为 `surface:"code"`、`confirm_untrusted:true` 及
`directory={root_name,files:[{path,content_base64}]}`；path 相对根目录，空文件显式用
空字符串编码。directory 与 archive/snapshot 输入互斥，客户端不能指定宿主目录、
文件模式或信任元数据。

Go 校验 1..1024 个文件、含隐式目录最多 1024 项、总解码内容至多 8 MiB、确定性
快照 ZIP 至多 4 MiB；根名最多 255 UTF-8 字节，路径最多 4096 字节，每段最多 255。
绝对路径、逃逸、Windows 保留名、根 `.git`、重复、大小写别名和文件/目录冲突均拒绝。
快照按路径排序，使用固定普通文件/目录模式，临时物化只供 Go 原生解析器读取；所有
上传内容保持惰性，临时目录在返回前清理。

包身份来自上传根名这一逻辑来源，内容变化只改变 revision。复用既有
`StageSnapshot`、actor/operation-key 绑定与 Store 原子重放：同 key 同输入返回现态，
换输入冲突；数据库重开后相同请求仍指向原安装，已撤销条目保持撤销。
入口只暂存，后续审查、启用与任务选择继续使用 ADR 0189 的精确组件和代次绑定。

### 旧 Skill Catalog 发布者信任

新增 `GET/POST /api/v1/skills/catalog/publishers` 与
`skill-catalog-control.v1`，登记在 `http-openapi-contract` family。读取位于读 bearer
之后；写入要求控制 bearer、服务能力启用、严格 JSON 和 `confirm:true`，actor 固定为
`http_operator`。

授予绑定公钥、SHA-256 指纹、显示名和可选团队；撤销只接受指纹和已观察的
`expected_updated_at`。首次授予显式传空时间，仅允许新建；对已有记录的授予、重新信任
或撤销必须匹配当前规范 UTC 时间戳。Store 在事务内核对并更新，时间严格前进；CLI
写入也推进同一版本，因此界面的陈旧决定返回冲突。信任记录与 Catalog 审计同事务
提交。丢失响应后刷新实际记录并重新确认，不增加第二套幂等账本。

Catalog 信任沿用 [ADR 0105](0105-signed-skill-packages-team-catalog-pinned-imports.md)
的 pin/enable 合同，与 Plugin 发布者信任分别管理。信任公钥不批准某个包的贡献，
也不改变已撤销安装的状态。

## 能力与输入信任

扩展清单新增可选 `onboarding.plugin_source_import` 与
`onboarding.native_skill_directory_import`。前者要求真实来源 controller 与扩展控制
开启，后者要求安装 controller 与 Skill 安装控制开启。旧 adapter 可继续提供 ZIP
上传而不实现来源获取；旧服务缺少新 flag 时，客户端按 false 处理。

请求只由操作者显式提交。上传字节、仓库内容、远端包和公钥显示名均不可信；Go 拥有
路径/大小/格式/摘要校验、信任写入和暂存重放。包内容不能自授 Tool、进程、网络或
凭证权限。来源获取的网络读取只服务本次固定来源，导入不启动包内代码。

## 备选方案与拒绝路径

- 浏览器提交宿主目录路径会把宿主文件读取范围交给请求者；采用相对路径和显式字节，
  Go 冻结并重新解析内容。
- 强制 Git 用户另填 ZIP SHA-256 会重复 commit pin；采用本次 acquisition 投影绑定
  请求与实际字节，同时允许操作者额外提供摘要。
- 用安装的历史 source 绑定来源重放会拒绝合法跨来源复用；保留历史 source，另返回
  已校验 acquisition，摘要不一致仍拒绝。
- Catalog 信任已有版本和审计，采用 observed-version CAS；陈旧点击或未知提交结果
  需要重新读取和确认，不猜测授权，也不自动重放授予。

未知字段、重复 JSON 字段、非 JSON、超限、混合来源、未确认、缺少控制授权与错误
方法均按既有 HTTP 边界拒绝。原生目录与固定 ZIP 的新安装均从 staged 开始；批准、启用和
任务选择仍是单独动作。本次不增加 Catalog pin/enable 的 GUI 写入口或模型管理工具。

## 验证与回滚

定向回归覆盖真实本地 TLS 来源、跳转/摘要拒绝、慢来源越过普通 HTTP 写时限、获取
中取消与到期、跨来源重放、撤销态与数据库重开；Git 用真实 SHA-1/SHA-256 仓库验证
固定旧 commit 的 ZIP blob。目录测试覆盖资源字节、确定性、路径/容量拒绝、惰性暂存
和重放。Catalog 测试覆盖真实事务 CAS、CLI 竞争、审计、过期版本拒绝与当前公钥绑定。
OpenAPI/生成类型、能力缺失时的界面状态与显式确认均保留定向检查。

协议注册仅追加标识并生成清单，运行 baseline evolution 与 Surface 检查；它不改变
运行权限。回滚可关闭新 capability、移除适配入口和表单，保留既有安装、快照对象、
发布者记录、审计与协议读取。没有数据库降级或历史 checksum 改写。
