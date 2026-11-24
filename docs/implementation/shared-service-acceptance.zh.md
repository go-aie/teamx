# 共享服务与真实模型增量验收

日期：2026-11-24。范围：多空间方案 S3b.3。**共享服务双空间与真实外部模型的增量检查已通过；S3b.3 的正式出口仍等待 S3b.2a 的前置验收。** 实际收据见证据（本地验收文件 `shared-service-acceptance-evidence.json`）。S3b.2a 记录仍保留真实飞书 OAuth／刷新／DPoP 的未关闭门槛；本记录不代替该验收，不据此开放多用户或声明整体 S3 完成。

## 证据复用与新增范围

| 项目 | 来源与适用条件 |
| --- | --- |
| 原生执行、恢复合同、锁及额度、超时保留原 owner | [执行接入记录](agent-execution.zh.md)及证据（本地验收文件 `agent-execution-evidence.json`）；本轮不修改执行、Session／Store 或关闭协议 |
| 真实 SRT＋DSH 文件／Shell 隔离、代理结清、取消、故障及轮换 | 同上；复用锁定依赖，正常服务启动重新核验实际依赖摘要及宿主前置；不重跑独立探针及故障矩阵 |
| GitLab／飞书工具准入、Git 全入口关闭、个人 Git 回归及基础保护 | [首发工具记录](platform-tools-admission.zh.md)及证据（本地验收文件 `platform-tools-admission-evidence.json`）；保持其真实 CLI／合成生命周期与真实平台的证据区别，飞书真实授权门槛仍未关闭 |
| 同一共享核心中的双空间串联、实际外部模型和 MCP／CLI | 本轮增量，使用正常 `runConfiguredServer --home`、真实 SRT／DSH、独立 SpaceRuntime 和现有首次模型配置／归档／恢复入口 |

新增[一个 Linux e2e 场景](../../internal/room/space_shared_service_linux_real_test.go)，先由 A、B 分别使用真实模型完成 MCP `inbox_list`／`inbox_read` 和产品 CLI 本地消息回执，再归档 A，在 A 停止时执行 B，恢复 A 后继续执行两侧。续轮可复用已知 `stream_ref` 直接 `inbox_read`，保留每轮实际读取与 CLI 回执检查。检查新运行对象／epoch、原 User／Space／成员身份、原 SessionID／generation 恢复、旧 service epoch 和旧委托凭据拒绝，以及 B 的原业务实例持续可用。成功回执必须同时具有已完成回合和实际工具诊断，单独出现模型文本不算通过。

部署工厂新增工具策略选择接点，生产仍使用原 `deploymentHostTools`。测试通过私有装配选择同一受限工具策略，并沿正式服务创建所有运行时；没有新增配置、公开模式或绕过认证的 HTTP listener。`multi-user` 的生产拒绝门槛保持，后续认证及开放仍属 S4a。首次配置保留现有的全服务模型验证槽，依次等待持久验证完成，不新增队列或公平调度。

## 本轮结果

用户指定 Linux 宿主上，复用 SRT `0.0.83`、Node `24.22.2`、DSH `0.2.2-rc.1` 及固定工具安装；核心使用 UID/GID `65534`、清空 capabilities、`no-new-privileges`。只写入新建私有验收目录，不修改系统安装、挂载、账号、父目录权限或既有服务。实际模型为默认配置中的 `deepseek-v4.1-flash-1017`，经其 HTTPS 上游完成真实文本／工具验证及五轮 MCP／CLI 场景；没有外部平台业务写入。

`TestLinuxSharedServiceArchiveWithModelCallbacks` **204.35 秒通过**。A 原 SessionID／generation 恢复，B 原服务实例继续使用；旧 service epoch 收到 identity 拒绝，旧委托凭据返回 HTTP 401，最终共享 Session 额度为零。原生／受限工具装配的定向回归、工厂缺失前置拒绝和定向 race 通过；本轮未扩大到整个 Room 包或全仓回归。

早期失败均为验收夹具或断言问题：发布文件与私有根重叠、遗漏显式启用登录、上传期间启动、未等待全局模型验证槽，以及续轮不必要的 list 断言。相应记录保留在证据中；修正后重跑本条场景，没有降低 SRT 策略或改变生产恢复／结清条件。结束后独立检查本次进程已退出，删除测试模型／管理凭据副本及可重建二进制，保留脱敏收据；原锁定依赖安装未改动。

## 可重复入口

在满足 SRT 前置的非 root Linux 上，显式提供：

```sh
TEAMX_SHARED_SERVICE_CONFIG=/absolute/private/source.toml \
TEAMX_SHARED_SERVICE_OUTPUT=/absolute/fresh/output \
TEAMX_SHARED_SERVICE_CLI=/absolute/pinned/teamx \
./room.test -test.v \
  -test.run='^TestLinuxSharedServiceArchiveWithModelCallbacks$' \
  -test.timeout=9m
```

测试产物由 `GOOS=linux GOARCH=amd64 go test -tags=e2e -c ./internal/room` 构建。source 是显式准备的测试配置，包含 Linux DSH launcher、受控 SRT manifest、稳定 loopback 端口及 HTTPS 真实模型的默认 catalog；凭据按原 `model-credentials` 约定置于 source 同目录。CLI 必须位于 manifest 的固定只读发布项，不能与私有数据保护根重叠；PATH 提供锁定 lark-cli 以装配首发受限工具。测试创建全新 home，不将 source 的平台连接、环境或 Computer 配置复制进去；报告不写入密钥。

本轮不执行真实平台业务写入，不启用托管 Git、Scion 或 Computer，不跑 Room 整包、全仓回归、发行、容量或长时观察。S3b.2b、S4b、精细硬配额及 Gordis 接入保持后置。S3b.2a 的独立真实平台缺口关闭后，仍须核对证据适用条件，才能关闭该组合的 S3b.3／S3 出口并进入 S4a。
