# TeamX 前端模块

TeamX 前端使用浏览器原生 ES Modules，由 Go 嵌入并提供静态资源，不需要前端构建步骤。

`internal/web` 与 `internal/room` 平级：`assets/` 存放前端资源，`tests/` 存放前端测试与辅助脚本，`embed.go` 和 `handler.go` 负责嵌入及静态响应。该包不依赖 Room；Room 服务挂载静态响应，并继续负责 `/v1/ui` 动态数据、成员活动和身份校验。

## 入口与模块职责

`internal/web/assets/app.js` 只启动应用。`application.mjs` 负责模块组装、当前 Room 身份、会话切换和全局刷新协调。

`features/` 中的模块按功能组织：

| 模块 | 职责 |
| --- | --- |
| `conversation.mjs` | 消息渲染、草稿、发送、引用、提及和重新开始讨论 |
| `rooms.mjs` | Room 列表、选择、创建、重命名、删除及默认助理恢复 |
| `layout.mjs` | 侧栏折叠、尺寸和响应式可见状态 |
| `workspace.mjs` | Workspace 配置、平台选择和持久操作恢复 |
| `members.mjs` | 成员详情、私有活动、CLI 工具及平台摘要 |
| `inbox.mjs` / `policy.mjs` | 收件箱分页与阅读位置、自然语言规则和跟进详情 |
| `notifications.mjs` | 房间及成员通知订阅、目标、来源详情和配置 |
| `platform-interactions.mjs` | 会话中的连接授权与对象选择卡片 |
| `connections.mjs` / `onboarding.mjs` | 共享连接表单、平台接入及成员平台添加 |
| `*-connection.mjs` | GitLab、Lark、Scion 各自的账号接入和授权生命周期 |
| `settings.mjs` | 系统时区、设置导航和对话框生命周期 |

已有的 `client.mjs`、`markdown.mjs` 和 `time.mjs` 继续提供请求与身份辅助、正文渲染和时间处理。

## 状态与依赖

每个功能通过工厂创建实例，状态保存在实例闭包内，不保存在模块级可变变量中。应用先安装各模块接口，再统一绑定事件，避免初始化过程中调用尚未创建的功能。

模块通过显式传入的接口协作。会话切换、导航失效、成员活动重置等操作使用所属模块的方法；对外视图句柄通过只读 getter 获取。当前 Room 和服务端快照仍由应用共享，Workspace 上的通知配置也沿用现有上下文结构。这次拆分保留业务模型，不重新设计状态存储。

修改异步流程时，应保留原有 Room 实例、服务 epoch、成员身份、请求版本和通知 scope 校验。关闭视图或切换会话后，旧请求不得写入新视图，也不得自动重放结果不明的写请求。

## 样式与资源

`style.css` 是样式入口，通过 `@import` 按顺序加载 `styles/` 中的基础、会话、成员、响应式、导航、Workspace、设置、成员详情、通知和交互样式。

首次拆分保留全部 CSS 规则及原有顺序，包括跨功能覆盖和媒体查询。后续合并重复选择器或调整规则顺序时，需要单独验证桌面、窄屏以及私信/团队房间布局。

`internal/web/embed.go` 嵌入入口、功能模块和样式文件，`handler.go` 只提供嵌入文件中的公开页面、脚本与样式。新增功能文件应放在对应目录；新增更深目录时需要更新嵌入模式。前端修改后仍需重新编译并重启 Go 服务。

## 验证

```sh
node --test internal/web/tests/web*_test.mjs
go test ./internal/web ./internal/room
```

DOM 测试直接导入 `application.mjs` 及真实模块依赖，通过实例参数替换 DOM、网络和计时器，不再删除 import 或动态拼接执行源码。Go 资源测试检查嵌入文件、静态路由和资源引用。

可使用现有 Chromium 用例验证真实浏览器中的资源加载、桌面和窄屏交互：

```sh
TEAMX_DM_BROWSER=1 go test ./internal/room \
  -run '^(TestDirectConversationBrowser|TestDefaultAssistantRestorationBrowser)$' -count=1
```

该浏览器验证使用临时数据和受控后端，不代表真实模型或外部平台验收。
