# AIO Computer 打包与部署

TeamX 的可选电脑运行环境由固定 AIO 基础镜像与当前源码构建的 `teamx-computer` 组成。普通 TeamX 构建、安装和启动无需此容器。正式部署使用下文的受管 Runtime，两个锁文件与辅助程序必须配套更新。使用范围与升级步骤见 [电脑接入说明](../../../docs/agent-mcp.zh.md)。

## 本机 Docker 托管

TeamX 服务直接运行在 macOS／Linux 宿主、Docker 已安装并运行时，可在「设置 → 电脑」选择「本机 Docker 托管」，打开全局开关并保存。服务自动准备固定派生镜像、生成私有密钥、创建命名卷及容器、等待管理合同通过；不需要执行下文的手动 Docker 命令。关闭全局电脑后先完成 screen／输入／Session 结清，再确认容器退出；再次开启复用原卷、密钥及 profile。

镜像仓库为 `ghcr.io/go-aie/teamx/computer`，安装版本固定的引用、registry manifest digest 和 config digest 编译在[镜像锁](../../../internal/computer/docker/image.lock.json)中；拉取使用 manifest digest，兼容 Docker 的传统与 containerd 镜像存储。现有手动连接缺省不变；托管模式不接管既有试验或日常容器，不复制旧 Cookie。

仅宿主 TeamX 服务需要操作本机 Docker 的权限，socket 不进入 Computer。托管容器固定 `--init`、`restart=no`、`no-new-privileges`、1 GiB shm；默认 4 CPU／6 GiB，可在首次创建前调整。两个命名卷沿用 `/var/lib/teamx-computer` 与 `/workspace`，只发布 loopback 管理端口，端口由 Docker 分配。CPU／内存创建后暂不可改；screen 运行上限仍在 TeamX 在线管理。服务进程退出不等同于全局停用，正常停用完成后不再要求 Docker 在线。

镜像与本机生命周期的当前验收、发布待办和平台限制见[实施记录](../../../docs/implementation/computer/docker-lifecycle.zh.md)。当前基线仍是 `linux/amd64`，Apple Silicon 按模拟执行记录，不宣称原生 arm64 容量。

### 镜像候选与发布

2026-11-24 已推送固定候选 `runtime-a6ce189578019f83`，远端摘要与镜像锁一致；当前 GHCR 包仍为 private，匿名访问返回 HTTP 401，公开分发尚未就绪。发布回执、后续核验与 Linux 宿主边界见[发布记录](../../../docs/implementation/computer/docker-publication.zh.md)。

维护者使用下列独立流程准备预构建产物；普通用户不需要本地编译或构建镜像。构建默认不会发布，也不会更新已采用的运行实例：

```sh
python3 scripts/computer_image.py build --output .local/computer-image-candidate
```

候选包含源码／二进制摘要、构建日志、镜像元数据和 `image.lock.json`。经复核后，用候选锁文件显式发布：

```sh
python3 scripts/computer_image.py publish \
  --lock .local/computer-image-candidate/image.lock.json \
  --output .local/computer-image-candidate/publication.json
```

发布命令只推送锁定的候选引用，使用现有 Docker 登录，不读取或打印访问令牌。回执记录实际 registry digest，并生成 `published-image.lock.json`；核对公开可见性、匿名拉取及真实验收后，将该锁纳入 TeamX 版本构建。初次 GHCR package 可能缺省私有，须在发布采用前完成公开访问核验。仓库独立的「Computer Runtime image」手动 workflow 也可构建；只有明确选择 publish 才登录并推送，不增加普通 Release CI 的模型或浏览器依赖。

显式本机生命周期验收以及浏览器／可选双真实模型组合：

```sh
TEAMX_COMPUTER_DOCKER_REAL=1 TEAMX_COMPUTER_EVIDENCE="$PWD/.local/computer-docker-proof" \
  go test ./internal/room -run '^TestComputerRealManagedDockerLifecycle$' -count=1 -timeout=12m -v
python3 scripts/computer_managed_acceptance.py --run --output .local/computer-managed-browser
```

第二条可加 `--agent-config /absolute/path/to/config.toml` 运行真实 DSH／模型；不提供时 Agent 为受控后端，Runtime、Docker 和浏览器仍为真实实现。验收仅创建一次性归属资源，结束后显式删除这些测试容器／卷；生产停用只停止并保留数据。

## 受管 Screen Runtime

正式底座与文末的历史原型分开构建：入口为 `cmd/teamx-computer`，构建模板为 [Dockerfile.runtime](Dockerfile.runtime)，实现与恢复合同见[实施记录](../../../docs/implementation/computer/managed-runtime.zh.md)。它提供认证管理服务，原生工具仅经 TeamX 的成员身份、执行记录和准入路径发布，不向 Agent 暴露裸上游端口。

默认 Go 构建／测试不需要容器。显式真实验收：

```sh
python3 scripts/computer_runtime_acceptance.py --run \
  --output .local/computer-runtime-acceptance-fresh
```

可选 Runtime 单独打包，普通 TeamX 发布包不携带或启动它。以下打包只需 Go 与 Python，不需要 Docker：

```sh
python3 scripts/package_computer.py \
  --output .local/teamx-computer-runtime.tar.gz
```

归档包含静态 `linux/amd64` 辅助程序、固定锁文件、Dockerfile 和 `source.json`（源码／二进制摘要、Go 版本、Git revision 与工作区修改标记）。输出必须不存在；打包不会覆盖已验收产物。校验和文件中的文件名相对归档目录解析。

在归档所在目录核验后解包并构建：

```sh
cd .local
shasum -a 256 -c teamx-computer-runtime.tar.gz.sha256
tar -xzf teamx-computer-runtime.tar.gz
docker build --platform linux/amd64 -t teamx-computer:managed teamx-computer-runtime
```

以下为部署方手动启动示例。`/absolute/private/computer-token` 应是提前生成、只允许所有者读取的随机密钥文件（至少 32 字符），不会作为环境变量或命令参数值发送给 screen。容量是运行上限；持久卷备份必须同时覆盖 Runtime 元数据和 profile。

```sh
docker create --init --platform linux/amd64 --name teamx-computer \
  --cpus 4 --memory 6g --shm-size 1g --security-opt no-new-privileges \
  --mount type=volume,src=teamx-computer-state,dst=/var/lib/teamx-computer \
  --mount type=volume,src=teamx-computer-files,dst=/workspace \
  --publish 127.0.0.1:18090:18090 --entrypoint /bin/sh teamx-computer:managed -c \
  'chown 0:0 /run/computer-token && chmod 600 /run/computer-token && exec /usr/local/bin/teamx-computer -listen 0.0.0.0:18090 -token-file /run/computer-token -max-screens 2'
docker cp /absolute/private/computer-token teamx-computer:/run/computer-token
docker start teamx-computer
```

上述启动命令会先修正复制文件可能保留的宿主 UID，辅助程序只接受 root 持有的私有密钥文件。必须使用 `--init`；辅助程序据专属 UID 和所有保留端口证明单屏停止，孤儿进程需要 init 回收。内部 VNC、CDP、worker、browser MCP 和 aiod 都只监听容器 loopback，不发布端口。宿主只访问认证管理入口；不要发布这些原始端口或挂载 Docker socket、个人 Home、TeamX 数据根及宿主成员 Workspace。非本机管理连接应在部署层配置 HTTPS。容器可以在 Computer 未使用时不部署，不是 TeamX 安装或启动依赖。

profile 保留在 `/var/lib/teamx-computer/profiles/<screen_id>`；停止／再次 Ensure 使用相同 profile 和新 epoch。删除容器时需要保留上述数据卷，成员／Room 删除会核验单屏停止并保留 profile 待清理记录，不自动删除数据卷。Chromium 仍使用固定构建已验证的 `--no-sandbox`，不承诺同容器内的恶意代码隔离；管理凭据和 Runtime 元数据受 root 文件权限保护。

## TeamX 在线接入

部署好 Runtime 后，在 TeamX“设置 → 电脑”填写管理地址和密钥，测试或直接保存并应用，无需修改 TOML 或重启 TeamX。全局缺省关闭。保存应用后，在成员档案打开「启用电脑」，即可经统一 MCP 使用网页／桌面工具，在右侧「电脑」观看、接管和交还。每成员独立 profile；文件交付与 MCP search 尚未提供。

当前 Runtime 包含 owner inventory、member admission、控制租约、服务端只读 RFB 和输入退出 Fence 合同；应从当前源码重建受管镜像，旧二进制缺少合同会在核验时被拒绝。普通 TeamX 构建和默认回归仍不需要 Docker、镜像或辅助程序。连接替换、恢复与真实验收方法见[在线连接管理记录](../../../docs/implementation/computer/online-connection.zh.md)。

## 发布与组合验收

始终从固定 digest 构建，记录产物的 image ID／digest 和源码摘要。不要用浮动镜像覆盖正在使用的实例。备份两个持久卷及宿主 Computer 状态／私有凭据；更新同实例保留数据卷，工具合同变化由 TeamX 结清和轮换。旧试验容器不自动迁移 Cookie，也不自动销毁。

```sh
python3 scripts/computer_view_acceptance.py --run --group \
  --agent-config .teamx/config.toml \
  --output .local/computer-combined-acceptance
```

此命令使用一次性容器和虚构站点，显式调用真实模型，运行两个 DSH Agent 与 Chromium 接管交还；模型费用按已配置服务计。未传 `--agent-config` 时 Agent 后端为受控替身，应分开标记证据。普通回归不会因检测到 Docker 而自动运行真实验收。

## 历史可行性原型

以下原型保留供复验，不用于日常接入。验收结论见 [历史记录](../../../docs/implementation/computer/runtime-feasibility.zh.md)。

在仓库根目录显式运行：

```sh
python3 scripts/computer_runtime_feasibility.py --run \
  --output .local/computer-feasibility-fresh
```

输出目录必须不存在。脚本从 [固定依赖](dependencies.lock.json) 对应 digest 构建派生镜像，创建随机名称的隔离容器和两个专用数据卷；不映射宿主目录、不发布端口、不挂 Docker socket。运行条件为 4 CPU、6 GiB 内存、1 GiB shm、默认 Docker seccomp、`no-new-privileges`，不需要 privileged。候选镜像是 `linux/amd64`；在 ARM64 上运行属于模拟执行。测试结束停止并核验本次容器，删除本次容器与卷，留下证据和构建缓存。`--keep-on-failure` 仅供调试，失败报告会列出待清理的准确资源名称。

原型替换上游默认单屏 entrypoint。`prototype/screens.py` 用两个固定专属 UID 启动各自的 Xvnc、XFCE、D-Bus、Xauthority、XDG runtime、Chromium、CDP、aiod、computer-use 和 browser MCP；所有操作端口只监听容器 loopback。profile 存于 `/var/lib/teamx-computer/{A,B}`，交付目录为 `/workspace/agents/{A,B}/downloads`，共享目录为 `/workspace/shared`。共享目录使用共同组、setgid 和协作 umask，profile 的父目录保持私有。PID 1 回收孤儿进程；单屏停止按专属 UID 检查并清除整个范围，SIGTERM 后仍存活的进程升级为 SIGKILL，再核验进程与端口均消失。X11／ICE 公共 socket 目录由 root 以 1777 创建；仅在对应 UID 的进程清零后清理其残留 ICE socket，防止重启后的 PID 重用阻止 XFCE 启动，并保留另一屏的活动 socket。

Chromium 当前使用 `--no-sandbox`，同一容器是共同信任范围；该原型不能证明恶意代码隔离。浏览器使用独立 profile、基本密码存储和持久 Cookie，关闭背景页节流以保持页面 MCP 在标签页切换时可用。正式部署的 Chromium sandbox／凭据保护须在运行底座阶段明确验证。

`prototype/view.py` 只证明 RFB 3.8 的服务端只读条件：允许像素格式、编码列表和画面更新请求；键盘、鼠标、剪贴板和未知扩展一律断开。它没有生产认证、WebSocket、租约或接管协议，不能作为对外网关使用。原始控制端口仅由容器内验收客户端使用。

[工具锁](tools.lock.json) 保留选中工具的完整原生定义，并给出复合 GUI 工具未来的动作准入集合；原型没有对 Agent 发布工具。完整实际 `tools/list` 随验收证据保存，通用 shell／code、任意文件操作、浏览器服务关闭和 JavaScript evaluate 均未进入选中集合。`verify.py` 对原生调用不自动重试；就绪等待仅重查工具清单和页面文本。正式唯一调用路径、执行记录、去重、fencing、Drain、恢复和工具发布属于后续阶段。
