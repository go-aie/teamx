# 实施记录

这里保存已实施阶段和被替代方案的历史记录。阶段性验收记录已去重，仍需维护的验证方法和未完成项保存在对应模块说明；保留方案只用于追溯设计。当前使用方法见[项目说明](../../README.md)、[Server 架构](../server.zh.md)、[通知手册](../notifications.zh.md)和[飞书说明](../lark.zh.md)；待实施方案继续保留在 `docs/`。

本目录只提交可维护的实施说明。验收日志、截图及 JSON 报告保留在本地，不纳入版本控制；复验入口与结论记录在对应 Markdown 中。

| 记录 | 范围 |
| --- | --- |
| [空间归档、结清与恢复](spaces-archive-restore-s2b.zh.md) | 空间 S2b：原 owner 结清、超时保留、新运行对象恢复及独立后台运行 |
| [平台工具与私有 CLI 上下文](platform-tools-admission.zh.md) | 空间 S3b.2a：工具准入、托管 Git 禁用与 Linux 飞书私有凭据上下文；真实飞书验收门槛保留 |
| [独立 SRT 隔离检查](srt-independent-isolation.zh.md) | 空间 S3a.1：文件、网络与监督条件的独立实测；DSH 接入见执行记录 |
| [SRT 与 DSH 隔离检查](srt-dsh-isolation.zh.md) | SRT 包装 DSH 的原始兼容性证据与边界 |
| [OpenCSG SSO 与用户生命周期](opencsg-auth-preflight.zh.md) | 空间 S4a 代码及本地闭环：认证／持久登记、会话、owner 授权与本机 CLI 撤权结清；真实 SSO／Linux 组合验收及生产门槛仍保留 |
| [空间执行隔离路线验证](space-execution-isolation.zh.md) | 空间 S3a 历史探针：DSH／文件／Shell／CLI、UID／容器及取消恢复证据保留；每空间业务 worker 路线已被替代，下一步 S3a.1 验证 SRT |
| [空间与执行资源准入](space-resource-admission.zh.md) | 空间 S3b 部分实现：共享核心中的空间／占有 Session 额度及结清释放保留；SRT 接入、工具链／硬资源与组合验收仍未完成 |
| [Agent 执行环境接入与恢复](agent-execution.zh.md) | 空间 S3b.1 完成：原生／SRT 执行合同、非 root 真实 DSH 的 MCP／CLI 回调、取消／故障／重启恢复／轮换及资源结清；多用户仍关闭 |
| [GitLab 服务应用与空间私有授权](gitlab-service-applications.zh.md) | 空间 S4b：共用授权编排、控制面应用与 Secret、按空间提交 grant、管理员轮换／停用及结清；真实 GitLab／Linux 发布验收留 S5 |
| [共享服务与真实模型增量验收](shared-service-acceptance.zh.md) | 空间 S3b.3 增量通过：同服务双空间、真实模型 MCP／CLI、A 归档时 B 继续执行、原 Session 恢复及结清；正式出口仍等待 S3b.2a 真实飞书验收 |
| [多空间创建、切换与独立运行](spaces-create-switch-s2a.zh.md) | 空间 S2a：创建／改名／切换、私有运行时与凭据、目录占用、后台恢复、双真实 DSH 与双 AIO 验收；归档／重开留 S2b |
| [默认空间与正式身份](space-identity-s1a.zh.md) | 空间 S1a：可恢复 User／Space 登记、原地归属、统一默认空间授权、浏览器与执行身份；运行时装配见 S1b |
| [默认空间运行时与部署装配](space-runtime-s1b.zh.md) | 空间 S1b：实际个人组合、默认 SpaceRuntime、配置与资源归属、原关闭权威、超时保留及原 Session 恢复；S1 完成；创建／切换见 S2a |
| [测试改造最终验收](test-suite-final-validation.zh.md) | F5：固定快照完整 integration／race、身份与时序夹具修正、成本分布及停止决策；CLI／Space 失败与未达预算保留 |
| [Workspace 夹具隔离](workspace-fixture-isolation.zh.md) | F4：私有 Git／HOME 环境、真实准备与重启边界、四槽并行和局部成本对照 |
| [动作 ledger 恢复分工](action-ledger-recovery.zh.md) | 测试 F3：四类真实落盘故障改由新组件重读，保留文本／图片真实进程恢复；回执与身份补强、健康修复和局部收益判断 |
| [Lark 配置族编译 CLI 夹具](lark-configuration-cli.zh.md) | F2 首族：只读原生协议、私有身份／scope／门闩、真实进程取消；整族对照、构建复用与扩散门槛 |
| [平台配置规则减装配](platform-configuration-rules.zh.md) | F1：补丁规则最小依赖、单成员连接夹具、请求拒绝及正式访问身份修复；相关族对照与未验边界 |
| [Computer 首版发布与采用](computer/release-adoption.zh.md) | C4：旧 MCP／environment 入口退出、可选 Runtime 打包、双真实 DSH 与 Chromium 组合、升级采用与无容器复核 |
| [Computer 本机 Docker 托管启停](computer/docker-lifecycle.zh.md) | 本机固定镜像、全局创建／启动／结清停止、资源归属与重启恢复；发布与真实平台验收边界单列 |
| [Computer 成员能力与 Agent 执行](computer/agent-capabilities.zh.md) | C2：统一 MCP、成员开关、Session 能力、外部执行结清／轮换；真实模型文本／像素任务、关闭后请求 Schema 和离线普通回合证据 |
| [Computer 观看与人工接管](computer/view-control.zh.md) | C3a 统一电脑面板、服务端只读、排他接管、输入退出 Fence、暂停意图和真实双屏／浏览器验收；文件交付后置，日常采用另见 C4 记录 |
| [Computer 全局设置与在线连接管理](computer/online-connection.zh.md) | C1b：在线接入、私有凭据、原子配置与单一应用操作、逐屏结清／重启、无容器路径及真实 AIO／浏览器组件；成员与 Agent 闭环留 C2 |
| [受管 Computer 与 AIO Screen Runtime](computer/managed-runtime.zh.md) | C1a：Provider／Screen、认证管理入口、唯一原生调用、去重与 fencing、Drain／Stop、双屏真实 AIO 与 Runtime 崩溃恢复；设置和 Agent 接入留后续阶段 |
| [共享 Computer 双屏运行可行性](computer/runtime-feasibility.zh.md) | C0：固定新版 AIO 派生镜像、独立登录与双屏操作、21 项原生工具、只读观看、单屏停止和容器重启；正式服务与 Agent 接入留后续阶段 |
| [右侧环境画面与人工接管](agent-sandbox/environment-viewer.zh.md) | 历史旧入口，已由共享 Computer 替代；noVNC 侧栏、独立网关配置、暂停与交还、成员与登录隔离；真实 AIO 输入／虚构登录和受控 Agent 恢复验收 |
| [Agent 工具目录精简投影](agent-tool-discovery.zh.md) | S3b：明确 Agent 字段、准确入口与配置授权、可用性及修复诊断；所选组合验证和八回合真实模型只读对照 |
| [现有工具绑定总开关短路径](agent-tool-toggle.zh.md) | S3a：服务端冻结现有绑定和授权、稳定回执、休眠授权恢复、并发复核与撤权结清；完整编辑合同保留 |
| [DSH 内部助手操作与生命周期验证](agent-collaboration-boundaries.zh.md) | S2a：打包版本、子 Agent 实际工具与成员边界、取消／关闭及恢复／轮换证据；逐项兼容限制与修复单，不代表完整 S2／P4b |
| [工作区图片、相对资源与下载](workspace-resources-and-downloads.zh.md) | F3：图片验证与放大、文档相对链接／锚点、图片预算和有界下载；隔离 HTTP／浏览器证据，真实模型闭环留 F4 |
| [Agent 常驻流程收敛](agent-resident-guidance.zh.md) | S1d 候选：已有 topic 的直接路由与安全边界、最终工厂指导和分层验证；URL 真实模型成本门槛未闭合，未切换日常 Session |
| [飞书嵌套动作别名评估](agent-lark-action-alias.zh.md) | S1c-C2：共享合同／回执对照、最终输出 Call 所有权差异与保留结论；不删除别名、不改生命周期 |
| [飞书保存目标读取入口收敛](agent-lark-read-entry.zh.md) | S1c-C1：根 lark read 别名移除，准确 ENTRY 的正文／上下文分页、账号恢复及隔离；原 Runtime/Host、HTTP 与结清保留 |
| [GitLab 发送 Skill 按任务加载](agent-gitlab-send-skill-topic.zh.md) | S1b-B2：准确 ENTRY 的 managed-send topic、文本／图文与准确讨论、跨轮目标复用和 unknown 恢复；默认完整指南兼容及分层验证 |
| [平台设置 Skill 按任务加载](agent-platform-skill-topics.zh.md) | S1b-B1：通知总开关、选择卡片两个自足 topic；公共路由、异步恢复、完整指南兼容及分层验证 |
| [Agent 后端身份与通信说明](agent-backend-guidance.zh.md) | S1a-A3：公共身份边界、DSH 内部通信与 review 归属；真实模型对照、子 Agent 指令接收证明及 Session 生效边界 |
| [Agent 正常业务指导校正](agent-business-guidance.zh.md) | S1a-A2：流式输入/来源恢复、事项自动关联与结算、准确工具入口和托管发送指导；隔离模型对照及 Session 生效边界 |
| [GitLab 隔离托管图文动作](gitlab-image-send-s1.zh.md) | 阶段 1：项目原图上传、单次评论／准确讨论回复、回读与最小 Agent 合同；后续真实 Agent 与恢复见阶段 2 |
| [GitLab 图文 Agent 使用与恢复](gitlab-image-send-s2.zh.md) | 阶段 2：指引／字段反馈、普通 Room／默认助理各 16 场景真实 DSH、独立进程恢复／并发与定向 race；正式图文保持关闭，真实 GitLab 验收见阶段 3 |
| [GitLab 图文真实验收与开放](gitlab-image-send-s3.zh.md) | 阶段 3：真实 Agent 发布、原图下载比对、独立进程回执重查、真实通知回流和浏览器验证；正式注册开放，日常服务／Session 未切换 |
| [GitLab 完整 URL 托管目标入口](gitlab-image-send-s4.zh.md) | 阶段 4：完整 MR／note／discussion URL 核实及稳定引用，全新成员托管链路、真实只读定位和两条授权发布；保留首次模型审计失败及分层复验边界，日常服务未重启 |
| [飞书内置工具应用运行边界](architecture/tool-runtime.zh.md) | P2t：所选工厂运行绑定、help／read／actions 应用与 HTTP 分离、共享原图宿主及原 Calls／动作结清；其余 R6 调用族与产品裁剪留后续 |
| [通知来源运行绑定](architecture/notification-runtime.zh.md) | P2n：飞书 messages 显式 Bind／准入／加载、原 owner 与结清；外部通知 module 的持久投递、重开与缺 codec 保护 |
| [外部本地工具公开装配](architecture/extension-assembly.zh.md) | P1b：公开 Tool／Compose／boot、正常默认入口、显式选择贯穿离线操作；独立 module 的发现／执行与结清验收，产品裁剪留 P2r／P5 |
| [公共平台目录与内置工厂解耦](architecture/platform-catalog.zh.md) | P1a：通用目录与三个内置工厂分包、专用来源查询绑定、默认完整入口及依赖／定向验证；外部作者入口与产品裁剪留后续 |
| [扩展与按需构建的基线及分支交接](architecture/plugin-handoff.zh.md) | P0：当前工作树、owner／锁／关闭链、Gordis／Build 发布 API 与定向验证；平台 P1a 可进入，DSH 跨包前须收敛宿主操作，保留组件超时与未验收范围 |
| [飞书完整托管图文动作](lark-image-send-s2.zh.md) | 隔离 self／群原图上传与单次发送、完整内容冻结和最终授权复核；真实验收与正式开放见阶段 4 记录 |
| [飞书图文 Agent 使用与恢复](lark-image-send-s3.zh.md) | 指引、可选图片权限修复、独立进程恢复与并发、浏览器及两种入口各 12 回合真实 Agent 隔离验收；阶段 4 真实开放见下条 |
| [飞书图文发送：真实验收与开放](lark-image-send-s4.zh.md) | 阶段 4 真实 DSH／飞书六样本、同 ID／重启恢复及回流通过，正式 self／指定群图文已注册；含历史 unknown 修复证据与部署采用边界 |
| [Room 图片附件完整链路验收](room-image-attachments-acceptance.zh.md) | 阶段 4 真实 HTTP／存储、Chromium、重启、原请求重试和真实 DSH 读图／History；实体手机待用户验证 |
| [Room 图片附件客户端](room-image-attachments-client.zh.md) | 阶段 3 选图／粘贴、草稿恢复、消息预览及模拟 HTTP 浏览器验证；后续真实验收见上条 |
| [Room 图片附件读取](room-image-attachments-reading.zh.md) | 阶段 2 正式读取／History、本地 resolver、来源去重与交付保护；前端见上条，完整真实验收留阶段 4 |
| [Room 图片附件存储](room-image-attachments-storage.zh.md) | 阶段 1 对象持久化、HTTP、消息合同与回收；Agent 适配见上条 |
| [测试健康度与核心回归提速](test-suite-health.zh.md) | T1–T4 修复、提速、规则下沉与浏览器分层证据；核心全仓 7 分 33 秒通过、预算未达；最新完整集成见第四批记录，全仓 race 待验 |
| [Room 授权与工具测试收缩](authorization-tool-matrix.zh.md) | §9.2 第二批：动作规则及工具矩阵承接、模型夹具收缩；切片约缩短 13.2%，完整集成 12 分 49 秒通过、八分钟预算未达 |
| [Lark 原生协议测试矩阵下沉](lark-protocol-matrix.zh.md) | §9.2 第三批：候选校验及错误矩阵下沉、四槽诊断；Lark 切片约缩短 19.6%，完整集成通过，八分钟预算未达 |
| [定时等待与日历发布夹具收缩](timer-assembly.zh.md) | §9.2 第四批：受控负向窗口、Inbox 最小规则状态及真实发布组件；切片中位数约缩短 24.2%，完整集成通过，八分钟预算未达 |
| [Lark provider 与成员最小夹具](provider-fixtures.zh.md) | H1 首组：去无关 GitLab 装配、单／双成员按需创建；整族对照未证明提速，后续 provider 族暂不扩散 |
| [核心测试规则与夹具收缩](core-test-fixtures.zh.md) | H2：Input／History、Workspace、平台配置及输入升级的规则承接、薄接线与不可变种子；完整四族对照和定向验证 |
| [动作与引用撤权矩阵收缩](platform-revocation-matrix.zh.md) | H3.1：保留真实撤权入口及三个阶段，补齐 owner 隔离和正式 OAuth callback；整族 42.4 → 22.1 秒，单并发与定向 race 通过，既有 vet 阻挡保留 |
| [后端生命周期依赖收敛](architecture/backend-lifecycle.zh.md) | R5b.1：原包接通执行、结清、轮换、恢复、检查点与模型应用合同；真实 DSH 手动／自动轮换与正常恢复已验，成员状态机迁移及 Codex 接入另行实施 |
| [平台在途调用登记与结清](architecture/platform-calls.zh.md) | R6a.1：全部调用族共用登记、取消、完成回执与等待；准入、绑定变更及配置仍在原 manager 门槛内 |
| [平台静态能力装配](architecture/platform-assembly.zh.md) | R3b：平台类型、工具、通知来源、输入 codec 和引用能力使用同一已校验描述；registry 注入快照，运行权限与生命周期留在外层 |
| [回合调度器](architecture/turn-scheduler.zh.md) | R5a：serial／parallel 授予、排队、取消／释放、退出结清和服务冻结门槛；共享身份归位，成员状态机及后端生命周期保留 |
| [快照 writer 与健康围栏](architecture/persistence-writer.zh.md) | R1a：通用快照读写／删除、同路径串行、共享健康围栏和关闭结清；原目录锁及业务 owner 保留 |
| [平台精确工具入口解析](architecture/platform-entries.zh.md) | R3a：无 I/O 绑定／入口／连接匹配，正常列表与执行接入；保留按需 registry 读取、错误顺序和原准入／结清 |
| [模块边界重构基线](architecture/baseline.zh.md) | R0：受管／单 Room 入口、History 合同、状态 owner／持久路径／关闭顺序、后续调用点与测试索引、生产依赖约束及基线失败；后续状态见各单元记录 |
| [日历界面与任务信息分层](calendar/refinement.zh.md) | 紧凑工具栏、月历导航、分钟时间轴、分层表单与详情；桌面及手机视口验证 |
| [私信日历首版交付与组合验收](calendar/release.zh.md) | C4 重复任务、会话隔离、暂停恢复、离线周期、受管服务重建与页面组合；汇总 C1–C3 证据，区分真实模型、受控执行和模拟视口 |
| [会话日历体验与管理](calendar/experience.zh.md) | C3 周视图、手机日程、任务编辑、历史与聊天跳转；浏览器视口模拟，首版组合与交付见上条 |
| [Agent 历史入口收敛与生产路径验收](agent-history-h4.zh.md) | 默认 History MCP、旧 CLI 退出及生产路径验证；现有运行服务的切换独立记录 |
| [Agent 历史恢复与集成验收](agent-history-h3.zh.md) | H3 隔离 Agent／恢复指引、真实 DSH 代表任务、真实 GitLab／飞书图文、工具集安全刷新及容量并发；旧命令和正式服务切换留 H4 |
| [Agent 历史 MCP 与图文交付](agent-history-h2.zh.md) | H2 隔离两工具、HTTP／CLI 模式、材料与账号复用、输出生命周期和真实 DSH／ACP 图文小样本；生产尚未启用 |
| [Agent 历史读取视图与续页](agent-history-h1.zh.md) | H1 保留流目录、固定快照、正文续页、预算与只读边界 |
