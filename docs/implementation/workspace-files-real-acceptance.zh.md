# 工作区文件真实闭环验收

范围：完成 [工作区文件与预览方案](../WORKSPACE-FILES-AND-PREVIEW-PLAN.zh.md) 的 F4。2026-11-21 在全新私有 home、单成员托管 Room 和独立工作目录中，使用真实 TeamX 服务进程、DSH／模型、生产 CLI 发送链路及 Chromium 验收通过。日常部署未修改，没有平台业务写入。本阶段新增可重复的真实验收和证据记录，无产品代码改动。

## 环境与入口

后端基于 `c25cb6f7178220545176d4804a715e4874cef3e9` 加当前工作区的 F1–F3 及既有改动构建，实际二进制 SHA-256、模型探测与启动器摘要见[验证数据](workspace-files/real-acceptance-evidence.json)。本次为 macOS arm64、Go 1.27.1、Node 26.7.0、Chromium 147.0.7727.15；模型为 `deepseek-v4.1-flash-1017`。

DSH 使用现有配置中的源码启动器 `apps/cli/lib/bin.js`，版本 `0.2.1-alpha.1`，源码 HEAD 为 `5badb15009ae1756c3afe0ae0cef1faafc290ccc`，跟踪代码干净；ACP 身份为 `deepseek-harness-acp 0.0.1`。这不代表发布包启动器验收。只复制选中模型和凭据到隔离 home，不复制平台连接。

[Go 验收](../../internal/room/workspace_files_real_test.go)负责进程启停、真实模型执行、可信消息来源、文件摘要与 Session 核对；[浏览器脚本](../../internal/room/testdata/workspace_files_real_browser.cjs)负责实际页面操作。执行条件和复跑命令见[Workspace 技术参考](../reference/workspace.zh.md#文件生成与预览)。默认测试及 CI 不执行真实模型，须显式设置验收开关。

私有原始报告位于 `.local/workspace-files-real-20261121-04/report.json`，日志位于 `.local/workspace-preview-regression-20261121/`。隔离 home 含凭据及 Session 数据，公开记录仅保留必要身份、摘要和合成示例截图。

## 实际闭环

| 操作 | 结果与证据 |
| --- | --- |
| 生成与交付 | 模型实际生成 `docs/preview.md`、`docs/guide.md`、32×32 PNG 和 32 MiB 下载文件；验收程序没有代写这些内容。真实 CLI 发送 `[预览示例](docs/preview.md)`，持久消息带冻结 cwd 的可信 `workspace_root_id` |
| 点击与阅读 | Chromium 点击原消息，以一次 preview 请求打开 Markdown；源码切换、相对图片、文档／锚点导航、所在目录、展开及 Markdown 下载通过 |
| 修改与刷新 | 页面发送第二次需求，模型仅修改主文档；新交付出现后原预览仍保留旧内容，手动刷新才显示 `WORKSPACE_PREVIEW_V2` 与新增句子，源码模式保持。文件摘要独立核对 |
| 正常重启 | 服务正常停服后以相同地址启动，新 epoch 下原链接仍可打开；1600、1000、390 宽度通过，手机尺寸浏览器返回有效。SessionID `cabd1b10-7689-4520-af23-2b9651157417`、cwd、Agent 成员读取状态与消息序号保持不变 |
| 目录变化 | 使用生产 Workspace apply 切换到另一空目录；旧消息明确提示来源变化，旧图片请求返回 `workspace_source_changed`。恢复同一默认目录后原链接重新可用，未转读其他目录 |
| 消息清空 | 在真实 32 MiB 下载已接收 16,844 字节后，通过生产接口清空原消息；已准入传输继续完成 33,554,432 字节且 SHA-256 与原文件一致；新的消息预览、图片和下载请求均返回 `reference_unavailable` |
| 文件保留 | 清空后直接从文件面板仍能打开文档及图片，草稿保留，生成文件摘要不变。清空没有删除工作区文件 |

整个模型任务共两次 human 请求、两次 Agent 执行、两条 Agent 交付，没有为查看或重启多发消息。目录切换测试等待操作完成、Active 服务以及会话列表中匹配的新运行时 epoch 就绪，再重开页面；仅观察操作完成不足以证明页面已经采用新身份。

截图：[修改后桌面预览](workspace-files/real-desktop.png)、[展开阅读](workspace-files/real-expanded.png)、[窄桌面](workspace-files/real-narrow.png)、[手机尺寸](workspace-files/real-mobile.png)、[来源变化](workspace-files/real-source-changed.png)、[清空后直接浏览](workspace-files/real-after-clear.png)。

## 回归结果

- 真实闭环 `TestWorkspaceFilesRealDSH` 通过：用例 62.59 秒，Room 包 63.054 秒；三个浏览器步骤均无页面错误。
- `node --test internal/web/tests/*_test.mjs`：824 项通过。F1–F3 实际 TeamX HTTP／Chromium 回归：8 个场景通过，52.501 秒，包含默认／外部目录、单聊／多人 Room、消息入口、相对资源及下载。
- `go test ./...` 未全绿：Room 包在 544.376 秒后报告既有 `TestStaticAssemblySourcesAndCapabilitiesAgree`（GitLab Events 装配不一致）及 `TestInboxAndActionCoreHasNoNativeProviderRules`（原生飞书规则／依赖）失败，与 F1–F3 记录一致，未修改这些实现。
- 全套首次运行另有 `TestInputBridgeResumeConfigThroughStateSymlink` 的 DSH 清理超时；该用例单独复跑和整个 `internal/agent/dsh` 包复跑均通过。保留首次失败记录，不将整体回归记为通过。Web 包及其他包通过。
- 新浏览器脚本语法检查、`git diff --check` 通过。最终通过结果来自全新环境完整运行；此前夹具启动校验和目录切换等待问题已修正。

## 未验范围

手机尺寸 Chromium 仅证明响应式行为，实体手机、远程设备、Linux 主机、发布包 DSH 启动器未验。本次真实模型任务位于单成员托管 Room；默认助理与多人 Room 的文件场景通过 F1–F3 实际后端／浏览器回归，未另做这些会话形态的真实模型生成。

没有推进文件编辑、工作区 HTML 执行、PDF／Office 转换、附件汇总或产出物登记等后续扩展；也未修改日常 Room、提交或推送代码。
