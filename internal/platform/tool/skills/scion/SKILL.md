# Scion 原生开发与 Review

先读取当前成员的 `"$TEAMX_CLI" tools list`，再使用 `tools run scion --help`、`tools skill scion`。工具在服务端当前成员的有效 Workspace 根目录执行；shell 中的临时 cd/env 不会传入服务。子目录使用原生 `-C`，文件参数相对此目录解释。缺少程序、登录或身份不符时报告给操作者，不搜索凭据、直接运行 scn、登录换号或绕过入口。

工具固定 `scn 0.1.13`，保留原生 JSON、stdout/stderr 与退出码；没有 GitLab MR/PR 兼容层。实例与 Hub 由成员所选连接注入。无工作副本读取时明确指定已核实的仓库，例如：

```sh
"$TEAMX_CLI" tools run scion --json --repo OWNER/REPOSITORY repo info OWNER/REPOSITORY
"$TEAMX_CLI" tools run scion --json --repo OWNER/REPOSITORY review ls --state all --limit 100
"$TEAMX_CLI" tools run scion --json --repo OWNER/REPOSITORY review show NUMBER
"$TEAMX_CLI" tools run scion --json --repo OWNER/REPOSITORY review comments NUMBER
```

`truncated=true` 表示未读完，不能据第一页断言没有对象。`review show` 的 Review 字段位于 JSON 顶层，另附 revisions/checks/verdicts/timeline；base 必须从对应 revision 读取。保留仓库 canonical name、Review number、实际网页链接、target、revision 与完整原生 base/head ObjectId；这些 ID 不是 Git 的 40 位 SHA。固定 v0.1.13 的 changeset 文本为 `c1` 加 64 位十六进制摘要，必须保留前缀。CLI 的 Review JSON 不含网页 URL；普通 `OWNER/REPO` 的原生网页路由为 `INSTANCE/OWNER/REPO/reviews/NUMBER`（没有 GitLab 的 `/-/merge_requests/`）。在实例、canonical repo 和 number 已回读核实后按该路由交接，不把未返回 URL 当作创建失败。

开发必须先核实当前工作副本、目标分支、基线和测试入口。Scion 使用 `.scion/`，没有 `.git/`，不运行 Git 命令（包括 status），也不使用 Git fetch/checkout/MR refs；Git 可能向上发现服务或其他父目录仓库，不能将其当作当前项目。TeamX Workspace 设置支持选择 Scion 类型与已验证连接，为每个 membership 独立初始化；没有受管工作副本时请操作者在设置中显式应用，不自行覆盖非空目录。clone 连接不会自动启用成员工具，日常命令仍需成员显式绑定。成员各用独立副本，不跨成员路径交付文件。

原生开发顺序是任务分支 `switch -c NAME`、修改与测试、`commit -m TEXT --author 'Name <email>' PATH...`、`land`，再读取实际发布 head。作者必须明确提供，认证 subject 与提交作者是两个概念。没有 staging area；省略 commit 路径会包含全部改动，不把 add 当隔离。禁止擅自清除已有改动、pending stack 或冲突。分支名区分 Room、membership 和任务。

**两种 land：** `scn land` 发布当前任务分支，可能 rebase 并产生 rewritten ID；重新核实发布后的 head 与代码树，必要时补测。`scn review land N` 合入 Review 的目标分支，需要用户具体决定；评审通过不构成合入/关闭授权。当前流程使用普通版本化评论，不自动 approve/request-changes、合入或关闭。

发布任务分支后，先查是否已有本任务 Review，再以实际 head 创建 Review。修正时复用任务分支及同一 Review，commit/land 后 `review push NUMBER FULL_HEAD`，读取新 revision 并重新交接。`review update` 同样可能改变版本，不能沿用旧结论。

独立评审先读 Review 与完整讨论，确认自己的副本干净且 pending/冲突允许切换，再 `switch --detach FULL_HEAD`。通过 `status --no-ahead-behind` 核实实际 base=head、detached=true、pending 为空，查看准确 base/head 的 diff 与完整代码，独立运行测试。发布前再读 Review，版本变化则旧结论只适用旧 revision。普通评论显式绑定版本：`review comment NUMBER '正文' --revision REVISION`。发布后读取实际评论 ID、作者、正文和版本再向 Room 交付。

交接包括实例、canonical repo、Review number/实际链接、target、revision、完整 base/head、实际测试、发现与未验项。同一平台账号的不同成员可能显示相同远端作者，不把独立副本误称独立账号。

退出 0 按具体对象回读确认；1 一般失败、2 用法错误、3 冲突、4 暂时错误，5 可能是认证或权限失败。创建、评论、push、land 遇到超时、中断或断流不自动重发、不回退 REST。先记录目标、时间、版本及返回 ID，再只读核查远端作者、正文、revision/head。Review land 返回成功也可能仍在排队，不能仅凭 action 文本宣布 landed。
