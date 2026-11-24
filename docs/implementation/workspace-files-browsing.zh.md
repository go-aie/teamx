# 工作区文件浏览实施记录

范围：落实 [工作区文件与预览方案](../WORKSPACE-FILES-AND-PREVIEW-PLAN.zh.md) 的 F1。仅使用隔离测试数据与受控 Agent 适配，不修改日常 Room，也未启动真实 DSH／模型验收。

## 已实现行为

会话右上角「… → 工作区文件」进入独立文件页，右侧只显示目录或当前文件。支持默认／外部目录、单聊／多人 Room、共用目录合并展示、当前目录筛选、隐藏项开关、相对路径定位、刷新、Markdown 渲染／源码切换。文本只读，最大 1 MiB；超限或不支持类型显示原因，不返回截断正文。

文件栏有独立拖宽记录，宽屏优先保留约 600px 阅读区和 480px 聊天区；窄桌面采用覆盖面板，手机全屏。展开、Escape 和浏览器返回沿同一个文件状态逐层退出。关闭恢复原侧栏状态、成员设置页、草稿、聊天位置和焦点；覆盖聊天时暂停可见阅读判定。隐藏的成员诊断停止读取，文件页关闭／后台时取消其读取，目录和正文不定时重读。

新增 human 只读接口 `/v1/workspace/files/sources`、`/v1/workspace/files` 和 `/v1/workspace/files/preview`，来源由已保存成员配置解析，不启动／恢复 Agent。纯函数 `workspaceRootID` 统一计算稳定目录指纹，不依赖整个 Workspace 配置版本。尚未结清的目录操作使来源暂不可用；来源变化后要求重新选择，不转读其他成员或服务 cwd。

文件读取在管理器锁外执行，以根和每级目录句柄约束路径，校验打开对象身份；符号链接逐级解析，包含绝对根内链接和链接后的父目录语义。非阻塞打开并拒绝 FIFO／设备／socket，拒绝根外、`.git`、`.teamx` 和配置的保护路径。受管工作区仅豁免管理祖先，不允许浏览同级数据或受保护后代。读取后及交付前复核文件、根和当前来源，使用全局 4 槽队列和可取消等待。

## 验证

前端 **818 项通过**；文件／附件定向 Go 测试通过，最终来源准入复核通过。四种工作区文件浏览组合及两项既有页面回归均通过实际 HTTP／Chromium。

完整 `go test ./internal/room ./internal/web` 已运行：Web 包通过；Room 包在 581.867 秒后报告两项失败，不能记为全量回归通过。失败是 `TestStaticAssemblySourcesAndCapabilitiesAgree` 的 GitLab Events 声明／生命周期不一致，以及 `TestInboxAndActionCoreHasNoNativeProviderRules` 对既有 `actions_target.go`、`agent_contract.go` 的飞书依赖检查；这些检查和目标文件均未因本阶段修改。保留该回归问题，未扩展 F1 去修改平台装配或动作核心。

- 后端边界：目录及文本、UTF-8／NUL、类型伪装、1 MiB 上限、5,000 项扫描及 1,000 项返回、隐藏项直接读取、保护路径与链接、真实 FIFO、并发链接替换、根／文件替换、取消、human／执行凭据、HTTP 身份和来源隔离。
- 前端：原设置与草稿恢复、源码模式刷新、筛选和列表位置、展开、隐藏诊断暂停、关闭／服务变化／来源切换后的迟到响应、来源失效必须显式重选、正文不进入存储。
- 实际 Chromium + TeamX HTTP：单聊／多人 Room × 默认／外部目录，1600×1000、1000×850、390×850；验证筛选、定位、隐藏保护项、渲染、拖宽、展开、返回、原栏开启／关闭恢复、来源切换，以及关闭／跨 Room 后的迟到响应。
- 回归实际单聊详情和既有目录选择页面，使用新的会话菜单进入详情。

执行命令：

```sh
node --test internal/web/tests/*_test.mjs
go test ./internal/room ./internal/web

TEAMX_FILES_BROWSER=1 TEAMX_DM_BROWSER=1 TEAMX_WORKSPACE_BROWSER=1 \
NODE_PATH="$PWD/packaging/verifier/node_modules" \
TEAMX_FILES_SCREENSHOTS="$PWD/docs/implementation/workspace-files" \
go test -tags=e2e ./internal/room \
  -run '^TestWorkspaceFilesBrowser$|^TestDirectConversationBrowser$|^TestWorkspaceDirectoriesBrowser$' \
  -count=1 -v
```

截图：桌面（本地验收文件 `desktop-preview.png`）、窄桌面（本地验收文件 `narrow-preview.png`）、手机尺寸（本地验收文件 `mobile-preview.png`）。测试结果与环境见 验证数据（本地验收文件 `evidence.json`）。操作方法见 [Workspace 使用指南](../workspace.zh.md#查看工作区文件)，HTTP 合同见 [技术参考](../reference/workspace.zh.md#文件只读接口)。

## 后续边界

F2 的消息来源字段、执行绑定及消息链接已在后续授权中实施，见 [消息文件链接记录](workspace-message-links.zh.md)；本记录与验证数据仍仅对应 F1。F3 的图片、相对资源和下载也已在后续授权中实施，见 [资源与下载记录](workspace-resources-and-downloads.zh.md)。F4 真实 DSH／模型闭环已在后续授权中通过，见[真实闭环记录](workspace-files-real-acceptance.zh.md)；本记录仍只对应 F1。当前证据来自 macOS 的 Chromium 及手机尺寸模拟，没有声称 Linux 实机、实体手机或远程设备验收完成。
