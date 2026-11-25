# Linux standard 的 SRT 打包与安装

日期：2026-11-24–25。范围：空间方案 **S5.0 的交付机制**。Linux amd64 standard 追加可选 SRT 资源，安装、同组合升级与归档检查共用既有入口。首发候选最终冻结仍等待所选 S3／S4 的真实验收出口；正式版本多用户门槛保持关闭，不声明 S5a／S5b 或生产支持。

2026-11-25 阶段交接：S5a 本轮开发实机部署与人工环境交付已收尾，开发包 alpha 环境保留，完成内容／未完成项见[交接记录](space-deployment.zh.md#阶段交接2026-11-25)。后续构建与服务器操作见 [Linux 打包、部署与升级](../linux-deployment.zh.md)；下文是本单元原始证据，不替代最终候选资格。

2026-11-25 的 S5a 增量另见[空间部署与恢复验收](space-deployment.zh.md)：宿主合同增加 Ubuntu 22.04 的独立固定工具组，并修复普通核心读取安装登记的权限和首次组合配置入口。下文保留 S5.0 当时 Ubuntu 24.04 开发归档的原始证据，不能据此覆盖新产物或实机验收。

## 固定输入与宿主前置

保持 standard 的 TeamX、Node `24.22.2`、DSH `0.2.2-rc.1`、模板、glab `1.127.0`、lark-cli `1.0.99`。仅 Linux amd64 standard 增加 SRT `0.0.83`；其他目标、core／full 及个人原生路径不新增 SRT 运行前置。

[固定资源合同](../../packaging/srt/runtime.lock.json)引用与 `internal/agent/srt` 完全一致的 npm lock，完整依赖使用包内 Node/npm `ci --omit=dev --ignore-scripts` 安装并校验 integrity。监督代码仍由 TeamX 内嵌，代理依赖随完整 npm 树交付；许可证及依赖清单进入归档。构建／安装不复制实验目录、开发者 home 或私有状态，启动不安装 `latest`。lock、实际源码、打包脚本及公开文件摘要进入 manifest，SRT 目录进入归档 inventory 和独立运行时摘要。

**辅助程序明确选择固定宿主前置**：Ubuntu 24.04 amd64／glibc 2.39，bubblewrap `0.9.0-1build1`、socat `1.8.0.0-4build3`、ripgrep `14.1.0-1`。合同列出固定 `.deb` 的 HTTPS 来源／摘要、实际程序摘要及系统库要求；安装时核对版本、普通非 setuid 程序、完整动态库解析；发行包自身的程序别名（例如 `socat → socat1`）只允许解析到 `/usr/bin` 内，并固定真实程序路径及摘要，记录 Node／辅助程序实际解析的系统库摘要。系统库的 Ubuntu 安全更新可由宿主管理，但已保存描述的摘要／恢复条件不会静默变化。准备命令见[安装指南](../installation.zh.md#linux-amd64-的可选-srt-执行)，S5a 不另行临时拼依赖。

namespace、seccomp、procfs 和服务账号的实际权限属于宿主前置。生成的 SRT systemd 单元清空 effective／bounding／ambient capabilities，并设置 no-new-privileges；原执行工厂在业务开放前实际验证 namespace 与监督资源，缺失／不合格拒绝，不回落原生。不因文件存在或模块可加载声明内核运行能力通过。

## 安装、升级与原 owner

`install.sh --execution srt --srt-endpoint ORIGIN ...` 透传正式 `teamx service install`。默认不选 SRT。首次安装保存执行选择与端点授权，核对发布后的实际 release，生成服务账号拥有的 `home/execution/<release>.json`（目录 `0700`、文件 `0600`），固定 Node、SRT、DSH／CLI、辅助程序、系统库、私有 home 和精确端点。包内不包含账号、token 或验收机绝对路径。

原 service 安装／停服／备份／恢复仍为唯一部署 owner，Space、Room、输入、Session 与执行资源仍由原业务 owner 负责。重新安装不转换执行组合或扩展端点；启动核对登记选择、当前实际 release、描述路径和端点，拒绝 native 回落、旧 release 或端点扩权。普通 native 不读取 SRT 描述、不做 SRT 前置检查，原生 systemd 单元内容保持。

升级在停服前校验目标 release 的 SRT 输入；停服、结清并一致备份后写新描述、切换 release。旧 release、旧描述和 Session 证据保留；中途失败保持停止及备份，不自动回放业务。实际路径／依赖变化造成恢复不兼容时，原执行／Session 恢复合同继续拒绝并要求显式处理，不静默重置或轮换。恢复仍使用原绝对路径和完整备份；宿主依赖也须满足原摘要。

## 针对性检查与产物

定向 Go 检查覆盖：固定 release／npm 合同、辅助程序缺失／摘要不符、无效 origin、native 无 SRT I/O、生成路径／权限、私有目录链接／权限拒绝、旧描述与 Session 证据保留、SRT 服务权限、登记与实际配置不符拒绝。既有 service／bundle 回归及相应 integration 检查复用原用例。

旧归档 smoke 在首次 Space 登记后把 `data_dir` 改成 `.`，触发了正确的身份／根目录保护。本轮改为在首次登记前设置自定义根，继续验证只读归档、外部 home、ACP 与重启，并断言登记身份及根始终保持；没有放宽业务启动检查。

Node 合同检查覆盖安装参数透传／升级选择固定、归档资源缺失／修改拒绝、目标／包型范围、缓存对全部 SRT lock 输入的绑定，以及快照／发行下载复验的 SRT 校验器摘要绑定。普通构建与 CI 只做归档／受控 ACP smoke，不运行真实模型、SSO、平台授权或浏览器业务。

本地构建及报告保留在 `.packaging/srt-work/`：隔离 Ubuntu 24.04 amd64 构建环境，Go `1.27.1` 和 Node 由固定下载摘要校验。当前宿主为 ARM，amd64 通过模拟运行，不能作为真实 amd64 宿主／systemd 验收。归档、同名 SHA-256、smoke 报告和实际检查结果以该目录为准；不会自动上传、发布或切换日常服务。

**本轮结果：检查通过，首发候选未冻结。** 定向 Go／integration、安装与资源合同 24 项、快照／发行合同 59 项通过；最终归档的完整启动 smoke 通过。真实产物的配置生成、`0600` 权限、`socat1` 实际路径、15 个只读发布／系统库项，以及缺失／替换辅助程序在执行前拒绝通过。复制回本地后再次校验归档摘要，manifest 的 **730 个构建／打包输入摘要**与当前工作区一致。

开发归档：`.packaging/srt-work/candidate-final/teamx-dev-f7226dbb3c4b-dirty-linux-amd64-standard.tar.gz`（318,820,863 字节），SHA-256：

```text
d9816188770d53598aec8185d9b2e4af3459fb84374610e7203dd6e2e82f06d0
```

同目录保留 `.sha256` 和 `-report.json`，汇总为 `.packaging/srt-work/delivery-checks.json`。这是当前工作区构建的开发包；没有冻结或发布首发版本。静态依赖／配置检查不计为真实 SRT 执行、普通服务账号、systemd／HTTPS、SSO、平台授权、真实模型或容量验收。

| 组合 | 本轮交付与证据 | 保留门槛 |
| --- | --- | --- |
| Linux amd64 standard／native | 保留原默认；归档启动、HTTP、固定 ACP、关闭／重启与只读归档 smoke 通过 | 实际设备／模型使用不由 smoke 声明 |
| Linux amd64 standard／SRT | 固定 npm 资源、宿主前置、服务权限模板、真实路径及部署描述检查通过 | 所选宿主的普通账号／namespace／执行仍留 S5a／S5b |
| 多用户／OpenCSG 及所选 GitLab API | 候选含当前实现，生产开关保持关闭 | 所选 S3／S4 真实出口及 S5a／S5b 后才正式支持 |

复建入口：

```sh
./scripts/package.sh --channel dev --profile standard --target linux-amd64 \
  --output-dir /absolute/new-candidate --cache-dir /absolute/cache
# 缓存齐全时追加 --offline；在匹配的 Linux amd64 环境运行。
./scripts/package-smoke.sh /absolute/candidate.tar.gz --report /absolute/new-smoke.json
```

最终首发冻结必须在所选 S3b.2a／S3b.3／S4a 及所选 S4b 门槛关闭后重建，绑定最终源码与包摘要。S5a 消费同一冻结包做普通服务账号、systemd／HTTPS、新装／升级／备份恢复；S5b 负责真实所选能力、并发与容量。本轮不开放未选 Git 代执行、Computer、多用户或生产部署。
