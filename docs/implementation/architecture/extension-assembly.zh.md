# 外部本地工具的公开装配与启动

日期：2026-11-21。对应[第二阶段方案 P1b](../../GORDIS-PLUGIN-AND-SELECTIVE-BUILD-PLAN.zh.md#93-p1ap1b从唯一描述来源到真实外部装配)，承接 [P1a](platform-catalog.zh.md)。本次公开本地工具所需的最小描述与启动入口，并接入普通 `cmd/teamx`；保留默认完整组合。用户指南见[本地工具扩展](../../extensions.zh.md)。

## 实现与边界

开工基准为 HEAD `b1dcb2702493ae9e48b9407296f13edb7e78599d` 加 P1a 和既有工作树。已有文档、Room 测试与本地运行目录继续保留；本次未提交、发布、重启日常服务或执行远端业务写入。逐命令、依赖、源码摘要与限制见验证证据（本地验收文件 `extension-assembly-validation.json`）。

| 入口／位置 | 本次责任 |
| --- | --- |
| `platform.Tool` | 公开 Name、Description、Version、Executable；独立工具不伪造 Provider、平台账号或内部动作能力 |
| `extension.Extension / Compose` | 公共纯声明、不可变组合与名称／冲突校验；允许显式空组合 |
| `internal/app/extensions` | 将公共工具与内置平台工厂转换到 P1a 的唯一 Catalog；平台贡献保持不透明，公开类型无 internal 参数或私有必需方法 |
| `platforms.NewCatalogWithTools` | 在同一目录中检查平台工具与独立工具的共同名称空间；不创建第二套运行 registry |
| `builtin/platforms` | 各 Provider 只公开纯 `Extension()`；完整汇总只在外层显式选择，完整描述与查询绑定收于 `internal/app/builtinplatforms` |
| `boot.Run / cmd/teamx` | 普通与外部入口使用同一 Compose／启动路径，保留原命令和受控工具退出码 |
| `teamx extensions check` | 只投影静态选择，不读配置／账号／executable，不经过 manager 或日历构造 |
| `testdata/external-tool` | 独立 Go module 与本地 echo 程序；不导入 TeamX internal，通过真实隔离服务发现、绑定和执行 |

公开的 Extension／Composition 使用内部实现的不透明类型别名；其作者可用字段只有公共工具值，内部平台贡献通过本 module 的适配构造。Composition 没有返回内部 Catalog 的公开方法。构造不启动资源，Compose 不解析 executable 或读取凭据，列表／执行阶段才沿原路径检查程序可用性和准入。

## 同一选择贯穿所有入口

原 `room` 默认全集依赖已删除。普通命令在 main 显式选择 GitLab／飞书／Scion；boot 和通用装配不导入完整汇总。当前 Room 仍有 Provider 的具体查询／配置和 P1a 来源绑定依赖，因此**boot 的生产依赖尚未裁掉具体 Provider**。

服务配置、manager、每 Room Store 及所有离线打开传递同一个静态 Catalog：单 Room／受管启动、运行恢复、关闭 Room 的模型／Workspace 检查、输入格式转换和离线平台／通知升级都保留选择。通知投影、context_only、History 与来源详情改用所属 Store 的能力查询；不再回到隐式完整目录。输入转换的备份／目标 Store 也继承该选择，缺 codec 沿原拒绝路径失败，恢复兼容选择后可以续做原升级。

这些变更只传递装配事实；没有移动 Inbox、Member、连接、订阅、文件 writer 或输入证明的可变 owner。Store 上的 Catalog 指针是不可变能力查询，不是新的持久状态。原路径、JSON／JSONL、持久格式、目录锁、Calls、授权与关闭链未改。

旧回归夹具在 `platform_fixture_test.go` 显式选完整目录，再调用真实的配置构造；这个文件只进入测试二进制。显式选择测试和外部 module 验收直接走生产构造，不依赖夹具默认。为避免大面积机械修改原用例，原便捷构造与纯投影测试调用保留为此文件中的薄包装。原有测试未删除或改弱。

## 外部 module 验收

fixture 使用 `example.com/teamx-local-tool`，在隔离副本中以 `GOWORK=off` 和明确的本地 replace 指向本 checkout；不改根 go.mod／go.sum，不依赖相邻 Qi／Gordis checkout。`v0.0.0` 只是该测试 module 的占位，发行需另固定含新 API 的真实已发布版本。

自定义程序只选择 `local-echo`：静态检查和服务端 catalog 都没有偷偷补回内置工具。隔离服务通过原 API 创建成员及无账号绑定；离线 ACP fixture 经正常 Session 打开取得临时执行凭据，再调用自定义程序的 `tools list/run`。验证字面参数与 NUL stdin、受限进程环境、子程序退出码 23、管理 token 不得冒充成员、取消和服务退出 0。没有平台连接、远端 Workspace 绑定或远端业务写入。

这证明公开工具声明在正常服务中实际执行；ACP 程序是确定性离线 fixture，故意等待取消，不伪造输入持久接收证明。它不算真实 DSH／模型执行、恢复或 P4 的证据。

## 验证

| 检查 | 结果 |
| --- | --- |
| 公共合同／内置工厂／应用组件与架构 `-race -count=1` | 通过，24.109 秒；覆盖重复工具、跨内置冲突、零 executable 查询、不可变快照、空选择和生产依赖 |
| 独立外部 module 构建／受控验收 | 通过，最终自动化检查 10.239 秒；真实隔离服务、临时成员凭据、无账号本地工具、退出码／环境隔离和取消／关闭 |
| Room 定向 `-tags=integration -race -count=1`，7 分钟上限 | **70 项顶层全部通过，无跳过**，204.608 秒；覆盖显式选择、离线升级／恢复、History／图文／context_only、精确工具入口、撤权／结清、JSON 请求和生产 CLI |
| 扩大 Room core，12 分钟上限 | 命令退出 1，448.953 秒；**954 项顶层通过、1 项已有边界断言失败、3 项跳过**，无超时。此命令在最终纯封装和验证镜像传参收口前启动，最终树另由上述定向 race、组件与编译覆盖 |
| 全仓 `go test -tags=e2e ./... -run '^$'` | 最终树配置编译通过，未执行业务测试 |
| 正常 `cmd/teamx` 构建、`--help`、`extensions check` | 通过；静态选择仍是 GitLab／飞书／Scion 三工具，产物只在 `.local/extension-assembly/` |
| 公共包／工厂／应用组件／架构 `go vet` | 通过 |
| 全仓 `go vet ./...` | 未通过：两条已有 JSON tag 告警，详见下方 |
| `git diff --check`，根 go.mod／go.sum 与 HEAD 对照 | 通过；未新增依赖 |

首次扩大 core 检查使用 6 分钟包上限：937 项顶层通过，一项已有边界断言失败，随后包累计超时；超时时列出的在跑用例只运行约 0–9 秒，不能据此判定单项超时或死锁。后续最终验证见上表和机器证据，不把首次命令升级为通过。

已有边界断言 `TestInboxAndActionCoreHasNoNativeProviderRules` 指出 `actions_target.go` 的飞书字面值与 `agent_contract.go` 的飞书导入；两文件均与 HEAD 相同，本次没有修改规则或绕过断言。全仓 vet 的两条 JSON tag 告警仍来自与 HEAD 相同的 `room_action_images.go`／identity 定义。完整集成、全仓 race、真实模型／平台、浏览器与发行资源裁剪不在本次通过清单。

## 收益、成本与下一项

| 问题 | 结果 |
| --- | --- |
| 外部作者改哪些公共代码 | 外部 fixture 仅写工具声明、可执行程序和自定义 main；无需修改 TeamX 公共 handler，也无需账号／Provider 伪装 |
| 选择是否真实 | 静态目录与服务发现只包含所选工具，空选择保持为空；产品的具体 Provider 依赖与资源仍待 P2r／P5 |
| 状态责任是否单一 | 使用原唯一 Catalog、成员绑定、准入、Calls 与进程执行；无新增凭据缓存、Actor、writer 或业务 registry |
| 验证与适配成本 | 需要把原隐式默认查询传到离线读取与投影；回归原图文／context_only／恢复链，新增外部 module 构建和真实隔离调用验证 |
| 适配是否有明确用途 | 公共声明转换后直接进入原工具执行；内部平台封装只承载已有工厂，未提前公开通知 Repository、图片宿主或动作账本 |

下一项为 P2n：飞书 messages 的运行绑定、准入／加载／退出及外部通知作者合同。P2t 承接内置工具调用族，P2r／P3／P5 才形成实际可裁剪与资源交付闭环；Agent 分支仍按宿主准备进入 P4a。本次没有引入 Gordis 依赖或新后端，也不把 P1b 完成称为整体插件交付完成。
