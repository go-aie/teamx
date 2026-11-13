# 统一安装入口

日期：2026-11-13。普通用户通过一次 `curl …/install.sh | bash` 完成安装与常驻启动，之后统一使用 `teamx service`。当前操作见[安装与运行](../installation.zh.md)；底层部署、备份与生命周期仍复用[常驻部署](server-deployment.zh.md)。

## 实现

- `packaging/install.sh` 兼容 macOS 自带 Bash，识别四个构建目标，默认下载 standard。正式发行脚本固定版本与仓库，归档和摘要从同一 tag 获取；源码和随包脚本可通过 `--archive` 离线安装。
- 先验证 SHA-256，再用系统 tar 将唯一的 `bin/teamx` 内容写入私有临时目录。引导程序内部执行现有 `teamx service install`，完整归档的清单、平台、权限、链接、版本发布、home、系统服务与认证检查沿用 Go 实现。无需执行临时包的 `start init`，也不恢复下载目录的扩展属性；不修改全局 Gatekeeper。
- Linux 按需使用 sudo，创建或复用专用普通账号。私有下载目录使用 0700；调用部署时使用 umask 022，使程序目录可由普通服务账号遍历，home 和凭据沿用各自的私有模式。
- 部署成功后才建立指向 `<server-root>/current/bin/teamx` 的命令链接；无关同名文件拒绝覆盖。macOS 默认命令目录为 `~/.local/bin`，配置 shell PATH 并给出当前终端生效方式；Linux 为 `/usr/local/bin`，管理时使用 sudo。
- 安装后的 CLI 从真实可执行路径发现所属安装根，自定义根和 current 切换后均不必反复填写 `--server-root`。显式参数仍可选择目标。新增 `teamx service --help`，安装结果输出实际浏览器地址与私有管理 token 文件路径，不打印 token 内容。
- `bundle-files.json` 将安装器纳入归档；正式发行额外导出固定版本的安装器，纳入 `SHA256SUMS` 和 `release.json`，发布前核对它与同一源码／版本／仓库一致。原候选归档不会为此重新打包。
- README、安装指南、Server 架构、打包／发行说明、模型首启与默认助理说明统一到新流程。终端前台运行只在开发说明保留，不再作为普通用户的并列安装选项。

## 验证

- `bash -n packaging/install.sh`、`node scripts/check-packaging.mjs`、`git diff --check` 通过。
- `node --test scripts/*.test.mjs`：58 项通过。最初有一项观察工具测试因沙箱禁止本地监听而失败，允许本地监听后完整通过。
- 随后补充默认目录和隔离属性场景，`node --test scripts/package-install.test.mjs`：9 项通过。覆盖管道执行、四目标选择、固定 tag 下载、离线安装、SHA 不匹配／缺失／重复、下载失败、空引导文件、不覆盖无关命令、安装失败不发布命令、重复安装不重复写 PATH、包含引号／美元符号／反引号／空格的路径、Linux 新账号／已有账号和 root／sudo 分支。
- macOS 隔离属性检查使用带 quarantine 的本地 fixture 归档；确认原下载文件标记保持不变，部署后的 fixture 可执行文件未继承该标记。
- `go test ./internal/room -run 'Test.*(CLI|Help|Service|Bundle)' -count=1`：当前工作区通过。覆盖服务／归档既有回归，以及命令链接随 current 切换、自定义根发现、帮助入口、实际地址和自定义 token 路径提示。验证期间曾被并行飞书改动的缺失方法阻断，隔离副本先通过；该阻塞消除后已在当前工作区通过同组回归，未修改其他工作。

## 验收边界

安装器测试使用真实 Bash、tar、摘要、文件和符号链接操作；下载、系统身份、Linux sudo／账号准备和部署动作使用 fixture。Go 回归不等于本次已重新安装真实 LaunchAgent 或 systemd 服务。带 quarantine 的脚本 fixture 也不等于真实 Mach-O／Gatekeeper 验收。

本次未发布 Release、未改动本机常驻服务或真实账号。公开 `latest/download/install.sh` 需在包含本次改动的正式 Release 发布后验证。真实联网下载、原生服务安装、PATH 生效、`teamx service status/stop/start` 以及既有平台／长期观察门槛仍须按[发行指南](../../packaging/releasing.zh.md)验收，不扩大发行支持范围。
