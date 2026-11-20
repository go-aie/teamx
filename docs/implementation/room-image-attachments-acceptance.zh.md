# Room 图片附件：完整链路验收

对应 [Room 图片附件方案](../ROOM-IMAGE-ATTACHMENTS-PLAN.zh.md)阶段 4。用户于 2026-11-20 指示执行；全部自动化写入位于隔离 Room，没有迁移或修改日常 Room，没有平台业务写入。

**状态：自动化真实链路已通过；仍需用户提供 iOS Safari、Android Chrome 的实体手机结果，阶段 4 尚未全部完成。** 用户选择自行进行真机验证，检查单见下文。

## 真实链路与环境

新增 `TestRoomImagesRealBrowserAndModel` 和 `testdata/room_images_browser.cjs`。不是模拟 API：使用生产登录／身份中间件、单 Room／托管／默认助理私信 handler、真实 multipart 上传、原生消息提交、InputLog、附件对象、CSP 及正式图片 content 接口。服务关闭后从同一数据目录重新装配，保持 HTTP 地址和浏览器标签页草稿身份，再验证新 epoch 下预览。

测试运行于 macOS arm64、Go 1.27.1、Node 26.7.0、Chromium 149.0.7827.55。浏览器自动化驱动真实文件 input 的 filechooser，并通过 `navigator.clipboard.write` 配合系统粘贴快捷键产生实际 paste 事件；不使用合成 ClipboardEvent 代替该路径。桌面视口 1440，另检查 390、320 像素布局；它们不是实体手机。

三种入口均验证：

- 选图与粘贴各上传一张图片，有文字时继续添加，blob 缩略图经过实际 CSP，图文消息提交后两图均可预览、放大及下载。
- 发送纯图片消息时，在真实 handler 提交后丢弃 HTTP 回执；刷新后按原 epoch、消息 ID 和附件 ID 重试，实际请求相同，不再上传，不重复发布。
- 普通文字消息继续发送；已上传未发送的草稿经刷新可恢复。服务重启后，三条已发布消息及草稿都使用新 epoch 预览，无额外上传。
- 页面无脚本错误，桌面及窄屏无横向溢出。模拟 API 的菜单、取消、失败重试、乱序、切换等行为仍由阶段 3 浏览器脚本和前端回归覆盖。

单 Room 的**同一批浏览器消息和同一份附件对象**继续进入真实 DSH／模型验收。成员在发图前已加入，重启后恢复原生未读游标；成员调度采用受控 sink，以便将真实模型调用限定为验收所需的两个 Session。没有将该测试描述为完整自动调度器或生产部署验收。

模型为当前配置的 `deepseek-v4.1-flash-1017`，通过实际 DSH／ACP／HTTP MCP 及框架交付确认工作：

1. 测试图中的随机八位数字仅存在于图片像素，不出现在文件名、消息正文、模型指令或工具输入中；图像文件不放进 Agent 工作目录。
2. 第一个 Session 仅调用原 `inbox_read` 一次，读取三条消息和三张图片块。逐块 SHA-256 与浏览器上传后持久化的对象相同，并检查实际发向模型请求中的 `image_url` 块。系统剪贴板可能重编码 PNG，因此按各已存对象分别校验，不能要求全部编码字节等同于源 PNG。
3. 实际框架确认使原生 `ReadThrough` 从 0 到 3。第二个全新 Session 仅调用原 `history_read` 一次，同样从图片读出完整数字；读取前后 Room 快照一致，游标保持 3。新 Session 没有首个回答或图片上下文可用于回忆。
4. 不新增工具、下载调用、必填参数、图片提示词旁路或模型启动图片注入。模型只给出私有回答。

## 本次发现并修复

- 最外层身份中间件原先将所有 `/v1/` POST 当作 JSON，真实 multipart 上传被提前拒绝。上传改按查询参数校验 human caller，直接流入既有 Room handler；登录、同源、Agent 禁用和完整 Room／epoch／operator 校验仍执行。新增回归覆盖 admin、登录 cookie、本地免登录、大于 1 MiB 的原字节传递、伪造／重复／缺失 caller、错误 Origin、未登录和 Agent 凭据。
- 实际 Chromium 剪贴板的 items／files 可能为同一张图片创建不同时间戳的 File，原去重会重复上传。改为按稳定元数据和出现次数匹配两种表示，同时保留单个列表中的独立图片条目；补充不同时间戳及多条同名图片回归。
- 私信默认收起成员栏时，隐藏的调整手柄仍使页面横向多出 5 像素。收起时不显示手柄，三种入口均用实际布局断言复验。

## 保留、清空和回收

真实模型读取结束后，仍使用该隔离 Store 的真实文件和生产回收逻辑，将**测试时钟**推进 48 小时：未引用的浏览器草稿对象被回收，三张已发布引用保留；未结清的正式读取结果使真实 HTTP 清空返回 409；执行 seal 后清空返回 200，过期且已无引用的图片被回收。该证据不是等待 48 小时的长期运行测试。

实际文件读取期间的正式页／History pin、取消、成员移除、关闭、清空失败和发布不确定边界，复用并重跑阶段 1、2 的定向 integration／race。它们是真实本地文件和 MCP 编码的组件／集成证据；没有将受控取消夹具描述为真实远端模型的中途取消验收。

## 复现及证据

```sh
TEAMX_ROOM_IMAGES_REAL=1 TEAMX_ROOM_IMAGES_MODEL=1 \
TEAMX_INPUT_COMPARISON_CONFIG="$PWD/.teamx/config.toml" \
TEAMX_DSH_OUTPUT="$PWD/.local/room-images-real-new" \
go test -tags=e2e ./internal/room \
  -run '^TestRoomImagesRealBrowserAndModel$' -count=1 -timeout=10m -v
```

配置文件必须提供可用的 DSH 启动器、支持图片的默认模型及本地凭据引用；输出目录须为新的绝对路径。省略 `TEAMX_ROOM_IMAGES_MODEL=1` 只验证浏览器／存储，不代表模型验收。此入口不会加入默认 CI。

其他验证：

```sh
node --test internal/web/tests/web*_test.mjs
node scripts/room-image-ui-probe.mjs .local/room-images-ui-regression
go test -race -tags=integration ./internal/room ./internal/web \
  -run '^TestAccess|^TestRoomAttachment|^TestRoomImageMessagePreview$|^TestRoomsCatalog|^TestPlatformAttachment|^TestModelAttachment|^TestDSHMaterials|^TestLark.*Image|^TestHistoryMCP|^TestUnifiedRoomFormalImages|^TestManagedWeb|^TestWebRoomAssets|^TestWorkspaceAppHTTPBinding$|^TestAssets$|^TestWebAssetReferences$' \
  -count=1 -timeout=4m
go vet ./...
go test -tags=e2e ./... -run '^$'
git diff --check
```

前端回归 782 项；其他命令、真实链路结果及各层验收边界汇总于[机器记录](room-image-attachments-acceptance-evidence.json)。原始报告、截图、隔离 Room 和 DSH 状态保留于本地 `.local/room-images-real-*`，不将凭据、会话 token 或 Base64 图片写入文档。e2e 全仓命令仅编译；没有全跑全仓 integration／race。

## 用户真机验收

本次已为用户启动独立的 LAN 单 Room：`http://192.168.2.8:18089/`（地址随当前局域网而变，不是永久部署）。数据、启动配置和登录口令位于 `.local/room-images-phone/`，口令不写入文档；Mac 终端可用 `cat .local/room-images-phone/auth/admin-token` 查看。手机和运行 TeamX 的 Mac 需在同一可互通网络，使用该隔离 Room 即可，不需操作日常 Room。手机测试服务没有配置 Agent；模型证据来自上述自动隔离验收。

分别记录真实 iOS Safari、Android Chrome 的机型、系统和浏览器版本、访问主机，以及下列结果：

| 项目 | iOS Safari | Android Chrome |
| --- | --- | --- |
| 点击「+ → 添加图片」，从手机照片／文件选择并发送，刷新后可预览 | 待用户验证 | 待用户验证 |
| 打开系统选择器后取消，原有草稿不变 | 待用户验证 | 待用户验证 |
| 分两次追加图片，顺序和数量正确，图文可一起发送 | 待用户验证 | 待用户验证 |
| 切换到其他应用或系统选择器后返回，按实际上传状态继续 | 待用户验证 | 待用户验证 |
| 图片粘贴是否提供 File；不可用时选图替代路径仍完成发送 | 待用户验证 | 待用户验证 |

测试使用无敏感内容的 PNG／JPEG／WebP。若设备交付 HEIC／HEIF，应出现明确格式提示，再选择受支持的格式，不能以修改扩展名代替转换。粘贴不可用可以如实记录；系统选图路径必须可用。拿到用户反馈前，两个真机列保持待验，不用窄屏模拟或桌面成功代替。
