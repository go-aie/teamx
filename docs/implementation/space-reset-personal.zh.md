# 个人空间重置

2026-11-25：[Space 重置方案](../SPACE-RESET-PLAN.zh.md)阶段 1 已实现。个人模式可从设置 → 通用底部的“重置此空间”发起；模型设置页和暂不可用页也提供状态、原请求重试。阶段 1 交付时多用户 API 在状态修改前返回 403；后续阶段 2 已接通 Linux 开发版，见[多用户实施记录](space-reset-hosted.zh.md)。所选 Linux 安装交付结果见[阶段 3 记录](space-reset-delivery.zh.md)，未验证组合不随其声明。

## 行为与实现

- `spaces.json` 保存最新 `reset` 操作和原请求版本；`stopping → cleaning → initializing → completed` 与原有空间生命周期一起提交。受理后关闭业务准入，复用原 runtime、HTTP、模型验证、OAuth、通知、Agent 和 Computer 关闭协议。未结清不删除；重试只推进原操作，完成后的重试只处理新实例装配。
- 清理已登记 Data／Teams 中的业务材料，以及 Home 下明确归属 TeamX 的模型密钥、候选、验证、操作和迁移前像。保留配置中的部署／Computer 字段、安装身份、登录、其他空间、指定目录文件和 Computer 文件／占用；仅释放已结清的本空间工作目录绑定。macOS 原生飞书登录不执行登出、撤销或 Keychain 操作。
- 规范路径和根 inode 在清理前复核。删除使用已验证的 `os.Root` 描述符逐层下降，链接只解除链接；保护文件的父目录清理兄弟条目，保留根与持锁文件。顶层链接、被替换的根、未登记挂载或无法分类的自定义 `env` 在继续前拒绝。
- 重置候选文件仅含前后配置哈希和根身份，不复制旧配置或密钥。配置通过 `configDocument` 冲突检查、原子发布和持久确认；新实例重新映射业务运行参数。启动在模型迁移、业务扫描前识别未完成操作；stopping 仍检查持久 Agent 结清证据，cleaning／initializing 各自幂等续跑。初始化、备份和升级接点拒绝尚未完成的重置。
- 浏览器先取得 Space 与 reset ID，再恢复缓存。标识变化时清除该空间草稿、偏好及业务引用，冻结旧页写入并取消显示任务；保留其他空间和登录。普通重启保留草稿。Data 内保留的部署／管理员文件不会被首次模型设置误判为旧业务数据；未保护的业务文件仍拒绝首用写入。模型设置提交必须携带 GET `/v1/setup` 返回的 `runtime_epoch`；普通业务和上传继续使用原有 epoch／Room 校验。

2026-11-28 模型初始化统一：个人版新增空间重置后，读取创建时选定的同 owner 来源空间的当前默认模型和凭据，与多用户版共用首次模型设置入口及验证、保存、失败反馈和显式重试规则。新模型凭据独立保存，不恢复目标空间重置前的模型或旧 Room。来源不可用或凭据缺失时保留可用参数供手动设置；个人默认空间没有外部来源时仍进入手动配置。首次设置响应前已提交自动验证，页面直接显示进度；失败或中断后的普通重启不重复自动调用模型。

初始团队模板统一位于**服务启动配置所在目录**的 `defaults/teams/`。个人安装版、便携包和 Linux 多用户版共用这一规则；空间私有的 `config.toml` 不改变来源。安装和启动从包内 `share/teamx/teams` 准备当前版本的 defaults，升级不覆盖已有空间的团队修改。

源码开发在停服后按打包公共文件清单准备模板，再照常启动：

```sh
make prepare-defaults CONFIG=.teamx/config.toml
./bin/teamx serve --config .teamx/config.toml
```

新建空间从该来源初始化自己的 Teams，重置清理后从同一来源恢复。普通重启不向已有空间重新复制模板。无需 `--reset-teams-source`，不再根据 DSH 或 Node 的路径猜测模板位置。模板缺失、包含链接、不能通过托管模型模板检查时，在受理前报错；defaults 不得落在任何重置清理目标中，也不会被空间重置删除。

## 验证

所有写入均使用新建临时 Home 或 `.local/space-reset-*` 私有验收目录，没有重置日常空间，没有平台远端写入。

2026-11-28 增量验证通过：个人来源模型、Hosted 默认模型／凭据隔离、OpenCSG 网关、重置及首次模型设置的定向 integration race；架构检查；隔离 Chromium 双标签、缓存隔离、无来源的手动设置和有来源空间重置后自动进入聊天。新增模型初始化验证使用可控模型桩，不代表真实服务商或日常 `test` 空间已完成验收；本次没有重启日常服务。

- 核心／integration：默认、新增、归档空间；凭证与迁移副本清理；外部目录、Computer、安装身份及管理员文件保护；重复正文／版本拒绝；配置默认值与新实例参数；setup 旧 epoch；已受理验证 writer 结清；无持久执行证据不删除；不确定提交及各持久阶段续跑。
- Chromium：设置确认、双标签、模型设置、普通刷新保留草稿、重置清除旧缓存、其他空间缓存保留、页面零脚本错误。截图及报告在 `.local/space-reset-browser-20261125-01/`。
- 真实 DSH／模型：`.local/space-reset-real-20261125-02/` 中完成重置前 `TEAMX_RESET_BEFORE_OK` 和重置后 `TEAMX_RESET_AFTER_OK` 对话；重新验证模型，使用新 Room 与 RuntimeEpoch，双标签检查通过。旧配置仅作为参数／密钥的只读来源。
- 独立进程：编译后的 `teamx serve` 在 stopping／目录锁冲突时被强制结束，重新启动自动使用原 request ID 完成重置，旧材料删除并重新进入模型设置。

复验入口：

```sh
go test -race -tags=integration ./internal/room \
  -run '^Test(SpaceReset|SpaceArchive|Spaces|AccessBrowserModel)' -count=1 -timeout=3m
node --test internal/web/tests/web*_test.mjs
go vet ./...
make test-e2e-check
TEAMX_SPACE_RESET_BROWSER=1 go test -tags=e2e ./internal/room \
  -run '^TestSpaceResetBrowser$' -count=1 -timeout=3m
TEAMX_SPACE_RESET_PROCESS=1 go test -tags=e2e ./internal/room \
  -run '^TestSpaceResetProcessRestart$' -count=1 -timeout=2m
```

真实模型复验额外设置 `TEAMX_SPACE_RESET_REAL_CONFIG` 为已有托管模型配置的绝对路径；`TEAMX_SPACE_RESET_OUTPUT` 指向尚不存在的私有绝对目录。测试只读取来源配置及对应私有模型凭据，所有新业务写入都在输出目录中；不输出密钥。真实模型用例不要加入普通 CI。

普通前端回归 881 项、vet、e2e 编译与定向 race 已通过。完整普通 Room 回归在既有 `TestServiceRetentionRejectsUncertainReferencesOrRecoveryMaterialBeforeDeletion/symlink-backup` 处达到 5 分钟总预算；未发现先行断言失败，但不能称为全量通过。完整 race 中另发现既有飞书 `TestLoginRequestsSupportedCompleteScopeSet` 和 `TestLoginIncludesCurrentReaderAndSendDependencies` 的 scope 断言失败，独立重跑可复现；本阶段没有修改飞书 scope 逻辑。完整 race 的 Room 包也在既有飞书图片独立进程恢复组合处达到 8 分钟总预算；全仓结果未通过，以上限制保留，未据此缩减重置验收。

上述证据属于阶段 1；Linux 多用户凭证、临时根和共享应用隔离的后续结果见[阶段 2 记录](space-reset-hosted.zh.md)，安装产物组合验收仍属阶段 3。
