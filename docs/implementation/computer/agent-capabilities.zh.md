# Computer 成员能力与 Agent 执行

原始日志、截图和 JSON 回执仅在本地保存；本文保留验收结论、复跑入口及已知限制。

日期：2026-11-23。范围：共享 Computer 方案 C2。全局连接、成员偏好、统一 MCP、Session 能力与执行结清已接通；观看／人工接管、文件交付和日常旧入口切换分别留给 C3a、C3b、C4。

## 使用与接口

先在全局“设置 → 电脑”保存并启用连接，再在成员详情打开“启用 Computer”。私信与多人 Room 使用同一成员字段。开关缺省关闭；全局关闭时保留成员偏好，显示等待全局连接。启用和关闭均有持久操作 ID、独立设置版本及应用状态；响应丢失后读取成员状态，不重新发出桌面动作。

| API | 行为 |
| --- | --- |
| `GET /v1/members/computer?room_id=…&room_instance=…&agent_id=…&membership_id=…` | 读取成员偏好、版本、全局开关及原操作／恢复进度 |
| `POST /v1/members/computer` | 以完整成员身份、当前 `service_epoch`、认证人类 `caller`、`expected_version`、32 位 `operation_id` 和 `enabled` 保存／应用 |
| TeamX `/mcp` | 在 Session／回合凭据身份下发现原生工具、调用原生工具及查询 `computer_status` |

受管成员设置写入 `managedMember.computer_enabled` 和 `computer_settings_version`；非受管单 Room 与后加成员用私有成员记录保存同名字段，不修改模型选择、推理强度或其他成员。全局与成员的有效能力变化会等待结清并轮换受影响的 Session；Room 历史、screen ID 和 profile 保留。普通同实例凭据／地址／容量更新不轮换 Session。全局停用卸载工具，但不覆盖成员偏好。

首版 AIO 目录含 21 个原生工具。视觉工具只发布给接受图片输入的 Session；文本模型采用页面文本／DOM 路线。工具默认沿用原名，固定映射亦纳入合同摘要。Agent 只接收 TeamX MCP 地址，不接收 Runtime 地址、管理密钥或 screen 身份。

旧 `agent_mcp_file`／environment 的退出仍属 C4；本阶段不修改已有日常试验绑定。带旧连接的成员不能同时采用正式共享 Computer，避免形成未经受管执行门的第二条路径；移除旧连接后再显式采用。

## 执行与生命周期

调用路径只有 `TeamX /mcp → 成员／Session／回合准入 → computer.Service → 受管 Screen.Call → 原生 MCP`。Session 凭据、回合输入凭据与 CLI Inbox 模式共用 facade。身份和可信调用 ID 从认证框架上下文取得，原生参数不能选择其他成员、screen 或连接。

原生定义与结果使用有界完整 JSON 传递，保留完整 Schema、annotations、结构化内容、图片、`isError` 和未知扩展。当前 MCP SDK 的类型解码会丢弃部分未知字段，因此 facade 在 SDK 的 Input／History 服务之外保留原生 JSON；基础 MCP 协议和输入消费仍由现有 owner 处理。Computer 结果不进入 `teamx/received` 或输入消费证明。

输入桥只为 Input／History 白名单保留透明重试。Computer 遇到网络错误、409、5xx 均不自动重放。变更调用先保存最小宿主关联，再交 Runtime；同一可信调用 ID 只核验原操作。已核验只读调用不保存结果账本，但参与当前执行作用域的结清。

本地 DSH 退出、取消请求和关闭开关都不是远端停止证明。回合 Finish、owned close、活跃／关闭 Room 轮换及恢复，统一检查 `computer-execution.json` 中的未结清作用域。远端 Drain／必要的单屏 Stop 在异步生命周期任务中执行；Actor、配置和 Store 锁内只检查已保存事实。停止失败保持 pending；`Release()` 只撤销凭据，owned close 可在同一资源上重试未结清核验。

Runtime 对成员持久化连接版本／偏好版本的准入策略。停用或删除先关闭远端准入，再查询 inventory／停止 screen；即使创建响应丢失、旧请求迟到或暂时没有宿主 handle，也不能跳过结清或在删除后重建。更换实例后分别核验新目标与旧资源：已结清的旧实例离线不阻塞当前连接；新实例创建丢失回执时，不能被旧的已停止 handle 遮蔽。已退休成员身份不能重新启用，替换成员必须使用新 membership。C2 要求 Runtime 宣告 `member_admission` 能力，旧辅助程序须随本阶段更新。

Runtime 的所有者 CAS 交接立即撤销旧服务代次，未结清事实仍属于原 screen；一个 screen 的 pending 不阻止其他 screen 的恢复。普通结清只关闭旧 Session／turn，不用永久通配围栏封死后续 Session。实例替换／停用停止旧 screen；仅合同更新时保留原 screen。成员／Room 删除停止并核验原 screen，再保留不可复用的待清理记录；本阶段不主动删除 profile 数据。

无法取得业务结果时，先取得停止证明，再持久保留 unknown 提示，最后退休宿主关联。重新创建画面／启用成员不会清除该提示，也不会补发原动作。画面代次变化会在回合上下文要求重新观察、弃用旧元素句柄。

## 本地状态与恢复

| 位置 | 内容 |
| --- | --- |
| `computer-state/members.json` | 完整成员归属、原连接引用、screen／epoch、远端准入关闭事实、unknown 和待清理标记 |
| `computer-state/member-changes/` | 成员设置意图、版本、应用阶段及已有轮换操作 ID |
| `computer-state/connection.json` | 单一全局连接应用及受影响成员／轮换关联 |
| `computer-state/operations/pending.json` | 变更调用的最小宿主关联，不含原始参数或完整结果 |
| 成员的 `computer-settings.json` | 非受管单 Room／后加成员的偏好与版本，完整身份校验，不复制模型配置 |
| 成员的 `<generation>.computer.json` | 冻结的有效能力；凭据、endpoint 和租约不进入能力指纹 |
| 成员的 `computer-execution.json` | 未结清的 generation／turn 关联，供所有生命周期入口读取 |

新 Agent Launch／DSH Options 传递通用能力摘要。缺省关闭保持旧的空能力指纹。输入桥本身的内容摘要仍属于 DSH 恢复指纹：旧桥接 Session 不能静默恢复，测试使用 Git 基线中的原桥接文件生成隔离旧状态，须先验证正常停止，再沿既有轮换路径创建新代次；不改写旧 Session 的指纹，也不清空 Room 历史。

从未启用且没有资源／记录时，不构造或探测 Provider。正常停用后，在容器停止的条件下仍能恢复原普通 Session 并完成模型回合；有遗留调用时只阻断相关成员，后台分别核验，不以开关关闭掩盖旧执行。

## 验证与证据

检查命令与结果见检查记录。受控测试覆盖两种工具目录、两类凭据和 Inbox 模式、并行 screen、身份与版本冲突、未知 JSON 字段、响应丢失、关闭准入、停机／owned close、重启、普通／能力轮换、成员删除和默认关闭的零 Provider IO。全仓 Go、前端回归、定向 race 与 e2e 编译检查分别记录，不把受控后端当作真实模型证明。

真实供应方与模型验收：

```sh
python3 scripts/computer_connection_acceptance.py --run \
  --agent-config .teamx/config.toml \
  --output .local/computer-agent-acceptance
```

脚本使用固定 amd64 派生镜像及临时容器／卷，沿用只读本地虚构站点。真实 DSH／模型分别完成页面文本与随机八位图像验证码任务；图像只存在网页像素中，预期答案从模型进程环境移除；模型请求记录确认截图调用、图片材料到达及未使用替代读取渠道。成员关闭与全局停用后，记录实际模型请求中的工具名称、数量及定义字节，不记录凭据。从未启用时，两位真实普通 Agent 的请求没有 Computer Schema，Provider 构造数为零。未启用启动与离线恢复均从测试 PATH 移除 `docker`／`teamx-computer`，保留正常 Agent 工具链。停止 Runtime 容器后，TeamX 从同一持久状态恢复原 generation，普通模型回合继续，Provider 构造数仍为零。

实际请求的上下文体积汇总显示：文本路线共 50 项工具，其中 Computer 19 项，定义合计 29,677 字节；视觉路线共 53 项，其中 Computer 22 项，31,419 字节；关闭／从未启用／离线恢复后均为 31 项、Computer 0 项、23,158 字节。这些是完整工具定义的 UTF-8 字节数，不换算推测 token。

真实模型记录、请求检查、MCP 方法／工具调用和容器清理记录均为有界脱敏结果。仅复制报告，不发布私有 Session、凭据或完整截图响应。CPU／容量来自 ARM 宿主模拟执行，不推导原生 amd64 容量。此前隔离尝试的模型／工具结清超时及验收夹具修正见尝试记录和所链失败日志；最终使用真实 TeamX CLI，明确普通回合指令，并审计图像来源，完整通过。不把失败尝试计为通过。

成员设置浏览器组件使用生产 HTML／CSS／模块与受控 HTTP 后端：

```sh
node internal/web/tests/member_computer_browser.mjs \
  .local/member-computer-browser
```

浏览器报告、桌面及390px 手机视口验证单一开关、身份／版本、丢失回执后读取原状态、全局关闭保留偏好和布局。它与真实 AIO／DSH 证据分别标明，不代表 C3a 的观看或接管。

C2 不发布文件工具、观看／接管、search 或独立 CLI；不替换日常唯一试验成员的旧连接，不复制 Cookie，也不重启日常 TeamX。完整浏览器＋双 Agent 的最终组合、打包与日常升级采用仍留 C4。
