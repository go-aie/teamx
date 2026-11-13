# 脚本准备 base 的同文件并行实验

流程只有三步：**脚本发布一个可运行的 base → 新建并配置 Room → 发送脚本生成的提示词**。不需要先运行分模块示例，也不需要 Agent 再开发 SEED；本页给出完整操作。

三个 developer 会在各自独立 Workspace／分支中修改同一份程序、测试和说明，最后观察冲突与集成。base 准备和发布已经实测；三名 Agent 的同文件并行修改尚未真实运行，原账本验收结论不覆盖它。

## 1. 运行 base 准备脚本

需要 Python 3 和已登录的原生 **scn 0.1.13**。脚本使用你的原生 CLI 登录，默认核对账号 `csgship`；它与 TeamX 页面里的受管登录分别配置，不会自动查找或复制 TeamX 私有凭据。已有原生登录可直接使用；没有时先通过 `bin/scn login https://scion.opencsg.com` 完成交互登录，或用 `--credentials-file /绝对路径/credentials.json` 指定已有登录文件。凭据不写进提示词。

在 TeamX 仓库根目录执行：

```sh
python3 .teamx/teams/scion-parallel-development/overlap/prepare.py --publish
```

默认目标为 `https://scion.opencsg.com/OpenCSG/teamx-demo`。脚本优先使用 PATH 中的 scn，其次使用当前目录的 `bin/scn`；路径不同时追加 `--scn /绝对路径/scn`。实例、仓库或预期账号不同，可分别指定 `--endpoint`、`--repo`、`--subject`。

如果原生 `whoami` 返回 `anonymous`，表示这份 CLI 登录未配置；TeamX 页面显示已登录并不意味着原生 CLI 也已登录。已有 TeamX 受管登录时，可明确通过 `--credentials-file` 引用对应账号的已有登录文件，直接复用，无需再登录或复制 PAT。账号确实不同才核对预期 `--subject`，不要把它改成 `anonymous` 来绕过检查。失败报告会保留实际账号、实例及 CLI 路径，分别说明未登录、账号不符或实例不符；身份检查失败不会创建远端分支。

脚本会自动：

- 在全新本地目录准备单文件 Todo 程序、共享测试和说明，并运行基础测试。
- 核对账号、仓库和 main 的完整 head，clone 到独立目录，仅创建并发布 `teamx/overlap-base/<本轮唯一标识>`，不改 main 或已有分支。
- 通过远端准确 head 回读三个文件、核对提交作者与发布账号，再独立运行取得的基础测试。
- 打印 base 分支、完整 head、代码链接和 `prompt.txt` 路径；提示词中的实例、仓库和版本已填好，不需要你手工替换 Room／membership ID。

初始代码已支持 add、list、done 和 JSON 文件持久化；后续三项增强会共同改动 `overlap_todo.py`、`test_overlap_todo.py`、`OVERLAP-TODO.md`。

结果保存在 `.local/scion-overlap/<本轮标识>/`。只有输出 **Status: published** 才继续下一步。要先看本地代码和测试，可省略 `--publish`；此模式不调用 Scion、不写远端，生成的提示词只是带占位版本的草稿。

## 2. 新建并配置 Room

在你已有可用模型的 TeamX 服务页面中完成：

1. 「＋ → 新建房间」选择 **Scion 并行开发**，确认有 architect 和 developer-a/b/c。
2. 四个成员分别在「平台」选择同一个已验证为 `csgship` 的 Scion 平台并启用「使用工具」。已有绑定直接复用。
3. 打开输入框下方的 Workspace：类型选 **Scion**，仓库 URL 填脚本的目标仓库，初始分支填脚本刚打印的 **base 分支**，选择该 Scion 平台，应用并等待四个独立副本准备完成。这里不用 main。
4. 只有成员显示「启用协作」时才点击一次；已经启用或运行中无需重复点击。

如果团队列表中没有该模板，先将本团队放入当前服务实际使用的 `teams/` 目录或 `server.teams_dir`，重启服务后再创建。安装版和项目版同时运行时，使用不同端口并确认当前页面属于配置好的服务。

原生 CLI 与 TeamX 平台可以复用同一个账号，但登录配置分别管理；Workspace 初始化也不会自动替代成员工具绑定。无需重新申请账号或在 Room 中传递 PAT。

## 3. 发送生成的提示词

打开脚本打印路径中的 `prompt.txt`，**整段复制到新 Room，发送一次**。它已经包含共同的准确 base 和本轮分支范围，也包含成员自己的只读核验要求，无需再发上一版的核验消息。

architect 按现有角色发布简短设计与任务表，然后三个 developer 从脚本发布的同一 head 并行修改：

| 成员 | 增强目标 | 共同修改的文件 |
| --- | --- | --- |
| developer-a | 优先级和旧数据兼容 | 同一程序、测试与说明 |
| developer-b | 状态过滤和统计 | 同一程序、测试与说明 |
| developer-c | JSON 输出并保留字段 | 同一程序、测试与说明 |

三项交付后，developer-a 在独立集成分支组合准确版本，记录实际冲突或首次组合测试失败，修正后重跑完整测试；architect 核对功能保留情况。它们不直接发布公共 base 或 main。

## 看什么结果

重点看参数解析和列表输出的改动能否组合：`list --status open --json` 是否同时有效、JSON 是否保留 priority、统计是否仍为全库计数、基础测试是否被误删。集成交付须给出完整 head、代码链接和实际测试输出。

同文件修改不保证必然出现文本冲突；无冲突也可能有功能覆盖或语义错误。保留三项交付与集成前后的失败记录，不把“三个分支各自通过”当作组合通过。

在成员「活动记录」观察真实工作；严格的并发结论还需要至少两名 developer 的实际编码时间区间有交集，不能只看三人同时显示运行中。旧账本的时间提取脚本固定了文件名，不适用于本场景。

准备脚本在远端响应不确定时会停下并保留 `report.json` 和工作副本，不重试发布或自动删除分支。先检查记录中的原分支及完整 head，再决定恢复；不要直接反复执行发布命令。下一轮使用新 Room，脚本会生成新标识，保留旧结果。

准备脚本已通过 8 项离线检查。2026-11-13 使用现有 `csgship` 登录完成真实分支发布、准确 head 的三个文件回读和独立下载后的 7 项基础测试，main 未改变；本机回执位于 `.local/scion-overlap/overlap-20261113T132133Z-20e9b5-authenticated/`。本轮可直接使用该目录的 `prompt.txt` 和报告中的 base 分支，继续第 2、3 步，无需重复发布。
