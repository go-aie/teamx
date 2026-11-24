# SRT 与完整 DSH 有界验证

日期：2026-11-24。对应主方案 S3a.1。**DSH 组合实测未通过；该结果不再阻挡独立 SRT 机制验证。** 生产启动及已有 S1／S2 未接入实验适配。

按后续用户澄清，机制与集成分别验收；独立探针已完成，见 [SRT 独立能力记录](srt-independent-isolation.zh.md)。下文保留 DSH 的失败事实，后续在 Linux 部署组合中验证，不改称通过。

## 入口与实际边界

`scripts/srt_isolation/run.py` 在新容器中执行前置检查；`native-run.py` 在用户指定的 `/root/luop/teamx/srt-isolation-*` 新目录中准备依赖并执行原生 Linux 探针。`native-launch.sh` 的一次性私有挂载已获得本次用户明确授权：仅将实验目录映射为进程内 `/mnt`，随后降为 nobody、清空 capabilities 并设置 no-new-privileges。未修改宿主目录权限、系统安装、Docker 配置或现有容器。该脚本是实验路径适配器，不是生产 launcher。

SRT 锁定为 0.0.83，源码 tag 指向 `0bbebed0aa9a8e628dc4df8bc30293550a1988eb`；归档 SHA256、全部 npm 传递依赖完整性及 Ubuntu 工具包摘要见脚本目录中的三个 lock 文件。Node 24.22.2、DSH 0.2.2-rc.1 复用仓库发布锁。原生目标为 Ubuntu 22.04 amd64、Linux 5.15.0-101-generic，bubblewrap 0.6.1、socat 1.7.4.1。发布程序只在新目录中解压，npm 禁止执行安装脚本。

## 已观察结果

| 检查 | 结果 |
| --- | --- |
| 本机 Docker LinuxKit 默认限制 | namespace 创建被拒，不能用依赖文件存在代替实际启动 |
| 原生 Linux 非 root namespace／SRT 启动 | 通过，保留完整 PID／网络 namespace 与 seccomp 前置，未启用 weaker 模式或 nested user namespaces |
| 完整真实 DSH ACP 初始化 | 通过；agentInfo 为 deepseek-harness-acp / 0.0.1，与 CLI 发布版本分开记录 |
| 受限网络中的普通 Node 客户端 | 精确 host:port 放行后，通过代理访问带限定合成凭据的端点，HTTP 200 |
| 真实 DSH 受控模型回合 | Connection error；认证模型请求及被拒模型请求均为 0 |
| 故障结清 | 原 Session 禁用；原恢复门拒绝，进程结清，独立检查私有挂载 namespace 无剩余 PID |

DSH 固定发布包的 `@deepseek-ai/dsh-http-proxy/lib/index.js` 强制把 loopback 放入直连规则。清空 NO_PROXY 并启用 NODE_USE_ENV_PROXY 能让普通 Node 客户端走 SRT，但不足以改变 DSH 自身的代理策略。真实 DSH 因而无法访问仍使用 loopback 地址的模型夹具；同类内部 MCP／CLI 回调不能直接假定可用。证据记录该模块摘要及独立策略探针，不以此推断外部域名模型也必然失败。

本次没有为了跑通而开放宿主网络、允许所有 Unix socket 或修改 DSH 内层模式。要求的 workspace-write／ask 保持；由于连接门未通过，其实际文件／Shell 行为尚未验收。

## 停止点

文件／Shell／子进程／链接跨域拒绝、正式 MCP／CLI 认证通道、DNS／重定向／IPv6／Unix socket 旁路、正常原 Session 恢复、取消及 wrapper／DSH／代理故障矩阵、并行 B、实际模型普通回合均未通过验收，不声明 SRT 隔离路线通过或多用户支持。

后续先评估 DSH 对指定内部端点显式走代理的有界增强，或核心提供受认证且能被这些客户端正确代理的稳定端点。两者均需重新验证目标／地址与授权绑定，不能以放开整个 loopback 或宿主网络替代。本记录不声明 DSH 组合通过，也不实现另一生产后端；独立机制验证与其后续集成分别推进。

## 资源与验证

实验早期错误地复制了多份发布依赖，造成宿主磁盘暂时耗尽。已清理本任务重复依赖及安装缓存；复跑改为硬链接，并在创建实验目录前检查剩余空间。此运行失误不计作 SRT 的机制失败。最终资源清理收据见配套 JSON。

普通验证：Go DSH 回归、Linux e2e 交叉编译、5 项清理责任测试、Python／Node／Shell 语法检查与 diff 检查。真实兼容性测试返回失败是本次路线结果，不是全部测试通过。脱敏运行与版本／策略摘要见 证据（本地验收文件 `srt-dsh-isolation-evidence.json`）。
