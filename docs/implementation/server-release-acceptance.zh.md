# 最终归档、浏览器与发行资格

日期：2026-11-12。对应[常驻 Server 方案 W6](../DESKTOP-APPLICATION-ANALYSIS-AND-PLAN.zh.md#w6最终产物与多浏览器验收)。本轮实现发行门槛、观察工具及同归档模型验收入口，并执行本机与 Docker 可覆盖的验证；**W6 正式验收尚未完成，不发布正式支持声明**。iOS／Android 设备暂不可用，Linux 使用本机 Docker 尝试，具体证据分别记录。

2026-11-13 补充：Linux arm64 已完成原生架构容器中的 standard 归档、19 项服务测试与真实 DSH 取消／原 Session 恢复验证，见下节。它是有效的 Linux arm64 证据，不是 amd64 模拟结果，也不等同完整 Linux 宿主机运维验收。

## 代码与使用方式

- Linux amd64／macOS arm64 standard 评估增加 `web_delivery`，与既有 `resident_server` 同时要求。记录必须匹配候选归档、源码、依赖和模型阶段 4 交付摘要；未提供记录保持 `not_run`，不能由已有模型领域报告自动补成通过。
- 模型场景检查真实首启、默认／固定引用、条目更新、换 Key、删除替换、等待／部分失败、刷新原操作、重启后调用及请求／执行快照；上游范围和旧 DSH 兼容性单列。浏览器矩阵固定列出四类，真实设备通过项进入 `supported_browsers`，缺失项进入 `pending_browsers`。平台动作要求授权与远端结果证据摘要；只读登录门槛仍独立保持零写入。
- 至少 24 小时观察必须填写实际时间、样本数、最大间隔和私有报告摘要，人工确认同一归档／实例、采集积压、限流恢复及失败处理。伪造短跑时长、冻结后稀疏采样、未核对的积压和早于观察结束的确认均拒绝。
- `scripts/server-observation.mjs` 校验管理 token 文件权限和已安装内容，认证后仅 GET 实例、Room、模型操作与已存订阅。拒绝远端明文 HTTP、带凭据／查询参数的地址及重定向；不发送消息、授权、模型验证或平台动作。输出目录 0700，报告与样本 0600，不保存 token、消息正文或原始配置。中断保留已采样记录；短跑为 `incomplete`，满时长也仅 `completed_pending_review`，始终不自动授予发行资格。时长按首末采样点计算，结束校验另记 `verified_at`，不能将归档校验开销算入 24 小时。完整命令与人工填报方式见[发行说明](../../packaging/releasing.zh.md)。
- `model-package-probe.mjs --model-maintenance` 在同一归档上追加在线模型维护／删除替换和有限迁移探针；显式将该归档的 Go、DSH 与 Node 用于后续调用，避免误用来源 home 的旧 Runtime。记录归档、manifest、依赖、模型交付摘要、验证脚本摘要、大小及首启／重启就绪时间和 Go 进程 RSS 单次样本。所有业务写入仅在全新隔离 home。
- 修复自动验收报告与发行评估的验证器清单不一致：共同使用 `acceptanceInputs`，纳入实际使用的模型 fixture。浏览器探针同时适应本机默认免登录与启用登录的入口；在线／迁移认证探针显式启用 token。随包运维指南补充登录、上下文重置、恢复、浏览器声明和平台限制。

同归档模型组合入口（来源 home 只读取已托管模型参数／凭据，输出目录必须不存在）：

```sh
node scripts/model-package-probe.mjs \
  /absolute/native-standard.tar.gz /absolute/managed-source-home \
  /absolute/new-private-evidence --model-maintenance
```

该入口使用真实上游，产生有限模型调用费用；不访问平台业务，不把来源 home 的 Room／历史导入新 home。报告为本机自动浏览器与模型交集，不能代替物理设备、系统服务或 24 小时记录。

## 自动检查

测试运行时的工作区已通过：45 项打包／候选／发行／观察检查，491 项前端测试，以及服务、模型中断恢复相关 Go race 检查。归档证据单独对应下述冻结输入。观察工具测试覆盖 GET 认证、实例／代际、订阅查询、私有输出、归档内容匹配、失败／冻结间隔与短跑不晋升；发行测试覆盖缺记录、缺真实设备、缺换 Key／请求证据、缺动作授权、伪造观察时长和公共投影去除额外私有字段。追加时间顺序检查后，发行测试 14 项再次通过。

```sh
node --test scripts/package-lib.test.mjs scripts/package-ci.test.mjs \
  scripts/package-release.test.mjs scripts/package-publish.test.mjs \
  scripts/package-acceptance-lib.test.mjs scripts/server-observation.test.mjs
node --test internal/web/tests/*_test.mjs
go test -race -tags=integration ./internal/room \
  -run '^(TestService|TestServer|TestOnlineModels(RestartPendingTargetAndCommitUnknown|FailureIsolationRetryAndNoDuplicateRotation)|TestModelMigrationInterruptedFixedTargetsAndLegacySnapshot)' \
  -count=1 -timeout=3m
```

## 本机开发归档

从当前工作区公开文件冻结 `.local/server-release-source`，没有复制私有 `.teamx` home／凭据；完整文件摘要为 `.local/server-release-source.json`。构建快照与验证脚本分别留摘要，后续工作区变更不自动算作本归档内容。

| 项目 | 本次证据 |
| --- | --- |
| 来源 | HEAD `71285b012517e7704d1b66d699f062c9bc32efb6` + dirty 快照 |
| 归档 | `.local/server-release-package/teamx-dev-71285b012517-dirty-darwin-arm64-standard.tar.gz` |
| SHA-256 | `007a0e9cd55d2b326344d06c6091e2edff729a7e3a6985bdfbec681621ad31b9` |
| 依赖 | Node 24.22.2、DSH 0.2.2-rc.1、glab 1.127.0、lark-cli 1.0.99 |
| 模型阶段 4 基线摘要 | `371b765a28e9aada2637cc84e23406a5747f4572aa2f199c317759e8bb8dcdb7` |
| 归档 smoke | 同目录 `*-report.json` passed；首启／token、独占／幂等、无宿主 Go／Node／平台 CLI、HTTP／Room、SIGINT／SIGTERM、只读包／独立 home、fixture ACP／进程清理 |
| 大小 | 312,363,653 字节归档；75,011 个文件／链接；常规文件逻辑大小 1,142,490,946 字节，展开的文件系统占用约 1.3 GiB |
| 本机原生服务 | macOS 15.7.4 arm64，`.local/server-release-native-01/report.json` passed；真实 launchd、认证 setup、停止后保持停止、完整备份及同路径恢复，约 450.177 秒 |
| 原生服务清理 | `.local/server-release-native-01/cleanup.txt` 确认唯一测试 label 已注销，未登记用户正式 `com.teamx.server` |
| 真实网页首启／重启 | `.local/server-release-model-04/first-use/report.json` passed；无 init／export Key，失败验证不发布、刷新续查、真实首条及重启后对话 |
| 真实在线维护／删除 | `.local/server-release-model-maintenance-06/report.json` passed，9 项检查、21 次代理观测请求；默认／固定引用、刷新、8192 输出参数的真实后续调用、删除替换与重启后继续执行 |
| 有限迁移 | `.local/server-release-model-migration-06/report.json` passed，4 项检查、12 次请求；当前适配器创建的旧格式 Session，迁移保留原 ID／指纹、网页修复、再次重启后真实调用 |
| 原生服务上的真实模型 | `.local/server-release-native-model-01/report.json` passed；同一恢复后的原生测试实例，通过网页配置模型，确认消息已受理后关闭全部浏览器，真实 turn 完成并持久化回复；重新 bootstrap 后重新登录并完成实际后续调用，核对执行快照 |
| 原生只读观察短跑 | `.local/server-release-native-model-01/observation-final/report.json`，首末采样间隔 5 秒，3 个成功样本、0 个失败，实例／已安装归档匹配；无订阅，`incomplete`、`duration_met=false`。最终 label 注销证据为同目录外的 `observation-final-cleanup.txt` |
| 就绪／内存单次样本 | 首启 2,625 ms、Go RSS 14,400 KiB；重启 3,223 ms、Go RSS 17,632 KiB。监听就绪时测量，未包含后续 DSH 子进程，不作为容量或性能保证 |

汇总 `.local/server-release-acceptance-final.json`，`status=passed` 只表示其中列出的本机可执行检查通过，**`release_ready=false`、正式支持列表为空**。首启／浏览器与维护／迁移分别使用上述完成的子报告聚合，均使用同一归档的 Go／DSH／Node；维护的参数／凭据及验证回执取自该归档本次首启的全新 home。最后再次核对完整安装 inventory，真实调用后归档内容未改变。

原生模型交集由 `.local/server-release-native-model-probe.mjs` 执行，只重新登记 W5 探针的唯一测试 label，使用其恢复后的隔离 home／账号环境；未操作用户正式服务。原生服务的旧备份是设置态，新增模型状态的备份／升级／恢复仍未据此验收。观察工具修正计时后又对同一实例独立短跑，使用 `observation-final` 记录计时结论；首轮嵌在模型报告中的短跑数据仅保留为历史证据。最终再次查询确认测试 label 已移除。

真实上游为同一个 `deepseek-v4.1-flash-1017` 与同一份有效 Key；维护通过不同条目、路由及参数观测真实后续请求，并核对 Session 执行快照。第二份真实 Key、不同上游、旧 DSH 二进制、真实等待／部分失败恢复仍未通过。迁移证明当前适配器的旧数据格式承接，不扩大为旧 DSH 兼容声明。

上述为 dirty 开发候选，没有正式 tag、上传、签名、公证或干净账户下载验收。

归档验证期间，共享工作区另有访问令牌规则／设置页面增量；它们不属于冻结候选，不能用本次归档证据覆盖。探针也修复了既有假设：普通本机免登录不会创建 token 文件；带显示按钮的登录表单必须精确选择 `#login-submit`；设置入口在用户菜单中，默认／删除动作在模型编辑页。旧路径验证回执不能搬到新 DSH 安装上，第三次尝试因此被现有校验正确拒绝；后续改用同归档首启产生的回执。失败日志／报告保留在 `.local/server-release-model-01` 至 `-04`、维护／迁移 `-05`，没有放宽业务校验。已通过的首启／浏览器检查复用到最终汇总，修正后的维护／迁移在新 home 分别补验。

## Docker Linux amd64

按用户提供的本机 Docker 环境执行。Docker Desktop Engine 为 Linux arm64，Ubuntu 24.04 镜像为 amd64，镜像 ID `sha256:2198e4c65076e9cb0b3d695219df667c0e3b0072da64ca3c2648aca17dcecaf5`，容器观测 glibc 2.39／`x86_64`。这是 Apple Silicon 上的模拟执行，不能作为原生 amd64 发行资格。

主机交叉编译 Linux amd64 Go binary 与 integration test executable，再以容器普通 UID 10001、无网络、只读测试材料挂载运行。全新容器无 Go／Node／平台 CLI：18 项服务测试通过，1 项离线 ACP 测试因缺 Node 跳过。随后下载锁定的 amd64 Node 24.22.2，核对 SHA-256 `48cc0e21e048790e54c0cf5749c2725005313d652651c40b7a21feb9697aaf75`，仅加入测试 PATH 后补验 ACP 取消／子进程回收／同 home 重启，通过（3.78 秒）。

报告 `.local/server-release-linux-amd64/report.json`；命令记录与日志为同目录 `commands.txt`、`tests.log`、`acp-cleanup.log`，binary／test executable／日志摘要在报告中。通过内容包括真实 Linux 文件锁／权限和 HTTP 上的设置态、安装身份、归档拒绝／发布、备份恢复、模板与停止语义；模板调用使用可控 runner，ACP 使用 fixture。没有构建 Linux standard 最终依赖闭包，没有运行真实 DSH／上游模型、systemd、SSH 注销或主机重启。Linux 飞书固定账号发送仍为 `account_pinning_unavailable`。

## Docker Linux arm64

Docker Desktop 的 Engine 为 `aarch64`，本次容器和包内 ELF 均为 Linux arm64，未使用 CPU 架构模拟。Ubuntu 24.04.5／glibc 2.39、Linux `6.12.54-linuxkit`，运行镜像 ID `sha256:dbc393caa564793299e3f588149143891f13212af7e249d4cd93514b474b05dc`。

复用同一份 `.local/server-release-source` 公开源码冻结树，在 Linux arm64 构建容器内使用 Go 1.27.1／Node 24.22.2 和目标独立的 npm 缓存生成 standard 归档；原有四目标矩阵已包含 arm64。glab、lark-cli 和 Node 均按既定 lock 下载／核对摘要。构建与运行验证分开：后者为没有安装 Go／Node／平台 CLI 的 Ubuntu 镜像，以普通 UID 501 运行，验证器 Node 和 Go test executable 是外部测试材料，应用继续使用包内运行时。

| 项目 | 本次证据 |
| --- | --- |
| 归档 | `.local/server-release-linux-arm64-package/teamx-dev-71285b012517-dirty-linux-arm64-standard.tar.gz`，303,771,144 字节 |
| SHA-256 | `9c7e29394bb118e0e6f844164f628df163212c3a11ee70d9001896367ff7212f` |
| 归档 smoke | 构建时及无网络普通用户 Ubuntu 容器中分别通过；包括全部包内 CLI、原生依赖、首启／token、HTTP／Room、锁、信号、只读包及 fixture ACP 清理 |
| 服务测试 | `.local/server-release-linux-arm64-evidence/integration.log`：19 项通过、0 跳过；涵盖实际 Linux 文件／HTTP／进程行为，系统服务管理器调用为 fixture |
| 真实 DSH／模型 | `.local/server-release-linux-arm64-real-05/real-dsh/report.json` passed；`deepseek-v4.1-flash-1017`，真实 CLI 读取／公开回复、会话私有记忆、前台 Bash 工具取消、取消后继续和服务重启后原 Session 恢复；约 23 秒 |
| 清理及不可变性 | `cancelToolStopped`、`dshProcessStopped`、`resumedOriginalSession` 均为 true，关闭后锁释放，恢复后的实际模型调用完成；外层验证再次核对包内 inventory 一致 |
| 汇总 | `.local/server-release-linux-arm64-report.json`，本地范围 passed，`release_ready=false` |

本次真实 DSH 的容器条件是 `--init --user 501:501 --security-opt seccomp=unconfined --security-opt systempaths=unconfined`，`privileged=false`；系统提供 bubblewrap 0.9.0，DSH 仍使用 `workspace-write`。实际配置见 `real-05/environment.json`。默认 Docker 的 namespace 探针拒绝，单独调整 seccomp／systempaths 后可以运行沙箱；但 Node 直接作为 PID 1 时留下被收养的 bubblewrap zombie，第四轮在取消结清处超时，不能记为清理通过。加入 Docker init 回收器后，同一归档及原有取消／恢复断言完整通过。失败日志和状态保留在 `real-04`，不将这些条件推定为默认 Docker 配置均可用。

验收驱动还补齐了当前认证与消息输入契约：通过正常 `access init` 准备私有凭据，人工 CLI 使用 token 文件，重启保留同一配置／所有者／token；对消息预览显式要求执行 CLI `read --unread` 推进游标，保留原来的游标、私有记忆、进程身份和原 Session 断言。修改仅在 `internal/room/dsh_real_test.go` 外部验证器中；归档应用和 runtime 保持冻结。用于编译该验证器的隔离树为 `.local/server-release-linux-arm64-verifier`，差异及 executable 摘要在汇总中；进程观察回归与发行验证器检查另行通过。模型输入只以私有只读 bind 提供，没有写入镜像或导入平台账号。

这些结果补充原有发行矩阵中的 Linux arm64 证据。未运行该目标的真实浏览器／手机、平台账号、完整在线模型管理和 24 小时观察，也没有验证完整 systemd 宿主机、SSH 注销或机器重启；Linux amd64 的原生资格仍独立待补。本次不扩大正式发布清单。

## 未完成的正式出口

- 真实桌面 Chromium／Safari 与 iOS Safari／Android Chrome 的完整声明矩阵。Headless Chromium 与手机视口属于自动浏览器证据；用户明确暂时没有 iOS／Android 设备。
- Linux amd64 原生机器及同目标 standard 最终归档、systemd／SSH／重启、实际无人值守凭据／续期／账号固定发送；Docker 模拟结果不替代。
- 同一系统服务归档上的完整模型交集：第二份真实 Key、等待／部分失败的实际后续调用、新模型状态的手动升级／备份恢复，以及不同上游／旧 DSH 兼容性声明。真实模型的原生首启、后台执行与重启已通过；现有同一上游多条目与可控失败证据分别保留范围。
- 真实 HTTPS 远程与已明确授权目标上的平台读取／通知／授权／动作／远端结果组合。没有收到本轮平台动作目标，未执行真实平台写入。
- 连续至少 24 小时的真实后台采集／积压／限流／恢复观察。工具、默认时长和审核门槛已交付；本轮短测试不充当 24 小时结果，也没有对无订阅实例虚报观察通过。
- 干净账号真实下载启动、当前正式候选的手动升级／适用旧实例迁移／备份恢复与网络恢复组合；既有 W5／模型报告仅证明其原候选。

正式发布清单保持未扩展，四目标与 core／standard 原矩阵没有削减。W7–W10 的 Electron 与原生手机 App 路径继续保留。
