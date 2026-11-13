# P3：Linux 本地归档验收

> 实施记录：本文保留记录时的范围、结果与缺项，不作为当前操作指南。当前用法见[打包指南](../../../packaging/README.zh.md)及[正式发行操作指南](../../../packaging/releasing.zh.md)。

日期：2026-11-08。**Linux arm64 的本地 core 范围通过；Linux amd64 未验收，P3 完整矩阵未完成。** 与 macOS arm64 验收并行执行，使用独立源码快照、容器、产物目录和私有证据。范围沿用当前 P3 调整：core 归档、真实 DSH/ACP、生产页面的实际浏览器；Git、Scion、GitLab／飞书账号留待单独验证，P4 未实施。

## 环境与边界

- Docker Desktop 的 Linux arm64 虚拟机，内核 `6.12.54-linuxkit`，原生 `aarch64`，未使用 QEMU／Rosetta 架构模拟。
- Ubuntu 24.04.5 LTS／glibc 2.39，匹配 lock 中声明的 Ubuntu 24.04 基线。容器不等同于独立 Linux 物理机或完整桌面系统验收。
- 构建使用固定 Go 1.27.1、Node 24.22.2 和目标平台独立 npm 缓存。Go、Playwright 和 Chromium 属于外部验证器；服务与 DSH 的受限 PATH 只提供包内运行时和明确列出的系统工具。
- 当前无原生 Linux amd64 环境，amd64 未组包／未验收，不能由 arm64 结果推断通过。
- Linux full 仍由 P2 的 `git_tls_runtime` 阻塞，本次 core 验收不消除该缺项，也不证明正式发行资格。

## 验证入口

在满足声明基线的原生目标环境，先通过公共 `package.sh` 生成 core 归档，再执行：

```sh
./scripts/package-acceptance.sh /path/to/linux-arm64-core.tar.gz \
  --output-dir /path/to/new-private-evidence \
  --model-config /path/to/private-config.toml \
  --env-file /path/to/private.env
```

浏览器验证机需安装 Playwright／Chromium；可通过 `NODE_PATH` 和 `PLAYWRIGHT_BROWSERS_PATH` 指向隔离安装。模型配置只用于独立测试 Room，不沿用现有 Room、连接和 DSH 路径。每次重跑使用新的证据目录。

## 当前结果

最终独立一轮的顶层 `status=passed`、退出 0；`release_ready=false`。私有证据为 `.packaging/p3-linux-arm64-final-02/`，运行环境额外记录在该目录的 `environment.json`；构建及前序失败记录在 `.packaging/p3-linux-setup/`。

| 检查 | 结果 | 证据与范围 |
| --- | --- | --- |
| 最终归档离线 smoke | 通过 | 普通用户，受限 PATH；摘要、权限、链接、运行时、初始化、Room、信号、锁、只读包及单独标明的 ACP fixture |
| 真实 DSH/ACP | 通过 | 包内 Node 24.22.2／DSH 0.2.2-rc.1；真实模型、CLI 工具读写、私有记忆、前台运行工具取消、取消后继续、关闭、原 Session 重启后新回合及记忆恢复 |
| 进程与锁收尾 | 通过 | 取消前观察实际工具进程身份；取消后工具消失，关闭后 DSH 消失，目录锁释放，未要求重置 Session |
| 实际浏览器 | 通过 | Playwright 1.59.1／Chromium 147.0.7727.0；生产 UI、建／开 Room、桌面／窄屏、独立 home、配置／模板保留、两种信号退出与重启持久化；截图已人工查看 |
| 不可变性与证据绑定 | 通过 | 同一轮归档及包内文件未变；报告绑定 manifest、依赖指纹与验证器摘要，11 项验证器输入与当前源码一致 |
| Linux amd64、full、真实平台、完整 Linux 主机 | 未验收 | 不由本轮结果推断；没有远端平台写入或正式发行 |

最终浏览器步骤发送模型 prompt 0 次、远端平台写入 0 次。真实模型步骤仅操作独立验收 Room。

归档：`dist/p3-linux/teamx-dev-f8fe4031063e-dirty-linux-arm64-core.tar.gz`。

SHA-256：`8bf4c824c7e6fe49cf41cc8817889de6e06d9311c0f3c04d467d0429a9dffef2`。

初次普通用户运行的私有证据保存在 `.packaging/p3-linux-arm64-01/`：离线 smoke 通过；真实模型首回合因无可用沙箱而未推进已读位置，整体 `incomplete`；该轮浏览器安装尚未结束，也未通过。包内 Landlock 实际探针退出 125，报告内核未启用该能力；默认 Docker seccomp 下用户命名空间探针返回 `Operation not permitted`。不能将这个环境限制解释成 DSH 工具或取消／恢复通过。

仅放开 seccomp 的第二轮仍无法通过 bubblewrap 的实际探针：Docker 的默认系统路径遮蔽使其挂载沙箱 `/proc` 时返回 `Operation not permitted`。证据保存在 `.packaging/p3-linux-setup/round02-evidence.tar.gz`。

最终复验在独立容器中显式使用 `--security-opt seccomp=unconfined --security-opt systempaths=unconfined`，安装系统 bubblewrap 0.9.0，仍以普通用户和 DSH `workspace-write` 执行。实际验证 `privileged=false`，系统路径遮蔽列表为空，bubblewrap 的完整只读／PID 命名空间／proc 探针退出 0。上述配置只调整容器外层前提，不更改 DSH 的沙箱权限；不能称默认 Docker 或任意 Linux 均通过。

## 验证器修正与回归

- 按 OS 识别原生 arm64 内核名：Linux 为 `aarch64`，macOS 为 `arm64`。先修改隔离副本，确认并行 macOS 验收结束后才更新共享入口。
- Linux 的受限 PATH 在系统安装 bubblewrap 时保留 `/usr/bin/bwrap` 和其功能探针依赖的 `/usr/bin/true`；继续允许包内 Landlock 作为另一种后端，不导入宿主 Node／Go／Git。已用实际探针复现缺少 `true` 时的 `execvp true: No such file or directory`，第三轮完整证据保存在 `.packaging/p3-linux-setup/round03-evidence.tar.gz`。
- bubblewrap 使用独立 PID 命名空间。取消检查在工具运行时读取命名空间 PID，并从 DSH 子进程树和 `/proc/<pid>/status` 定位实际进程身份；取消后核对该身份消失，避免把沙箱中的 `$$` 当成容器外 PID。
- 新增进程观察回归，验证实际存活的子进程、错误祖先和退出后的身份；与既有验收证据拒绝测试一起在 macOS、Linux arm64 通过。打包校验 JS 测试 9 项通过。
- 浏览器读取 Room 目录时校验 HTTP 状态及响应结构，仅对明确的 `409 busy` 做有期限的只读重试，并记录重试次数。其他错误继续使验收失败，避免对错误响应直接访问 `rooms.some` 而丢失诊断。

第三轮浏览器完整通过，报告实际记录 Chromium `147.0.7727.0`，并观察到一次 `409 busy` 重试；真实 DSH 因上述缺少 `true` 的验证器环境问题失败，故该轮整体仍为 `incomplete`。不会将不同失败轮次的子项拼成顶层通过报告。

只修改外部验证器和测试驱动；本次新归档的应用、运行时和打包输入在验收期间保持固定。上述变更不重写既有 macOS 报告，也不冒充该报告执行过新验证器。
