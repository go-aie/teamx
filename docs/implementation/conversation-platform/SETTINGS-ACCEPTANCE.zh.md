# 对话式平台设置组合验收

日期：2026-11-12。对应[对话式平台设置方案](../../CONVERSATION-PLATFORM-SETTINGS-PLAN.zh.md) S3。

状态：验收入口已实现，模拟提供方的真实模型／Chromium 组合检查已通过；用户指定飞书“罗彭／罗彭-qa 测试群”和 GitLab `RussellLuo／product/agentichub/teamx-demo` 后，真实 GitLab 模型验收与真实飞书模型／浏览器组合均已通过。仅一个群且未指定人员，因此第二群切换及指定人员／同名私聊尚未真实验收，S3 不标为全量完成。

## 已实现的入口

- `TestConversationSettingsRealDSH`：真实已安装 DSH、当前配置模型、生产 TeamX CLI／HTTP／成员配置服务及 Chromium。`fixture` 使用模拟飞书 CLI；`lark` 使用显式指定的真实 CLI profile 和目标。
- `TestConversationGitLabSettingsRealDSH`：真实 DSH／模型／CLI／通知配置应用。`fixture` 使用既有模拟 OAuth／项目服务；`gitlab` 只复用明确选择的有效 access grant，远端传输仅允许同源 HTTPS GET，拒绝刷新、撤销和业务写入。
- `testdata/conversation_settings_browser.cjs`：读取同一次模型运行的权威配置检查点，验证完整设置中开关、范围、完整群／人员列表和独立提及条件，检查桌面／窄屏布局，再通过真实 UI 独立保存。

每条请求均来自 Room 的真实 human 消息。检查一个请求只产生一个已完成配置回执，核对完整成员、账号、原消息序号、稳定操作 ID、输入版本、实际结果版本及最终权威配置；重读同一原回执不得增加版本。拒绝／澄清场景检查配置和版本不变。通知修改不更改工具绑定、历史起点和轮询间隔，不新增通知草稿。

浏览器保存后，以旧版本提交一个独立的真实 human 请求，确认返回 409、不预留操作、不改变配置；随后恢复真实模型处理该请求，核对它读取新状态后完成修改并保留浏览器的独立变更。服务暂停用于固定这一检查次序，不把竞争时序当成验收结果。

所有运行使用新建 Room／成员和私有临时 Store。通知采集器不启动，不发送平台消息，也不修改原安装的接收配置。退出时关闭隔离服务并删除临时 Store；最终配置、原回执和 human 消息保留在私有报告中，报告不包含凭据。

## 本次证据与未完成门槛

模型为当前工作区配置的 `deepseek-v4.1-flash-1017`，未临时替换模型。

| 方案验收项 | 本次证据 | 真实平台门槛 |
| --- | --- | --- |
| 群移除、追加、完整替换 | 模拟提供方及指定真实群均通过实际 CLI 保存、原回执和版本核对 | 单个真实群已测；从一个真实群切到另一个仍未测 |
| 一句话组合群范围、私聊关闭、只 @我 | 模拟提供方及指定真实群均通过；一个配置操作，无新增卡片；工具与起点不变 | 本次单群组合已通过 |
| 指定私聊唯一目标、同名澄清、不可核验目标 | 模拟原生人员关系下真实模型通过；澄清展示可区分会话 ID；错误类型不改配置 | 指定真实人员后重跑；同名必须真实存在，不能伪造目录 |
| GitLab 待办多条件、活动项目／筛选 | 模拟提供方及真实 RussellLuo／项目 482 均通过；显式清空原因、另一来源不变、无待应用／错误状态 | 本次指定项目已通过 |
| 浏览器展示、独立保存、旧版本拒绝、模型再读 | 模拟及真实飞书提供方均在同一次模型运行中通过真实 Chromium；有桌面／窄屏截图 | 真实飞书已通过；GitLab 完整设置组合展示尚未专项执行 |
| 既有账号／群／项目选择和接续 | Chromium 既有账号／群及项目卡片回归通过；自动接续保留现有服务测试证据 | 尚无真实模型＋真实账号／群／项目选择提交后自动接续的完整组合证据 |

本地私有证据目录：

- `.local/conversation-settings-model-20261112-02/`：首次飞书模型／浏览器组合通过；`report.json` 含原消息、原操作及最终配置，另有 `browser-report.json`、桌面／窄屏截图。
- `.local/conversation-settings-model-20261112-03/`：加强浏览器冲突后的原回执和版本断言后复跑通过。
- `.local/conversation-settings-model-20261112-04/`：补齐唯一／同名候选前置核验及澄清至少展示两个真实候选 ID 后复跑通过，作为最新飞书模型／浏览器证据。
- `.local/conversation-settings-gitlab-model-20261112-03/`：GitLab 三项真实模型场景通过。
- `.local/conversation-settings-gitlab-model-20261112-04/`：统一 fixture／真实提供方入口后复跑通过。

首次模型启动因未提供模型环境文件而终止，未调用模型或配置平台；补充工作区已有环境文件后通过。GitLab fixture 初始化曾使用不支持的 `issue` 类型，模型运行前被拒绝；改为产品支持的 `work_item` 后通过，不修改生产类型语义。

以上模拟提供方证据不能代替真实平台验收，也不证明平台事件进入 Inbox 或可见提醒送达。以下另记用户指定真实目标的结果。

## 用户指定真实目标验收

- 飞书：CLI 当前身份确认为“罗彭”；群名搜索唯一命中 `oc_b6ccfe7a898b71ed34ab8d1badd8d662`（罗彭-qa 测试群）。没有选择其他群或人员。
- GitLab：原生 `/api/v4/user` 确认为 `RussellLuo`、用户 ID `13`；项目完整路径 `product/agentichub/teamx-demo`，原生项目 ID `482`。复用当前安装的有效 access grant，仅同源 HTTPS GET，不刷新或撤销授权。
- `.local/conversation-settings-real-20261112/gitlab-01/report.json`：真实模型＋真实 GitLab 通过，三条请求分别将版本从 1→2、2→3、3→4；回执绑定原 human 消息、原操作及 GitLab 用户 ID 13。活动项目／筛选、待办多值筛选、原因显式清空均与权威配置一致，另一来源／工具／历史起点／轮询不变；应用无待处理或错误。
- `.local/conversation-settings-real-20261112/lark-01/`：实际完成最后一个群的移除／关闭、重新关注、完整指定范围＋只 @我，以及群 ID 不得作为人员私聊的拒绝检查。浏览器保存成功后，验收断言将私聊空列表的 `null` 与 `[]` 视为不同而失败；修正断言，仍严格比较非空列表中的原生人员会话 ID，不改变生产配置语义。
- `.local/conversation-settings-real-20261112/lark-02/`：上述断言修正后复跑通过。移除最后一群时版本 1→2 且关闭群／总接收，重新关注 2→3，组合修改 3→4；错误类型拒绝保持版本 4。Chromium 展示精确群 ID 与开关／条件，独立保存至版本 5；旧版本 4 写入返回 409，真实模型重读后保存版本 6，保留浏览器改动。`browser-report.json` 和桌面／窄屏截图保留证据；报告明确为部分验收。

真实 GitLab 运行总预算六分钟，复用的 access grant 至少还需七分钟有效期；超时后取消，不通过刷新共享授权延长验收。飞书只使用指定群，报告为部分验收；它覆盖最后一群关闭／恢复，不宣称完成从一个真实群切换到另一个真实群。未指定人员时完全跳过人员搜索、指定列表保存及同名场景，保留错误类型的无修改检查。

这些运行不启动隔离采集器、不发送平台消息，退出后关闭隔离服务并删除临时 Store。报告保留隔离配置及回执。额外对运行中原安装的六份通知配置／版本／起点及工具绑定做逻辑快照核对，未发现配置差异；采集进度属于持续运行状态，不以整份采集快照的文件哈希判定配置变化，结果见 `.local/conversation-settings-real-20261112/live-settings-check.json`。真实 GitLab 完整设置组合展示和真实选择提交后的模型自动接续仍未专项执行。

## 重跑模拟提供方＋真实模型／浏览器

从仓库根目录执行。输出必须是尚不存在的绝对路径；模型环境文件使用现有私有文件，不把密钥写入命令或报告。

```sh
TEAMX_SETTINGS_DSH=fixture \
TEAMX_SETTINGS_CONFIG="$PWD/.teamx/config.toml" \
TEAMX_DSH_ENV_FILE="$PWD/.env" \
TEAMX_SETTINGS_BROWSER=1 \
TEAMX_DSH_OUTPUT="$PWD/.local/conversation-settings-model-NEW" \
go test -tags=e2e ./internal/room \
  -run '^TestConversationSettingsRealDSH$' -count=1 -timeout=25m -v

TEAMX_SETTINGS_GITLAB_DSH=fixture \
TEAMX_SETTINGS_CONFIG="$PWD/.teamx/config.toml" \
TEAMX_DSH_ENV_FILE="$PWD/.env" \
TEAMX_DSH_OUTPUT="$PWD/.local/conversation-settings-gitlab-model-NEW" \
go test -tags=e2e ./internal/room \
  -run '^TestConversationGitLabSettingsRealDSH$' -count=1 -timeout=13m -v
```

## 真实提供方输入

飞书使用私有 JSON，通过 `TEAMX_SETTINGS_INPUT` 传入，并改用 `TEAMX_SETTINGS_DSH=lark`。其余模型／浏览器／输出参数同上。下列占位值必须替换成用户指定目标，不可直接运行：

```json
{
  "config": "/absolute/path/config.toml",
  "lark": {
    "version": 1,
    "executable": "/opt/homebrew/bin/lark-cli",
    "profile": "SELECTED_PROFILE",
    "brand": "feishu",
    "cli_version": "1.0.99"
  },
  "groups": ["oc_SELECTED_GROUP_1", "oc_SELECTED_GROUP_2"],
  "private_query": "唯一人员姓名",
  "private_chat": "oc_VERIFIED_PERSONNEL_CHAT",
  "ambiguous_query": "真实存在的同名人员姓名"
}
```

允许一个或两个指定群；两个群必须不同且可核验。单群仅验证最后一群移除／恢复及该群完整范围保存，报告为部分验收。`private_query` 和 `private_chat` 可以同时省略，此时不搜索或保存任何人员目标；若填写，姓名搜索必须唯一对应给定人员会话。同名场景需要至少两个可区分的真实同名候选。未给同名查询时报告为部分验收，不能宣称通过重名场景。错误类型拒绝使用已核验群 ID，不制造平台消息或补建人员会话。

GitLab 使用私有 JSON，通过 `TEAMX_SETTINGS_GITLAB_INPUT` 传入，并改用 `TEAMX_SETTINGS_GITLAB_DSH=gitlab`：

```json
{
  "config": "/absolute/path/config.toml",
  "origin": "https://SELECTED_GITLAB_ORIGIN",
  "oauth_snapshot": "/absolute/path/private-platform-connections.json",
  "connection_id": "SELECTED_EXACT_CONNECTION_ID",
  "project_id": 123,
  "project_path": "SELECTED_NAMESPACE/SELECTED_PROJECT"
}
```

复用已有 TeamX OAuth 快照读取机制：输入须为权限 0600 的原生快照；只复制指定连接／应用／授权到临时 Store，源文件不改。access grant 至少还剩七分钟有效期，真实运行总预算六分钟；传输禁止刷新／撤销，避免轮换原安装的授权。模型环境文件通过 `TEAMX_DSH_ENV_FILE` 指定；此输入不包含密钥。运行结束原安装的配置保留原样，隔离成员配置仅保留证据。

## 基础回归

以下已通过；真实模型专项与这些服务／浏览器回归分别报告：

```sh
go test ./... -count=1 -timeout=8m
go test -race -tags=integration ./internal/room \
  -run '^Test(PlatformMember|PlatformSelection|GitLabMember|GitLabProject|GitLabAccountSelection)' \
  -count=1 -timeout=4m
node --test --test-reporter=dot internal/web/tests/web_*test.mjs
TEAMX_SELECTION_BROWSER=1 TEAMX_CONNECT_BROWSER=1 \
go test -tags=integration ./internal/room \
  -run '^Test(PlatformSelectionConversationAndMemberConfiguration|GitLabProjectSelectionConversationBrowser)$' \
  -count=1 -timeout=3m
git diff --check
```

未实施 S4–S6；没有增加通知编辑卡片、共享草稿或对话轮询参数。
