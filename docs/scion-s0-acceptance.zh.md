# Scion S0：凭据与实例探测

日期：2026-11-07。范围为 [接入方案](SCION-SOFTWARE-DEVELOPMENT-PLAN.zh.md) 的 S0，目标实例 `https://scion.opencsg.com`，仓库 `OpenCSG/teamx-demo`。

当前结论：**S0 通过。固定 CLI、隔离 PAT 登录、实际账号、仓库身份、默认分支及基线提交、Hub 读取均已验证。** 初次探测发现仓库没有分支；用户随后明确选择用已展示的 README 初始化 `main`，现已完成原生提交、发布、远端核验及另一目录的独立 clone。S1–S4 未实施。

## 环境与凭据

- 运行平台：macOS arm64。
- 固定官方发布：`scn 0.1.13`。复用此前下载的 `scn-v0.1.13-aarch64-apple-darwin.tar.gz`，本次重新计算 SHA-256，匹配方案中的已核实值 `4a3d15ce772070fddd89837d66e443b22f4baf9cc253bd64175e3b19456eeb07`。未将本地源码 HEAD 构建冒充发布包。
- 解包后二进制 SHA-256：`1d5c4c2b09aeed7245fae2a0bd0a5a13d3f927f2406d211b7d78f14953f4ff3f`，与缓存的已解包文件一致。匿名发布详情 API 本次仍返回 HTTP 401；没有把它当作重新认证下载成功的证据。
- 本次运行目录：`.local/scion-s0-20261107-1/`。二进制、临时 HOME、profile 和原始探测输出均在此隔离目录下，已被 Git ignore。
- 用户将原生 PAT 放在 `.env` 的 `SCION_PAT`。探测程序只解析这个键，将其通过 stdin 交给 `scn login --with-token`，没有 `source .env`，也没有把令牌放入 argv、文档或探测输出。
- 显式设置 `SCION_CREDENTIALS=file`、`SCION_CREDENTIALS_FILE=<运行目录>/profile/credentials.json`、`SCION_NO_DAEMON=1`，以及同为 `https://scion.opencsg.com` 的 `SCION_ENDPOINT`、`SCION_HUB`。HOME/XDG_CONFIG_HOME 指向本次私有目录；没有继承 `SCION_TOKEN` 或使用全局登录作为备用。
- profile 目录权限 `0700`，凭据文件 `0600`。后续读取仅使用原生登录保存的文件，不再注入 PAT；独立的新 CLI 进程复核账号成功。

## 实测结果

以下为初始化之前的探测快照；空分支及 `REF_NOT_FOUND` 已由下一节的获准初始化补齐。

| 调用 | 结果与实际证据 |
| --- | --- |
| `scn --version` | 退出 0，`scn 0.1.13` |
| `scn --json doctor https://scion.opencsg.com` | 退出 0，`ok=true`，服务端 `0.1.13`，协议 `1.0`，包含 `auth.required`，未声明 `auth.device` |
| `login/repo info/token create/review ls --help` | 均退出 0；仅检查帮助，没有创建令牌或 Review |
| `scn --json login https://scion.opencsg.com --with-token` | 退出 0，`schema=scion.cli.login/v1`，账号 `csgship`，保存到指定 profile |
| `scn --json whoami https://scion.opencsg.com` | 退出 0，`schema=scion.cli.whoami/v1`，subject `csgship`，`auth_required=true`；与登录返回的 subject 一致 |
| `scn --json repo info OpenCSG/teamx-demo` | 退出 0，canonical name `OpenCSG/teamx-demo`，配置默认分支 `main`，非 archived、非 deleted |
| `scn --json --repo OpenCSG/teamx-demo branch ls` | 退出 0，`branches=[]` |
| `scn --json --repo OpenCSG/teamx-demo log main -n 1` | 退出 1，`reason=REF_NOT_FOUND`，`revision "main" not found` |
| `scn --json --repo OpenCSG/teamx-demo ls main:` | 退出 1，同为 `REF_NOT_FOUND` |
| `scn --json repo rules OpenCSG/teamx-demo` | 退出 0，`rules=[]`；本次未修改服务器分支规则 |
| `scn --json --repo OpenCSG/teamx-demo review ls --state all --limit 100` | 退出 0，`schema=scion.cli.reviews/v1`，repo 匹配，`reviews=[]`、`truncated=false`；已走显式 Hub 地址 |
| 新进程再次 `whoami` | 退出 0，subject 仍为 `csgship`，验证已保存的隔离凭据可被后续进程使用 |

原始 stdout/stderr 和带退出码的摘要保存在运行目录的 `evidence/`。探测开始时间为 `2026-11-07T09:53:05Z`；具体认证时间见私有 `evidence/summary.json`。

## 获准初始化与最终基线

用户明确选择：“由你用这份 README 初始化 main”。实际写入仅为：

```markdown
# TeamX Demo

用于验证 TeamX 软件开发团队通过 Scion 完成开发与代码评审。

此初始提交仅建立仓库基线。后续功能、测试与评审在独立任务分支中完成。
```

执行前再次核实账号 `csgship` 且分支列表仍为空，然后：

1. 使用固定 CLI 将指定仓库 clone 到本次运行目录的 `init-workspace/`，显式 `--ref main`。空仓库 clone 成功但 `base=null`，本地 `.scion/state.json` 的 format、endpoint、repo、ref 均与目标匹配。
2. 只复制这份 README。`status --no-ahead-behind` 返回 `added=["README.md"]`，其他变更为空；执行 `commit -m 'docs: initialize TeamX demo baseline' README.md` 一次。
3. 在 pending commit 上读取历史、目录及 README 内容，确认无父提交、仅有一个文件、正文与获准草稿逐字节一致。
4. 再次核对账号及空分支列表，`land --dry-run` 返回 `dry_run=true`、`rebased=false`；执行原生分支发布命令 `land` 一次以建立 `main`，未执行 `review land`。发布结果没有 rebase 或 rewritten。
5. 重新读取远端分支、`main` 历史、根目录及 README；均与返回的完整 tip 一致。本地状态为同一个 base、`pending=null`、无文件变更。
6. clone 到另一新目录 `verification-workspace/`。新副本的 `.scion` 身份及 base 再次匹配，README 字节一致，根目录仅 `.scion/` 与 `README.md`，不存在 `.git/`。

最终结果：

| 项目 | 核实值 |
| --- | --- |
| 仓库 | `OpenCSG/teamx-demo` |
| 默认分支 | `main` |
| 分支版本 | `1` |
| 完整原生提交 ID | `c1078e705cc235a88699caf8aaf51c45803df33aafb8849ea36949756f8a36d4a0` |
| 提交说明 | `docs: initialize TeamX demo baseline` |
| 父提交 | 无，初始提交 |
| 文件 | 仅 `README.md`，199 字节 |
| 登录 subject | `csgship` |
| 提交 author / committer | 原生默认 `scion` / `scion`，author email 为 `scion@localhost`；本次隔离 HOME 未配置作者，没有将这个默认作者误记成登录账号 |

本次初始化的目标、获准范围、README 校验值、pending/published ID 和完成状态记录在私有 `evidence/initialization.json`；写入前先保存意图，没有自动重试远端写入。对应命令的 stdout/stderr 同样保存在 `evidence/`。

初始化及独立 clone 核验完成时间：`2026-11-07T09:59:15Z`。私有摘要共记录 33 项命令检查，包括初始化前预期的两次 `REF_NOT_FOUND`，不将这两个前置条件探测写为成功命令。

## 发现与下一步

1. 原生 PAT 登录路径已真实通过，解决了此前短期 REST Bearer 无法被 CLI 再交换的问题。浏览器登录不在本次通过范围内。
2. `repo info.default_branch=main` 只是仓库配置，不能证明分支和提交存在。空仓库 clone 的实际 `base=null` 进一步证明，后续必须显式校验 ref，而不能仅以 clone 退出 0 判定可开发。获准初始化后已取得真实完整基线。
3. 原生 `.scion` clone、指定文件 commit、pending commit 读取、分支 land 及独立 clone 回读均已实测通过，支持方案采用原生 CLI 的技术方向。这只验证了操作者脚本路径，没有实现或验证 TeamX 的 RepositoryAdapter。
4. 本次只验证一次初始 `main` 发布，没有覆盖非默认任务分支、并发、rebase、冲突和失败恢复。Review 仅做真实列表读取，创建/更新/评论权限及行为尚未实测。
5. 临时 HOME 未配置作者时，原生提交落为 `scion <scion@localhost>`。后续工具接入必须提供明确的非秘密作者配置或 `--author`，并继续区分作者和认证 subject。
6. TeamX 工具注册、成员绑定、Workspace 适配、真实 DSH 和页面闭环仍在 S1–S4 验证。多成员独立副本、配置切换和 Session 恢复不能由本次两个操作者副本的 clone 结果替代。

本次文档与私有探测输出通过令牌泄漏检查；本地 `.scion` 元数据中没有保存 PAT。TeamX 生产代码未修改，现有 Room、连接绑定和 Session 未操作。除了用户明确选择的 README 初始提交及 `main` 发布，没有创建 Review、发表评论或执行 Review 合入/关闭。

本阶段两份 Markdown 的换行与尾空格检查通过。全工作区 `git diff --check` 发现无关的 `internal/room/web/index.html` 第 223、258 行尾空格，未修改这些并行工作；没有将全工作区检查报告为通过。未运行 Go/前端回归，因为本阶段未修改相应生产代码。
