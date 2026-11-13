# 常驻部署、归档与手动升级

日期：2026-11-12。对应[常驻 Server 方案 W5](../DESKTOP-APPLICATION-ANALYSIS-AND-PLAN.zh.md#w5常驻部署归档与手动升级)。运维入口与部署材料已实现；本机自动验证和真实系统服务探针分别记录，原生 Linux／平台无人值守出口尚未通过，不表示 W5 全部验收完成。操作说明见 [安装与运行](../installation.zh.md)。

## 实现

- 新增部署机 `teamx service install/inspect/start/status/stop/restart/enable/disable/backup/restore/upgrade/uninstall`。Linux 是 root 管理、专用普通账号执行的 systemd 系统服务；macOS 是当前普通用户的 LaunchAgent。固定服务名，同名的其他登记或修改过的 unit 拒绝控制；命令不作为 Agent 工具暴露。
- Go 内置安全解包和现有 `checksums.txt` inventory 校验，无需部署机预装 Node。要求本机架构 standard、可信渠道取得的归档摘要、唯一根、完整摘要／模式／内部链接和许可材料，拒绝越界路径、特殊文件、hardlink、重复项及私有状态。完整临时目录校验后原子发布；build-id 包含版本／平台／profile 和摘要前 12 位，同 ID 的不同内容拒绝。
- 安装串行锁独立于运行 home／data 锁。复用 W1 的安装登记与真实版本目录、共享 `init`／首启认证；`init --server-root` 仅固定受管默认 DSH 选择器，已有配置、身份和模板不覆盖。Linux 初始化按部署 UID 执行，不以 root 创建模型凭据，不重定向系统 HOME。已有非空的其他 home 不自动导入。
- 服务使用明确、最少的账号环境，不读 shell rc、不继承 export Key。缺模型是认证后的 `setup_required`，不启动业务或触发模型验证。受管 `serve` 始终要求登录保护；仅本机与 HTTPS 代理模式保持 loopback，局域网模式可切换为全接口监听并限制私有来源和本机 IP Host。管理器认证探针仍通过 loopback 运行。普通可移动 bundle 的本机默认免登录保持原行为，归档 smoke 先测该首启，再显式启用 token 完成凭据测试。随包提供 Caddy 与 journald 示例，安装不修改主机证书、代理或防火墙。
- systemd：45 秒停止预算、`KillMode=mixed`、10 秒退避、10 分钟内最多 5 次启动。LaunchAgent：45 秒预算，有界监管最多 5 次失败重试、间隔 10 秒；`stop` 使用 bootout，不由 KeepAlive 拉起。启停与持久 enable/disable 分离。Mac 监管进程负责 10 MiB 日志轮转，保留 5 份历史；Linux 使用主机 journald 保留策略。
- `status` 区分管理器、所有者认证、安装身份、设置／业务状态、ServiceEpoch、成员数和模型操作概况；启动核对 token 与 installation ID，业务态还读取模型／成员恢复概况。端口连通或公开 health 不能替代认证检查。单个平台故障不作为管理器重启依据。
- 升级先校验发布，停止旧服务，取得 home 和 data 独占锁，完整备份后原子切换 current，再启动认证检查。保留全部旧 releases；启动检查失败保持新选择、停服和备份，不冒险自动切回旧 binary。
- 备份复制整个 home 和所有 releases，记录原 current、安装／账号登记与文件 inventory；包括托管 Key 历史、首启状态、验证／模型操作／有限迁移、模板、Session／rotation 和 Workspace。备份发布和恢复前均校验。外部 data／teams／token／Runtime 及越界链接明确拒绝，平台官方凭据另行备份或重新授权。
- 恢复仅对同一登记、账号和原绝对路径执行，先备份当前状态；保留 `home.before-restore-*`、新旧程序版本，再按备份选择启动。模型迁移和中断查询／重试复用已有控制器，不另建迁移／Session 状态机，不由部署命令重放验证或平台动作。
- standard 目标的候选资格新增 `resident_server`（仅 Linux amd64、macOS arm64），manifest 绑定[模型阶段 4 交付记录](model-deletion-migration-delivery.zh.md)摘要。真实部署、适用迁移与模型操作恢复缺失时保持 `not_run`；Linux 固定账号发送也是明确门槛。原四目标和 core／standard 发行矩阵保留。

## 验证

定向验证命令：

```sh
go test -race -tags=integration ./internal/room \
  -run '^(TestService|TestServer|TestOnlineModels(RestartPendingTargetAndCommitUnknown|FailureIsolationRetryAndNoDuplicateRotation)|TestModelMigrationInterruptedFixedTargetsAndLegacySnapshot)' \
  -count=1 -timeout=3m

node --test scripts/package-lib.test.mjs scripts/package-ci.test.mjs \
  scripts/package-release.test.mjs scripts/package-publish.test.mjs \
  scripts/package-acceptance-lib.test.mjs internal/web/tests/web_access_settings_test.mjs

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/teamx-server-linux-amd64 ./cmd/teamx
node scripts/check-packaging.mjs --target linux-amd64 --profile standard
node scripts/check-packaging.mjs --target darwin-arm64 --profile standard
```

定向测试覆盖损坏／越界／重复／多根／hardlink／私有归档、已安装内容被修改、安装串行锁、活跃 home 与旧 data writer、一致备份恢复、历史 Key／模板／rotation／操作记录保留、保留新旧版本、拒绝外部依赖、拒绝错误实例与损坏备份、模板转义、Mac plist lint、显式停止语义、日志轮转、认证后的 setup 与未知安装身份拒绝。模型恢复复用实际控制器的 fixture 测试，检查提交后中断的固定目标重试、不重复完成的 rotation，以及有限迁移中断续接；不是实际平台／模型部署验收。

定向 race 验证通过（最终备份并行修改另以 `TestService*` 带 race 复验）；打包／资格及访问设置测试共 49 项通过；两目标清单检查和 Linux amd64 Go 交叉编译通过。交叉编译不代表 Linux 原生依赖或系统服务通过。

`go test ./... -timeout=8m` 在约 374 秒后结束，唯一失败是 `TestMemberAgentFailedTaskAdmissionDoesNotConsumeBudget` 的 `actorx: stopped`；该既有成员测试单独带 race 复验通过。其他包通过，**本轮不记为全量全绿**，没有为掩盖该失败改变成员调度逻辑。

归档构建期间检测到本轮改动及其他会话的 `access_settings.go` 变更，源码漂移保护拒绝了混合输入。后续使用 `.local/server-deployment-source` 的隔离源码快照打包；快照复制公开源码、随包白名单和模型交付记录，不复制工作区私有 home／凭据。源码列表及摘要保存在 `.local/server-deployment-source.json`。候选仍为 dirty 开发快照，不是正式发行。

本机 macOS **15.7.4 arm64** 上的最终开发归档与真实 launchd 探针均通过：

| 项目 | 结果／范围 |
| --- | --- |
| 来源 | `.local/server-deployment-source` 隔离快照；HEAD `baf64e2abf29` + dirty，逐文件摘要见 `.local/server-deployment-source.json` |
| 最终归档 | `.local/server-deployment-frozen-package-07/teamx-dev-baf64e2abf29-dirty-darwin-arm64-standard.tar.gz`，312,283,375 字节 |
| SHA-256 | `dfde75e417e3d006ede1009729a44ef79f35b9fea0e5c6522ee4839fe6ab2381` |
| 归档 smoke | 同目录 `*-report.json` 为 passed；版本／help、默认本机首启与显式 token、初始化幂等、独占、用户模板保留、丢失 token 拒绝、只读包、外部 home、ACP fixture、关闭／恢复与子进程清理通过 |
| 真实 launchd | `.local/server-deployment-native-07/report.json` 为 passed，归档摘要与上述最终归档相同；约 450.5 秒，覆盖认证 setup、bootout 后保持停止、完整一致备份、同路径恢复重启、原身份／token 与程序完整性 |
| 清理 | 唯一测试 label 已注销；当前 GUI domain 可读取，精确 label 查询明确返回 `Could not find service`；没有在真实用户 LaunchAgents 目录登记测试服务 |
| 体积与维护成本 | 本次版本约 7.5 万个文件，展开约 1.3 GiB（文件系统占用样本）；保留全部版本，备份也复制闭包，需提前留足空间。单次探针时间不是一般性能保证 |

实际归档探针发现并修复了内部快照解析器误拒绝 manifest 扩展字段、Go 全局排序与 Node 目录遍历排序不一致的问题；fixture 现包含完整 manifest 扩展字段和目录／同名前缀文件。首轮实际备份达到原测试预算，随后保留逐文件 fsync，改为最多 32 个复制任务并行、最多 64 个复制文件描述符，增加完整归档探针预算；最终同一归档完成整套恢复验证。中间源码或验证失败的候选不作为上述最终产物。

这些证据属于 dirty 开发候选，`resident_server` 正式资格仍为未通过；真实模型、平台凭据、登录启动和 Linux 等门槛见下节。

收尾时共享工作区另有 LAN 接入增量，修改了 `access_settings.go`、`server_access.go` 和受管服务监听／状态入口。该并行改动保留，当前工作区兼容回归带 race 已通过（约 15 秒），记录见 `.local/server-deployment-current-workspace-race.log`；上述归档仅对应冻结快照，不将 LAN 增量计为已完成归档验收。

真实 LaunchAgent 探针入口（Mac，输出目录必须不存在）：

```sh
TEAMX_TEST_SERVICE_ARCHIVE=/absolute/native-standard.tar.gz \
TEAMX_TEST_SERVICE_OUTPUT=/absolute/new-private-output \
go test -tags=integration ./internal/room \
  -run '^TestServiceNativeLaunchAgentArchive$' -count=1 -timeout=20m
```

探针从同一归档校验发布，使用当前 UID、隔离账号 HOME／数据和唯一测试 label；结束时注销该测试任务，不操作用户的 `com.teamx.server`。它覆盖真实 launchd 启动、无 export Key 的认证设置态、正常停止、停止后不自动拉起、一致备份与同路径恢复、重启身份和程序完整性。它不等于正式固定服务名安装、登录／注销、真实 Keychain、模型／平台或机器重启验收。

## 尚缺的出口

- 原生 Linux amd64 专用账号、systemd 实际安装、退出 SSH、机器重启、cgroup 对独立 DSH 进程组的超时清理。
- macOS 正式登录启动／注销／休眠，以及真实平台 Keychain 重启解锁。
- 真实平台账号登录、用户 token 自动续期、DPoP 恢复、浏览器关闭后的实际采集／投递／执行；W0 的合成 app secret 后端探针不能替代。
- Linux `Pinned.Pin` 仍返回 `account_pinning_unavailable`，未升级 CLI、复制 raw token、删除账号核验或改用机器人发送。原生 Linux 全能力发行继续阻断。
- 同一实际部署归档上完成真实模型升级后调用、已提交操作中断后的查询／显式重试、已完成成员不重复轮换、适用旧实例的一次迁移／中断续接；本次自动测试和既有模型阶段 4 记录不扩大为这些交集已验收。

W6 的最终候选、真实桌面／手机浏览器与至少 24 小时观察仍独立保留；Electron 和手机原生 App 继续在 W7–W10。
