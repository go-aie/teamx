# 飞书内置工具的应用运行边界

日期：2026-11-21。对应[第二阶段 P2t](../../GORDIS-PLUGIN-AND-SELECTIVE-BUILD-PLAN.zh.md#94-p2np2t补通知和工具的完整运行边界)，也是 R6a.2 中飞书 help／read／actions 一个完整调用族；其他调用族及整条 R6 仍未完成。

实施基准为 HEAD `fc9ae9d0a8ad09599cc9fd851c448fec2e84e431` 加 P1a／P1b／P2n 与既有工作树。没有提交、改变日常服务、迁移存储或新增根模块依赖。逐项命令、源码哈希、原始日志位置及限制见[验证证据](tool-runtime-validation.json)。

## 接入与原 owner

| 位置 | 职责 |
| --- | --- |
| `platforms.Tool.Bind`／`Catalog.ToolRuntime` | 所选工厂声明内置应用适配；目录检查绑定与 `InternalOnly` 一致，不调用 Bind 或获取资源。普通进程工具保持原路径 |
| `app/platformtools` | 每次调用的 Host／Runtime／Prepared、目标读取请求、动作 Session、最终 dispatch 检查及图片租约合同；不依赖具体 Provider 或 HTTP |
| `app/builtinplatforms/lark` | help 与 read／actions 参数分发、飞书目标读取／分页投影、原文字／图文动作的运行包装、上下文合并、图片准备与释放及权限诊断 |
| `room.builtinToolHost` | 捕获原请求身份，委托原准入、保存目标读取、动作解析／执行和共享帮助。结构化动作正文仍由原 DTO 严格解码，与根 actions 入口共用校验和投影 |
| `tools_http.go`／`tools_builtin.go` | HTTP 请求检查、NDJSON framing、输出取消和错误投影；删除固定 Provider dispatcher 及原内嵌参数分发，不再由传输层核验或修改业务字段 |
| `roomActionImages` | 原 Room 宿主保留发布／对象 pin、字节和执行身份核验；通过窄 `ImageLease` 暴露有序原图、Check 和 Close，飞书及 GitLab 使用同一实现 |

每次应用调用先按 help 类型通过原 `preparePlatformCallLocked`，得到同一 R6a.1 `Calls` 登记。嵌套读取／动作保留原来的独立受管 Call；没有新表或第二套取消 owner。HTTP 输出结束或出错后才完成外层 Call，慢读者、断开、撤权及关闭仍由 `watchToolIO` 和原等待合同结清。

公开精确入口仍需读取连接身份以匹配绑定和 generation；私有类型 help 只读取绑定。help 不解析执行凭据、探测权限或启动 CLI，执行路径仍读取并核验账号。成员身份、绑定、账号、凭据代际及最终结果的复核保留在原 manager 门槛内，业务控制命令仍在门槛外等待原 Calls。配置变更、连接授权／刷新、订阅及 Followup／动作账本没有迁移或双写。

## 图文与结果合同

图片仍只接受原 `{message_seq, attachment_id}`。原宿主核验已发布人类消息、可见性、附件元数据／实际字节、原执行身份及 Session generation；上传不持有 Store、附件生命周期或业务修改锁。失败的准备返回空租约并释放全部部分 pin，正常租约直到动作 Session 关闭才释放。GitLab 只改为消费同一租约合同，其账号／OAuth／上传／评论调用族未迁移。

飞书仍使用同一固定账号 pin、同一共享客户端预算和原绝对期限。实现侧合并调用取消与期限时，显式保留动作 owner 安装的最终 dispatch 检查；发送前继续核验完整文字、有序图片字节摘要、准确目标与最新授权。图片权限独立于文字能力，缺权限不自动降级发送文字或图片子集。self／指定群／普通回复的支持范围不变。

原账本处理同 ID 重试、重启查询、unknown 和晚到已知结果；运行适配不新建幂等表、不重新上传／补发。`actions read --id` 在 Session 恢复期间仍通过原只读入口返回完整回执。候选 locator、动态 input_schema、精简动作投影与安全字段诊断保持，`--request-file`／stdin 继续由客户端处理，服务端不读客户端路径。

## 验证与限制

| 检查 | 结果 |
| --- | --- |
| 关键调用族 integration＋race | 48 个顶层用例、含子用例 240 项通过，Room 451.682 秒；3 个无子进程环境的辅助入口跳过，对应独立进程恢复父用例已通过 |
| 最终差异及关闭重试 race | 7 个顶层用例、含子用例 47 项通过；覆盖图片可选权限、字段诊断、所有失败原图源的空租约／pin 释放、关闭超时保留 owner 及再次结清 |
| 工厂选择与 help 按需读取 | 独立集成 race 通过；HTTP 使用选中 Bind，公开入口读取连接身份但不解析执行 access，私有类型 help 可跳过损坏连接快照；调用结束 Calls 清空 |
| 通用目录／实现适配／架构 race | 通过；通用合同不引入具体 Provider，静态检查不绑定运行资源 |
| 飞书动作组件 race | 21 个顶层用例、含子用例 167 项通过 |
| 默认构建、help、装配检查、全仓 e2e 配置编译 | 通过；编译不代表真实浏览器／后端验收 |
| 标准 vet | 改动的应用包／架构通过；Room 保留原 `roomActionContext` 的两条重复 JSON tag 告警，integration vet 另有未改动的 GitLab ImageRef 字面量告警 |
| 扩大检查 | 飞书组件整包 race 在 SearchBacklog 用例超时；首轮较宽 Room 集成超过五分钟总预算。两项保留日志且不计通过，没有修改无关用例或 CI 门槛 |

验证结果见配套 JSON。本轮只进行本地组件、隔离 CLI／HTTP／受控子进程、故障注入和恢复验证；没有改变飞书实际协议或子进程实现，没有进行真实远端业务写入、真实模型或浏览器验收。全仓业务与全仓 race 不计为通过。

## 收益与停止点

| 问题 | 结果 |
| --- | --- |
| 新能力少改公共代码 | 内置工具分发由工厂绑定，通过不含 HTTP 的应用合同执行；外部本地进程工具和通知作者 API 保持。没有公开完整 Room／动作账本 |
| 依赖是否可选择 | 通用目录和运行合同的生产依赖不含具体 Provider；Room 仍直接依赖飞书运行适配及连接／配置等实现，产品尚未裁剪 |
| 状态责任是否单一 | 原 manager／Calls／动作账本／Store／附件 owner 保留；移动的是原 Session 内运行包装，不增加业务缓存或注册表 |
| 维护成本 | 删除固定 HTTP dispatcher 和原命令分发，应用调用可独立测试；共享 JSON 校验与帮助不复制，图文只保留一个原图宿主实现 |

P2t 完成后可停在当前完整组合。P2r 继续处理飞书连接核验、能力／订阅配置、授权／刷新／撤销、通知投影／升级和资源依赖；本次 Room 的准入／查询适配也仍在其清单内。P3／P5 的 Gordis 生命周期、实际产品依赖和资源裁剪均未实施；P4 的 DSH 工厂准备仍是独立分支，没有 Codex 或 Gmail 接入。
