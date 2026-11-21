# GitLab URL 入口真实写验收清单

状态：用户已单独授权并执行。实际上传 2 张图片、发布 2 条 note，未增加其他写入；[普通评论 228616](https://git-devops.opencsg.com/product/agentichub/teamx-demo/-/merge_requests/12#note_228616)、[讨论回复 228617](https://git-devops.opencsg.com/product/agentichub/teamx-demo/-/merge_requests/12#note_228617) 均已独立回读及下载原图核实。原始整轮模型调用审计失败及后续只读恢复见[实施记录](gitlab-image-send-s4.zh.md)。方案第 3.2 节要求重新明确真实写入范围；阶段 3 的授权不沿用。

账号：`RussellLuo`（GitLab user ID `13`），实例 `https://git-devops.opencsg.com`。

项目：`product/agentichub/teamx-demo`（Project ID `482`），MR `!12`（全局 ID `13190`）。两个链接均已用当前账号通过正式 URL resolver 只读核实。

| 动作 | 完整 URL | 准确正文 | 原图 |
| --- | --- | --- | --- |
| 一条 MR 普通评论 | [MR !12](https://git-devops.opencsg.com/product/agentichub/teamx-demo/-/merge_requests/12) | `TeamX GitLab URL 入口验收：第一张原图（蓝色）。` | 第一张蓝色测试图 |
| 一条现有讨论回复 | [讨论内评论 228578](https://git-devops.opencsg.com/product/agentichub/teamx-demo/-/merge_requests/12#note_228578) | 无文字 | 第二张绿色测试图 |

线程完整 ID：`680f5bcbb2f1d20b8beff17953e04503799e674f`。不新建讨论。

| 原图 | 大小／尺寸 | SHA256 |
| --- | --- | --- |
| [before.png](../../.local/gitlab-image-send/url-real-20261121/before.png) | 302 字节，160 × 90 | `9a499c9b0a09795fd237027e41983dfb27438cd602a18fc3003bb5f49cc1659c` |
| [after.png](../../.local/gitlab-image-send/url-real-20261121/after.png) | 302 字节，160 × 90 | `03e8d7eac53d3c71b26bfcd0447701eacc8bd418c86fdac571202e98af9c0b6e` |

上限：共上传 2 张图片、发布 2 条 note（普通评论和线程回复各 1 条）。两张图片从人类 Room 消息取得，保持原始字节。每个样本先自然语言提出完整 URL，再由真实 Agent 发现账号入口、resolve 和 execute；不预存 target_ref／原生 ID，不附加命令教程。结果须独立回读，核对作者、完整正文、图片 SHA256、顺序和线程归属。

使用独立验收数据和一次性派发目录，不重启日常服务，不切换已有 Session，不刷新／撤销日常 OAuth grant。持久派发预算约束跨进程补发；不确定结果停止发送并查询原回执。只读复核和同 ID／重启回执恢复不占新增写入额度。

私有输入：[input.json](../../.local/gitlab-image-send/url-real-20261121/input.json)。派发目录已消耗，不能重跑以补发。执行入口为 `TestRealGitlabActionURLAgentSend`，须单独设置 `TEAMX_GITLAB_ACTION_URL_SEND_REAL=1`，旧阶段的 opt-in 不会启用它。
