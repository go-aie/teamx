# 为指定 Agent 追加 HTTP MCP

运维可以通过 `server.agent_mcp_file` 为指定成员追加 HTTP MCP。这是显式测试配置入口，没有成员设置 UI，也不管理远端环境的创建、停止或任务结清。

```toml
[server]
agent_mcp_file = 'agent-mcp.json'
```

文件路径相对 `config.toml` 所在目录解析。文件必须属于运行 TeamX 的账户、是普通文件且权限为 `0600`，不接受符号链接。文件可以包含认证 Header，应放在 Agent 工作区之外，不提交到 Git，也不提供给模型读取。

```json
{
  "version": 1,
  "bindings": [
    {
      "room_id": "实际 Room ID",
      "room_instance": "实际 Room 实例 ID",
      "agent_id": "assistant",
      "membership_id": "实际成员 Membership ID",
      "servers": [
        {
          "type": "http",
          "name": "sandbox",
          "url": "http://127.0.0.1:18080/mcp",
          "headers": [
            {"name": "Authorization", "value": "Bearer <测试环境密钥>"}
          ]
        }
      ]
    }
  ]
}
```

四个身份字段必须全部匹配，不按显示名或 Agent 名称复用能力。`teamx` 命名空间保留给内置输入/历史工具；每个绑定最多追加 8 个服务。未匹配的成员保持原有工具配置。

配置在服务启动时读取，修改后重启 TeamX。新建和正常恢复 Session 都保留追加的 MCP，TeamX 自身的临时凭据独立刷新。URL、名称或 Header 名称变化参与既有 DSH 恢复指纹，变更能力时应结清并轮换 Session；仅凭据值更新不改变恢复身份。不要通过原地更换上游工具集合来绕过能力变更检查。

这里只为 DSH 提供远端工具，DSH 的 Bash、文件和 TeamX CLI 仍在宿主执行。HTTP 取消或本地进程退出不证明远端任务已停止；正式 Sandbox 生命周期与产物交付仍需后续集成。网页搜索测试应使用独占的无登录浏览器，不复用个人浏览器登录态。

若将测试成员的独立 Workspace 挂载到 AIO，可以把浏览器下载目录设为该挂载下的 `downloads/`。AIO 1.11.0 的原生 `browser_screenshot` 只返回图片，不保存 PNG；[测试容器截图导出脚本](../scripts/agent_sandbox_browser_artifacts.py) 可在固定版本容器中安装一个显式补丁，设置 `TEAMX_BROWSER_ARTIFACT_ROOT` 后将截图写入 `screenshots/`，并在工具文本中返回相对路径，保留原图片结果与工具 Schema。只挂载该成员 Workspace，匹配宿主 UID，禁止把整个 TeamX 数据目录或个人目录挂进去。工作目录切换不会自动重建 Docker 挂载。

## 在右侧观看环境

在同一成员绑定中增加 `environment`，即可从会话工具栏或菜单打开右侧 **环境** 面板。它与文件、日历共享侧栏，支持展开、调整宽度和重新连接；多个成员配置环境时可选择来源。没有绑定的会话显示未配置状态。

```json
{
  "version": 1,
  "bindings": [
    {
      "room_id": "实际 Room ID",
      "room_instance": "实际 Room 实例 ID",
      "agent_id": "assistant",
      "membership_id": "实际成员 Membership ID",
      "environment": {
        "base_url": "http://127.0.0.1:18080",
        "headers": [
          {"name": "Authorization", "value": "Bearer <环境网关密钥>"}
        ],
        "vnc_path": "/vnc/",
        "websocket_path": "/websockify"
      }
    }
  ]
}
```

可以在已有的 `servers` 旁添加这个字段，也可以只配置 `environment`；画面入口和 MCP 工具入口彼此独立，观看不会增加或修改 Agent 的工具。配置在服务启动时读取。

`base_url` 是 TeamX 服务端可达的 HTTP/HTTPS 网关地址，只含协议、主机和端口，不含路径、查询参数或用户密码。两个路径分别指向 noVNC 静态资源目录和 VNC WebSocket；默认值如上，`vnc_path` 必须以 `/` 结尾。上游密钥只由 TeamX 服务端使用，不出现在 iframe URL、消息或前端响应中。前端复用 TeamX 登录，通过同源 HTTP/WebSocket 访问，只转发指定环境的 noVNC 核心模块和画面连接。

默认使用 noVNC 客户端观看模式。点击 **接管操作** 后，服务端持久保存成员暂停状态，停止后续调度，等待当前回合自然结束；只有执行正常结束、环境归当前页面接管时，才开放鼠标和键盘。等待期间可取消接管。存在异常执行、Session 阻断或其他页面正在接管时，不开放输入。多个有效成员绑定共用同一环境网关及 VNC 连接时暂不支持接管，应使用成员独占环境。

可直接在远端网页完成登录，不需要把账号密码发到聊天。电脑可点击画面后使用键盘；也可以点击画面内的 **键盘** 后输入或粘贴文字，手机使用此入口唤起软键盘。临时输入不会写入 TeamX 消息或接管事件，不自动同步系统剪贴板。

点击 **完成接管** 后收回输入，向该成员投递一条固定的完成事件，提示重新观察当前页面并继续任务。仅恢复接管前正在启用的成员；原本已暂停、接管期间被另行暂停或恢复失败的成员保持暂停。完成事件按接管 ID 去重，丢失响应后可以用原 ID 重试，不会重新发送登录输入，也不会从中断处重放原工具调用。

关闭、切换侧栏、切换会话或页面暂停会收回输入、释放连接并取消接管，成员保持暂停；需要时可在成员设置中启用。页面定期核对接管归属；失联最多 20 秒后接管租约过期，服务重启也不会自动恢复成员。环境和浏览器本身继续运行。服务端限制同时最多 8 条画面连接，并在成员移除、会话身份失效或退出登录后结清对应连接。

操作连接须经过服务端接管状态核验；普通观看仍使用 noVNC `viewOnly`。上游 VNC 是部署方信任的桌面，不能把客户端观看模式理解为对管理者实施的 RFB 协议只读隔离。

传统 AIO 1.x、官方 `aio-daemon` 和 `aio-computer` 预制镜像均可使用 noVNC；迁移时重新核对网关地址、认证、路径以及 VNC 与 Agent 是否连接同一个显示环境。裸 `aiod` 仅提供 API，需要部署方另配 VNC 网关。此功能不依赖截图导出补丁，也不管理环境的创建、启动或回收。

可选真实画面验收使用独立测试 Room，Agent 执行由受控替身提供，不调用真实模型，也不导航或修改上游页面：

```sh
TEAMX_ENVIRONMENT_CONFIG=/绝对路径/agent-mcp.json \
NODE_PATH="$PWD/packaging/verifier/node_modules" \
go test -tags=e2e ./internal/room -run '^TestEnvironmentBrowser$' -count=1 -v
```

配置文件须满足上述私有权限要求，并包含一个 `environment`。可通过 `TEAMX_ENVIRONMENT_ARTIFACTS` 指定本地截图目录。

人工输入验收另设显式开关，使用临时 HTTP 表单和虚构账号，Agent 执行仍为受控替身。AIO 浏览器须可访问宿主的 `host.docker.internal`（其他部署可通过 `TEAMX_ENVIRONMENT_FIXTURE_HOST` 指定可达宿主）：

```sh
TEAMX_ENVIRONMENT_CONFIG=/绝对路径/agent-mcp.json \
TEAMX_ENVIRONMENT_INPUT_BROWSER=1 \
NODE_PATH="$PWD/packaging/verifier/node_modules" \
go test -tags=e2e ./internal/room -run '^TestEnvironmentHandoffBrowser$' -count=1 -v
```
