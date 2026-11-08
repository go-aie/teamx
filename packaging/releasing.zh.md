# 正式发行操作指南

本指南供维护者将同一批版本候选验收并发布到 GitHub Release。只需要开发快照时，使用[构建与发行指南](README.zh.md)中的 Package snapshots。

正式发行的包型以统一 lock 的 `release.profiles` 为准，当前是四个平台的 core/standard 共 8 个包，推荐 standard；它们都必须具备发行资格。full 不在默认正式发行范围内，Linux full 的 `git_tls_runtime` 缺项仍保留。已有的单目标开发包通过结果不能代替新候选验收。

## 准备仓库与测试模型

将以下 Workflow 和完整打包代码提交到 GitHub 仓库，手动入口所在的 Workflow 需先进入默认分支；版本 tag 也必须包含相应代码：

| Workflow | 触发方式 | 用途 |
| --- | --- | --- |
| [package.yml](../.github/workflows/package.yml) | Pull request 或手动 | 开发快照 |
| [release.yml](../.github/workflows/release.yml) | 推送 `vX.Y.Z` tag，或手动选择包型 | 构建并下载复验候选 |
| [release-publish.yml](../.github/workflows/release-publish.yml) | 在同一版本 tag 上手动运行 | 真实验收、资格汇总及正式发布 |

在仓库的 **Settings → Secrets and variables → Actions**，或 **Settings → Environments → release-acceptance → Environment secrets** 中配置以下值，入口详见 [GitHub Secrets 设置说明](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets)：

| Secret | 内容 |
| --- | --- |
| `TEAMX_RELEASE_MODEL_CONFIG` | 测试 TOML，包含有效的 `[runtime.model]`；`api_key_env` 使用 `OPENAI_API_KEY`，按模型服务填写协议、地址、模型名及兼容参数 |
| `TEAMX_RELEASE_MODEL_API_KEY` | 测试模型 API Key，地址须能从 GitHub runner 访问，额度须覆盖完整矩阵验收 |

API Key 从模型服务商控制台创建，直接保存到 GitHub Secrets。测试 TOML 只提供模型设置，不复制本机 Room、平台连接或其他凭据。不要把密钥放入源码、Workflow 输入或公开验收 JSON。

候选构建不使用模型或平台凭据。发行验收使用包内真实 DSH 和生产页面，运行会消费模型额度；Go、Node 和固定 Playwright 只作为验证机工具，不进入用户归档。私有回答、Session、日志和截图不上传 Artifact，步骤结束后清理。平台账号的检查由维护者在隔离环境完成，不放进 Actions。

发行使用 GitHub 自带 token，无需另建 PAT。只有最终发布 job 拥有 `contents: write`；模型验收使用 `release-acceptance` Environment，其访问规则由仓库设置决定。

## 1. 构建版本候选

准备干净源码及与 HEAD 一致的 `vX.Y.Z` tag，将 tag 推送到 GitHub。版本必须为三段数字，不接受同名分支代替 tag。

**Release candidates** 会按统一 lock 在原生 runner 构建全部 8 个包，并在独立 job 下载同一归档、核对摘要和运行检查。一次成功构建产出的候选 Artifact 和下载复验 Artifact 各 8 个；记下该次运行的 run ID、完整 commit 与 attempt。

候选构建成功只表示归档组装和传递检查通过。缺少真实模型或人工验收时，资格报告仍可为 `incomplete`；这不会自动发布 Release。候选 Artifact 保留 30 天。

手动运行 Release candidates 可选择单个包型，用于单独检查或 full 实验包。正式发行只接受由 tag push 触发、覆盖默认发行矩阵的候选运行，不能用手动运行替代。

重新构建候选时应完整重跑 Workflow，避免不同 attempt 混用。过期、缺失或其他运行的同名 Artifact 不能补作原候选。程序、运行时、配置或最终归档变化后，需要对新的归档重新绑定验收证据。

## 2. 完成用户环境与平台验收

下载候选 Artifact，先解开外层压缩包，保留其原始五个文件。在对应版本源码中为每个候选生成一个待填写模板；以下大写参数替换为实际值，输出文件必须不存在：

```sh
node scripts/package-release.mjs qualification-template \
  CANDIDATE_DIR TARGET PROFILE FULL_COMMIT vX.Y.Z NEW_QUALIFICATION.json
```

模板中的检查默认 `not_run`、观察值为 false，不能直接作为通过证据。使用匹配系统、原生架构和声明基线的隔离环境，完成下列操作：

| 检查 | 必须确认的行为 |
| --- | --- |
| `clean_machine` | 全新解压、独立 home、没有宿主 Go／Node／平台 CLI，系统依赖满足基线；full 还需没有宿主 Git |
| `downloaded_system_security`：macOS | 实际下载并保留 quarantine，记录首次启动是否被阻止；被阻止时按系统提示允许运行，确认最终能启动 |
| `downloaded_system_security`：Linux | 在声明系统中检查实际动态库和运行能力均在基线内 |
| `git_tls_clone`：full | 包内 Git 的 helper、证书验证、HTTPS clone 与本地 commit 均可用，未借用宿主 Git |
| `platform_login_read_bind`：standard／full | 用隔离账号完成 GitLab／飞书登录、只读查询和 TeamX 连接绑定，远端写入为 0 |

保留每项操作的私有证据，计算其 SHA-256 后填写 `evidence_sha256`、实际 UTC 时间 `tested_at`、`status=passed` 和观察结果。`fixture` 必须为 false，`method` 为 `maintainer_manual`。人工记录表示维护者对实际操作的确认，不会被当作自动测试结果。

将本次发行矩阵中各模板的 `records` 合并到同一个公开 JSON，保留共同的版本与完整 commit：

```json
{
  "schema_version": 1,
  "version": "vX.Y.Z",
  "commit": "完整40位commit",
  "records": []
}
```

每个 target/profile 只能出现一次，归档 SHA-256 必须对应实际受测文件。JSON 只放固定观察项和证据摘要，不放账号、主机路径、密钥、模型回答或原始日志。缺项、fixture、错包、错误架构／基线、重复记录和无效证据会阻止发行；不要修改摘要来匹配重建包。

## 3. 运行验收与发布

手动触发 **Publish qualified release**，使用同一个版本 tag 作为 ref，输入成功候选运行的 `candidate_run_id`、`candidate_run_attempt` 和公开 `qualifications` JSON。CLI 可明确指定 tag 并从文件读取 JSON：

```sh
gh workflow run release-publish.yml --repo OWNER/REPO --ref vX.Y.Z \
  -f candidate_run_id=RUN_ID -f candidate_run_attempt=ATTEMPT \
  -F qualifications=@/absolute/public-qualifications.json
```

先把仓库、版本、运行编号及文件路径换成实际值。候选来源必须是当前仓库的 Release candidates、由同一 tag/commit 的 push 触发，且指定的是已成功的最新 attempt，完整 Artifact 尚未过期。

Workflow 下载原候选，在匹配平台重新检查身份和归档，再执行真实 DSH/ACP、工具取消、原 Session 恢复及生产页面浏览器验收，最多并行 2 项。随后将自动结果与人工记录按归档摘要核对。所有 8 个包的必需项通过后，才汇总发行材料。

可以先省略人工 JSON 检查自动验收，但缺项会使发行停止；重跑真实验收会再次消费模型额度。

发布 job 创建 draft，上传原候选，下载远端资产重新计算摘要，完整匹配后公开。发布时不重新构建，也不改动已验收归档。正式 Release 包含各包的归档、摘要和验收报告，以及 `release.json`、`SHA256SUMS`、`LICENSES.tar.gz`、使用说明和发行说明。

## 失败后的处理

- **构建、下载复验或验收失败**：检查对应 job 和资格报告，解决缺项；候选字节变化时重新验收。验收或汇总失败，应完整重跑发行 Workflow。
- **发布 job 失败**：可在同一发行运行中仅重跑失败的 publish job，复用原汇总 Artifact。同一 draft 仅在版本、commit 和摘要完全一致时恢复，已上传资产先下载核验，再补传缺项。
- **新一轮发行遇到旧 draft**：新的验收运行会产生新的上下文，不能覆盖不匹配的 draft。先检查旧 draft，由维护者明确处理后再发起发行。
- **公开结果不确定**：先检查远端 Release。流程不会自动覆盖已公开版本，也不使用 `--clobber` 替换资产。

## 可选的 macOS 签名与公证

当前发行不要求 Apple 证书，默认 `unsigned`；下载后的系统启动行为仍是必需验收项。需要签名时，在构建候选前设置变量 `TEAMX_MACOS_SIGNING=developer-id` 和完整的 `TEAMX_SIGN_IDENTITY`（`Developer ID Application: 名称 (TEAMID)`），并配置：

- `TEAMX_MACOS_P12_BASE64`、`TEAMX_MACOS_P12_PASSWORD`：Developer ID Application 证书及私钥的 PKCS#12 导出和密码。
- `TEAMX_APPLE_API_KEY_BASE64`、`TEAMX_APPLE_API_KEY_ID`、`TEAMX_APPLE_API_ISSUER`：App Store Connect API 私钥及对应标识。

证书在 Apple Developer 账号的 Certificates 中创建，API key 在 App Store Connect 的 Users and Access／Integrations 中创建，凭据保存到 GitHub Secrets。Workflow 使用临时 keychain，签名和公证成功后才生成最终归档；失败不会退回 unsigned。裸 CLI 归档使用在线公证票据，不宣称已 staple。签名会改变程序字节，须重新构建候选并验收。
