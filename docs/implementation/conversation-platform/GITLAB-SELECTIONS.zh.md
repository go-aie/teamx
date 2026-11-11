# 对话式 GitLab 账号／项目选择与活动配置

日期：2026-11-11。对应方案 CG3。已实现 GitLab 固定账号／项目选择器、项目搜索分页、单项目活动及现有筛选配置；本地服务、CLI、模拟 DOM、真实 Chromium 和指定账号／项目的真实模型接续验收通过。首次应用配置回流仍属于 CG4。

## 能力与命令

```sh
# 一个实例内选择已核验账号；不代替新账号 OAuth 或首次应用配置。
teamx platforms selection start --provider gitlab --kind account \
  --origin https://gitlab.example --operation-id PICK_ID --source-seq SEQ --notify inbox

# 在精确账号下搜索项目，next_page 为 0 表示结束；否则作为下一次 page-token。
teamx platforms projects --platform-id ID --platform-version V --query NAME [--page-token PAGE]
teamx platforms selection start --provider gitlab --kind projects \
  --platform-id ID --platform-version V --query NAME \
  --operation-id PICK_ID --source-seq SEQ --notify inbox

# 自然语言修改替换完整草稿；版本与手动编辑共享，仅原 human 可提交。
teamx platforms selection update --operation-id PICK_ID --version DRAFT_VERSION \
  --project-id PROJECT_ID --project-path namespace/project [--event-type TYPE]

# 明确请求的单项目活动；已有待办、其他目标和工具设置保持。
teamx platforms member notifications --platform-id ID --platform-version V \
  --notification-version N --operation-id STEP_ID --source-seq SEQ \
  --events-enabled true --project-id PROJECT_ID --project-path namespace/project
```

活动筛选支持重复 `--event-type`：`note`、`push`、`merge_request.opened`、`merge_request.merged`、`merge_request.closed`；空字符串清除为全部活动。待办筛选支持重复 `--todo-reason`、`--todo-type`，空字符串将对应维度设为空；未传维度保持原值。表单提供保留筛选、全部活动和单类型快捷选项；自然语言草稿可保存多个类型，未主动修改时完整保留。

同一原请求、同一账号的所有通知修改收敛为一次配置操作，可组合 `--events-enabled`、`--todos-enabled`、项目和筛选；先收集真正需要的选择，再提交。后续新请求使用其真实消息依据。关闭某个来源只改变该来源；开启总开关会顺带激活另一休眠来源时返回冲突，要求用户明确两个来源的选择，不能静默扩大范围。新项目活动订阅不启用待办或工具。

项目活动沿用一个成员／账号仅一个项目的能力；变更项目是替换该活动来源，不宣称多项目追加。个人待办仍按现有契约覆盖账号全部可访问项目，当前代码没有独立的“当前 Workspace”待办范围字段；Skill 明确说明限制并引导支持的设置，不克隆仓库、不绑定或修改 Workspace，也不将全部项目订阅谎称为限定范围。

## 身份、回执与恢复

- 选择协议增加显式 `provider`，缺省继续按已有飞书记录读取；仅飞书和 GitLab 两类分派，不新增 provider 注册框架。账号选择固定实例，候选包含 username、原生用户 ID、连接 ID／版本，提交以服务核验名称和身份为准。
- 项目选择固定完整成员和账号版本，候选同时展示名称、完整 namespace/path 和原生 ID；支持远端搜索及数字页码。权限不足返回 `permission_denied`，404、限流和凭据错误分别分类，不暴露远端正文、Token 或传输错误，不将失败当作空目录。
- 提交项目选择先通过原账号读取 `/projects/:native_id`，比较原生 ID 与路径；远端读取后再次校验成员、账号、操作版本及期限。改名、替换账号或并发草稿变更均冲突，不猜测其他项目。
- 成员配置提交前同样按原生 ID／路径复查；调用原活动服务后，在来源写入前比较其 path → ID 核验结果，防止两次读取之间路径被复用成其他项目。未知操作保留原回执与已保存效果，不重放或换 ID 重试；未结清配置通过完整设置恢复。
- 对话与完整设置使用同一草稿版本及不可变选择回执。关闭设置会刷新候选元数据，保留本地选择和输入；分页重载前，已选项目仍在摘要中可核对。重复提交只保存同一结果；过期表单、成员替换和手动／自然语言冲突不改目标。
- 直接复用原选择事件、Inbox 投递、消费关联、处理确认和三种接续。选择保存、事件消费及实际订阅是独立事实；不更改调度、暂停、预算或 Session 语义。原 human 管理接口仍拒绝 Agent 身份。

## 本地验证

```sh
go test ./... -count=1 -timeout=8m
go test -race -tags=integration ./internal/room \
  -run '^Test(GitLabProject|GitLabAccountSelection|GitLabMember|DefaultAssistantGitLabConnect|PlatformMember|GitLabUnified|GitLabConnect|GitLabAuth|GitLabSetup|PlatformConnect|PlatformSelection|LarkSetup|LarkAuth)' \
  -count=1 -timeout=4m
node --test --test-reporter=dot internal/room/web_*test.mjs
TEAMX_CONNECT_BROWSER=1 go test -tags=integration ./internal/room \
  -run '^Test(GitLabProjectSelectionConversationBrowser|GitLabConnectConversationBrowser|DefaultAssistantGitLabConnectConversationBrowser|PlatformConnectConversationBrowser)' \
  -count=1 -timeout=3m
git diff --check
```

上述检查均通过：最终全量 Go 的 Room 包耗时 148.999 秒；integration＋race 广泛专项 84.324 秒，收尾改动及重启专项补验 32.416 秒；前端 395 项通过，真实 Chromium 组合回归 20.521 秒。

服务／CLI 专项覆盖同实例双账号、同名项目分页、原生 ID／路径不一致、403／404、读取期间账号与草稿变更、成员替换、表单过期、重复提交、未知结果不重放、接续及重启读取；来源筛选修改保留未指定维度、待办、间隔和历史起点。前端覆盖分页精确选择、单项目活动筛选、设置回流、草稿冲突、权限失败保留选择和旧飞书回归。

真实 Chromium 的模拟远端组合测试覆盖 GitLab 项目分页／筛选／提交／刷新／窄屏，并回归 GitLab 和飞书连接组件。关闭设置后按已有契约重新读取第一页，已选 ID／路径仍保留；测试核对选择摘要，并重新加载第二页确认原 ID 仍被选中。

## 指定实例／账号／项目的真实验收

用户指定：实例 `https://git-devops.opencsg.com`、账号 `RussellLuo`、项目 `product/agentichub/teamx-demo`。隔离服务使用 `127.0.0.1:18091`，只复用已有已核验连接，未复制 Room／订阅／工具／Workspace。日常服务数据未修改。

真实 Chromium 只发送一条自然语言请求：复用指定账号，先展示项目选择表单，提交后自行经 Inbox 继续，开启全部项目活动类型。浏览器从真实 GitLab 候选选择目标、设置全部活动，并验证完整设置关闭、稳定组件、390px 窄屏、提交及刷新。提交选择后未发送聊天，真实模型经普通 Inbox 进入第 2 回合，完成配置、再次读取核验并反馈。

已核对的结果：

- 账号原生 ID `13`，连接 `8d09ed31e88d888088a60b35e14ed57d`，版本 1；未创建新连接或发起 OAuth。
- Room `8d26b51b7016ce0f4389935e39a71d3b`／instance `8acfa4c698e2b669106ac259bddbd176`／Agent `chief-of-staff`／Membership `955d774d8e9f6af501338deeef074ff9`。
- 选择 `4d6e316be7606ebc10405f1ae152de5d` 为 `completed/result_version=1`；原生项目 ID `482`、完整路径 `product/agentichub/teamx-demo`。稳定事件已投递至 Inbox 序号 1，消费关联落盘、`consumed_through=1`、`handled_version=1`。
- 成员配置 `fb652b2d518484edcf9ae1b20c8e202a` 为 `completed/result.status=applied`；通知版本 1，活动订阅 `5e0cb45909aa526fa77f88dfde6d25a7` 的目标、项目 ID／路径与回执一致，筛选为全部活动，无 pending/errors；接收起点为 `2026-11-11 13:26:13 +08:00`。
- human 消息始终只有一条，模型第 2 回合完成；个人待办关闭、工具未绑定／修订 0，Workspace 无仓库或项目绑定。没有 GitLab 业务写入。

证据保存在忽略目录 `.local/acceptance/conversation-gitlab-selection/real/`：`verification.json` 为 `passed=true` 的无凭据报告，截图包含 `selection.png`、`mobile-selection.png`、`submitted.png`、`completed.png`、`settings.png` 和 `mobile.png`。最后用更新后的二进制重启隔离服务，回执、消费关联、选择已保存展示与 `passed=true` 核对仍保持，未新增回合或配置；验收结束后已停止隔离服务。

真实同实例双账号和真实权限不足／改名／并发竞争未实测，使用本地模拟服务验证；本轮没有再次做 OAuth，不能替代 CG2 尚待人工授权的新 OAuth 组合验收。订阅保存不等于真实项目事件已到达或已触发提醒，本阶段不宣称该提醒链路通过。CG4 的首次应用配置回流及完整组合验收仍待实施。
