# Space 重置的 Linux 安装交付验证

2026-11-25。**所选 Linux amd64 standard／SRT 开发组合的交付验证通过。** 范围按本轮约定收敛为：检查最终包资源，验证新装／升级、正式启动方式及一次重置冒烟。阶段 1／2 的功能矩阵不重复执行。

## 产物与源码绑定

- Linux amd64 standard 开发归档：`teamx-dev-2b1aefdf3ae7-linux-amd64-standard.tar.gz`，由目标机原生构建，无架构模拟。
- SHA-256：`a58e8d0baf6ab80da34e29d6e3e00600eaad4eef7f772e0412455548c3f9ce95`。
- 工作区未提交的重置实现也纳入独立冻结快照；`2b1aefdf3ae7` 是验收快照的提交标识，不是项目分支上的提交或正式 tag。归档 manifest 的 **743 项编译／打包输入**与该冻结快照逐项匹配，包含重置模块、页面、私有临时根及下述 UID 修复。
- 包内模板、Node `24.22.2`、DSH `0.2.2-rc.1`、SRT `0.0.83`、glab `1.127.0`、飞书 CLI `1.0.99` 的固定资源及完整归档 smoke 通过。原始未修复包保留为失败证据，不作为交付结果。

远端产物与日志：`/data/teamx-reset-delivery-20261125/`。本地归档、摘要、smoke 报告、输入比对及脱敏安装报告保存在 `.local/space-reset-delivery-20261125/`。

## 本轮发现并修复的问题

root 执行 Linux `service stop`／备份／升级时，新增的“是否存在未完成重置”检查使用了当前进程 UID，误拒绝属于普通服务账号的 `spaces.json`，报 `identity directory must be a private 0600 file`。这是安装组合才暴露的接点问题。

维护入口现在传入已验证服务登记中的 UID，并复用现有 `readIdentitySnapshotForUID`；普通运行时继续要求当前账号，仍检查 `0600`、文件身份和未完成操作，不从文件本身推断可信 owner。回归覆盖登记 UID 匹配／不匹配，以及未完成重置阻止部署；定向 race、停服／升级／备份回归与 vet 通过。修复后重新冻结源码并重建整个归档，安装验证使用新摘要。

前序失败日志另存；其中验收驱动的代理地址格式、旧版 setup 没有 `runtime_epoch`、setup 可写字段误写为 `ready` 等输入／协议断言问题均已纠正，不归因于产品，也不将失败轮次拼成最终通过。

## 安装环境与最终检查

在用户指定 Linux amd64 机器上创建两个独立 Ubuntu 22.04 systemd 容器，分别验证最终包直接冷安装及旧包升级。安装器固定使用 `teamx.service`，因此不能在已有宿主安装旁登记第二套系统服务。两个容器都有独立的服务、Home 和进程命名空间，不挂载日常空间，也不发布端口；仅挂载本轮验收材料。

外层容器为 privileged／private cgroup namespace，提供内核命名空间前提；容器内真正运行原 systemd 单元，TeamX 进程为 UID 999，`NoNewPrivileges=yes`，bounding／ambient capabilities 为空。该环境不作为容器部署安全建议。正式启动不依赖宿主 Go、Node 或 CLI，使用安装包和声明的 Linux 系统依赖。

两条路径使用同一最终摘要，**最终两轮均独立完整通过**，结果分别记录于 `final-cold/installed-report.json`、`final-upgrade/installed-report.json`；本地汇总为 `delivery-report.json`：

1. 最终包通过包内 `install.sh` 冷安装；另一实例从 `dev-808bfc30211f` 通过公开 `service upgrade --archive` 升级，执行正常停服、结清、一致备份、回读校验、切换及认证控制面检查。
2. 使用隔离的两个 Space 和本地会话夹具，核对升级保留安装身份、空间登记、指定根目录和登录会话。认证 provider 配置走正常生产装配，但本轮没有重新执行真实 SSO。
3. 通过已安装核心的控制面发起重置，完成后可写模型设置页可访问，RuntimeEpoch 更新，旧模型候选、私有 CLI／临时标记删除；另一个 Space 的私有标记保留。页面模块确实来自安装包。
4. 目标 Teams 的 **28 个文件**与**当前安装 release** 的 `share/teamx/teams` 逐一一致；Data 根 inode 保留，执行描述为私有 `0600`。
5. 同一重置请求重试不会清掉重置后新写入的标记；通过正式服务入口重启后，原 reset ID、安装身份与另一个空间的标记仍保留。

本轮没有联网模型／平台业务操作，容器网络关闭；模型、真实飞书材料及完整浏览器行为沿用阶段 1／2 的证据。它只关闭本记录所选 Linux 开发组合的安装交付检查，不扩大到 macOS 安装组合、真实双账号 SSO、强制 refresh 竞态或正式发行冻结。其他必需功能证据若缺失，归属其原阶段，不能改名推迟到交付验收。

宿主日常 `teamx.service` 在验证前后均为 active、PID 666295；本轮没有升级、重启或重置它。验收后销毁临时容器，保留归档和脱敏回执。正式版本多用户的既有发行前置保持原状。
