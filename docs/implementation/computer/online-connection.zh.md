# Computer 全局设置与在线连接管理

原始日志、截图和 JSON 回执仅在本地保存；本文保留验收结论、复跑入口及已知限制。

日期：2026-11-23。范围：共享 Computer 方案 C1b。全局连接管理已实施；正式成员开关、Agent MCP 工具与 Session 能力／轮换留给 C2。

## 使用与接口

全局“设置 → 电脑”提供启用开关、AIO 管理地址、仅写入的管理密钥、“最大运行屏幕数”高级项，以及测试连接、保存并应用和进度刷新。设置保持现有普通宽度；私信／多人 Room 共用安装级连接，单 Room 服务也提供相同设置入口。

默认关闭。没有 `[computer]`、省略 `enabled`、显式关闭但保留离线地址，均不构造 Provider、不访问远端，也不需要 Docker 或辅助程序。打开设置只读本地快照；测试连接和启用才核验远端。只保存停用连接无需服务在线。

| API | 行为 |
| --- | --- |
| `GET /v1/computer` | 本地脱敏配置、版本、最近核验的运行数和有界应用记录 |
| `POST /v1/computer/validate` | 核验地址、认证、实例、完整原生工具合同及管理 inventory；不保存、不 Claim、不创建 screen |
| `POST /v1/computer/save` | 按请求 ID 接受一个持久保存／应用操作，返回 202 |
| `GET /v1/computer/operations/{id}` | 查询原操作；响应丢失或刷新页面后继续查询 |

写入要求当前安装、完整服务身份、固定人类所有者、配置版本和请求 ID；沿用现有访问认证、同源和 Agent 执行凭据限制。旧服务代次失权。密钥更新明确为 `keep` 或 `replace`，空输入保留原密钥。URL 必须是明确 HTTP(S) origin，远端要求 HTTPS；禁止内嵌凭据、代理路径、query 和 fragment，不跟随重定向。

## 状态所有权与持久化

- `internal/computer` 只提供编译期 Provider 注册和通用管理合同；`builtin/computer` 在普通 CLI 装配 AIO。注册不构造连接，Room 不导入 AIO 实现。
- `internal/room/computer_*` 保存当前不可变连接快照并协调应用。只有一个待应用操作；最多保留 32 条有界操作记录，不保存原始工具参数、图片或结果。
- `[computer]` 的非敏感字段、接受的 instance／contract、持久版本及操作 ID 写入现有 `config.toml`。与时区、模型和访问设置共享 `configDocument` 锁、文件 CAS 和原子发布；只修改 Computer 子树，保留其他字段及注释。外部编辑冲突不会覆盖文件。
- `computer-credentials/` 使用独立命名空间、0700 目录、0600 不可变版本文件及文件／目录同步。配置和操作记录只含引用，HTTP 响应不返回引用或密钥。密钥不写浏览器 storage；storage 只保留原操作 ID。
- `computer-state/connection.json` 保存目标、原连接、阶段、待停止 screen 及已确认停止进度，使用独占目录锁与通用快照 writer。提交不确定时关闭准入并显示等待恢复，不能把不确定记录报告为完成。

正常启动从本地状态返回就绪，远端恢复在后台推进。无关联的普通 Room 功能不等待 Computer。进程停止只关闭本地协调器，正常运行 screen／profile 的回收由明确的停用／替换操作负责。

## 应用、结清与恢复

操作按“核验 → 结清 → 逐屏停止 → 提交”推进；不需要停止 screen 的同实例连接／凭据和容量更新直接提交。候选地址和凭据版本在接受后固定，实例及合同在首次核验后冻结，提交前再次核验；不能因重启或地址后端变化而接受另一个实例。验证失败时保留旧配置；同请求 ID 重试返回原记录，换请求内容会冲突，多页面只能接受一个待应用操作。

替换实例、合同改变或停用时，先关闭新调用准入，再查询原实例拥有的 screen。已有受控调用／远端 Drain 未结清时保持等待；旧连接仅用于核验和停止。逐屏确认停止后才发布候选，新实例在此之前不创建 screen、不执行任务。Runtime inventory 必须属于当前安装／所有者，并具有可信持久状态；Runtime 提交不确定时不得将内存里的 stopped 当作停止证明。

停用意图先持久化并立即阻止新调用，接受请求不依赖远端在线。离线或 unknown 不会被写成已结清，恢复仍使用原 ID、原连接和原 screen。进程在每个持久阶段重开，以及配置已提交而回执未确认、Stop 已完成而进度未确认，均继续原目标，不重复桌面动作。停止保留 profile；降低容量只限制后续创建，不停止已运行 screen。

C1b 只有受控服务入口，未向 Agent 发布 Computer 工具。若 screen 对应实际 Room 成员且替换需要成员协调，则停留待处理并明确提示需要成员能力／Session 协调，不能越过 C2 门槛。已有试验 MCP／environment 接入和日常连接均未切换。

## 验证与证据

默认回归不探测 Docker。定向测试覆盖三种默认关闭状态、真实 TeamX HTTP 普通消息操作、零 Provider 构造／探测、离线保存与停用、身份拒绝、首次启用、验证失败保留旧配置、CAS／多页竞争、仅写入凭据、响应丢失查询、同实例地址／凭据更新、容量降低、实际成员协调拒绝、远端结清、新实例不提前接任务、候选身份变化、每步重启、Stop／配置回执丢失、journal 不确定围栏和无关配置／注释保留。

定向 race、Provider／Runtime／架构回归、Room 完整回归、前端完整回归及全仓编译检查见检查记录。本次 Room 完整集运行约 470 秒；新加的故障恢复和无容器用例另由最终定向 race 覆盖。

真实供应方验收显式运行：

```sh
python3 scripts/computer_connection_acceptance.py --run \
  --output .local/computer-connection-acceptance-fresh
```

脚本使用固定基础 digest 和隔离的无认证 Docker 客户端配置构建两个独立 Runtime；不会调用桌面 registry 凭据助手，不改用户 Docker 配置。仅创建随机名称的容器／数据卷和 loopback 管理端口，使用虚构本地站点。通过真实 TeamX HTTP 保存连接，并由受控入口使用真实 AIO screen；核验同一服务代次在线更新、同实例修改不停止 screen、更换实例先停止旧 screen、候选运行数为零、停用和普通 Room 消息仍可用。返回原实例重新创建桌面时，用同 screen ID／新 epoch 和持久页面标记检查 profile 保留。随后停止两个真实 Runtime 容器并确认 PID 为零，再重启同一持久目录的 TeamX，验证仍为关闭、Provider 构造数为零，普通 Room 消息可继续发送。此证据不代表真实 Agent／DSH 模型验收。

真实供应方报告、HTTP／screen 结果和执行日志及容器停止后的重启结果保留有界证据。ARM 宿主模拟执行固定 linux/amd64 镜像，不推导原生 amd64 容量。结束核验两个容器均已停止、PID 为零，删除本次容器和四个数据卷；未修改日常容器或 Room 数据。

浏览器组件验收使用生产 HTML／CSS／模块与受控设置 HTTP 后端，独立于真实 AIO 验收：

```sh
node internal/web/tests/computer_settings_browser.mjs \
  .local/computer-settings-browser
```

浏览器报告、桌面与390px 手机模拟视口验证布局、显式测试不保存、丢失回执后读取原操作及密钥清空，截图已实际查看。完整浏览器＋真实 AIO＋双 Agent 的组合验收仍属 C4。
