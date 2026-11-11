# 测试

## 日常入口

从仓库根目录执行。日常先跑 `make test`；integration 按改动模块运行，全仓 integration 与 race 用作阶段性检查，无需每次修改都串行全跑：

```sh
make test                     # 核心回归，等价于 go test ./... 并附带超时
make test-integration         # 核心 + 本地集成，不访问真实平台或模型
make test-race                # 核心 + 本地集成的 race 检查
node --test internal/web/tests/web*_test.mjs
```

本地集成夹具还需要 Python 3、Git 和支持 `env -S` 的环境（macOS 或 GNU env）。

保留 Go 默认的构建和结果缓存。需要确认修改后的真实耗时或排查偶发问题时，加 `-count=1` 强制执行；日常入口不强制清缓存或禁用结果缓存。

按改动模块执行 integration，无需每次覆盖整个仓库：

```sh
make test-integration TEST_PACKAGES=./internal/room TEST_RUN='Lark'
make test-integration TEST_PACKAGES=./internal/platform/lark
make test-integration TEST_PACKAGES='./internal/room ./internal/platform/gitlab' TEST_RUN='GitLab|ReferenceAction'
```

`TEST_RUN` 是 Go 的测试名正则。未匹配的测试不会运行；按模块的验证不能描述为全仓通过。

## 分层约定

| 层级 | 内容 | 入口 |
| --- | --- | --- |
| 核心 | 状态转换、数据校验、权限、存储提交边界、Actor 生命周期和快速组件回归 | 无额外构建标签 |
| integration | 本地 CLI 协议、HTTP/前端组合、独立进程重启、Git 仓库操作、完整业务链路与组合矩阵 | `integration` |
| e2e | 已安装的外部工具专项验收、真实平台/模型、Chromium 与人工浏览器验收 | `e2e`，另加场景要求的显式启用参数 |

integration 文件使用 `//go:build integration || e2e`，使 e2e 同时具备本地测试的辅助代码。真实验收函数必须受 `e2e` 标签保护，不能仅靠环境变量在默认测试中跳过。复用的辅助函数留在普通 `*_test.go`；不得将模拟器或测试开关加入正式 CLI。

核心测试可以使用临时目录和 `httptest`。是否归为 integration 取决于是否验证进程、协议或完整业务组合，不能只看文件名或是否读写文件。关键校验和失败处理仍需在核心层有快速回归；移到其他层的测试继续保留全部断言。

## 保持执行速度

- Room 中拥有独立 Store、账号队列且不修改共享环境的测试使用 `parallelFixtureTest`；Lark 协议夹具使用 `parallelClientTest`。各包内同时最多执行 4 个，限制 CLI 进程与磁盘同步压力。修改环境、当前目录或共享状态的测试保持串行。不要在并行父测试中调用 `t.Setenv` 或 `t.Chdir`。
- 本地飞书 CLI 夹具使用独立 `ClientFactory`，保留队列、取消、容量及身份校验，但不等待模拟远端请求的生产限流间隔。真实验收和正式服务使用进程级生产队列；FIFO、间隔、冷却与取消由 `internal/platform/lark/budget_test.go` 专门验证。
- 快照目录初始化按目录和父目录身份复用，避免每次写入前重复同步；每次提交仍执行文件同步和重命名后的目录同步。目录或父目录被替换、初始化失败时不会复用旧证据。
- Store 关闭时显式释放文件锁，避免并发 fork 尚未 exec 的子进程通过继承描述符短暂保留旧锁；核心测试通过复制描述符稳定验证这一边界。
- CLI 夹具复用正在运行的测试二进制，保留构建标签和 race 检测，避免嵌套编译；需要正式 CLI 的本地测试共享一次生产构建，loopback 测试无需预先准备二进制。仅短 CLI 子进程取消 race 检测器默认的退出等待，其他 race 选项保留。
- Python 协议夹具仅使用标准库，以 `python3 -S` 启动，避免加载用户 site 配置。
- 新增业务组合优先放在组件层；真实进程测试覆盖必要的协议、重启和资源清理边界。不要给每个业务分支重复完整进程启动流程。

需要查看耗时明细时：

```sh
go test -json -count=1 -timeout=8m -tags=integration ./internal/room > /tmp/teamx-room-tests.jsonl
```

JSON 中测试的 `pass` / `fail` 事件包含 `Elapsed`。串行父测试通常已包含串行子测试耗时；并行父、子测试的计时还会受暂停、等待和资源竞争影响，不能简单累加。整个套件应看包级结果及从开始到结束的墙钟时间。

## 私信与默认助理

模型与生命周期回归覆盖私信固定单成员、创建去重、冻结配置、旧单成员 Room 不改型、跨文件一致性、默认一次性创建、无输入不启动模型、暂停和失败恢复、默认删除保护，以及旧删除记录的显式恢复与提交中断。

按需 Skill 的核心检查覆盖当前成员身份、服务代次、浏览器边界和固定资源请求。独立二进制在源码目录外读取使用指南、无平台绑定读取接入 Skill，以及平台 Skill 的准确账号入口替换，可按下列本地集成入口验证；这些检查不代替真实模型遵循工作流程的验收：

```sh
go test -tags=integration ./internal/room \
  -run 'TestUsageSkill|TestDefaultAssistantSkill|TestExplicitToolBindingPreservesRolesAndReadsSkillWithoutCredentials' \
  -count=1 -timeout=2m
```

```sh
go test ./internal/room -run 'TestDefaultAssistant|TestDirectConversation|TestRoomWithoutDirectFields' -count=1 -timeout=3m
go test -race -tags=integration ./internal/room \
  -run 'TestDefaultAssistant|TestDirectConversation|TestRoomWithoutDirectFields|TestPlatform(Connect|Selection|Member)' \
  -count=1 -timeout=4m
```

可选 Chromium 场景需要本机 Playwright 和 Chromium，使用实际 HTTP／CLI 及临时数据，飞书 CLI／成员 backend 为本地夹具：

```sh
TEAMX_DM_BROWSER=1 go test ./internal/room \
  -run '^(TestDirectConversationBrowser|TestDefaultAssistantRestorationBrowser)$' -count=1 -timeout=3m
TEAMX_CONNECT_BROWSER=1 go test -tags=integration ./internal/room \
  -run '^(TestDefaultAssistantPlatformConnectionBrowser|TestPlatformConnectConversationBrowser)$' -count=1 -timeout=3m
TEAMX_SELECTION_BROWSER=1 go test -tags=integration ./internal/room \
  -run '^(TestDefaultAssistantPlatformOnboarding|TestPlatformSelectionConversationAndMemberConfiguration)$' -count=1 -timeout=3m
```

浏览器覆盖分组和选择恢复、默认删除保护、旧实例显式恢复、普通私信删除、独立草稿、详情 Tab／焦点、窄屏，以及账号／群选择和完整设置返回。接续测试核对原请求、完整成员身份、Inbox 结果与单次配置；受控 backend 的自动回合与 CLI 驱动的等待／主动继续分别验证，不称为真实模型。

已有单账号真实 DSH／模型、飞书与浏览器验证，覆盖默认 DM 的新 OAuth 连接和自动接续，以及复用已核验账号后的群关注、持续规则保存、暂停后恢复接续。用户主动继续的原操作查询由本地 CLI 回归覆盖；第二真实账号、新群消息触发实际业务提醒、真实远端故障和并发自然语言反馈去重仍未实测。连接、订阅保存与实际提醒不能互相代替，复验范围见[飞书真实验收](lark.zh.md#真实验收)。

## 本机验证记录

2026-11-08，macOS ARM64、Go 1.27.1，构建缓存可用，以下执行均使用 `-count=1`：

| 范围 | 耗时 |
| --- | --- |
| 全仓核心回归 | 约 70 秒 |
| Lark 模块 integration | 约 37 秒 |
| 全仓核心 + integration | 4 分 45 秒 |
| 全仓核心 + integration，race | 5 分 03 秒 |

以上均已通过。日常入口保留测试结果缓存；首次依赖下载、机器负载和修改范围会影响实际耗时。

## 少量按需 e2e

日常只检查 e2e 是否能够编译：

```sh
make test-e2e-check
```

真实验收按需选择一个场景，提供该场景要求的配置、授权和独立数据目录：

```sh
go test -tags=e2e ./internal/room -run '^TestRealDSHRoom$' -count=1 -timeout=25m -v
```

该命令仍需场景的显式启用参数，否则会跳过。具体方式见 [Agent 运行时](agent-runtime.zh.md#真实验收)、[Workspace](workspace.zh.md#真实验收)、[通知](notifications.zh.md#接口与验证)与 [GitLab 工具](../tools/gitlab/README.zh.md#真实验收)。不要把全部 e2e 加进日常核心或 integration 入口。

阶段验收结束后，将仍需维护的复跑方法与未完成项收进对应模块说明，删除重复的阶段记录；不要求每次实施新增一份永久验收文档。私有回执、防重记录和必要失败证据独立保留。
