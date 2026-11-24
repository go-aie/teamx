# 共享 Computer：双屏运行可行性

原始日志、截图和 JSON 回执仅在本地保存；本文保留验收结论、复跑入口及已知限制。

2026-11-23，TeamX `fd2558b` 工作树。[实施方案](../../SHARED-COMPUTER-INTEGRATION-PLAN.zh.md)的 **C0 已通过**。最终固定构建完成 17 项检查、131 次原生 MCP 调用，覆盖全部 21 项选中工具。真实容器、Chromium、computer-use 和 RFB 均参与验证；仅使用容器内虚构站点和账号。

本次交付为独立多屏原型、固定派生镜像模板、工具合同和可复跑证据。正式 Provider／Screen 管理服务、全局设置、Agent／DSH 接入、右侧接管界面及文件交付仍按后续阶段推进。

## 固定构建与复跑

| 项目 | 实际构建 |
| --- | --- |
| 上游镜像 | `enterprise-public-cn-beijing.cr.volces.com/vefaas-public/aio-computer:1.0.1`，固定 digest `sha256:5c5a6949718f984cf60fec02d19436a0738d81d113e782f366a03936640f5f1d` |
| 最终派生镜像 | `sha256:7f0d87bbdbcc9373659273b8d3e16f9e8f00d4a1d1c8740873f6effc3e4c9f68` |
| 实际组件 | aiod／computer-use `0.9.2`；Chromium `146.0.7680.31`；browser MCP `1.2.29`；TigerVNC `1.12.0`；XFCE session `4.16.0` |
| 平台 | 镜像 `linux/amd64`，本机 `arm64`、Docker Desktop／Rosetta 模拟运行；未验证原生 ARM64 构建 |
| 运行限制 | 4 CPU、6 GiB 内存、1 GiB shm；默认 Docker seccomp、`no-new-privileges`；无 privileged、宿主目录挂载、Docker socket 或宿主端口发布 |

完整版本和二进制摘要见[依赖锁](../../../packaging/computer/aio/dependencies.lock.json)，实际运行与清理结果见运行报告。源码摘要与最终工作树一致。上游 digest 固定内容；派生镜像另记录本次实际构建 digest。

在仓库根目录运行，输出目录须为新目录：

```sh
python3 scripts/computer_runtime_feasibility.py --run \
  --output .local/computer-feasibility-fresh
```

入口只在显式 `--run` 时调用 Docker，创建随机命名的隔离容器及专用 profile／文件卷，完成桌面重建、容器重启和停止核验后删除本次容器与卷。调试参数和目录说明见[原型说明](../../../packaging/computer/aio/README.zh.md)。普通构建、Go 测试和 TeamX 启动均不依赖该容器。

## 多屏组成与验收结果

| 绑定 | A | B |
| --- | --- | --- |
| 专属 Unix UID | 2101 | 2102 |
| display | `:11` | `:12` |
| VNC 控制／只读端口 | 5911／5931 | 5912／5932 |
| CDP／browser MCP | 9223／18211 | 9224／18212 |
| aiod／worker | 18311／18411 | 18312／18412 |
| profile | `/var/lib/teamx-computer/A/profile` | `/var/lib/teamx-computer/B/profile` |

全部端口仅监听容器 loopback。每屏各自运行 Xvnc、XFCE、D-Bus、Xauthority、XDG runtime、Chromium、aiod、computer-use 和 browser MCP。环境与进程清单、实际 CDP 页面 URL、worker 显示信息均写入功能证据。两屏的会话总线不同，GUI 图片和对应 VNC 画面的采样像素全部相同，固定背景色也分别匹配 A／B。

| C0 必验项 | 最终结果与证据 |
| --- | --- |
| 同容器两组完整会话同时运行 | 通过；独立 UID、进程、D-Bus、CDP、GUI、VNC 清单与画面一致 |
| 并行输入、导航／点击，无焦点或页面串用 | 通过；A 通过 GUI 输入随机标记时，B 同时通过页面工具填入自己的标记并点击计数器；执行区间重叠，两侧正文不含另一侧标记 |
| 标签页与下载各自独立 | 通过；两屏建立不同 private URL，下载各自固定负载，校验目录、内容和 SHA-256，无另一侧下载文件 |
| 共享目录协作 | 通过；A 创建文件、B 回读并追加，使用共同组、setgid 和协作 umask |
| 独立登录与持久恢复 | 通过；两个浏览器访问同一 origin，A 登录后 B 仍为匿名；分别登录 `fictional-A/B`。A 单独重建及整个容器正常重启后，均恢复自己的 Cookie 登录 |
| 原生页面／GUI／截图 | 通过；全部 21 项选中工具实际调用成功；截图为真实可解码图片块，包含 1280×900 桌面图像 |
| 人工控制 A，B 继续 | 通过；真实 RFB 控制连接向 A 输入，B 同时完成页面 MCP 操作；A 的鼠标移动也不改变 B 的指针 |
| 服务端观看只读 | 通过；只读 RFB 连接可取得完整画面，随后发送键盘、鼠标、粘贴和未知扩展报文均被服务端断开；A 正文及指针未改变。原始控制连接作为键盘输入的正向对照 |
| 单独停止 A 的完整进程范围 | 通过；GUI 的 F8 快捷键启动双重 fork、脱离会话且忽略 SIGTERM 的后台进程。停止后 A 的 31 个进程归零、后台 PID 消失，CDP／GUI／MCP／VNC 等六个端口均关闭；B 的原进程继续，并在停止期间完成 12 次页面调用 |
| 重建临时 socket | 通过；注入 A 的残留 ICE socket，进程清零后重建 A 将其清除，B 的活动 ICE socket 保留；容器重启后两屏桌面进程完整 |

容器重启证据见持久化验证。最终运行、重启及清理均无 OOM；清理记录为 `Running=false`、`Pid=0`、退出码 0，容器和两个专用卷均已删除。现有试验 Agent、配置和旧 AIO 容器未切换。

## 工具合同

完整原生清单实际有 **31 项**。两屏清单相同，选中 **21 项**；名称、描述和完整 Schema 保存在[工具锁](../../../packaging/computer/aio/tools.lock.json)，没有改写原生参数或添加别名。选中定义的确定性摘要为 `febe4aeeb66bb7910e9ca0394a3fc3a44adceac1d6ec97cd5c241559d9a2a249`。

| 用途 | 选中原生工具 |
| --- | --- |
| 桌面 | `browser_gui_screenshot`、`browser_gui_execute_action` |
| 导航与读取 | `browser_navigate`、`browser_go_back`、`browser_go_forward`、`browser_get_markdown`、`browser_get_text`、`browser_read_links` |
| 表单与页面操作 | `browser_form_input_fill`、`browser_click`、`browser_select`、`browser_hover`、`browser_get_clickable_elements`、`browser_scroll`、`browser_press_key` |
| 标签页 | `browser_new_tab`、`browser_tab_list`、`browser_switch_tab`、`browser_close_tab` |
| 下载与页面截图 | `browser_get_download_list`、`browser_screenshot` |

其余 10 项不进入首版集合，包括全部 `sandbox_*` 工具、`browser_get_info`、`browser_evaluate` 和 `browser_close`。GUI 复合动作的候选准入集合另存工具锁，后续受管调用层负责执行该准入，保持原 Schema。本原型没有向 Agent 发布工具，也没有建立生产调用旁路。变更工具调用不透明重试；就绪等待只重查目录、页面文本及进程。

当前验证到原生 MCP 图片块、实际图片及其画面对应关系。真实模型的图像理解、能力挂载／卸载、调用 ID、去重、取消与恢复仍属于 C1a／C2 的相应验收。

## 资源与边界

| 实测项 | 最终构建结果 |
| --- | --- |
| 容器启动至两屏实际就绪 | 20.05 秒 |
| A 单独停止并核验 | 5.16 秒，含 SIGTERM 等待及后台进程 SIGKILL |
| A 单独重建至登录恢复 | 8.35 秒 |
| 容器重启至双登录及画面验证结束 | 18.51 秒 |
| 功能阶段 CPU／内存 | 48.96 秒采样，平均 2.45 个 CPU 核，cgroup 内存峰值约 1.87 GiB |
| 重启阶段 CPU／内存 | 14.94 秒采样，平均 3.69 个 CPU 核，cgroup 内存峰值约 1.79 GiB |

原始有界采样见功能资源和重启资源。内存采用 cgroup 统计，包含缓存；这些数值来自 ARM 宿主上的 amd64 模拟执行和轻量虚构页面。容量结论限于两屏，原生部署、更高并发及重页面须按实际环境重测。

Chromium 使用 `--no-sandbox`，容器采用默认 seccomp 和 `no-new-privileges`。这里证明独立工作状态和按 UID 停止的可行性，同容器仍是共同信任范围。正式运行底座需继续验证管理凭据、准入、fencing 和停止证明。原型只读代理只实现容器 loopback RFB 3.8；认证、WebSocket、排他接管与租约在后续阶段实现。

## 发现的问题与回归

预跑记录见失败与修复证据。实际运行问题包括后台标签页查询超时、共享文件缺少组写权限，以及重启后残留 XFCE ICE socket 与 PID 重用冲突。最终配方补齐浏览器后台运行参数、协作 umask，并在 screen 进程清零后仅清除其 UID 的残留 ICE socket。最后一项已增加故障注入和 B 活动连接保留检查。

证据采集同时修正了命令截断、Rosetta 包装进程识别及浏览器提示栏引起的输入焦点偏移。失败预跑不计入通过次数；本报告的固定构建从全新容器／数据卷开始，完整通过功能阶段和重启阶段。

现有聊天复用、成员 MCP 配置隔离、History 只读 Schema／可见性、输入目录与投影共 6 项定向 Go 回归通过，命令和结果见回归记录。本次未修改 TeamX 的 Go／前端运行代码；C0 之外的正式管理服务和 Agent 生命周期验证继续由后续阶段交付。

实现依据：[上游启动说明](https://sandbox.agent-infra.com/daemon/start/quick-start)、[Computer Use](https://sandbox.agent-infra.com/daemon/basic/display)、[Browser API](https://sandbox.agent-infra.com/daemon/basic/browser)、[MCP 聚合说明](https://sandbox.agent-infra.com/daemon/examples/mcp)。上述文档用于定位接点；本报告的工具、版本和通过结果来自随附的固定构建实测。
