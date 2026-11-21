# 飞书图文发送：真实验收与开放

日期：2026-11-21。对应[方案](../LARK-IMAGE-SEND-PLAN.zh.md)阶段 4。

状态：真实 DSH／Agent → 托管动作 → 飞书 user → 本人／指定单群的六个内容样本及只读回流检查通过。正式能力 schema 与托管执行共同开放 Room 原图发送，普通回复保持文字。代码开放后的组件、race 和浏览器结果见下文。日常服务未重启，既有 Session 未轮换；代码注册完成不代表正在运行的旧二进制已经采用。

## 账号、目标与批准范围

用户明确指定「罗彭-qa 测试群」，并批准完整验收、补齐权限以及修复后的额外本人纯多图复验。沿用原 profile `cli_a954531d70f99cdb`，用户罗彭 `ou_7d4bcb9114144c5ae2c92b4fd2c3c627`，未选择机器人或替代账号。

本人自聊为 `oc_69b92a6c8b1ae12c6db9feb5e53c1145`；群为 `oc_b6ccfe7a898b71ed34ab8d1badd8d662`。原生搜索只有一个准确候选，独立 GET 核实群为普通内部 group／chat、normal、all_members，成员查询核实当前用户在群内；本人关系另行核实。原生账号 GET 的租户标识为准，不使用搜索结果中的不同标识代替。

初次图片预检准确报 `image_upload_application_scopes_missing`，原文字能力仍 ready。用户在[原应用权限页](https://open.feishu.cn/app/cli_a954531d70f99cdb/auth)开通用户身份 `im:resource`，随后完成原账号的最小 scope 用户授权。重新核验账号和目标后，`TestRealLarkImageSendPreflight` 通过。未修改其他成员绑定或日常订阅。

[准确样本](lark-image-send-acceptance/input.json)固定账号、目标、说明、原图 SHA256 和外部持久调用目录，不含凭据。两张 320×200 PNG 为非业务测试图：[红底一条白柱](lark-image-send-acceptance/image-1.png)、[蓝底两条白柱](lark-image-send-acceptance/image-2.png)。它们通过实际 HTTP 上传并发布到隔离 Room，由 Agent 从正式读取／History 选择源引用。

| 入口和目标 | 单图，无文字 | 双图，无文字 | 说明＋双图 | 成功样本数／上传数 |
| --- | --- | --- | --- | --- |
| 普通托管 Room → 本人 | succeeded | succeeded | succeeded | 3／5 |
| 默认助理 → 指定测试群 | succeeded | succeeded | succeeded | 3／5 |

两条图文消息均使用以下准确说明，空行和 Markdown 保持字面文本：

```text
TeamX 图片发送验收

说明与两张原图合为一条；**这段保留字面文本**。
```

原清单为 6 条消息／10 次上传。另有一条已发出的本人双图因旧回读器兼容缺口记为 unknown，用户批准修复后额外复验。**整轮实际为 7 条消息／12 次上传，达到批准上限。** 本人原单图复用既有成功证据；恢复和重查没有重复发送。没有真实故障注入，后续检查均只读或使用本地夹具。

## 真实问题与修复

飞书原生 post 回读给图片节点增加 `width`／`height`，给文字节点增加空 `style`，并可能增加 `content_v2` 镜像。旧夹具只返回发送 payload，严格回读因此把已发送的本人双图记为 `unknown/send_verification_failed`。

修复后仍核验完整内容：尺寸须与源图字节解码结果一致，镜像须与正文逐行逐节点相同；文字仅允许空 style，额外文字、缺图、换序、不同尺寸、非空样式及其他字段均拒绝。发送 payload 不引入这些读取元数据。新的单元和托管夹具包含真实返回格式及对应拒绝用例。

`TestRealLarkImageSendReadback` 独立重读旧远端 ID，核实原用户、自聊、准确上传 key、源图尺寸及镜像，平台写入为零。旧账本仍为 unknown、Attempts=1，未改写为 succeeded，未重放旧动作；[原消息核实证据](lark-image-send-native-readback-evidence.json)保留这一事实。新的正常双图 succeeded 回执来自用户批准的额外任务。

另外两类失败也完整保留：

- Agent 曾将多行请求暂存本地 JSON，未满足受控工具要求。内置 Skill 已明确使用 `--request-file -` 和 stdin here-document；随后真实 Agent 用同一完整 ID 恢复成功回执，没有新增原生写入。
- 群图文任务曾把说明的 JSON 展示形式当作正文。准确内容守卫在第一次上传前拒绝，原动作保留 not_applied，原生调用为零。任务改用清晰的字面正文，在已证明未发送的前提下使用新 ID 完成原批准样本，没有超出写入预算。

验收审计保留诊断长度限制；对被截断的 bash 输入，从隔离 DSH 的完整 Session journal 按准确 tool-call ID 恢复参数，要求与诊断前缀一致后再审计完整命令。不会将截断内容直接放行，也未提高生产诊断预算。准备阶段的成员加载、Origin 和操作者问题均在原生上传前失败；相关失败报告亦保留。

## 完整链路与恢复证据

`TestRealLarkImageDSHSendAndRecovery` 使用真实模型／DSH、普通 Room 与默认助理、正式 HTTP 图片发布、Inbox／History、内置 Skill、最终简化的 TeamX CLI 合同和托管图文实现。原验收通过后，测试中的私有图片注册注入已删除，后续回归直接使用正式注册。

每个成功动作核实完整输入、唯一远端消息 ID、作者和准确目标；独立固定账号客户端再次核对原文、空行、字面 Markdown、全部原图上传 key 和顺序。本人使用 self_delivery，群使用当前成员 grant、policy_token 与直接人类指令，二者没有相互借用授权。

同 ID 完整请求恢复、实际 CLI 子进程恢复以及服务重启后的真实 Agent 原回执查询均通过。账本保持原完整记录，上传和消息标记数保持不变。真实 Agent 读取保留 History 也有完成记录；先前失败审计不覆盖或删除。

`TestRealLarkImageSendBackflow` 在另一 Go 包中只读六条实际成功消息，并交给生产来源分类：本人自聊排除，群内本人图文为 context_only，不能形成自动处理回路。此项验证未更改日常订阅，也不代表测量了飞书搜索索引延迟或长期轮询。

原始私有证据在 `.local/lark-image-send/real-20261121-04/` 和 `real-20261121-07/`，包含各次失败报告、DSH 记录、完整动作与独立回读。[公开验收摘要](lark-image-send-s4-evidence.json)仅保留不含凭据的必要结果。原生调用目录 `/tmp/teamx-lark-image-send-dispatches-20261121` 位于 Room 之外，必须保留；不能删除标记绕过重复写入保护。

原生进程守卫逐项核验账号、目标、原图摘要、冻结输入、uuid 和完整 payload；只有写前落账且 Attempts=1 才允许上传，每次上传／消息 POST 前都排他创建并 fsync 标记。原生响应原样转交，不伪造平台回执。七项离线守卫检查包含真实子进程转交和重复调用拦截。

## 正式开放与部署采用

- 应用 catalog 为 self／指定单群组合图文 schema，Room 的正式 preparer 同时提供可信图片来源及发送器；删除验收专用注册别名和注入 helper。普通回复仍为原文字合同。
- 平台行显示「图文范围：本人、指定群」，图片权限状态与成员工具许可／目标权限分别说明；缺图片资源权限不阻断文字能力。
- [飞书使用说明](../lark.zh.md)和内置 Skill 说明保留的人类 Room 原图、本人／受支持单群、一条完整消息、顺序及未知结果查询方式。
- 本次没有新建发送 UI、扩大群范围、开放通知源图片或普通图片回复，没有修改日常服务配置／订阅、重启日常服务或轮换现有 Session。已构建的测试 CLI 和隔离服务仅属于验收；日常采用需之后构建并启动新版本，现有 Session 的指引采用另行记录。

## 验证结果

真实验收：`TestRealLarkImageSendPreflight`、`TestRealLarkImageSendReadback`、`TestRealLarkImageDSHSendAndRecovery`（58.980 秒的最终只读恢复运行）和 `TestRealLarkImageSendBackflow`（23.510 秒）均通过。前期已成功的发送样本从完整证据复用，最终恢复运行没有新增原生写入；不把默认跳过计为真实通过。

正式注册后的检查均通过：

- 应用 catalog、整个 Lark integration 包、内置 Skill 和 Web Go 包（Lark 包 75.124 秒）。catalog 检查 self／群包含图片 schema、回复仍为文字。
- Room 定向集成（238.118 秒）：托管图文、Agent 合同、能力反馈、原文字本人投递、最终发送保护、来源生命周期、动作账本、独立进程恢复及原生写入守卫。
- Room 必要 race（133.981 秒）：真实写入限制的离线守卫、发送前授权保护、跨进程恢复和恢复期间账本查询；原生内容核验对应 `TestImageSend*` race 也已通过。
- 四个受影响前端文件共 125 项测试通过。
- Chromium（13.136 秒）：正式注册，权限 ready／应用缺 `im:resource` 两种模式，1440／390 宽度；检查本人／指定群标签、文字工具仍启用、官方修复链接、正常图片加载、无横向溢出及页面错误，未发生平台上传或发送。截图保存在 `.local/lark-image-send/browser-final/`；初次浏览器用例未选择目标 Room，已修正后通过。
- `git diff --check` 及新增证据文本格式检查通过。
- 最终 e2e 编译检查通过，未设置真实写入 opt-in 时按预期 SKIP，不计为额外真实验收。同期新增 GitLab 夹具存在 `base` 局部变量重名，最后编译前仅将该 HTTP transport 变量重命名，未改变其测试行为。

未执行全仓回归。阶段 1～3 的来源生命周期、取消、故障、unknown 及模型夹具覆盖保留原记录，本次不在真实会话重做故障注入。
