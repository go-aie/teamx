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
