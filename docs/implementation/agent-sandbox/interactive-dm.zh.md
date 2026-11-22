# AIO 浏览器测试私信

2026-11-22，按用户要求，在当前 TeamX 中创建 **AIO 浏览器测试** 私信，供手动发消息测试浏览器和网页搜索。这是本地 DSH 连接远端浏览器的交互环境；DSH 仍在宿主机，不代表 S2 容器内 DSH 或远端生命周期验收已经完成。

## 运行配置

- 使用 S1 相同 digest 的 ARM64 AIO 1.11.0 镜像，专属容器 `teamx-aio-dm-79c2234ae0`，2 CPU、3 GiB 内存、512 MiB 共享内存，显式使用 `seccomp=unconfined`。
- 独立 Docker bridge 网络允许公网访问；HTTP 入口仅发布至宿主 `127.0.0.1:18080`，使用独立密钥。TeamX 通过 [运维 MCP 绑定](../../agent-mcp.zh.md) 只为该完整成员身份追加 `sandbox`，保留 `teamx` 输入和历史 MCP。密钥位于 Git 忽略的私有 `0600` 配置中。
- `/mcp` 指向 AIO 自带的 browser MCP，共 21 个浏览器工具；不向模型暴露 Hub 的 Shell、文件和代码工具。
- 将该私信独立 Workspace 挂载为容器 `/home/teamx/workspace`，使用与宿主一致的 UID 501。仅有这一处宿主目录挂载，未挂载 Docker socket、TeamX 状态或个人目录。
- 浏览器下载写入 `downloads/`；已安装 [截图导出补丁](../../../scripts/agent_sandbox_browser_artifacts.py)，`browser_screenshot` 自动写入 `screenshots/` 并返回相对路径。截图文件限定为 PNG、16 MiB 和 2500 万像素以内；原 MCP 图片块保留。

与 S1 固定发布包验收有一项区别：这里沿用当前 TeamX 已配置的 Node 26.7.0、开发工作区 DSH 0.2.1-alpha.1，以及 `deepseek-v4.1-flash-1017`。实际运行摘要及源码摘要另行记录，不将其描述为 S1 发布包的重复验收。

## 已核验

1. 当前 TeamX 的 Agent 正式通过 `teamx` MCP 读取测试私信，再通过 `sandbox` 浏览器执行公网搜索、读取标题和结果并截图；最终通过 TeamX CLI 在同一个私信正式回复结果和相对 PNG 链接，服务端消息序号为 2。
2. 截图确实写入该成员 Workspace 的 `screenshots/`；已验证容器 UID 与宿主进程可以读写同一挂载目录。
3. 在配置的公网 TeamX 地址使用真实 Chrome 验证：右侧 **文件 → screenshots** 可加载 Agent 生成的 PNG，正式回复的 **截图** 链接也能打开同一张图片。尺寸 1280 × 937、169,646 字节，HTTP 内容 SHA-256 与共享目录文件一致，页面无 JavaScript 错误。
4. 完整 `go test ./...` 回归（Room 包 404.497 秒）、定向 race、配置文件权限/身份隔离测试通过；恢复 Session 时保留两个 MCP 且 TeamX 凭据独立刷新。另扫描该私信的持久状态及压缩 Session，未发现 AIO 密钥。
5. TeamX 已正常关闭原服务并部署新版本，当前服务健康，原有 9 个会话和新增测试私信均保留。

机器摘要见 [interactive-dm-evidence.json](interactive-dm-evidence.json)。原始运行材料、公开搜索轨迹、Agent 工具轨迹、Workspace 截图及浏览器 UI 检查材料放在 `.local/agent-sandbox/live-dm/`，未提交真实密钥或其他会话内容。

## 使用与维护

刷新 TeamX 后进入 **AIO 浏览器测试**，可以直接让 Agent 打开网页、搜索、点击和截图。截图可从消息链接打开，也可进入右侧 **文件 → screenshots**；下载文件在 **downloads**。

该私信的 **环境** 面板可直接观看同一 AIO 桌面：点击会话顶部的显示器图标，或在会话菜单选择 **环境**。需要人工登录时点击 **接管操作**，等待当前 Agent 回合结束后在画面中输入；登录成功后点击 **完成接管**，让 Agent 从当前页面继续。接管期间关闭面板或断线会让 Agent 保持暂停，可在成员设置中启用。配置方式见 [右侧环境说明](../../agent-mcp.zh.md#在右侧观看环境)，实现与验证见 [环境画面记录](environment-viewer.zh.md)。

容器为这次交互测试保留运行。容器名、ID、网络和挂载信息在私有运行目录的 `container.json` 中；TeamX 服务日志为 `teamx-service.log`。本次 TeamX 从原前台进程重启为后台进程，入口仍是 `bin/teamx serve --config .teamx/config.toml`，私有数据目录不变。回退用的原二进制及配置保留在同一运行目录。

测试环境采用固定挂载，不自动随 Workspace 设置变更；浏览器登录态留在容器内部。普通 TeamX Session 结束不会停止该浏览器环境。结束测试时，由部署方核验并停止专属容器、清理网络；如移除 MCP 绑定，应先结清并轮换该 Agent Session，再重启服务，避免将旧 Session 的工具配置当作仍兼容。
