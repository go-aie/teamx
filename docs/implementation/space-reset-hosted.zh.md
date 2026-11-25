# Linux 多用户空间重置

2026-11-25：[Space 重置方案](../SPACE-RESET-PLAN.zh.md)阶段 2 已实现，并完成多用户核心、Linux 真实凭证材料及默认模型增量验证。Linux 开发版接通原有设置页和控制面 API；正式发行仍受既有多用户部署门槛与阶段 3 安装组合验收约束。本次没有更新线上安装或重置已有用户空间。

## 行为

- 沿用阶段 1 的操作记录、关闭协议、固定清单、配置／模板初始化和浏览器缓存隔离。清理整个空间私有 `platform-cli/lark`，包括 CLI profile、应用密钥、加密 Token、主密钥、DPoP、锁及日志；清理模型候选、迁移副本、Data 内的平台连接和凭据。不会调用远端删除、登出或撤销授权。
- 受限部署的工具进程与飞书发现、应用配置、登录二维码、Pinned、图片下载／上传使用 `Home/platform-tmp`。CLI 子进程的 `TMPDIR` 也指向此根；普通工具使用该根下的单次私有目录。正常退出仍清理，崩溃遗留由重置清单清理。根不存在、权限不私有或被链接替换时拒绝执行，不回退系统临时目录。不修改服务进程的全局环境。
- 重置受理与管理员停用串行提交。停用先撤销准入；正在重置的空间由原 worker 继续结清，停用返回 busy 时不另起归档 worker，也不覆盖 reset 阶段。最终发布及重开前再核验 owner。停用发生在 completed 发布与释放清理锁之间时，同样转为 `suspended + completed`，避免启用等待条件永远不能成立。
- 用户不可用或资源不足时，清理可以完成并挂起；之后使用原启用／恢复路径。完成后的请求重试只处理装配，不重新清理。旧 worker 收尾按 request ID 核对，不能清掉后续重置的 busy 标识。
- 保留用户级网关凭据、系统登录及部署级 GitLab 服务应用。新实例使用现有网关默认模型验证／初始化流程，生成新模型凭据引用、Room 和 RuntimeEpoch。没有凭据或验证失败时留在模型设置，不恢复旧模型。页面区分“重置完成”和“默认模型尚未就绪”；仅 macOS 个人组合说明原生飞书登录保留。

没有新增部署配置开关、临时文件管理服务、业务数据代次或平台 reset 协议。入口能力由部署装配决定，客户端不能开启未获支持的组合。

## 验证与证据

核心／定向 race 覆盖：两用户各两空间、跨 owner 拒绝、其他空间 Runtime／登录／凭证保留、用户级网关与共享应用保留、新默认模型和 Room、已受理模型 writer 结清、停用后完成及恢复、completed 与重开之间停用、归档空间在资源不足时完成、同请求重试不二次清理，以及 stopping／cleaning／initializing 在新控制面实例中自动续跑。

飞书和工具定向 race 覆盖原有配置／登录、Pinned、图片及进程流程；新增私有临时根绑定、正常清理、根缺失／链接拒绝检查。共同临时目录函数放在 `internal/platform`，保留飞书与通用工具之间的包依赖边界。架构边界检查已通过。

Linux amd64 实机以非 root 的 `teamx` 身份运行隔离测试：

- 从已登录空间**只读复制**官方 CLI 的真实加密存储到新临时 Home 中的四个空间；未复制旧业务数据，未向原空间写回。
- 使用真实 `lark-cli 1.0.99 auth status` 核对副本中的 refresh 元数据；真实材料包含应用密文、OAuth 凭证密文、32 字节主密钥和 DPoP 密文／软件 signer 文件。重置一个空间后，其整个旧 CLI profile 与崩溃临时副本消失，另三个空间的主密钥保留。
- 使用已有用户级网关凭据，通过真实 DSH 执行四个空间首次默认模型验证和目标空间重置后的验证；`run-03` 中 **5 次均成功**；最终代码复验 `run-final` 为 3 次成功、2 次 `text:rate_quota`，同时验证“模型服务限流或额度不足”的已有设置反馈。两轮均完成目标清理和隔离检查，没有因模型失败恢复旧模型或重复清理。
- 两个登录主体由测试认证 provider 提供，使用同一真实网关来源；本次不声称完成两名真实 SSO 用户各自额度的安装组合验收。隔离测试没有新发起 OAuth、强制刷新原 grant 或执行远端平台业务写入；证明的是已有真实 OAuth／refresh／DPoP **持久材料的清理**，不声称强制刷新竞态的实机验收。长调用结清和迟到写入由核心／适配器测试覆盖。
- 验收前核对 `/tmp`，未发现 `teamx-tool-*`／`teamx-lark-*` 历史目录；没有扫描删除系统临时文件。其他旧环境仍须各自核对，不能据此宣称所有历史全局临时残留已处理。
- 测试结束删除整个临时业务 Home，包括其他三个空间的授权副本和新模型私有材料；仅保留脱敏报告。原服务继续运行。

实机报告在验收机 `/data/teamx-reset-acceptance-20261125/` 下的 `run-03/report.json`（五次模型成功）和 `run-final/report.json`（最终代码及真实限流反馈）；本地脱敏副本在 `.local/space-reset-hosted-20261125/`。本机 Chromium 复验通过确认、双标签、缓存隔离与重置后设置流程；页面测试另覆盖多用户失败反馈及不确定请求沿用同一正文。本轮前段的 881 项前端回归通过；新增重置页面 3 项及空间缓存定向测试、vet、e2e 编译和定向 race 通过。最终工作区全量前端为 885／888 通过：`web_browser_test.mjs`、`web_files_test.mjs`、`web_navigation_test.mjs` 各一项滚动位置断言 `100 != 37`，独立重跑可复现。这些会话／导航文件在任务期间出现其他改动，本次未修改它们；不声明最终全量前端通过。

完整平台 race 使用三分钟总预算时，在未修改的 `TestSearchBacklogRestartAndDelayedIndex` 中超时；不声明全平台回归通过。随后受影响的配置／客户端／Pinned／图片／进程族定向 race 通过。阶段 1 记录中的全量 Room／integration 限制仍保留，本次没有改动这些既有问题。

## 复验

```sh
go test -race -tags=integration ./internal/room \
  -run '^Test(SpaceReset|SpaceArchive|Spaces|Hosted)' -count=1 -timeout=3m
go test -race -tags=integration ./internal/platform/lark ./internal/platform/tool \
  -run '^Test(ScopedLark|Process|Client|Configure|Setup|Pin|Image|Send|.*Temporary)' \
  -count=1 -timeout=3m
go test ./internal/platform ./internal/architecture -count=1
node --test internal/web/tests/web*_test.mjs
go vet ./...
make test-e2e-check
```

真实材料测试仅在非 root Linux 显式执行，不进入普通 CI：

```sh
TEAMX_RESET_LINUX_SOURCE_SPACE=/absolute/registered/source-space \
TEAMX_RESET_LINUX_OUTPUT=/absolute/new-private-output \
go test -tags=e2e ./internal/room \
  -run '^TestSpaceResetLinuxCredentialMaterials$' -count=1 -timeout=9m
```

来源 Space 所属用户没有网关凭据时，可显式设置 `TEAMX_RESET_LINUX_GATEWAY_OWNER` 为同一部署中专供验收的凭据来源用户 ID。测试只读取来源；所有复制、新模型及清理写入均在新目录。不要把日常 Space 指定为输出目录。真实模型因限流／额度失败时，报告记录安全错误码并核对设置状态；不能把失败写成验证成功。

阶段 3 已收敛为最终包资源、新装／升级、正式启动方式与一次重置冒烟，进展见[安装交付验证](space-reset-delivery.zh.md)。本阶段已有功能矩阵不重复执行；真实认证／强制 refresh 竞态若属于必要功能证明，应记在原阶段，不能挪到交付验证。正式发行继续沿用既有前置。
