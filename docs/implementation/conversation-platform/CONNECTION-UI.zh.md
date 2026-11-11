# 对话式平台接入：最小对话连接实施记录

日期：2026-11-11。对应方案 C3。连接组件、本地回归与单账号真实浏览器／飞书／模型 Agent 组合验收已完成。C4 见 [Inbox 类型扩展](INBOX-EVENTS.zh.md)；群选择、字段编辑及 Inbox 自动接续继续留在 C5–C6。

## 已交付

- 连接操作持久化的 `interaction.id/kind`、原 human 消息序号和完整 Room／Agent／Membership 身份构成稳定引用。浏览器将固定组件附在该消息下，无需 Agent 先发第二条消息；start 返回后即可在 wait 期间操作。刷新／切房间后从操作记录恢复，不修改公共消息历史。
- 展示应用配置、账号授权、连接保存的独立进度与回执。活动会话提供官方授权按钮和同源二维码图片；两阶段授权自动切换。只有 `completed` 且 `connection.status=connected` 才展示已连接。
- 消息列表按消息序号复用节点，交互按操作 ID 复用节点；进度更新和新消息不销毁组件与焦点。时区、引用目标加载及 mention 背景仍分别更新。
- human 入口 `POST /v1/platforms/interactions/list|cancel` 校验当前服务代次、原房间及原用户；取消检查原操作的完整成员关联及版本，不接受替换目标。Agent 原有接口仍禁止浏览器调用，原 human 管理入口仍拒绝 Agent 调用。
- 对话和「设置 → 平台 → 当前对话的连接操作」使用相同投影、版本和取消服务。关闭设置、切房间、刷新、页面退出只停止观察，不取消由对话发起的连接。
- 重复取消返回原结果；取消响应丢失先读取原操作，不自动重放。原生 session cancel 不能绕过连接操作的持久化取消保护。成员移除／同名替换后不再提供授权材料，旧二维码也拒绝访问。原 human 仍可取消这个精确的旧操作，释放共享授权占用；结果不转交新成员。
- 接入 Skill 改为先提示使用对话组件，再尝试一次有界 wait，pending 后结束回合并等待用户主动继续；临时授权链接不写入聊天、规则或私有文件。没有加入自动唤醒。

## 本地验证

```sh
go test ./... -timeout=3m
node --test internal/room/web*test.mjs
go test -race -tags=integration ./internal/room \
  -run '^TestPlatformConnect|^TestLark(Setup|Auth|Login|Logout)|^TestPlatformsUpgrade|^TestManagedWeb(Bootstrap|Catalog)|^TestWebRoom' \
  -count=1 -timeout=180s
TEAMX_CONNECT_BROWSER=1 go test -tags=integration ./internal/room \
  -run '^TestPlatformConnectConversationBrowser$' -count=1 -v -timeout=120s
```

浏览器测试需要本机 Node 可解析 `playwright` 并已安装 Chromium；可通过 `NODE_PATH` 指定已有安装。可选 `TEAMX_CONNECT_BROWSER_ARTIFACTS` 指定截图目录。

结果：全仓普通 Go 测试、374 项前端测试、连接／原授权／升级／Web 入口专项 race 测试通过。真实 Chromium 使用实际 TeamX HTTP 服务、真实 start/wait CLI 子进程与模拟官方飞书 CLI，通过两阶段授权按钮、同源二维码图片加载、wait 期间操作、组件／焦点保留、页面刷新、切房间、关闭设置、结果保存与 390px 窄屏检查。本次模拟 CLI 的二维码是合成 PNG；图片链路通过不代表真实扫码通过，也不代表真实飞书或真实模型 Agent 验收通过。

本地浏览器截图保存在 `.local/acceptance/conversation-platform-connect/`：`application-authorization.png`、`account-authorization.png`、`settings.png`、`connected.png`、`mobile.png`。真实验收数据与配置位于其 `real/` 子目录，不纳入 Git。

扩大 race 回归另发现既有失败 `TestManagedWebUIEndToEnd`：`web_management_e2e.mjs:29` 引用已经不存在的 `message-count` 元素。独立、无本次改动的 HEAD `62795a4` 工作树运行同一测试也失败；本次未修改该旧场景。

## 真实验收

使用本次构建在 `127.0.0.1:8091` 启动独立实例，复用本机模型与团队配置，创建「飞书连接验收」Room；原实例 `127.0.0.1:8088` 继续运行。首次启动因未注入 `OPENAI_API_KEY` 无法开启模型，在进程中从本机 `.env` 显式注入该变量后重新启动，Agent 启动成功。凭据未写入报告或输出。

2026-11-11（北京时间）的实际链路：

1. 08:53，通过真实浏览器发送简短 human 消息「连接飞书，名称为验收飞书」。真实模型 Agent 读取接入 Skill、发起连接，并提示使用消息中的授权组件。
2. 用户完成应用配置，09:00 在 Room 发送「已授权，请继续」。Agent 查询原操作，准确区分应用已创建与账号尚未授权，未提前报告连接成功。
3. 用户完成账号授权，09:02 再发送「已完成」。服务核验同一 profile／账号并保存连接，原操作返回 `completed`、`stage=complete`、`result_version=1`；应用与授权均为 `applied`，连接为 `connected`、版本 2。
4. Agent 于 09:02 查询同一操作并回复连接成功。原消息关联保持 `source_seq=1`；后续 human 消息只触发主动查询，未创建第二个连接操作。
5. 独立读取管理接口，核对已保存平台名称「验收飞书」、连接版本 2、账号验证 `valid`，与操作回执账号一致。该 Room 工具绑定和通知订阅均为 0；只连接平台的边界得到核实。
6. 真实 Chromium 再次读取完成页面，确认授权按钮／二维码收起、成功回执保留、Agent 已续接反馈；刷新页面及打开／关闭完整设置后仍显示同一回执，无页面脚本错误。操作文件只保存稳定交互引用，不包含临时授权链接或二维码地址。

真实证据保存在 `.local/acceptance/conversation-platform-connect/real/`：`verification.json` 为无凭据的回执核对记录，`connected-real.png`、`settings-real.png` 为真实页面截图，用户提供的完成截图另存为 `user-completion.png`。本次通过的是单账号连接与用户主动继续；同回合等待、稳定渲染、切房间、重复点击和异常路径的自动化证据见上方本地验证，未声称这些异常均经过真实飞书实测。没有实现或宣称 Inbox 自动接续、群选择及 C6 完整体验。
