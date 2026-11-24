# 空间执行隔离路线验证

日期：2026-11-24。对应[多空间与多用户方案 §11.5 S3a](../WORKSPACE-AND-MULTI-USER-ANALYSIS-PLAN.zh.md#115-第三至五批的装配与验收边界)。源码 HEAD `479e79f35157cf15b8f60c793a7840b59d07647c`，承接已有未提交的 S1／S2 等改动。本单元只验证隔离路线；正式运行时接入、额度和多用户开放分别留给 S3b 及后续阶段。

**路线调整（同日）**：本记录是已完成的历史探针，原始收据、脚本和失败诊断保留；下文当时“选定每空间业务 worker／容器”的架构结论已被替代。当前主线为共享可信核心、每空间业务实例、独立 Agent 执行边界，优先进行 S3a.1 的 SRT＋完整 DSH 验证。该实验没有接通实际 SpaceRuntime／manager 的独立业务 worker，不能证明该架构最优，也不是 SRT 验收。当前生产中没有空间 worker 需要回滚；实验 root／UID launcher、外层策略和 Docker 挂载不移入生产。后续顺序及出口以[主方案 §11.2／§11.5](../WORKSPACE-AND-MULTI-USER-ANALYSIS-PLAN.zh.md#112-唯一联合实施顺序)为准。

## 实验入口与边界

入口为 [`scripts/space_isolation/run.py`](../../scripts/space_isolation/run.py) 和 Linux 专用的 `TestLinuxSpaceIsolationRoutes`。普通测试不启动 Docker、安装 CLI、使用模型或读取凭据。复跑使用新目录：

```sh
python3 scripts/space_isolation/run.py --output .local/space-isolation-new
# 已构建本实验镜像时，更新当前验收程序后复跑：
python3 scripts/space_isolation/run.py --reuse-image --output .local/space-isolation-new-2
```

Node、DSH、glab 和 lark-cli 使用仓库锁定版本；下载的原生程序核对归档 SHA256，DSH 使用 `npm ci`。镜像只接收验收程序及发布依赖，不复制仓库、home、配置或真实平台凭据。模型上游使用受控 SSE，实际启动真实 DSH、ACP、内置文件工具、bash 及子进程；不将它表述为真实模型服务验收。

实验用 A、B 表示同一用户的两个空间，用 C 表示另一个用户的空间，另设控制面和本空间服务凭据区；它们仅含合成正文。控制进程监督真实 DSH；测试专用 launcher 在执行 DSH 前清空附加组并降为各空间 UID。launcher 不进入正式业务装配，不新增生产隔离接口。

## 路线判断

| 路线 | 核验方法与判断 |
| --- | --- |
| 现有 DSH 沙箱 | 保持 `workspace-write` 和 `ask`，让两个逻辑空间使用相同 UID，直接通过内置文件工具读取 B 的 `0700/0600` 合成文件。此对照判断现有固定版本能否作为完整读取边界；不声称额外开发整进程沙箱策略不可行。 |
| 每空间独立 OS 身份 | 恢复 B／C 的独立 UID，检查文件工具、Shell、子进程、链接和凭据目录。另用真实本机 HTTP canary 验证不同 UID 是否仍可访问未鉴权服务。独立 UID 必须与网络／内部通道约束组合使用。 |
| 隔离执行容器及受监管进程 | 使用私有网络命名空间、只读发布文件系统和持久目录；不挂载宿主 home、其他空间、控制面和 Docker socket。Agent 与可信监督／凭据服务使用不同 UID，Agent 有效 capabilities 为零，继承 `no-new-privileges`。只保留受认证模型入口，关闭 UID 对照用的未鉴权 canary；验证真实另一个容器的监听不可达。 |

本机 Docker 提供 Linux 实验内核，不能据此宣称正式 Linux 部署或多用户产物已验收。`--network none` 用于验证默认关闭的边界，模型 fixture 位于同一隔离环境的受认证入口；生产模型／平台出口及其目标限制仍须 S3b 实现和验证。Docker 的文件系统、网络及权限选项依据[官方运行文档](https://docs.docker.com/engine/containers/run/)，是否满足本方案以实际探针为准。

**当时选定路线（现已被替代）：每空间受监管 worker／隔离容器，容器内再区分可信服务与 Agent 的 OS 身份。** 当时的依据是现有 DSH 沙箱读取范围不够，独立 UID 能保护私有文件但不保护未鉴权本机服务。实验验证了相应外层文件系统、进程和网络约束，没有证明必须拆分整个空间业务；这些技术事实仍有效，后续 SRT 等路线须提供自己的完整证据。

这项选择包含两个实测前提：

- 当前 Linux 容器里，固定 DSH 的 `workspace-write` 内置文件工具可用，Shell 因无可用的嵌套沙箱后端而明确拒绝。候选实验通过独立 launcher 标志和只读 overlay **显式**选择 DSH `danger-full-access`，由外层容器和 UID 执行强制隔离，`ask` 保留；这不是 DSH 自身沙箱通过，也不是自动降级。正式个人组合的配置和 launcher 均未修改。
- 容器必须有 init／孤儿进程回收责任。无 init 的早期实验中，取消后孤儿进程阻止原后端结清，Session 正确进入不可用；加 `--init` 后，原取消、进程树消失和后续执行通过。未删除观察、放宽结清或改成“回合返回即停止”。

## 生命周期与责任

Go DSH 后端继续唯一负责 ACP 启动、取消、进程树观察、Close、恢复标记及锁，不复制其结清状态。真实 shell 写下就绪标记后取消，独立检查 shell PID 消失及延迟副作用缺失；随后原 Session 再执行。B 的 DSH 在 A 取消和崩溃期间保持独立，之后继续写入自己的收据。

正常关闭后使用原目录与 SessionID 恢复；杀死 ACP 后检查原 Session 不再满足恢复门，保留旧状态并显式创建新的私有 Session。随后停止并移除旧实验容器，以同一镜像和命名卷创建全新容器，保持 `/lab/a/workspace`、状态路径及受认证模型端点不变，验证原正常关闭 Session 的恢复。路径或端点改变不计作无损恢复。

runner 只清理自己创建的容器和合成卷，分别观察停止后 `Running=false`、`Pid=0`，再移除资源；诊断失败也执行停止及独立核验。实验镜像可作为复跑缓存保留。持久业务、Room／成员身份、通知消费、外部写入 unknown 及归档结清仍属于原 owner，本单元不转移它们。

实验监督进程保留 `CHOWN`、`DAC_OVERRIDE`、`KILL`、`SETUID`、`SETGID`，仅用于初始化、降权、独立取证及跨 UID 结清；Agent 实测有效 capabilities 为零、无附加组。未使用 privileged、宿主 PID／网络、Docker socket 或 unconfined seccomp。S3b 必须明确这个可信启动／监督边界，不把这些权限发给 DSH 或普通用户。

## 验证记录与停止点

验证使用 Linux arm64／`6.12.54-linuxkit`、Node `24.22.2`、DSH `0.2.2-rc.1`、glab `1.127.0`、lark-cli `1.0.99`。ACP 的 `agentInfo.version=0.0.1` 是适配器身份，不替代实际 CLI 版本。

CLI 取证不只检查目录模式：真实 lark-cli 在隔离环境离线创建合成 app secret，生成官方密文和 32 字节 `master.key`，用新 CLI 进程重载成功；B 的真实 DSH Shell 可以读取自己的 glab 配置及 lark-cli app 上下文，A 切换至 B 的配置失败，直接读密文、主密钥及 glab 配置也被拒。没有真实外部登录或业务写入。

最终完整运行 `.local/space-isolation-20261124-10` 通过：30 项独立探针全部成功，主场景 24.70 秒、替换容器恢复 2.34 秒；二者退出码为 0，两个容器均结清并移除，合成卷已移除。`-08`、`-09` 为较早的成功版本；最终源文件和镜像摘要以 `-10` 为准。

普通验证为 `go test ./internal/agent/dsh`、Linux e2e 编译、Python 语法检查及 `git diff --check`。完整实际运行、容器替换和资源结清结果见脱敏证据（本地验收文件 `space-execution-isolation-evidence.json`）；早期镜像交付夹具及 init 缺失的失败不计作通过。原始报告保存在本地全新验收目录。

S3a 历史探针已完成，不声明新路线完成；S3 及多用户能力尚未完成。下一步为 S3a.1 SRT＋完整 DSH 的文件、网络、ACP、内层沙盒兼容、取消及恢复验证；通过后依次进入 S3b.1 正式执行接入、S3b.2 工具链／硬资源约束、S3b.3 完整组合验收。原来每空间业务 worker、空间 RPC 和统一 UID 分配不再作为前置，仍需的宿主配置由实际执行后端决定。Computer、第三方认证、真实平台登录／刷新／注销、旧 Session 向新后端采用及容量／发行验收不能由本轮实验代替。
