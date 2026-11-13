# 独立 Server 生命周期与版本目录

日期：2026-11-12。对应[常驻 Server 方案 W1](../DESKTOP-APPLICATION-ANALYSIS-AND-PLAN.zh.md#w1独立-server-基础)。本阶段只在隔离 loopback 环境验证；后续 token 登录、浏览器模型表单和 HTTPS 代理已在 [W2](server-access.zh.md) 实施，系统服务安装留在 W5。

## 实现范围

- `serve` 保留 foreground 信号适配；`runServer` 接受 context、listener factory、ready 回调和业务 factory。取消时沿用原有 Room／平台操作／通知／DSH 的关闭机制，业务停止受理与调度后再结清 HTTP；异常启动也释放资源。
- 新增 `serve --home DIR`，读取该 home 的 `config.toml`，不依赖工作目录或 stdin，不读取开发目录配置。home 锁覆盖配置读取到服务停止；原数据根锁仍保护业务目录，同数据根不能通过另一 home 或旧 `serve --config` 并发写入。
- 新增 `serve --server-root DIR [--home DIR]`。`current` 必须选中 `releases/` 下的真实版本目录，包含本机平台的 standard 清单；home 默认采用 W0 固定的 Linux／macOS 路径。此入口消费部署好的目录，不下载、解包或安装服务。
- 首次生成 `installation.json`，记录 schema、实例 ID、真实 server root 与 home；重启保持该身份，另一个 home 不能重新绑定同一登记。归档摘要验证、build-id 生成、安装串行发布与服务管理在 W5 实施。
- 初始配置及模板只写入 home，保留已有文件。资源从选中的真实 release 读取，包内工具发现使用真实 release 的 PATH；资源目录可只读。home 和 data 不允许落入安装根，路径解析不会改写宿主 HOME。
- 托管默认 DSH 配置保留 `<server-root>/current/bin/dsh` 作为下一次启动的选择器，启动时先解析选中的真实 DSH 路径，再交给 Room 冻结。切换版本只影响新建 Room；显式外部 Runtime 和已冻结的旧路径保持原值，不清理旧 release。
- `--home` 模式缺少模型、模型结构无效或凭据为空时进入 `setup_required`，不创建 Room manager／业务快照／平台 worker／DSH。`GET /healthz` 仅返回最少状态，其余请求均为 503；启动记录明确 `ready: false`，不提供配置读取或写入表单。配置修复后正常重启进入原业务启动路径。结构校验与真实模型连通性并不等价。
- 旧 `serve --config`／单 Room 开发模式继续保留；bundle 的 `start` 转入同一 home 生命周期，显式 `doctor` 仍负责依赖诊断。普通可移动 bundle 与独立的版本化 Server 部署保持各自目录约定。

## 本机使用方式

已有私有配置可直接运行：

```sh
teamx serve --home '/absolute/用户 home' --listen 127.0.0.1:8088
```

已经准备好版本目录时，可通过其中的 Go binary 使用托管布局：

```sh
'/absolute/server-root/current/bin/teamx' serve \
  --server-root '/absolute/server-root' \
  --home '/absolute/用户 home' \
  --listen 127.0.0.1:8088
```

不在本阶段运行 `teamx service install`，也不开放公网代理。首次状态只报告需要配置；W2 才提供认证后的浏览器设置与修复，当前隔离开发验证使用终端初始化／测试配置。

## 验证与边界

验证入口：

```sh
go test ./... -timeout=8m
go test -race -tags=integration ./internal/room -run '^TestServer' -count=1 -timeout=2m
go test -race -tags=integration ./internal/room \
  -run 'Test(ServeConfigCLIOverridesAndSecretIsolation|ProductionRoomCatalogAcrossWorkingDirectories|RuntimeRecovery|RuntimeShutdown|DefaultAssistantManagedStartup|Bundle|Startup|GlobalRuntimeDefaults)' \
  -count=1 -timeout=3m
go test -race ./internal/agent/dsh \
  -run '^Test(CancelTimeoutReapsDetachedTool|CloseDuringTurnAndWithoutCapability|CloseProtocolFailureStillReapsAndReleases|ProcessObserverSurvivesGroupInterrupt)$' \
  -count=1 -timeout=2m
```

测试覆盖：缺少／无效模型与空凭据、修复重启、setup 不写业务快照、home／数据独占、占用端口诊断、readiness 失败后的资源释放、中文和空格路径、只读 release、切换 current 后新旧 Room 的冻结路径与登记身份、外部 Runtime 保留。ACP 集成使用真实 Node 子进程和离线 fixture，核对正常 session close、进程退出以及重启后的助理和消息身份；不连接真实模型或平台。

本机 macOS arm64 上，新增 Server 测试带 race 通过，相关配置／bundle／目录／恢复／停止集成回归带 race 通过，DSH 脱离进程组工具与协议失败清理定向回归带 race 通过。ACP 使用宿主 Node 与离线 fixture，不是最终 standard 归档验收。

验证过程中，首次全量核心回归触及 3 分钟的整包预算，随后按 8 分钟预算重跑。现有配置集成测试也遇到恢复期间的 `busy` 拒绝，测试改为保留同一 create ID、有界重试该明确的受理前拒绝后通过，不重试未知结果。ACP fixture 最初缺少 DSH 要求的 `--version` 响应，修正测试包装后通过，未改动生产执行协议。

全量 `go test ./... -timeout=8m` 中，Room 包运行约 258 秒，一项现有 `TestGitLabConnectCallbackAutomaticallyContinuesThroughOrdinaryInbox` 在 `ConfirmInboxBatch` 收到 `busy` 后失败；该测试单独复跑通过（约 1.2 秒）。其他包通过，但本轮不能记为全量全绿。该测试直接装配原 GitLab／Inbox 服务，不经过新增 Server 生命周期入口；未为本阶段修改其业务逻辑或断言。W1 新增及相关恢复／停止回归均通过上述独立验证。

原生 Linux、systemd／launchd、真实 standard 升级、真实模型／平台、HTTPS 和手机均未在本阶段验收。Linux 飞书账号固定发送门槛沿用 [W0 记录](server-web-foundation.zh.md#linux-飞书结论)，未放开能力保护。
