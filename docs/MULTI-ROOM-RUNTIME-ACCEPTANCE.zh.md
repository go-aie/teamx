# 多房间独立运行验收记录

日期：2026-11-04。对应[实施方案第 4 步](MULTI-ROOM-RUNTIME-PLAN.zh.md)。代码和文档已实施，真实浏览器验收仍未通过。

## 组件、HTTP 与前端

- `go test ./... -count=1 -timeout=10m`：通过，Room 包约 201 秒。覆盖双实例路由、身份拒绝、同房间冲突、跨房间工具／通知／凭据及生命周期隔离、恢复和退出失败归属。
- `go vet ./...`：通过。
- 前端四个 Node 测试文件：111 项通过。覆盖双标签页独立选择和刷新、草稿及未确认发送、慢打开回执、重复创建／打开、原身份重试、迟到响应和后台房间操作不锁定当前房间。
- 实际 HTTP + 应用入口、Workspace 绑定及共享连接 handler 回归：通过。使用受控 DSH／模型和 DOM 替身，不代表人工浏览器或真实模型证据。
- `go test -tags=e2e ./internal/room -run '^$'`：仅编译验收入口，不执行真实 GitLab 操作。

定向竞态检查通过，约 86 秒；最后新增目录断言及 HTTP／CLI 入口补充回归通过，约 24 秒。竞态命令：

```sh
go test -race ./internal/room \
  -run 'TestRuntime|TestManaged|TestWorkspace.*(Apply|Retry)|TestService.*(Close|Shutdown)' \
  -count=1 -timeout=8m
```

## 真实 DSH

入口：`TestRealDSHManagedRooms`，通过，约 115 秒。使用本机 DSH `0.2.1-alpha.1`、Node `26.7.0`、模型 `deepseek-v4-flash`；服务、CLI、Room、MemberAgent、ACP、模型及工具均为真实链路，只使用隔离临时房间和本地文件，不写远端项目。

本次原始私有证据目录：`/tmp/teamx-multi-room-real-20261104-03/`。`report.json` 保存结果、两个实际 ServiceInfo、重叠执行成员状态和退出回执；`rooms-root/` 保存协作意图、预算、回合及各 Session 的正常停止／结清证据。临时目录可能被系统清理，凭据只从环境文件加载，不复制进报告。

验证结果：

- A 执行 60 秒工具，打开 B 后执行 B 的 20 秒工具；在 `2026-11-04T05:51:09.379528Z` 同时观察到 A、B 的 `active=true`。
- A → B → A 保留健康实例 epoch 和原 Session；两个房间的私有状态、SessionID、公共消息与预算独立。后台公共消息 `runtime-A-done`、`runtime-B-done` 均落入原房间，之后各自接收新输入并正常完成下一回合。
- 暂停及累计预算跨重启保留；两个启用房间在无人查看时恢复为可用实例，不依赖页面选择。
- 两个房间分别启动 120 秒工具并实际写入就绪标记后退出服务；两项完整身份的 `shutdowns` 均无错误，两房间均通过 DSH `CheckResume` 和 `CheckReset`，没有产生延迟副作用标记。
- 再次启动后两房间的中断回合保持暂停，累计预算分别为 A=5、B=4，旧工具没有重放。

重跑时选择新的绝对证据目录，并指定本机安装和模型：

```sh
# TEAMX_DSH_BINARY_PATH / TEAMX_DSH_BINARY_ARGS 按实际安装填写；后者必须是 JSON 字符串数组。
# MODEL_ID 必须显式指定；MODEL_BASE_URL 也可用下方测试参数设置。
go test -tags=e2e ./internal/room -run '^TestRealDSHManagedRooms$' -count=1 -timeout=12m -v \
  -teamx-management-real-output=/absolute/new-evidence-directory \
  -teamx-management-env-file=/absolute/model.env \
  -teamx-management-base-url=https://your-model-provider/v1
```

首次旧驱动未等待逐房间启动恢复，在显式打开时收到 `busy`；保留在 `...-01/`。驱动改为先读目录等待目标操作槽空闲，再只发一次打开请求。`...-02/` 完成初版真实并行验证；`...-03/` 为加入双房间无人查看恢复、运行工具退出及中断不重放后的完整结果。

## 浏览器边界

目标来源：`http://127.0.0.1:51364/`，由隔离 HTTP 测试服务提供。Chrome 工具在创建标签页之前拒绝来源访问，最终原文原因是 `A saved user permission setting blocks this action`，并说明可能由已保存权限或托管策略阻断。

用户已将默认浏览设置为“始终允许”，又添加了精确来源例外；重置工具会话后仍返回相同错误。这不是用户在本次对话拒绝授权，也不是 TeamX 页面失败。没有绕过门禁或改用其他控制方式。

尚缺：真实浏览器 A/B/A 切房、两个标签页分别停留 A/B、刷新及关闭标签页后执行不受影响的页面证据，以及视觉检查。DOM 双标签页与真实 DSH 分别通过，不能代替这项验收。
