# Agent 执行环境接入与恢复

日期：2026-11-24。范围：多空间方案 **S3b.1 已完成**。原生路径、受控替代执行与非 root Linux 真实 SRT＋DSH 的正式 Room 接入／恢复出口已经通过。不进入 S3b.2a，不开放多用户。

## 已实现的接点

- [执行合同](../../agent/execution.go)只承接进程请求、完整空间／成员／代际绑定、执行身份、ACP 文件通道及清理。原 `agent.Factory`／`Host`、Session、Store、输入消费和业务结果 owner 保持。
- [DSH 适配](../../internal/agent/dsh/execution.go)保留原生进程观察器，统一 ACP、版本查询和执行资源；取消检查、关闭及恢复检查不再直接依赖具体进程字段。个人默认原生执行，不读取 SRT 配置或初始化 SRT。
- [SRT 工厂](../../internal/agent/srt/factory.go)与每次执行独立的监督进程使用固定 `0.0.83` npm lock、Node `24.22.2`、显式工具路径与安装摘要。没有运行时安装／latest、Gordis、空间 worker 或新的业务 RPC。
- 部署装配把选择传给每个 SpaceRuntime，再由正式成员 Host 传给 DSH；模型验证、模型应用检查、原 Session 恢复与轮换使用同一选择。新空间只重绑自己的 User／Space，不复制其他空间的环境。

## 身份、策略及生命周期

`backend.json` 分别保存 DSH 本体路径／摘要、脚本摘要与 ACP 身份，以及执行实现／依赖摘要和有效策略摘要。策略包含正式 User／Space、完整成员身份、Session generation 与实际目录。随机 token、Key 和代理端口不进入执行身份。旧的原生参考文件仍可读取；新生成的脚本摘要独立校验。实现或策略变化拒绝原 Session 恢复，保留证据，不自动重置或重放。

执行策略默认拒绝读取宿主，只显式开放所需运行库、固定发布内容、工作目录及 DSH 自有 `home`／`agents`／临时文件。核心 backend／settlement／锁文件所在的状态父目录不获读写授权；`AGENTS.md` 和 patch 不可写。拒绝与运行库、私有根重叠的可写目录、预存硬链接及未经准入的挂载别名／嵌套挂载；链接扫描有明确上限。环境只带本 Session 的模型凭据、委托 token 和必要绑定，过滤其他连接及解释器注入变量。当前端点清单为精确目标，不接受通配出口或弱化网络／namespace 参数。

监督进程、代理和 DSH 通过原进程树观察器结清；`Ready` 后的固定基础进程与后来启动的工具区分观察。基础进程消失禁用原 Session，取消必须等待工具退出，关闭必须等待整棵进程树及 reader 退出。wrapper／ACP 返回本身不释放锁或额度。

恢复失败诊断与未结清资源分别判断：只有每个关闭错误分支都有已结清收据，Space／manager 才继续释放根 owner，并保留原 Session 不可恢复的诊断。混入任意未知错误、超时或 pending 分支均保留原 owner，不能因为错误树中存在一个已结清分支就释放其他资源。

部分打开失败返回非 nil Session；调用者的 Close 超时只限制等待，原对象继续持有执行、目录锁及共享 Session 额度。后续 Close 等待原清理；结清后才允许释放或由原轮换流程创建下一代。版本查询也走相同执行选择；独立运行检查有自己的临时目录及清理 owner，同空间未结清检查拒绝重复启动，另一空间不受该检查门槛影响。

## 装配与当前限制

默认无需新增配置。部署级配置识别以下选择：

```toml
[deployment]
mode = "personal"

[deployment.execution]
kind = "native"
```

显式选择 `kind = "srt"` 时还须给出绝对 `manifest` 路径。manifest 使用 `node`、`bwrap`、`socat`、`ripgrep`、`runtime`、`read_only`、`endpoints`、`private_roots`；每个程序／发布树声明 `path` 与 `sha256`。目录摘要由 `srt.DigestPath` 对安装后的文件名、类型、模式、长度、内容及内部链接计算；npm 完整 lock 同时校验。`runtime` 指包含固定 lock 与 `node_modules` 的安装根，`read_only` 包含 DSH 发布树。验证限定于显式部署 manifest；SRT 依赖安装与发行打包仍由后续发行验收覆盖。

仅所选 SRT 组合在首次业务凭据初始化、监听与恢复前做前置检查，拒绝非 Linux、root 核心、未清空 capabilities、缺少 no-new-privileges、依赖错误／警告或不合格 namespace。`mode = "multi-user"` 仍直接拒绝。

**内部回调适配。** 固定 DSH／Go 会无条件绕过 loopback 代理。执行层将已批准的 loopback URL 转为确定性的 `tx-<hash>.execution.invalid` 别名；仅本 Session 的 SRT 代理解析该别名到原地址／端口，未知别名不查 DNS，不修改 DSH 发布内容。允许项同时限定域名、解析地址和端口，拒绝其他私网解析结果，不开放宿主网络。TeamX 仅接受来自 loopback、无浏览器 Origin／转发头且具有对应委托凭据的别名请求；管理员凭据不能使用该入口。MCP 经过作用域认证后，才为 SDK 的 localhost guard 还原内部 Host。

CLI 通过现有输入桥取得运行时本地代理地址，强制代理连接该内部别名；普通凭据仍要求 HTTPS 或 loopback。随机代理端口不写入执行身份／配置。输入绑定文件放入只读的 `control` 目录挂载，以便核心原子替换文件时仍可见；没有开放状态父目录。

原 DSH 的 `workspace-write`／ask 保持，未启用 nested namespaces 或弱网络。此 Linux 宿主的 DSH 内层实际使用 Landlock fallback，并提示旧 ABI partial enforcement；隔离保证仍由外层 SRT 提供，已实际验证文件／Shell 跨目录拒绝。监督基线保留常驻代理，排除其每次连接 fork 的子进程；这些子进程仍由原进程树 owner 保留并在关闭时结清。

## 验证

本机完成：

- `agent`、DSH 与 SRT 包测试；新执行合同、独立 DSH 脚本校验、待清理运行检查、策略、环境及依赖摘要测试。
- Room 的 Model／DSH、部署拒绝、Space 绑定、原共享额度及资源生命周期定向回归。
- `integration`：公开 Factory 的原 Session 恢复；正式服务取消、进程退出与同 home 重启；正式成员 Host 的新建、恢复、轮换、关闭超时持有锁／额度及重试原执行。
- 新执行／策略／运行检查、回调代理认证与 Origin、正式成员及 Space／manager 关闭的定向 race；Node 语法及 diff 检查。

Linux 验收使用用户指定 SSH 宿主，写入限于 `/root/luop/teamx/agent-runtime-b3aa0fa55138`。用户将 `/root`、`/root/luop` 的父路径遍历权限调整为 `0701` 后，使用 UID/GID `65534`、清空 capabilities、`no-new-privileges` 的核心进程验收。依赖按既有锁提取到本次目录，未安装系统包、修改挂载／账号／父目录权限，未读取真实平台凭据。

`TestLinuxDSHExecution`（110.50 秒）使用真实 SRT `0.0.83`＋DSH `0.2.2-rc.1` 已通过：模型代理请求、工作目录写入、核心／另一空间／另一主体模拟私有目录读取拒绝、实际 Shell 写入／跨目录拒绝、原 Session 正常关闭恢复、双空间同时运行、取消后无迟到写入且另一空间继续工作。分别终止本次生成的 DSH／监督进程／代理后，原执行结清，异常 Session 拒绝恢复。模型端为受控合成 HTTP 服务，**不是外部真实模型验收**。实际命令与收据摘要见证据（本地验收文件 `agent-execution-evidence.json`）。

`TestLinuxSpaceSRTCallbacksAndRecovery`（90.25 秒）通过正常 `runConfiguredServer`／正式 Room／成员 Host，使用真实产品 CLI，完成三轮 MCP 列表／读取与 CLI 发消息、输入消费回执、正常关闭后同 SessionID 重启恢复、手动轮换后新 SessionID、正式 pause/cancel 后无迟到写入，以及 DSH 故障后的结清／原 Session 恢复拒绝。故障关闭仍返回恢复失败诊断，但 Space 已停止并释放根锁；本机另验 manager 根锁，以及关闭超时保留锁／额度、混合错误不误释放等路径。

此前 root 下的 17 项 DSH 合同检查及 6 项 SRT 策略检查仍保留为历史证据，不冒充真实 SRT 组合验收。

未运行整个 Room 包、全仓回归、真实模型或外部平台写入。

## 验收结论及后续边界

S3b.1 接入及恢复出口通过。双空间同时隔离／一侧取消不影响另一侧由真实执行测试证明；正式成员合同、代际拒绝、Space 装配与资源 owner 由对应回归和集成测试证明。这里明确区分证据层次，不把它们称作完整共享核心 S3b.3 组合验收。真实外部模型、完整首发工具与多账号开放仍属于原方案后续单元。

服务端代执行安全、首发工具裁剪及完整共享核心组合分别留在原方案后续单元；多用户保持关闭，不进入 S3b.2a。
