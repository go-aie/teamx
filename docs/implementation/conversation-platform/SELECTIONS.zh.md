# 对话式平台接入：账号与群选择实施记录

日期：2026-11-11。对应方案 C6。账号／群选择、少量字段编辑、完整 Skill 及设置回流已实施；已通过真实浏览器＋飞书账号＋模型 Agent 的“复用已核验连接 → 选择账号 → 选择群 → 自动接续 → 保存并核对关注配置”验收。

首次真实验收使用一个已核验账号，没有重新执行 OAuth；后续已补充同账号真实 OAuth 组合验收，见文末。多账号、草稿竞争、重复提交、未知交付与恢复由本地测试覆盖；不将这些证据称为真实多账号或重新授权组合验收。原 C3 的授权证据、C5 的连接结果事件验证与本次选择结果事件验证分别保留。

## 已交付

- `platforms selection start/status/update/wait/cancel/notify/acknowledge` 提供固定账号／群表单及可恢复回执。原 `connect`、`chats`、`member` 命令继续负责实际连接、候选查询和配置，不增加新的配置权威或回合输入。
- 账号表单支持选择现有已核验连接，或填写新连接名称。候选包含连接名称、账号名称、稳定账号 ID 和连接版本；提交使用服务核验的名称与账号。群表单固定原账号与版本，支持搜索／分页、精确群 ID、多选，以及显式消息模式修改。
- 选择操作保存原真实 human 消息、完整 Room／Agent／Membership、固定业务输入、初始草稿、当前草稿及版本。Agent 的自然语言字段修改与 human 手动修改使用同一版本；`update` 替换完整草稿，旧版本冲突。初始输入用于幂等核对，后续编辑不改变操作身份。
- 仅原 human 可提交表单。服务在提交时核验账号版本、成员和精确群元数据；远程读取后再次检查草稿、成员、账号和过期时间。重复提交同一已接受版本返回原回执；提交后目标冻结，不能改草稿替换。表单有效期 24 小时；显式取消／过期不会执行配置。
- 对话与“设置 → 平台”展示同一选择、草稿和结果。组件按交互 ID 复用节点；新消息、轮询及输入焦点保留。手动输入在提交时先做版本化草稿保存，再提交该版本；其他标签页／Agent 已更新时保留本地输入并提示核对，必须明确载入最新草稿才替换。关闭设置不会取消选择，返回时重新读取候选。
- 选择提交只证明用户选择已保存。Agent 查询原选择与最新需求，复用 C2 的稳定配置操作、独立配置版本与回执；设置中已经完成的相同配置先核对再跳过。连接、通知、工具结果各自核验，不顺带开通工具、发送能力或其他接收范围。
- 选择结果复用现有可靠 Inbox 交付和调度：稳定 `platform-selection:<id>:<result-version>` 事件、持久化待投递、原 DeliveryID 重试、消费关联、精确版本处理确认。只提取现有通知投递与接续投影的私有公共函数，没有新增队列协议、调度分支或通用事件入口。
- 同回合有界 wait、Inbox 自动接续和用户发言主动继续都查同一回执。Skill 在连接处理进入后续群选择时确认已处理的连接结果，后续群选择独立订阅和确认；晚到事件不重放已经完成的配置或完成反馈。

## 代码落点与持久化

- `platform_selection.go`：选择服务、受控 Agent／human 入口、固定候选、版本和结果校验、通知适配。
- `platform_selection_cli.go`：CLI 客户端和有界等待。`platform_connect_cli.go` 只增加路由及发现入口。
- `platform_connect_interaction.go`：将选择引用与连接引用一起投影给原消息及设置。`web/app.js`、`web/style.css` 增加固定组件与稳定交互。
- `platform_connect_notifications.go`：提取连接和选择共用的投递／调度状态投影；两类结果分别校验和保存。`platform_connect_store.go` 启动时验证选择及其通知记录。
- `skills/platforms/SKILL.md`：完整接入、字段修改、三种接续、设置回流及晚到结果处理。

选择记录保存在 `platform-selections/<operation-id>.json`；通知策略、不可变事件、交付和处理回执保存在 `platform-selection-notifications/<operation-id>.json`。记录不保存凭据、临时授权链接、二维码或通用表单描述。连接和选择不能复用同一操作 ID，避免交互引用碰撞。原连接／成员配置记录、订阅权威及 Inbox 格式不迁移。

## 本地验证

```sh
go test ./... -count=1 -timeout=8m
go test -race -tags=integration ./internal/room \
  -run '^TestPlatform(Selection|Connect|Member)|^TestLark(Setup|Auth|Login|Logout)' \
  -count=1 -timeout=180s
node --test --test-reporter=spec internal/web/tests/web_*test.mjs
TEAMX_SELECTION_BROWSER=1 go test -tags=integration ./internal/room \
  -run '^TestPlatformSelectionConversationAndMemberConfiguration$' \
  -count=1 -v -timeout=100s
git diff --check
```

Chromium 测试通过真实 TeamX HTTP／CLI，飞书 CLI 为模拟适配器。覆盖账号选择、同名群分页、消息刷新保留节点和焦点、完整设置返回、多标签页过期提交、刷新恢复及 390px 窄屏；随后由实际 CLI 保存所选群，并核对订阅目标、起点及不启用工具。截图位于 `.local/acceptance/conversation-platform-selection/`。

专项覆盖多账号精确身份、服务读取真实用户依据、自然语言／手动草稿竞争、未知字段、重复提交、提交后目标冻结、账号版本改变、成员替换、过期／取消、重启恢复、通知交付确认前后写入中断与稳定序号去重，以及没有新聊天时通过普通 Inbox 进入新回合。原连接、成员配置及授权设置一并回归。消费确认测试按现有 `ErrBusy` 语义等待事务完成，不绕过既有批次或消费检查。

最终结果：全仓 Go 测试通过（Room 包 152.007 秒）；连接／选择／成员配置及原飞书授权 integration＋race 专项通过（36.425 秒），随后新增的远程核验期间草稿竞争专项也通过 race。前端全量 385 项通过，最后的表单样式及目标摘要调整通过 6 项选择交互回归。原连接 Chromium 验收也通过（10.716 秒）。

期间曾在工作区并行的私信／会话导航编辑尚未完成时遇到 `renderConversation` 缺失及两项旧断言失败；未覆盖这些并行改动，待最新工作区稳定后全量重跑通过。首次专项还发现测试消费确认可能遇到既有短暂 `ErrBusy`；测试按相同批次回执等待后通过定向重复验证及最终 race。上述均没有放宽产品调度或消费边界。

测试日志、截图与真实证据保存在本地 acceptance 目录，不纳入 Git。

## 真实验收

使用本次构建在 `127.0.0.1:8092` 启动独立实例，复用之前已核验的“罗彭”账号（连接名“验收飞书”）；8088 原实例未修改。用户明确指定“罗彭-qa 测试群”为验收目标。

1. 10:00，通过真实浏览器发送一条 human 指令，要求依次展示账号和群表单并异步接续。真实模型读取接入 Skill，发布账号选择。
2. 10:00，浏览器选择已有账号，提交后保存 `user_selection` 事件；没有发送新聊天消息。模型由 Inbox 进入下一回合，核对原账号及连接版本 2，发布该账号下的群选择。
3. 10:01，真实飞书搜索返回一个同名候选。浏览器选择并提交精确目标 `oc_b6ccfe7a898b71ed34ab8d1badd8d662`；服务按原账号重新读取群元数据，群类型为 `group`。没有发送新聊天消息。
4. 模型再次通过 Inbox 自动进入新回合，使用原 `source_seq=1` 提交一次成员通知配置并查询权威设置；10:01:35 起仅接收该群新消息，`groups.scope=specified`、`message_mode=all`、私聊关闭、订阅版本 1，目标为验收 Room 的 `assistant` 及其完整 Membership。
5. 持久化核对：两个选择结果均为 `completed/result_version=1`，Inbox 序号分别为 1、2，消费到 2，两条处理回执 `handled_version=1`；Agent 共完成三个回合，human 消息始终只有一条，成员配置操作始终只有一条，未建立平台工具绑定。飞书通知搜索已有成功进度，未出现失败。
6. 最终浏览器核对原消息下两张已提交回执、完整设置和刷新后的相同结果。证据位于 `.local/acceptance/conversation-platform-selection/real-20261111/`：`verification.json`、`account-selected.png`、`group-selected.png` 与最终页面截图。

首次真实通过的是已核验连接的账号／群选择及两次选择事件自动接续。没有重新进行 OAuth、切换第二个真实账号、注入真实并发故障或发送群消息验证提醒；这些不能由模拟测试替代。真实连接结果事件的自动接续随后补验，见下一节。验收完成后已停止 8092 独立实例，避免继续运行测试订阅；保存的数据与回执仍可复查，8088 原实例保持运行。


## 同账号真实 OAuth 组合验收

2026-11-11，用户要求继续使用“罗彭”账号验收。新建空数据目录，在 8092 启动当次源码构建的独立实例；使用已有飞书应用 `cli_a954531d70f99cdb`，创建新的 CLI profile 和 TeamX 连接。没有复制之前的连接记录。第二账号按用户要求暂不验。

1. 10:17，通过真实 Chromium 在「飞书 OAuth 完整接入验收」发送唯一一条 human 指令，要求新建连接、重新 OAuth、订阅结果并结束回合，后续通过 Inbox 继续选择和关注“罗彭-qa 测试群”。真实 Agent 读取接入 Skill，创建连接操作 `5a3700324bb91c8307506a3f18872aeb`，确认 `notify=inbox` 后结束第一回合。
2. 用户在官方页面完成应用配置。服务投影从 `waiting/application` 切换到 `waiting/authorization`；用户第一次反馈两步完成时，服务仍显示 OAuth 待完成，页面上存在新的账号授权入口。明确提示后，用户完成第二阶段的「授权飞书账号」。本次没有用聊天消息代替授权完成信号。
3. 10:19:35，服务完成账号核验和连接保存：应用及授权均为 `applied`，连接为 `connected`、版本 2，操作 `completed/result_version=1`；固定输入没有复用 `platform_id`，profile 与前次不同。保存的应用命名空间、租户和账号 ID 与原“罗彭”账号完全一致。
4. 连接完成事件经 Inbox 序号 1 唤醒 Agent 第二回合。Agent 查询原回执，发布群选择操作 `46e0eb87ebd3c6c7d8dc558e4aad70e6`，确认连接结果已处理后结束回合。
5. 浏览器从真实搜索结果中核对群名和精确 ID `oc_b6ccfe7a898b71ed34ab8d1badd8d662`，仅提交这个目标；群元数据在提交时重新核验。群选择事件经 Inbox 序号 2 唤醒第三回合。10:20:20 起保存群订阅，Agent 查询权威配置后反馈完成。
6. 持久化验证：一个连接操作、一个群选择、一个成员配置操作；所有步骤保留原 `source_seq=1`。human 消息始终只有一条，Agent 完成三个回合。两个事件均消费，处理版本均为 1；群订阅版本 1，仅指定群、消息模式 `all`、私聊关闭，投递到完整验收 Agent／Membership。Room 没有工具绑定文件。
7. 最终 Chromium 复查通过：连接成功与群选择回执、授权按钮和二维码收起、完整设置共用结果、页面刷新与窄屏无横向溢出，未捕获页面脚本错误。

证据保存在 `.local/acceptance/conversation-platform-oauth/real-20261111/`，包括无凭据的 `verification.json`、`baseline.json`、`completed.png`、`settings.png`、`mobile.png` 及隔离实例数据。验收结束后停止 8092 实例，保留新 profile 和结果便于复查；8088 原服务不作变更。

本次补齐同账号真实 OAuth、连接结果自动接续和后续群选择／配置的组合证据。没有验证第二真实账号、权限撤销、注入远端并发故障或收到新群消息后的业务提醒；没有把这些路径记为通过。本次工作仅执行真实验收并更新记录，没有修改产品代码。
