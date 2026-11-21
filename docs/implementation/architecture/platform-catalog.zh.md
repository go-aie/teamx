# 公共平台目录与内置工厂解耦

日期：2026-11-21。对应[第二阶段方案 P1a](../../GORDIS-PLUGIN-AND-SELECTIVE-BUILD-PLAN.zh.md#93-p1ap1b从唯一描述来源到真实外部装配)。本次将公共目录与内置实现分包，保留当前完整组合；外部作者入口、通知生命周期迁移和产品裁剪继续由 P1b／P2／P5 验收。

## 实现与正常入口

开工基准为 HEAD `b1dcb2702493ae9e48b9407296f13edb7e78599d` 加工作树。已有文档、Room 测试与本地运行目录保持；未将其合并为本次成果。P0 的历史记录不覆盖，当前逐命令结果、生产依赖及剩余直接引用见[验证证据](platform-catalog-validation.json)。

| 位置 | 责任 |
| --- | --- |
| `internal/app/platforms` | 保留目录校验、不可变快照、纯工具入口解析及 Calls；删除原 `builtins.go` 和统一 `SourceBindings` |
| `builtin/platforms/{gitlab,lark,scion}/definition.go` | 分别维护原有类型、连接检查、工具、来源、codec、通知投影与动作声明；每个 Provider 只有一份描述 |
| `builtin/platforms/catalog.go` | 唯一显式完整选择：GitLab、飞书、Scion；通用目录不导入此汇总 |
| GitLab／飞书 `sources.go` | 分别接收原 `EventsPrepare/TodosPrepare`、`QueryScope → MessageReader` 窄查询能力，绑定到新来源副本；不读取账号、不执行查询、不修改静态来源 |
| `room/platform_assembly.go` | 原受管服务、单 Room／Store 和离线消费者共用的惰性目录改用外层完整汇总；原 executable 解析仍在请求工具目录时执行 |
| `room/platform_notifications.go` | 先由所选描述的 `Source.New` 创建未绑定来源，再由对应实现绑定查询操作；复核 ID 和 schema 后接入原生命周期 |

`Source.New()` 无参数，公共合同不枚举具体 Provider，也没有改成 `any` 或通用服务查找器。目录校验仍检查构造结果的来源身份／schema、能力声明及 codec。运行绑定仍通过原 manager 的查询方法和飞书客户端工厂发生；静态工厂不持有 manager、Store、账号或凭据。

默认集合保持 GitLab events／todos、飞书 SearchSource messages（当前 SearchSchema）与三个工具。飞书本人／指定群发送继续声明可选图片，普通回复仍为文字；GitLab 评论／讨论回复继续声明图片，纯文字不被静态联合 schema 一律限制为直接 Room 指令。原图片权限观察、动作执行和通知投影仍来自对应实现；声明不产生运行权限。

## 依赖结果与剩余边界

以下均检查生产 `go list -deps`，测试包可以使用真实 Provider 作为合同夹具。

| 检查包 | 包含的 Provider 实现 |
| --- | --- |
| `internal/app/platforms` | 无；也不依赖 `builtin/` |
| `builtin/platforms/gitlab` | 仅 GitLab |
| `builtin/platforms/lark` | 仅飞书 |
| `builtin/platforms/scion` | 仅 Scion |
| `cmd/teamx` | GitLab、飞书、Scion，仍为完整产品 |

生产依赖测试已加入公共目录不得导入内置汇总／实现、单个工厂不得反向导入汇总或其他 Provider 的约束。完整的剩余直接导入文件列表保存在证据的 `dependencies.remaining_production_provider_importers`，不以目录级隔离声称产品已经可裁剪。

Room 仍直接引用 Provider 的连接核验、账号／授权、成员与订阅配置、通知准入／加载、引用与图文动作、离线升级等。`defaultNotificationAdapters` 的 Provider 生命周期分派仍在，飞书 Admit 仍由原 `manager.mu` 门槛保护，GitLab 的加载核验也未迁移；这些是 P2n／P2r 的输入。专用前端、嵌入 Skill 与打包资源未裁剪，历史 codec 仍在 Provider 包内。P1b 才验证外部 module 的公开 Compose／boot 与实际工具执行，本次 `Definition` 的返回值仍使用 TeamX 内部合同。

## 状态责任与验证范围

本次只移动声明和纯绑定：连接／账号仍归 registry 与原 manager，订阅状态仍归 notification owner，收件／输入及文件 writer 仍归原 owner，工具准入与 Calls 的登记、取消和等待仍走原门槛。没有新增可变注册表、凭据缓存、Actor、双写、持久格式或关闭路径。取消、授权撤销和实际资源结清没有委托给静态目录。

验证结果见下方及机器证据；全仓 e2e 配置编译只证明所有入口可编译，不等于执行了真实模型、平台或浏览器验收。

| 检查 | 结果 |
| --- | --- |
| 目录／工厂／架构及 platform 组件 `-race -count=1` | 通过，命令总计 5.345 秒；含默认图文 schema、绑定零查询、错误透传、快照隔离及生产依赖约束 |
| Room 定向 `-tags=integration -race -count=1`，4 分钟上限 | 25 项顶层全部通过，无跳过，命令总计 136.514 秒；覆盖默认来源／codec、缺 codec 的旧分区保护、按需账号读取与错误顺序、无账号工具、精确入口／动作回执／慢输出撤权、双 Provider 重载、搜索投递、图文权限／最终检查及字段诊断、离线升级与输入恢复 |
| 全仓 `go test -tags=e2e ./... -run '^$'` | 配置编译通过，未执行业务测试 |
| `go build ./cmd/teamx` 及隔离产物 `--help` | 通过；产物位于 `.local/platform-catalog/` |
| 通用目录／内置工厂／架构 `go vet` | 通过 |
| 全仓 `go vet ./...` | 未通过：以下两条已有 JSON tag 告警 |
| `git diff --check` | 通过 |

`go vet ./...` 的两个告警来自已有 `room_action_images.go:21`：`roomActionContext` 同时嵌入 `ServiceInfo` 与 `MemberIdentity`，重复 `room_id/room_instance` JSON tag。已核对该文件及 identity 定义与 HEAD 相同；本次没有修改该上下文，也不将全仓 vet 记为通过。没有重跑全仓业务回归、完整来源积压 race、真实 DSH／平台、浏览器或发行打包；日常服务与配置未切换。

## 收益与下一步

| 问题 | 本次可复核结果 |
| --- | --- |
| 扩展是否少改公共代码 | 现有三份工厂已离开通用目录；新增静态声明可在独立工厂及外层选择完成。新来源的运行生命周期仍需原适配，不能算外部通知作者入口已经完成 |
| 依赖是否可选择 | 公共包与每个工厂有独立生产依赖闭包；实际产品仍完整，选择构建留 P2r／P5 |
| owner 是否单一 | 没有转移状态 owner 或建立第二套 registry；绑定仅复制来源并注入原查询操作 |
| 验证成本 | 保留原目录校验与快照测试，将默认组合测试归入内置汇总包；补充查询绑定及生产边界验证，沿原 Room 入口定向回归 |
| 适配成本 | 删除原同包内置描述和统一专用参数；新增实现侧绑定只做类型核验、来源复制与回调注入。未引入 Gordis 包装或全仓宿主接口 |

下一项为 P1b。P2n／P2t 继续分别迁飞书通知／工具的完整运行边界；P2r、P3 和 P5 负责剩余依赖、实际资源生命周期及代码／资源交付。Agent 分支仍沿 P0 记录的宿主准备操作进入 P4a。本次不扩展到这些单元，不新增 Gordis 根依赖。
