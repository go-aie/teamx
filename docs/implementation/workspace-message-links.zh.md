# 消息文件链接实施记录

范围：落实 [工作区文件与预览方案](../WORKSPACE-FILES-AND-PREVIEW-PLAN.zh.md) 的 F2，复用 F1 文件面板和受约束读取。使用隔离 Room、实际 TeamX HTTP／持久存储及受控执行组件；未操作日常部署，未执行真实 DSH／模型闭环。

## 已实现行为

Agent 原生消息中的 `[预览文件](docs/result.md)` 与引用式 Markdown 链接可直达文件预览。点击只发起一个带 `message_seq + path` 的预览请求，不先列来源或解析引用。文件页展示当前内容，支持刷新、源码、展开、返回聊天及「所在目录」；目录按钮显式转为当前工作区浏览。手机浏览器返回、Escape、焦点、草稿和原侧栏恢复沿用同一组件。

共用路径转换函数只解码一次 URL 转义，再按工作区根规范化；允许根内 `..`，拒绝越界、绝对路径、反斜杠、控制字符、协议和查询参数。代码、裸路径、引用文本、人类消息及平台通知不关联助理目录；HTTP／HTTPS 保持原外链行为。旧消息无可信来源时显示说明，不请求当前目录猜测文件。

Session 使用本次实际执行配置的规范 cwd 计算 `workspaceRootID`，随已有 executionBinding 和认证上下文进入 send。同一指纹作为可选字符串随已有私有 Session 配置快照冻结，避免重启时将路径别名重新解析到另一目录；新代际重新观察自己的 cwd，不继承旧代际的目录指纹。没有新增来源注册表或执行凭据体系，也不修改原执行配置。

恢复读取原代际快照；缺少可信快照、目录不存在或旧快照既无指纹又只保存了非规范别名时，省略文件来源，不改变原执行／恢复合同，也不阻断文字发送。消息指纹与正文原子提交，参与容量、分段日志加载和不可变比较；重试返回首次保存的事实。外部 JSON／CLI 不能提交指纹，Agent 业务 DTO 保持原白名单字段。

消息预览从持久历史核对原作者、当前 membership、来源指纹和消息可见性，再复用 F1 读取；交付前重新核验。历史读取在管理器锁外，不启动 Agent 或推进读取游标。后端不解析正文，不检查目标是否列在消息中；根内其他合法路径仍受相同权限约束。目录变化、成员退出、跨 Room、消息清空均拒绝旧引用，不转读新目录或同名文件。

## 验证

- 前端完整测试及相关 Go 测试通过；具体结果见 验证数据（本地验收文件 `message-links-evidence.json`）。
- Go 覆盖执行凭据链路及撤销、伪造字段、来源缺失、原子失败、容量、分段日志不可变比较、幂等、Store 正常重启及新 epoch、恢复时的新旧配置、目录变化、成员退出、跨 Room、清空与直接浏览的独立权限。
- 实际 HTTP／Chromium：单聊和多人 Room，1600×1000、1000×850、390×850；执行组件经 Session host 取得凭据，再通过生产认证／send 发送链接，浏览器点击。覆盖单请求直达、引用式链接、源码刷新、展开、返回、焦点和草稿恢复、所在目录、旧消息提示，以及清空期间迟到响应丢弃。
- F1 的单聊／多人 Room × 默认／外部目录四组实际浏览回归通过。
- 完整 `go test ./internal/room ./internal/web`：Room 包在 450.568 秒后报告两项失败，Web 包通过。失败与 F1 一致：`TestStaticAssemblySourcesAndCapabilitiesAgree` 的 GitLab Events 装配不一致，及 `TestInboxAndActionCoreHasNoNativeProviderRules` 的既有飞书依赖。相关检查与目标文件未因本阶段修改。完整运行后补入的别名冻结改动已复跑受影响的相关测试；最终代码单独复跑上述两项检查，仍报告同样失败，不能记为完整回归通过。

执行命令：

```sh
node --test internal/web/tests/*_test.mjs
go test ./internal/room ./internal/web \
  -run 'TestMessageWorkspace|TestWorkspaceFile|TestAccess|TestDSH|TestModel|TestWorkspace.*(Directory|Directories)|TestRoom(Post|Commit|Validation|History|Log)|TestAgent.*(Read|View)|TestRoomAttachment' -count=1
TEAMX_FILES_BROWSER=1 NODE_PATH="$PWD/packaging/verifier/node_modules" \
  go test -tags=e2e ./internal/room \
  -run '^TestWorkspaceFilesBrowser$|^TestWorkspaceMessageLinksBrowser$' -count=1 -v
go test ./internal/room ./internal/web
```

截图：窄桌面（本地验收文件 `message-1000.png`）、手机尺寸（本地验收文件 `message-390.png`）。操作说明见 [Workspace 使用指南](../workspace.zh.md#查看工作区文件)，参数和消息来源合同见 [技术参考](../reference/workspace.zh.md#文件只读接口)。

## 后续边界

F3 的工作区图片、文档相对资源、锚点及下载已在后续授权中实施，见 [资源与下载记录](workspace-resources-and-downloads.zh.md)；F4 的真实 DSH／模型生成—交付—修改闭环已在后续授权中通过，见[真实闭环记录](workspace-files-real-acceptance.zh.md)；本记录仍只对应 F2。受控组件验证了生产 Session host／认证／发送链路，不等于真实模型验收。手机尺寸 Chromium 不代表实体手机；本轮未做 Linux 或远程设备验收。
