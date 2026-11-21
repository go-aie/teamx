# 实施记录

这里保存已实施阶段和被替代方案的历史记录。阶段性验收记录已去重，仍需维护的验证方法和未完成项保存在对应模块说明；保留方案只用于追溯设计。当前使用方法见[项目说明](../../README.md)、[Server 架构](../server.zh.md)、[通知手册](../notifications.zh.md)和[飞书说明](../lark.zh.md)；待实施方案继续保留在 `docs/`。

| 记录 | 范围 |
| --- | --- |
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
