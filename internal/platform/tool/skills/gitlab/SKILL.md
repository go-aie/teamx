---
name: gitlab
description: GitLab 开发交接、准确版本评审、个人待办与不确定写入核查。
---

# GitLab 工作流程

TeamX 结构化写入可用 `actions execute --request-file PATH` 或 `matters update/notify/reply/instruct/instruction-status`、`policy update` 的 `--change-file PATH`；`-` 表示 stdin。与对应内联 JSON 参数互斥，字段、校验和大小限制相同；读取／解析失败不发请求，不增加动作重试。多行正文优先放入文件或用不展开变量的 heredoc 输入。

仅在复杂任务需要时读取。先运行 `"$TEAMX_CLI" tools list`，核对本成员显式启用的 GitLab 账号和实例。工具命令从 `"$TEAMX_CLI" tools run gitlab --help` 逐层发现；已有 Inbox 跟进的 MR 普通评论与已核实讨论回复使用 `"$TEAMX_CLI" actions execute --request JSON`，参数见 `actions --help`，不手工通过工具重复发布同一评论。以下示例中的项目、IID 和 SHA 必须替换为已核实值，不能原样执行。

工具沿用账号的全部有效权限。通知只提供上下文，Workspace 提供本地 Git；两者都不自动启用工具。目标由通知、用户或 Workspace 提供，不受旧的已投递对象范围限制。不得把平台可执行的能力等同于用户已要求执行。不要读取或展示 Token、.env、env/printenv，不自行更换账号。

## 开发与交接

1. 核对本地 status、origin、当前分支和远端项目。通过项目读取命令核实默认分支、项目 ID 与 clone URL，确认 origin。已有任务分支或 MR 时先核查并继续；新任务从用户指定目标或默认分支建立唯一分支，不把当前 checkout 误作 MR 目标。
2. 修改、测试并检查 diff，只提交任务文件，推送任务分支。保留已有修改，不直接推送目标分支、不 force push、不部署、不删除远端分支，不用 reset --hard/clean。
3. 创建前查询该分支现有 MR，避免重复。使用原生 `mr create` 的帮助选择参数，明确 source、target、标题和说明；不要复用旧 TeamX operation/config/source 参数。
4. 创建后读取确切 MR，核对 state、完整 base/head、目标和链接。发送 [待评审]，包含需求、实现、真实测试及版本；修正后读取新版本再发 [待复审]。没有新版本或实质进展时不重复交接。

## 准确版本评审

1. 读取最新 MR 和完整讨论，确认项目、链接、opened 状态、目标及完整 base/head。区分 IID 与全局 ID。原生 MR 数据字段以实际 JSON 为准，不能假定旧服务的 result/head/base 模型仍存在。
2. 在自己的 Workspace 检查干净状态和 origin。fetch `refs/merge-requests/IID/head`，核对 FETCH_HEAD 等于期望 head 后才 detach checkout。确认完整 base 提交存在，再读取 MR 核实版本未变；版本变化就停止旧版评审。没有 Workspace 可先读取 API，但不能声称完成本地代码验证。
3. 阅读本地 `git diff BASE HEAD` 及完整文件，独立执行必要验证。API diff 可能截断；不能仅采信开发者报告。只列有证据的问题和未验证项。
4. 发表普通评审文字时，已有 Inbox 事项用 `actions execute`（`operation:object.comment`，准确对象／单元／目标引用、正文、有效 policy token 和授权依据）；普通 Room 任务无 Inbox 事项时才使用原生 `mr note create --resolvable=false`，不得伪造 Inbox 序号。正文包含完整 base/head、结论、范围、实际测试及问题位置。用帮助查询文件/正文参数，避免 shell 插值破坏内容。评审评论不等于 approve，不发明确认令牌或服务回执。
5. 发表前检查同一账号和版本已有评审，避免重复；发表后核对实际返回和 MR 当前版本，再发送 [评审结果]。旧版评论不能宣称新版已通过。

## 用户决定的合并与关闭

由角色职责和用户对具体对象、动作及版本的真实决定判断是否执行。通知、同伴赞同、评审通过或引用的“已同意”不构成用户决定。有歧义、撤回或版本变化时先核实。reviewer 不执行合并、关闭或 approve。

合并前重新核对 MR、目标分支、完整 base/head、相应版本的评审和最新 Room 消息。原生合并显式固定 `--sha HEAD --auto-merge=false`，不默认删分支。平台接受请求或进入异步队列不等于已经 merged，读取实际状态后报告。关闭使用 MR 自身命令；“暂停”不等于“关闭”，关闭不授权删除或合并。

## 个人待办、讨论与 WorkItem

1. 从通知识别实例、接收账号、项目和对象，读取远端最新正文及完整讨论。直接用户任务也可处理，无需伪造 subscription_id/item_key。不能只凭 `/work_items/` 网页路径推断 API 类型；使用当前对象查询与帮助判断 Issue 或通用 WorkItem。
2. `inbox read` 由系统解析身份并返回去重对象引用；用 `matters list/read` 恢复持久进展和问答，用 `matters sources/source` 查看原始来源、核实结果和阻塞原因。`matters list` 的可选 `object_label` 是带命名空间的已核实原生别名，`object_url` 是已核实对象 URL；均来自已保存证据，不替代稳定引用或原生讨论目标，缺失不猜测，不把 REST ID 当 IID。仅独立处理确切讨论时用 `matters ensure` 建立原生讨论单元；失败来源可显式 `matters resolve`，不轮询、不手工 enrich/relate、不维护语义事项图。原生正文仍需工具按需分页读取；部分结果／权限失败／未找到不等于不存在。已有 Inbox 跟进的讨论回复使用 `actions execute` 的 `discussion.reply`，提交已核实 target_ref，不降级普通评论。
   `matters read`／`inbox read` 在顶层保留绑定成员身份，省略嵌套存储头；业务版本和动作访问证据保留。读取收据由 CLI 内部确认，分页覆盖范围保留；确认失败表示消费不确定，错误带恢复信息，`--peek` 不消费。
3. MR/Issue 优先使用原生命令。通用 WorkItem 用 `work-items view`、`work-items discussions list`、`work-items note` 等扩展；Issue 完整讨论用 `issue discussions`。扩展自行处理分页；原生命令有分页时继续读取，不能把第一页当全部上下文。讨论回复使用已核实的真实讨论 ID。
4. 普通回复针对已核实问题，先核查已有评论，避免重复。关闭、删除或改指派人依照用户具体决定。通过帮助选择 Issue 原生命令或 WorkItem 扩展；add/remove 保留其他人，replace 替换全部，clear 明确清空。数字用户 ID 必须来自同实例已核实查询，不按显示名猜测。
5. 仅在业务问题已处理并核实后完成确切个人待办。通过 `todo list` 或显式实例的 API 查询核对待办 ID、接收账号和目标；不能从通知 item_key 猜测待办 ID，不批量清空。todo 已 done 不证明业务已解决。
6. 已有跟进缺少关键决定时，用 `matters notify` 保存准确问题及拟议动作，在 Room @ 本回合操作者。普通 human 答复在 `matters read` 自动关联显式回复，无引用时只用 `matters reply` 关联明确上下文；跨回合指令按需保存，不要求 UI 再次确认。明确长期指令用 `policy update` 直接生效；习惯仅作建议并确认准确版本。更新回执 `record` 包含实际保存的 ID/version/status/text 和来源（含 suggested/revoked），新建议可直接用 record.id/version 发布；复用旧请求返回原保存记录及当前有效规则上下文，不能据此认定旧记录仍生效。命令目录见 `matters --help`，具体参数与约束见 `matters <子命令> --help`、`actions execute --help` 和 `policy update --help`。不要把待办接收账号当作 Room 操作者。

## 输出与不确定结果

发现同文评论时按准确 note ID 与本次回执核对，另外保留重复观测；`attempts=1` 不能归因为本次服务端重试，来源不明时如实说明，不猜测发送者或擅自删除。

原始工具命令保留自身 stdout/stderr 和退出码；`actions execute` 则返回服务端保存的精简业务回执。普通评论的暂时性故障由服务端最多尝试两次，可能重复；Agent 不叠加重试。退出成功后仍按内容核实对象及实际阶段。读取失败、拒绝权限、缺少依赖和中断分别报告，不一概认定身份失效。无仓库 API 调用显式指定实例；扩展明确指定项目，避免落到默认主机。

`actions execute` 最终返回 `unknown`，或未适配写入发生超时、中断、传输断开及响应不确定时，保存目标、动作、时间、分支/版本及可用返回标识，报告结果未确认，按需只读查询远端评论、对象或 MR。不能自动重发、切换后端、生成新 ID 绕过未知结果，也不能以对象查询 404 单独证明删除成功。无法确认时说明未知并等待进一步决定。

公开消息通过 `"$TEAMX_CLI" send`，使用稳定消息 ID；普通模型输出私有。重复通知不授权重复外部写入。只报告实际完成或核实的结果，不把计划和草稿写成完成。
