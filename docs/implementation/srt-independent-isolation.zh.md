# SRT 独立隔离能力验证

日期：2026-11-24。按本轮用户澄清，先验证 SRT 本身的机制，DSH loopback 兼容问题保留为后续 Linux 部署集成项。**锁定宿主与候选策略下，153 项检查通过；另有预存硬链接和故障监督两项适用条件。** 这不是完整 DSH／TeamX 生产组合验收，也不声明 S3 或多用户支持完成。

## 实际环境与入口

使用用户提供的 Ubuntu 22.04 amd64／Linux `5.15.0-101-generic`，SRT `0.0.83`、Node `24.22.2`、bubblewrap `0.6.1`。SRT npm 依赖使用完整 lock 和 integrity，Node／Ubuntu 工具包使用既有归档摘要；实际程序、seccomp helper、策略及测试二进制摘要在脱敏证据（本地验收文件 `srt-independent-isolation-evidence.json`）中。

实验不安装、不启动 DSH，不读取实际账号凭据，不访问生产平台接口。A、B 是同用户两个合成空间，C 是另一逻辑用户；所有文件使用同一 UID，以沙盒外可读的正对照排除 Unix 目录权限造成的假通过。逻辑主体标签不代替正式 TeamX 身份授权验收。

Linux 上所有新增文件及日志位于 `/root/luop/teamx/srt-isolation-*`。沿用已授权的私有挂载路径适配，仅在该进程树内将实验目录映射到 `/mnt`，随后降为 `nobody`、清空 capabilities 并设置 no-new-privileges。未改宿主目录权限、系统包、守护程序或已有容器。

```sh
mkdir -p .local/srt-independent-build
GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
  -o .local/srt-independent-build/probe-client scripts/srt_isolation/probe-client.go
python3 scripts/srt_isolation/native-run.py --independent \
  --client-binary .local/srt-independent-build/probe-client \
  --ssh root@YOUR_TEST_HOST --remote-base /root/luop/teamx \
  --output .local/srt-independent-new --allow-private-mount
```

须使用全新输出目录；私有挂载参数仅供已授权实验，不是生产 launcher。首次仅安装 SRT 的 5 个 npm 包，不再安装 DSH 的完整依赖。复跑可对仍保留的同一实验依赖使用 `--runtime-from` 硬链接，先比对 lock；本次收尾已移除这些可重建依赖，因此示例使用全新安装。

## 已取得的机制证据

| 对象 | 实际检查与结果 |
| --- | --- |
| 文件／状态 | A、B 各 38 项：自身工作目录及 Agent 状态可读写；只读配置不可改；其他空间／用户、核心、本空间服务凭据、合成宿主 HOME、核心 checkpoint 与 TLS 私钥均拒绝读写；发布目录和策略文件不可改 |
| 链接与子进程 | 文件／目录／相对／链式 symlink 及 `/proc/self/root` 别名被拒；沙盒内新建私有文件 hardlink 被拒；Shell、Python、Node、Go 子进程继承读取限制 |
| 精确网络出口 | A 20 项、B 17 项：Python／Node／显式代理 Go 客户端可访问各自受认证的指定端口；未授权端口、跨空间端点、元数据地址、未放行域名、IPv6 及 IPv4-mapped 目标和越权重定向被代理拒绝 |
| 直接网络与 IPC | 宿主上已确认可达的 IPv4 测试端口，在沙盒中不能通过原始连接访问；文件路径和 abstract Unix socket 均不可达。IPv6 拒绝通过真实 IPv6 fixture 的代理请求验证，不泛称穷尽全部 IPv6 旁路 |
| HTTPS | Python、Node、Go 均验证本次 fixture CA 和服务器证书成功，未使用跳过证书校验选项。公开 CA 在只读配置区，私钥在被保护的核心区 |
| DNS 地址判断 | 对固定发布包的实际 guard 模块做 15 项受控 resolver seam 检查：IPv4／IPv6／mapped／NAT64、云元数据、私网、宿主接口、精确端口例外、保留 localhost 域及单次解析的地址固定。此项是模块级受控来源证据，不冒充真实 DNS rebinding 端到端验收 |
| 正常退出 | 四个普通文件／网络执行均自行退出，进程树独立观察结清，无强制清理 |
| 异常退出与并行域 | 分别杀死 A 的 wrapper、实际工作进程及宿主侧代理桥接，B 仍可用；实验监督者逐 PID／启动时间匹配、终止并确认后代／代理退出。延迟子进程先等待双方就绪及显式 arming，清理后越过 6 秒副作用期限确认 A、B 均无迟到写入 |

其余断言包括端点存活正对照、私有原文件未改变、错误凭据没有抵达其他服务、故障清理及 DNS 收据检查，合计 **153 成功、0 失败**。HTTP 服务均仅监听本机且使用本次随机合成凭据，跨空间服务在测试前已验证真实可达。

环境由实验启动器显式筛选；观察到的最小环境不表示 SRT 会自动识别并清除任意部署凭据。普通 Go `ProxyFromEnvironment` 对 loopback 的默认绕过也被单独记录；通过的是显式代理 Go 客户端，不推导现有全部 CLI 均已兼容。

## 两项不能省略的适用条件

**预存硬链接不受私有路径隐藏自动保护。** 实验监督者在进入沙盒之前，特意把被隐藏区域的合成文件硬链接到获准工作目录。沙盒内可以通过这个已有别名读写原 inode，原文件的变化也在沙盒外得到确认。该观察单列为 finding，不纳入“拒绝已通过”的计数。SRT 在本次策略下能够阻止从沙盒内新建那个私有路径的硬链接，但不能把已暴露的同一文件内容重新变成秘密。

因此正式目录准入必须排除把核心／其他空间私有文件通过已有 hardlink、挂载或其他授权路径暴露给执行环境；同时审查 `/usr`、`/lib` 及少量 `/etc` 等运行时读取白名单，避免私有配置落入已开放区域。本次只验证合成私有区，不对宿主全部文件做凭据普查。

**SRT 不等于完整的故障监督者。** wrapper、工作进程或代理异常后，200ms 观察窗内仍有 13–14 个已识别的所属进程；实验监督者负责结清。通过的是“独立监督者可清理、B 不受影响、无迟到副作用”，不是“SRT CLI 自己已经全部清理”。正式接入要将这些代理和后代接回现有唯一关闭责任、锁和代际门，不能以 ACP 返回或 wrapper 退出释放所有权。

## 结论与后续边界

独立机制验证已不再被 DSH loopback 问题阻挡。SRT 可以作为进一步接入的候选，条件是明确目录暴露内容、精确策略与外部故障监督；不能概括为“自动控制内部所有操作”。CPU、内存、进程数、磁盘总量及沙盒外核心代执行仍需后续单元落实。

DSH 的 loopback、内外沙盒、实际模型和正式 MCP／CLI 通道、原 Session 恢复、取消及完整故障矩阵留到后续 Linux 部署组合验收；保留[已有 DSH 兼容收据](srt-dsh-isolation.zh.md)，不把它改写为通过。正式组合及多用户开放仍须通过相应完整出口。

最终运行位于 `.local/srt-independent-20261124-02`，程序退出码 0，状态 `mechanisms_verified_with_findings`。第一轮的候选策略读写区域重叠、B 重定向夹具及副作用计时问题已修正；不把其失败断言当作最终 SRT 机制结论。普通验证包括 Go 客户端交叉编译、Python／Node／Shell 语法检查、5 项既有清理责任测试及 diff 检查。

两个实验目录的私有挂载命名空间均无剩余 PID。可重建的 Node／SRT／工具依赖、安装缓存、归档和客户端二进制已清理，源码、日志和收据保留在指定目录；清理后宿主可用空间约 5.8 GiB。
