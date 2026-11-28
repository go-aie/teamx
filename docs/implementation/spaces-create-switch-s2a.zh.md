# 多空间创建、切换与独立运行（S2a）

日期：2026-11-24。状态：S2a 已实施并完成本记录所列验收。范围为[空间方案第 11.4 节](../WORKSPACE-AND-MULTI-USER-ANALYSIS-PLAN.zh.md#114-第二批实施单独立可用的双空间)的 S2a；S2b 归档／重开、多账号、强执行隔离及 Gordis P3 不在本轮范围。

## 使用与支持范围

个人部署的空间选择器可创建、改名和切换空间。切换打开带 SpaceID 的页面，各标签独立选择；离开页面不会停止原空间的通知、日历或 Agent 工作。新空间先单独设置模型，再使用自己的默认助理、聊天、模型、GitLab 连接与任务；不复制原空间的 Key、授权、历史、长期规则或自定义模板。

| 能力 | 原默认空间 | 新空间 |
| --- | --- | --- |
| 聊天、默认助理、模型、日历任务、工作目录 | 沿原配置及身份 | 私有装配，首次使用独立 setup |
| GitLab OAuth／连接／通知 | 原数据原地保留，原回调地址兼容 | 私有连接及 grant，回调、state 和 Cookie 路径绑定 Space |
| 飞书 CLI 授权与连接 | 保留已有能力 | 暂不开放；UI 和 HTTP 同时限制，禁止部署账号兜底 |
| Scion CLI 授权与连接 | 保留已有能力 | 2026-11-28 已补齐个人版空间私有接入，见下文 |
| 可选 Computer | 保留原 ComputerID 和既有状态 | 新的空间 ComputerID，私有凭据与卷，实际 Runtime 实例统一独占登记 |
| 执行隔离 | 可信个人部署的现有沙箱语义 | 同样不承诺 OS 读取隔离；不开放多用户 |

飞书的新空间限制适用于个人部署的 Linux、macOS。Scion 的个人新空间接入复用现有登录、核验、退出与通知／工具服务，凭据保存在各空间 Data 下的 `scion-profiles`，不复用默认空间账号；Linux 多用户的 Scion 限制继续保留。界面依据所选构建目录和服务端有效能力展示平台选项。未启用 Computer 的空间沿用无容器路径。

## 实现与唯一 owner

- `spaceServer` 管理空间目录和运行时引用，直接复用每个 `SpaceRuntime` 拥有的原 `roomManager`。Room、通知状态、操作回执、日历、Session 和结清仍由原对象负责，没有第二份业务 manager 或 Gordis 容器。
- 创建只接受客户端随机请求 ID 和显示名；ID 固定为 32 位小写十六进制，服务端决定 `<home>/spaces/<id>/{config.toml,data,teams}`。先用原持久 writer 原子发布可恢复登记，再初始化资源。重复请求返回同一身份，commit-unknown 围住后续写入；重启继续原登记，不盲目再创建。
- 原默认空间继续原地使用已有 Home／Data／Teams。`personal-space-registration.json` 保留首次登记证据，`spaces.json` 承载后续空间及版本化改名；新增空间不能改变原身份／根，拒绝路径别名、目录碰撞和任意客户端绝对路径。
- 新空间取得部署选择的 DSH launcher，并以空模型目录初始化。个人版网页创建时指定当前空间作为模型来源，后端核对同一 owner 后，用来源默认模型的参数和密钥进入普通首次模型验证；验证成功后在新空间独立保存配置与凭据，不复制验证回执或共享凭据引用。没有可用模型／密钥时回到普通设置表单。环境、连接、Computer 和个人模板不继承。2026-11-28 起，新空间从部署启动配置目录的 `defaults/teams/` 初始化团队，与重置共用模板来源；空间配置在模板复制完成后发布，重试补齐未完成的初始化，已有配置的空间不重新复制。分别构造模型／Computer 控制器、飞书请求队列、通知 Manager、SSE hub 和后台任务。setup 成功后先结清旧对象，再创建新运行对象和 epoch。
- HTTP 仍由单个实际 Server 提供。运行时的路由 listener 只发布原 HTTP handler，生命周期继续经过原 `Serve`／`Shutdown`；不创建第二个端口、HTTP 代理或另一套后台执行协议。删除原 setup 完成后重启整个入口的循环和可变默认 access 缓存；只替换已结清的目标空间。包装器保留 ResponseController、Flush 和 WebSocket Hijack；路由 listener 关闭新准入并等待已受理 handler，外层 Close 超时仍保留工厂 owner，避免 setup 在旧请求仍能启动 worker 时释放数据锁。
- 人类认证及持久会话只有入口一份；执行令牌、MCP 和 Room 身份保持每空间私有。SSE 和 Computer 长连接的周期复核使用共享认证权威，并保留本空间／成员／运行代际检查；退出登录撤销相关流。
- 关闭通过原 manager 冻结、取消和结清；控制面并行等待各空间。超时保留原对象、句柄及目录锁，重试作用于同一 owner；全部业务和持久写入结清后才释放部署／控制面锁。

目录占用在 `workspace-claims.json` 中持久登记，并在任何 manager 恢复前接入已有 Room 配置及 pending 目标；并发准入由同一个短锁协调。多用户版跨空间不允许相同、父子重叠的可写目录。2026-11-28 起，个人版允许跨空间共享公共目录及选择包含部署 Home 的祖先项目，空间私有目录仍受保护；当前规则见[工作目录参考](../reference/workspace.zh.md)。空间内原串行共享规则仍由原 manager 决定。旧目录在原操作持久结清后释放占用；未知结果保守保留。Computer 实例分配沿同一控制面 writer 登记，不因页面切换或临时禁用自动转移给另一空间。

## 请求与页面作用域

- `GET /v1/spaces`：已授权用户的空间、运行状态、创建能力及平台支持视图。
- `POST /v1/spaces`：`{request_id, name, model_source_space_id?}`，受控创建；个人版网页自动传入当前空间 ID。来源必须属于同一用户，不能指定目标自身；多用户版继续从用户级 Gateway 凭据初始化。省略来源保留空模型设置流程。来源 ID 随创建登记持久化，未知结果重试仍使用原请求；不接受业务根、owner 或浏览器提供的模型密钥。
- `POST /v1/spaces/<id>`：`{name, expected_version}`，版本冲突拒绝覆盖。
- `/s/<id>/`：页面与执行 endpoint；例如 CLI 使用 `--endpoint http://127.0.0.1:8088/s/<id>`，模型及工具获得绑定空间的 endpoint 与执行凭据。
- `/v1/spaces/<id>/<业务路径>`：显式业务 API。旧无空间路径继续只指向已登记默认空间。

每份页面固定一个 Space，切换使用完整页面导航。HTTP、SSE、图片、附件、下载、Computer viewer 及 WebSocket 均固定作用域；模块状态、Room 缓存、草稿、setup 草稿和浏览器存储不会随其他标签的选择变化。默认空间独自接入旧 UI 存储；新空间不能读取旧默认草稿。创建请求在浏览器保留原 ID／正文，未知结果不生成新的自动重试。单 Room 兼容模式不展示多空间创建入口。

自动模型初始化复用 `firstModelSetup.submit`、普通 Probe 和模型发布。失败／中断保留既有设置页的原因、预填值与重试入口；服务重启不反复自动验证已有失败操作。验证完成后各空间独立修改模型，重置也不重新导入来源模型。已有空间不会因为升级自动指定来源；无需迁移旧登记。

## 验证与证据

- 完整 Room 包回归通过：`go test ./internal/room -count=1`，516.780 秒。后续增加的故障、长任务、超时、作用域及流授权检查另做定向验证，不将旧快照的全包结果当作最终所有组合的全仓验收。
- 实际双 manager／通知 Manager：独立 setup、模型和 Computer 身份、改名版本围栏、错误空间／跨空间执行令牌及 MCP 拒绝、独立 SSE hello、共享登录与撤销。
- 默认空间缺模型／启动失败／执行长任务时另一空间继续使用；控制面的健康入口和共享静态资源不依赖默认 manager 成功启动；创建的未知发布恢复同一 ID；关闭超时保留原 owner 和锁，原对象重试结清后释放。
- 受控通知来源经过实际 manager、来源持久化和 Room Inbox：两空间使用同名来源／订阅，来源确认失败后重启继续原 pending，不串空间、不重复交付。
- Chromium：实际创建、独立 setup、改名、A→B→A→B、双标签选择和草稿隔离，迟到的 A 空间列表响应不能覆盖 B 页面，页面异常为空。
- 安装版 DSH／真实模型：`TestSpacesInstalledDSHRecovery` 通过，两空间各自配置模型、完成两轮独立对话，正常停服重启保持各自原 DSH SessionID，并更新空间 epoch。模型为 `deepseek-v4.1-flash-1017`，验收读取历史验收源的描述／Key，所有业务写入落在新建的 `.local/spaces-real-20261124-02`，没有外部平台写操作。见脱敏证据（本地验收文件 `spaces-create-switch-s2a-evidence.json`）。

- Computer：`TestSpacesRealComputerAssignmentAndStreams` 通过。macOS 上使用固定发行镜像创建两个全新的 Linux AIO Runtime；ComputerID、容器、卷及实例 ID 均不同，实际实例有持久空间分配，跨空间重复分配和 viewer 访问被拒。B 的真实 Chromium screen 经 `/s/<id>/.../websockify` 完成 RFB 握手，并在长连接周期授权复核后继续通信。通过路径为 `.local/spaces-computer-real-20261124-05`，47.19 秒；所有验收容器及卷已清理，未操作日常 Runtime。早期夹具缺少产品 Provider 注册以及冷启动预算不足的运行均不计通过。
- 867 项前端回归全部通过；后续回调返回及默认草稿接入另做针对性回归。定向 race 覆盖双空间、通知 pending、长任务、关闭超时、环境授权与原关闭协议。Agent／DSH／平台／Web 相关包、`go vet ./internal/room ./internal/web`、全仓 e2e 编译和 `git diff --check` 通过。

真实外部平台授权／发送没有在本轮重跑，GitLab 的 scoped callback、Cookie、返回目标与私有 registry 组合做针对性检查；不将本地受控来源冒充真实平台验收。

## 停止点

本轮只交付创建、改名、切换和独立使用。空间列表没有归档／重开／删除入口；不声明完整 S2、多用户、强执行隔离、共享空间或 Gordis P3 完成。后续 S2b 复用同一登记和原结清协议，再增加归档／新实例恢复。

后续更新（2026-11-24）：S2b 已完成，归档／新实例恢复及完整 S2 的当前出口见 [S2b 记录](spaces-archive-restore-s2b.zh.md)；以上保留 S2a 当时的范围与停止点。
